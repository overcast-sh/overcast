package appsync

// handler.go — implemented GraphQL API management handlers.
//
// Implemented:
//   - CreateGraphqlApi   POST   /v1/apis
//   - GetGraphqlApi      GET    /v1/apis/{apiId}
//   - ListGraphqlApis    GET    /v1/apis
//   - UpdateGraphqlApi   POST   /v1/apis/{apiId}
//   - DeleteGraphqlApi   DELETE /v1/apis/{apiId}
//   - TagResource        POST   /v1/tags/{resourceArn}
//   - UntagResource      DELETE /v1/tags/{resourceArn}
//   - ListTagsForResource GET   /v1/tags/{resourceArn}

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// appsyncTagCfg tunes the shared tag validator to AppSync's error shape
// (#1052). This replaces a locally re-implemented duplicate of the same
// rules (validateTagMap, tagKeyPattern, tagValuePattern) that never called
// serviceutil.ValidateTags — a parallel validator this codebase's own rule
// says to retire rather than extend, and one whose tagKeyPattern rejected
// any digit in a tag key, which no AWS documentation supports.
var appsyncTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "BadRequestException",
	InvalidCode:     "BadRequestException",
	ExceededMessage: "tags cannot exceed 50 entries.",
}

// Handler holds AppSync handler dependencies.
type Handler struct {
	cfg   *config.Config
	store *Store
	log   *serviceutil.ServiceLogger
	bus   *events.Bus
	clk   clock.Clock

	// schemaParser validates and parses uploaded SDL schemas.
	sp *schemaParser

	// invoker invokes Lambda functions synchronously (for AWS_LAMBDA data sources).
	invoker events.FunctionSyncInvoker

	// dynamoInvoker invokes DynamoDB operations (for AMAZON_DYNAMODB data sources).
	dynamoInvoker events.DynamoDBInvoker

	// cognitoValidator verifies AMAZON_COGNITO_USER_POOLS bearer tokens against
	// the local Cognito service. Read only when
	// OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH is on; nil leaves relaxed
	// authorization untouched. See cognito_auth.go.
	cognitoValidator events.CognitoTokenValidator

	// Execution engine — optional, nil until implementations are wired.

	// vtlEvaluator evaluates VTL mapping templates.
	vtlEvaluator MappingTemplateEvaluator

	// jsEvaluator evaluates APPSYNC_JS resolver code.
	jsEvaluator CodeEvaluator

	// subscriptions manages active WebSocket subscriptions.
	subscriptions *subscriptionManager

	authorizerCacheMu sync.Mutex
	authorizerCache   map[string]cachedLambdaAuthorizerIdentity
}

type cachedLambdaAuthorizerIdentity struct {
	identity  map[string]any
	expiresAt time.Time
}

func newHandler(cfg *config.Config, store *Store, log *serviceutil.ServiceLogger, clk clock.Clock, sp *schemaParser) *Handler {
	return &Handler{
		cfg:             cfg,
		store:           store,
		log:             log,
		clk:             clk,
		sp:              sp,
		jsEvaluator:     newJSEvaluator(clk),
		vtlEvaluator:    newVTLEvaluator(clk),
		subscriptions:   newSubscriptionManager(clk, log),
		authorizerCache: make(map[string]cachedLambdaAuthorizerIdentity),
	}
}

// publish emits an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Source: serviceName, Payload: payload})
	}
}

// region returns the request's region (from SigV4 / X-Overcast-Region header),
// falling back to the configured default. Used for ARN minting so that ARNs
// returned to clients carry the deployment region they actually called.
func (h *Handler) region(r *http.Request) string {
	return middleware.RegionFromContext(r.Context(), h.cfg.Region)
}

// regionCtx is the context-only variant of region for code paths that don't
// have an *http.Request in scope (helpers called from handlers).
func (h *Handler) regionCtx(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, h.cfg.Region)
}

// writeJSON writes a JSON response with the correct AppSync content type.
// AppSync uses application/json (not application/x-amz-json-1.0), which is what
// protocol.WriteRESTJSON is for — this stays as a name local callers already
// use rather than as a second implementation of it.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	protocol.WriteRESTJSON(w, r, status, v)
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// maxResultsLimit is both the cap AppSync's List* operations put on maxResults
// and the page size they apply when a client omits it: AWS documents the
// parameter's valid range as 0-25 and defaults it to the same 25, so an omitted
// maxResults yields a page and a nextToken rather than the whole collection.
// https://docs.aws.amazon.com/appsync/latest/APIReference/API_ListGraphqlApis.html
const maxResultsLimit = 25

func paginateList[T any](r *http.Request, items []T) ([]T, string, *protocol.AWSError) {
	q := r.URL.Query()
	start := 0
	if token := q.Get("nextToken"); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil || parsed < 0 || parsed > len(items) {
			return nil, "", badRequestError("nextToken is invalid.")
		}
		start = parsed
	}

	max := maxResultsLimit
	if raw := q.Get("maxResults"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > maxResultsLimit {
			return nil, "", badRequestError(fmt.Sprintf("maxResults must be between 0 and %d.", maxResultsLimit))
		}
		max = parsed
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + max
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next, nil
}

func writeListJSON[T any](w http.ResponseWriter, r *http.Request, field string, items []T) {
	page, next, aerr := paginateList(r, items)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	body := map[string]any{field: page}
	if next != "" {
		body["nextToken"] = next
	}
	writeJSON(w, r, http.StatusOK, body)
}

// requireAPI loads an API by ID from the URL, writing 404 on miss.
func (h *Handler) requireAPI(w http.ResponseWriter, r *http.Request) (*GraphqlAPI, bool) {
	apiID := chi.URLParam(r, "apiId")
	api, err := h.store.GetAPI(r.Context(), apiID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return nil, false
	}
	if api == nil {
		protocol.WriteJSONError(w, r, notFoundError("GraphQL API "+apiID+" not found."))
		return nil, false
	}
	return api, true
}

// ─── CreateGraphqlApi ────────────────────────────────────────────────────────

// CreateGraphqlApi handles POST /v1/apis.
func (h *Handler) CreateGraphqlApi(w http.ResponseWriter, r *http.Request) {
	var api GraphqlAPI
	if !serviceutil.DecodeJSON(w, r, &api) {
		return
	}
	if err := validateGraphqlAPIInput(&api); err != nil {
		protocol.WriteJSONError(w, r, err)
		return
	}

	apiID := uuid.NewString()
	api.ApiId = apiID
	api.ARN = protocol.ARN(h.region(r), h.cfg.AccountID, "appsync", "apis/"+apiID)
	api.Owner = h.cfg.AccountID

	if api.ApiType == "" {
		api.ApiType = "GRAPHQL"
	}
	if api.Visibility == "" {
		api.Visibility = "GLOBAL"
	}
	if api.IntrospectionConfig == "" {
		api.IntrospectionConfig = "ENABLED"
	}

	api.Uris = localGraphQLURIs(serviceutil.ClientBaseURL(h.cfg, r), apiID, h.region(r))
	api.Dns = map[string]string{
		"GRAPHQL":  h.graphQLDNS(r, apiID),
		"REALTIME": h.graphQLDNS(r, apiID),
	}

	if err := h.store.PutAPI(r.Context(), &api); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	// AWS's CreateGraphqlApi does not create an API key as a side effect, even
	// for authenticationType=API_KEY: the response shape
	// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_CreateGraphqlApi.html)
	// carries no apiKey field, and the AWS console's "default key" is a
	// separate CreateApiKey call it makes after CreateGraphqlApi succeeds, not
	// something the API itself performs. Callers that want a key call
	// CreateApiKey explicitly (see AWS::AppSync::ApiKey's CloudFormation
	// handler for the pattern). Overcast auto-created one here until #62;
	// removing it is a breaking change for anything that relied on the
	// implicit key.

	h.publish(r, events.AppSyncAPICreated, events.ResourcePayload{Name: api.Name})

	writeJSON(w, r, http.StatusOK, map[string]any{"graphqlApi": api.withLocalURIs(serviceutil.ClientBaseURL(h.cfg, r), h.region(r))})
}

// ─── GetGraphqlApi ───────────────────────────────────────────────────────────

// GetGraphqlApi handles GET /v1/apis/{apiId}.
func (h *Handler) GetGraphqlApi(w http.ResponseWriter, r *http.Request) {
	api, ok := h.requireAPI(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"graphqlApi": api.withLocalURIs(serviceutil.ClientBaseURL(h.cfg, r), h.region(r))})
}

// ─── ListGraphqlApis ─────────────────────────────────────────────────────────

// ListGraphqlApis handles GET /v1/apis.
func (h *Handler) ListGraphqlApis(w http.ResponseWriter, r *http.Request) {
	apis, err := h.store.ListAPIs(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	apiType := r.URL.Query().Get("apiType")
	if apiType != "" && !containsString([]string{"GRAPHQL", "MERGED"}, apiType) {
		protocol.WriteJSONError(w, r, badRequestError("apiType is invalid."))
		return
	}
	owner := r.URL.Query().Get("owner")
	if owner != "" && !containsString([]string{"CURRENT_ACCOUNT", "OTHER_ACCOUNTS"}, owner) {
		protocol.WriteJSONError(w, r, badRequestError("owner is invalid."))
		return
	}
	baseURL := serviceutil.ClientBaseURL(h.cfg, r)
	filtered := make([]*GraphqlAPI, 0, len(apis))
	for _, api := range apis {
		if apiType != "" && api.ApiType != apiType {
			continue
		}
		if owner == "CURRENT_ACCOUNT" && api.Owner != h.cfg.AccountID {
			continue
		}
		if owner == "OTHER_ACCOUNTS" && api.Owner == h.cfg.AccountID {
			continue
		}
		filtered = append(filtered, api.withLocalURIs(baseURL, h.region(r)))
	}
	writeListJSON(w, r, "graphqlApis", filtered)
}

// ─── UpdateGraphqlApi ────────────────────────────────────────────────────────

// UpdateGraphqlApi handles POST /v1/apis/{apiId}.
func (h *Handler) UpdateGraphqlApi(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.requireAPI(w, r)
	if !ok {
		return
	}

	var update GraphqlAPI
	if !serviceutil.DecodeJSON(w, r, &update) {
		return
	}
	if err := validateGraphqlAPIInput(&update); err != nil {
		protocol.WriteJSONError(w, r, err)
		return
	}

	// Preserve server-generated fields.
	update.ApiId = existing.ApiId
	update.ARN = existing.ARN
	update.Owner = existing.Owner
	update.Uris = existing.Uris
	update.Dns = existing.Dns

	// Preserve defaults when not provided.
	if update.ApiType == "" {
		update.ApiType = existing.ApiType
	}
	if update.Visibility == "" {
		update.Visibility = existing.Visibility
	}
	if update.IntrospectionConfig == "" {
		update.IntrospectionConfig = existing.IntrospectionConfig
	}
	// Preserve tags if not provided in update.
	if update.Tags == nil {
		update.Tags = existing.Tags
	}

	if err := h.store.PutAPI(r.Context(), &update); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	h.publish(r, events.AppSyncAPIUpdated, events.ResourcePayload{Name: update.Name, ARN: update.ARN})

	writeJSON(w, r, http.StatusOK, map[string]any{"graphqlApi": update.withLocalURIs(serviceutil.ClientBaseURL(h.cfg, r), h.region(r))})
}

// ─── DeleteGraphqlApi ────────────────────────────────────────────────────────

// DeleteGraphqlApi handles DELETE /v1/apis/{apiId}.
func (h *Handler) DeleteGraphqlApi(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")

	if err := h.store.DeleteAPIAndChildren(r.Context(), apiID); err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}

	h.publish(r, events.AppSyncAPIDeleted, events.ResourcePayload{Name: apiID})

	writeJSON(w, r, http.StatusOK, map[string]any{})
}

// graphQLDNS returns the hostname a client resolves to reach this API, minted
// through the shared host-routed helper so it is a name this router accepts.
// It replaces a hardcoded "amazonaws.com", which advertised a name that
// resolves to real AWS and can never reach the emulator.
//
// REALTIME deliberately returns the SAME host as GRAPHQL. Real AWS serves
// realtime from a separate appsync-realtime-api hostname; Overcast colocates
// both under /_overcast/appsync/apis/{apiId} (see service.go), so the appsync-api host is
// the one that actually routes. Advertising an appsync-realtime-api name would
// reintroduce exactly the defect this replaces: a hostname Overcast hands out
// but cannot serve. Splitting them needs a fourth dispatch label -- tracked as
// a follow-up in docs/plans/host-routing-precedence.md.
func (h *Handler) graphQLDNS(r *http.Request, apiID string) string {
	return serviceutil.HostRoutedHostname(h.cfg, r, middleware.LabelAppSyncAPI, apiID, h.region(r))
}

// localGraphQLURIs mints the executable GraphQL and realtime endpoints, in the
// canonical AWS host-routed shape wherever the base can carry a subdomain:
//
//	http://{apiId}.appsync-api.{region}.{host}:{port}/graphql
//	ws://{apiId}.appsync-api.{region}.{host}:{port}/realtime
//
// That is the shape every AWS client expects and the one CloudFormation's
// Fn::GetAtt GraphQLUrl hands on to stacks. It was unconditionally path-style
// ({base}/_overcast/appsync/apis/{apiId}/graphql) — a URL only Overcast understands — on the
// grounds that *.localhost does not resolve on Windows or macOS. That is true
// of a bare localhost base and false of every wildcard-DNS domain, so the
// choice belongs to serviceutil.SupportsHostRouting rather than to the whole
// service: an OVERCAST_HOSTNAME deployment gets the AWS shape, and a bare
// localhost one keeps the URL that resolves there.
//
// Both forms stay served either way, so nothing already holding a path-style
// URI breaks. See docs/plans/host-routing-precedence.md §8.
//
// REALTIME shares the appsync-api label with GRAPHQL for the same reason
// dns.REALTIME does — see graphQLDNS.
func localGraphQLURIs(baseURL, apiID, region string) map[string]string {
	if !serviceutil.SupportsHostRouting(baseURL) {
		return map[string]string{
			"GRAPHQL":  fmt.Sprintf("%s/_overcast/appsync/apis/%s/graphql", baseURL, apiID),
			"REALTIME": fmt.Sprintf("%s/_overcast/appsync/apis/%s/realtime", websocketBaseURL(baseURL), apiID),
		}
	}
	return map[string]string{
		"GRAPHQL": serviceutil.HostRoutedURLFromBase(
			baseURL, middleware.LabelAppSyncAPI, apiID, region, "/graphql"),
		"REALTIME": serviceutil.HostRoutedURLFromBase(
			websocketBaseURL(baseURL), middleware.LabelAppSyncAPI, apiID, region, "/realtime"),
	}
}

func websocketBaseURL(baseURL string) string {
	if strings.HasPrefix(baseURL, "https://") {
		return "wss://" + strings.TrimPrefix(baseURL, "https://")
	}
	if strings.HasPrefix(baseURL, "http://") {
		return "ws://" + strings.TrimPrefix(baseURL, "http://")
	}
	return baseURL
}

func (api *GraphqlAPI) withLocalURIs(baseURL, region string) *GraphqlAPI {
	if api == nil {
		return nil
	}
	out := *api
	out.Uris = localGraphQLURIs(baseURL, api.ApiId, region)
	if api.Dns != nil {
		out.Dns = make(map[string]string, len(api.Dns))
		for k, v := range api.Dns {
			out.Dns[k] = v
		}
	}
	if api.Tags != nil {
		out.Tags = make(map[string]string, len(api.Tags))
		for k, v := range api.Tags {
			out.Tags[k] = v
		}
	}
	return &out
}

// ─── Tags ────────────────────────────────────────────────────────────────────

// TagResource handles POST /v1/tags/{resourceArn}.
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	arn := chi.URLParam(r, "*")
	api, err := h.apiForARN(r, arn)
	if err != nil {
		protocol.WriteJSONError(w, r, err)
		return
	}

	var req struct {
		Tags map[string]string `json:"tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if len(req.Tags) == 0 {
		protocol.WriteJSONError(w, r, badRequestError("tags is required."))
		return
	}

	merged := make(map[string]string, len(api.Tags)+len(req.Tags))
	for k, v := range api.Tags {
		merged[k] = v
	}
	for k, v := range req.Tags {
		merged[k] = v
	}
	// Validated against the merged set, not just the incoming delta, so the
	// 50-tag limit holds across repeated TagResource calls (#1052).
	if aerr := serviceutil.ValidateTags(appsyncTagCfg, merged); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	api.Tags = merged

	if storeErr := h.store.PutAPI(r.Context(), api); storeErr != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, storeErr))
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{})
}

// UntagResource handles DELETE /v1/tags/{resourceArn}.
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	arn := chi.URLParam(r, "*")
	api, err := h.apiForARN(r, arn)
	if err != nil {
		protocol.WriteJSONError(w, r, err)
		return
	}

	tagKeys := r.URL.Query()["tagKeys"]
	for _, k := range tagKeys {
		delete(api.Tags, k)
	}

	if storeErr := h.store.PutAPI(r.Context(), api); storeErr != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, storeErr))
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{})
}

// ListTagsForResource handles GET /v1/tags/{resourceArn}.
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := chi.URLParam(r, "*")
	api, err := h.apiForARN(r, arn)
	if err != nil {
		protocol.WriteJSONError(w, r, err)
		return
	}

	tags := api.Tags
	if tags == nil {
		tags = map[string]string{}
	}

	writeJSON(w, r, http.StatusOK, map[string]any{"tags": tags})
}

// validateGraphqlAPIInput checks a GraphqlAPI's fields, including tags — on
// both CreateGraphqlApi and UpdateGraphqlApi. Real AWS models no tags member
// on UpdateGraphqlApiInput, but this handler's decode target is the same
// GraphqlAPI struct either way, so a client-supplied `tags` on update is
// stored (existing.Tags is kept only when Tags is nil) and therefore must be
// validated here too — #1052 found it was not, the one gap left once
// CreateGraphqlApi and TagResource's own checks were confirmed reaching
// equivalent rules (just not through the shared validator).
func validateGraphqlAPIInput(api *GraphqlAPI) *protocol.AWSError {
	if api.Name == "" {
		return badRequestError("name is required.")
	}
	if !containsString([]string{"API_KEY", "AWS_IAM", "AMAZON_COGNITO_USER_POOLS", "OPENID_CONNECT", "AWS_LAMBDA"}, api.AuthenticationType) {
		return badRequestError("authenticationType is invalid or missing.")
	}
	if api.ApiType != "" && !containsString([]string{"GRAPHQL", "MERGED"}, api.ApiType) {
		return badRequestError("apiType is invalid.")
	}
	if api.Visibility != "" && !containsString([]string{"GLOBAL", "PRIVATE"}, api.Visibility) {
		return badRequestError("visibility is invalid.")
	}
	if api.IntrospectionConfig != "" && !containsString([]string{"ENABLED", "DISABLED"}, api.IntrospectionConfig) {
		return badRequestError("introspectionConfig is invalid.")
	}
	if api.QueryDepthLimit < 0 || api.QueryDepthLimit > 75 {
		return badRequestError("queryDepthLimit must be between 0 and 75.")
	}
	if api.ResolverCountLimit < 0 || api.ResolverCountLimit > 10000 {
		return badRequestError("resolverCountLimit must be between 0 and 10000.")
	}
	if len(api.OwnerContact) > 256 {
		return badRequestError("ownerContact must be 256 characters or fewer.")
	}
	if err := validateLogConfig(api.LogConfig); err != nil {
		return err
	}
	if err := validateAdditionalAuthenticationProviders(api.AdditionalAuthenticationProviders); err != nil {
		return err
	}
	return serviceutil.ValidateTags(appsyncTagCfg, api.Tags)
}

// validateLogConfig checks the shape of a create/update request's logConfig,
// stored as raw JSON for zero-cost passthrough (see GraphqlAPI.LogConfig).
// AWS models LogConfig.fieldLogLevel as required
// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_LogConfig.html)
// with values NONE, ERROR, ALL, INFO, or DEBUG; other LogConfig fields
// (cloudWatchLogsRoleArn, excludeVerboseContent) are passed through
// unvalidated, matching every other passthrough-typed ARN/bool field this
// handler does not otherwise check.
func validateLogConfig(raw json.RawMessage) *protocol.AWSError {
	if len(raw) == 0 {
		return nil
	}
	var cfg struct {
		FieldLogLevel string `json:"fieldLogLevel"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return badRequestError("logConfig is invalid.")
	}
	if !containsString([]string{"NONE", "ERROR", "ALL", "INFO", "DEBUG"}, cfg.FieldLogLevel) {
		return badRequestError("logConfig.fieldLogLevel is invalid or missing.")
	}
	return nil
}

// validateAdditionalAuthenticationProviders checks the shape of a
// create/update request's additionalAuthenticationProviders, stored as raw
// JSON for zero-cost passthrough (see
// GraphqlAPI.AdditionalAuthenticationProviders). AWS models each element's
// authenticationType as optional but, when present, constrained to the same
// enum as the top-level field
// (https://docs.aws.amazon.com/appsync/latest/APIReference/API_AdditionalAuthenticationProvider.html).
// The nested openIDConnectConfig/userPoolConfig/lambdaAuthorizerConfig
// objects are not validated here, matching the top-level fields of the same
// shapes.
func validateAdditionalAuthenticationProviders(raw json.RawMessage) *protocol.AWSError {
	if len(raw) == 0 {
		return nil
	}
	var providers []struct {
		AuthenticationType string `json:"authenticationType"`
	}
	if err := json.Unmarshal(raw, &providers); err != nil {
		return badRequestError("additionalAuthenticationProviders is invalid.")
	}
	for _, p := range providers {
		if p.AuthenticationType != "" && !containsString([]string{"API_KEY", "AWS_IAM", "AMAZON_COGNITO_USER_POOLS", "OPENID_CONNECT", "AWS_LAMBDA"}, p.AuthenticationType) {
			return badRequestError("additionalAuthenticationProviders authenticationType is invalid.")
		}
	}
	return nil
}

// apiForARN extracts the API ID from a resource ARN and loads the API.
func (h *Handler) apiForARN(r *http.Request, arn string) (*GraphqlAPI, *protocol.AWSError) {
	// The SDK URL-encodes the ARN in the path; decode so lookup works.
	if decoded, err := url.PathUnescape(arn); err == nil {
		arn = decoded
	}
	// ARN format: arn:aws:appsync:<region>:<account>:apis/<apiId>
	parts := strings.SplitN(arn, "/", 2)
	if len(parts) < 2 {
		return nil, notFoundError("Resource not found: " + arn)
	}
	// parts might be ["arn:aws:appsync:...:apis", "<apiId>"] or just the tail
	apiID := parts[len(parts)-1]
	// Handle the full ARN: extract the last segment after "apis/"
	if idx := strings.LastIndex(arn, "apis/"); idx >= 0 {
		apiID = arn[idx+len("apis/"):]
	}

	api, err := h.store.GetAPI(r.Context(), apiID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if api == nil {
		return nil, notFoundError("Resource not found: " + arn)
	}
	return api, nil
}
