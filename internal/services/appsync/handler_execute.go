package appsync

// handler_execute.go — GraphQL query execution and API_KEY authentication.
//
// Implemented:
//   - ExecuteGraphQL        POST /_overcast/appsync/apis/{apiId}/graphql
//   - API_KEY, OPENID_CONNECT, AWS_LAMBDA and AWS_IAM authentication, plus the
//     multi-auth fallback chain. AMAZON_COGNITO_USER_POOLS lives in cognito_auth.go.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

const lambdaAuthorizerDeniedFieldsKey = "__overcastDeniedFields"

type graphQLExecutionRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
}

// ─── ExecuteGraphQL ──────────────────────────────────────────────────────────

// ExecuteGraphQL handles POST /_overcast/appsync/apis/{apiId}/graphql.
func (h *Handler) ExecuteGraphQL(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")

	// 1. Load the API.
	api, err := h.store.GetAPI(r.Context(), apiID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, err))
		return
	}
	if api == nil {
		protocol.WriteJSONError(w, r, notFoundError("GraphQL API "+apiID+" not found."))
		return
	}

	// 2. Decode the GraphQL request body. Lambda authorizers receive the query
	// context in their requestContext, so decode before auth.
	var gqlReq graphQLExecutionRequest
	if !serviceutil.DecodeJSON(w, r, &gqlReq) {
		return
	}
	if gqlReq.Variables == nil {
		gqlReq.Variables = map[string]any{}
	}

	// 3. Authenticate. Authorization failures on this data-plane endpoint use
	// AppSync's GraphQL error envelope, not the AWS JSON error envelope that
	// the management-plane /v1/apis operations use — see writeGraphQLAuthError.
	identity, authErr := h.authenticateRequest(r, api, &gqlReq)
	if authErr != nil {
		writeGraphQLAuthError(w, r, authErr)
		return
	}

	// 4. Load the parsed schema (cache → store → error).
	parsed := h.sp.Get(apiID)
	if parsed == nil {
		stored, storeErr := h.store.GetSchema(r.Context(), apiID)
		if storeErr != nil {
			protocol.WriteJSONError(w, r, protocol.Wrap(protocol.ErrInternalError, storeErr))
			return
		}
		if stored == nil {
			writeGraphQLErrors(w, r, []GraphQLError{{Message: "No schema defined for API " + apiID}})
			return
		}
		reparsed, parseErr := h.sp.Parse(stored.Definition)
		if parseErr != nil {
			writeGraphQLErrors(w, r, []GraphQLError{{Message: "Schema parse error: " + parseErr.Error()}})
			return
		}
		h.sp.Put(apiID, reparsed)
		parsed = reparsed
	}

	// 5. Parse and validate the query against the schema.
	schema := parsed.Opaque.(*ast.Schema)
	doc, parseErr := parser.ParseQuery(&ast.Source{Name: "query", Input: gqlReq.Query})
	if parseErr != nil {
		writeGraphQLErrors(w, r, []GraphQLError{{Message: parseErr.Error()}})
		return
	}
	if validationErrs := validator.ValidateWithRules(schema, doc, nil); validationErrs != nil {
		writeGQLValidationErrors(w, r, validationErrs)
		return
	}

	// 6. Execute — resolve each field via stored resolvers.
	result := h.executeQuery(r, api, parsed, doc, gqlReq.OperationName, gqlReq.Variables, identity)
	writeJSON(w, r, http.StatusOK, result)
}

// ─── Authentication ──────────────────────────────────────────────────────────

// authenticateRequest validates the incoming request based on the API's auth config.
// It returns a RequestIdentity on success (may be nil for API_KEY) or an AWSError on failure.
// Supports multi-auth: tries primary, then additionalAuthenticationProviders.
func (h *Handler) authenticateRequest(r *http.Request, api *GraphqlAPI, gqlReq *graphQLExecutionRequest) (map[string]any, *protocol.AWSError) {
	// Try the primary auth type first.
	identity, authErr := h.tryAuth(r, api, api.AuthenticationType, api.UserPoolConfig, api.OpenIDConnectConfig, api.LambdaAuthorizerConfig, gqlReq)
	if authErr == nil {
		return identity, nil
	}

	// If primary fails and there are additional providers, try each.
	if len(api.AdditionalAuthenticationProviders) > 0 {
		var providers []struct {
			AuthenticationType     string          `json:"authenticationType"`
			UserPoolConfig         json.RawMessage `json:"userPoolConfig,omitempty"`
			OpenIDConnectConfig    json.RawMessage `json:"openIDConnectConfig,omitempty"`
			LambdaAuthorizerConfig json.RawMessage `json:"lambdaAuthorizerConfig,omitempty"`
		}
		if err := json.Unmarshal(api.AdditionalAuthenticationProviders, &providers); err == nil {
			for _, p := range providers {
				id, pErr := h.tryAuth(r, api, p.AuthenticationType, p.UserPoolConfig, p.OpenIDConnectConfig, p.LambdaAuthorizerConfig, gqlReq)
				if pErr == nil {
					return id, nil
				}
			}
		}
	}

	return nil, authErr
}

// tryAuth attempts a single authentication method.
func (h *Handler) tryAuth(r *http.Request, api *GraphqlAPI, authType string, userPoolCfg, oidcCfg, lambdaCfg json.RawMessage, gqlReq *graphQLExecutionRequest) (map[string]any, *protocol.AWSError) {
	switch authType {
	case "API_KEY":
		if err := h.authenticateAPIKey(r, api); err != nil {
			return nil, err
		}
		return nil, nil
	case "AMAZON_COGNITO_USER_POOLS":
		return h.authenticateCognito(r, userPoolCfg)
	case "OPENID_CONNECT":
		return h.authenticateOIDC(r, oidcCfg)
	case "AWS_LAMBDA":
		return h.authenticateLambdaAuthorizer(r, api, lambdaCfg, gqlReq)
	case "AWS_IAM":
		return h.iamIdentity(r), nil
	default:
		// Unknown auth types are accepted by the local emulator.
		return nil, nil
	}
}

// authenticateAPIKey validates the x-api-key header against stored API keys.
func (h *Handler) authenticateAPIKey(r *http.Request, api *GraphqlAPI) *protocol.AWSError {
	keyValue := r.Header.Get("x-api-key")
	if keyValue == "" {
		return unauthorizedError("Missing x-api-key header.")
	}

	keys, err := h.store.ListApiKeys(r.Context(), api.ApiId)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}

	now := h.clk.Now().Unix()
	for _, k := range keys {
		if k.Id == keyValue {
			if k.Expires > 0 && k.Expires < now {
				return unauthorizedError("API key has expired.")
			}
			return nil
		}
	}

	return unauthorizedError("Invalid API key.")
}

// authenticateOIDC checks for a Bearer token and extracts claims, including the issuer.
func (h *Handler) authenticateOIDC(r *http.Request, oidcCfg json.RawMessage) (map[string]any, *protocol.AWSError) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, unauthorizedError("Missing or invalid Authorization header for OPENID_CONNECT.")
	}
	claims := parseJWTClaims(token)

	// If configured, use the issuer from config.
	issuer, _ := claims["iss"].(string)
	if len(oidcCfg) > 0 {
		var cfg struct {
			Issuer string `json:"issuer"`
		}
		if err := json.Unmarshal(oidcCfg, &cfg); err == nil && cfg.Issuer != "" {
			issuer = cfg.Issuer
		}
	}

	identity := map[string]any{
		"sub":      claims["sub"],
		"issuer":   issuer,
		"username": claimString(claims, "username", "cognito:username"),
		"claims":   claims,
		"sourceIp": sourceIPs(r),
	}
	return identity, nil
}

func (h *Handler) authenticateLambdaAuthorizer(r *http.Request, api *GraphqlAPI, lambdaCfg json.RawMessage, gqlReq *graphQLExecutionRequest) (map[string]any, *protocol.AWSError) {
	var cfg struct {
		AuthorizerURI                string `json:"authorizerUri"`
		IdentityValidationExpression string `json:"identityValidationExpression"`
		AuthorizerResultTTLInSeconds int    `json:"authorizerResultTtlInSeconds"`
	}
	if len(lambdaCfg) > 0 {
		if err := json.Unmarshal(lambdaCfg, &cfg); err != nil {
			return nil, unauthorizedError("Invalid Lambda authorizer configuration.")
		}
	}
	if cfg.AuthorizerURI == "" {
		return map[string]any{"resolverContext": map[string]any{}}, nil
	}
	if h.invoker == nil {
		return nil, unauthorizedError("Lambda authorizer invocation not available.")
	}

	token := r.Header.Get("Authorization")
	if token == "" {
		return nil, unauthorizedError("Missing Authorization header for AWS_LAMBDA.")
	}
	if cfg.IdentityValidationExpression != "" {
		matched, err := regexp.MatchString(cfg.IdentityValidationExpression, token)
		if err != nil || !matched {
			return nil, unauthorizedError("Unauthorized")
		}
	}
	cacheKey := api.ApiId + "\x00" + token
	if cfg.AuthorizerResultTTLInSeconds > 0 {
		if identity, ok := h.getAuthorizerCache(cacheKey); ok {
			return identity, nil
		}
	}
	if gqlReq == nil {
		gqlReq = &graphQLExecutionRequest{Variables: map[string]any{}}
	}
	if gqlReq.Variables == nil {
		gqlReq.Variables = map[string]any{}
	}

	event := map[string]any{
		"authorizationToken": token,
		"requestContext": map[string]any{
			"apiId":         api.ApiId,
			"accountId":     h.accountID(),
			"requestId":     r.Header.Get("x-amzn-requestid"),
			"queryString":   gqlReq.Query,
			"operationName": gqlReq.OperationName,
			"variables":     gqlReq.Variables,
		},
		"requestHeaders": flattenHeaders(r.Header),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}

	functionName := lambdaFunctionNameFromARN(cfg.AuthorizerURI)
	outcome, err := h.invoker.Invoke(r.Context(), functionName, payload)
	if err != nil || outcome == nil || outcome.FunctionError != "" {
		return nil, unauthorizedError("Unauthorized")
	}
	var resp struct {
		IsAuthorized    bool           `json:"isAuthorized"`
		ResolverContext map[string]any `json:"resolverContext"`
		DeniedFields    []string       `json:"deniedFields"`
		TTLOverride     *int           `json:"ttlOverride"`
	}
	if err := json.Unmarshal(outcome.Payload, &resp); err != nil || !resp.IsAuthorized {
		return nil, unauthorizedError("Unauthorized")
	}
	if resp.ResolverContext == nil {
		resp.ResolverContext = map[string]any{}
	}
	identity := map[string]any{"resolverContext": resp.ResolverContext}
	if len(resp.DeniedFields) > 0 {
		identity[lambdaAuthorizerDeniedFieldsKey] = resp.DeniedFields
	}
	ttl := cfg.AuthorizerResultTTLInSeconds
	if resp.TTLOverride != nil {
		ttl = *resp.TTLOverride
	}
	if ttl > 0 {
		h.setAuthorizerCache(cacheKey, identity, ttl)
	}
	return identity, nil
}

func (h *Handler) getAuthorizerCache(key string) (map[string]any, bool) {
	if h == nil || h.authorizerCache == nil || h.clk == nil {
		return nil, false
	}
	h.authorizerCacheMu.Lock()
	defer h.authorizerCacheMu.Unlock()
	cached, ok := h.authorizerCache[key]
	if !ok {
		return nil, false
	}
	if !cached.expiresAt.After(h.clk.Now()) {
		delete(h.authorizerCache, key)
		return nil, false
	}
	return cloneIdentity(cached.identity), true
}

func (h *Handler) setAuthorizerCache(key string, identity map[string]any, ttlSeconds int) {
	if h == nil || h.clk == nil || ttlSeconds <= 0 {
		return
	}
	h.authorizerCacheMu.Lock()
	defer h.authorizerCacheMu.Unlock()
	if h.authorizerCache == nil {
		h.authorizerCache = make(map[string]cachedLambdaAuthorizerIdentity)
	}
	h.authorizerCache[key] = cachedLambdaAuthorizerIdentity{
		identity:  cloneIdentity(identity),
		expiresAt: h.clk.Now().Add(time.Duration(ttlSeconds) * time.Second),
	}
}

func cloneIdentity(identity map[string]any) map[string]any {
	if identity == nil {
		return nil
	}
	clone := make(map[string]any, len(identity))
	for k, v := range identity {
		clone[k] = v
	}
	return clone
}

// extractBearerToken extracts a Bearer token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return auth[len(prefix):]
	}
	return ""
}

// parseJWTClaims extracts a JWT's claims, yielding an empty map for anything
// it cannot read. This is NOT a security validation — the emulator trusts the
// token. Strict Cognito enforcement calls serviceutil.DecodeJWTClaims instead,
// because it has to tell a malformed token apart from one carrying no claims.
func parseJWTClaims(token string) map[string]any {
	claims, err := serviceutil.DecodeJWTClaims(token)
	if err != nil {
		return map[string]any{}
	}
	return claims
}

func claimString(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := claims[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func sourceIPs(r *http.Request) []string {
	var ips []string
	for _, part := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		ip := strings.TrimSpace(part)
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if remote != "" {
		ips = append(ips, remote)
	}
	if ips == nil {
		return []string{}
	}
	return ips
}

func (h *Handler) iamIdentity(r *http.Request) map[string]any {
	accountID := h.accountID()
	accessKey := sigV4AccessKey(r)
	username := accessKey
	userArn := ""
	if user, ok := h.iamUserForAccessKey(r.Context(), accessKey); ok {
		username = user.UserName
		userArn = user.Arn
	}
	identity := map[string]any{
		"accountId": accountID,
		"sourceIp":  sourceIPs(r),
	}
	if username != "" {
		identity["username"] = username
		identity["user"] = username
		if userArn == "" {
			userArn = "arn:aws:iam::" + accountID + ":user/" + username
		}
		identity["userArn"] = userArn
	}
	return identity
}

type iamUserRecord struct {
	UserName   string `json:"UserName"`
	Arn        string `json:"Arn"`
	AccessKeys []struct {
		AccessKeyID string `json:"AccessKeyId"`
		Status      string `json:"Status"`
	} `json:"AccessKeys"`
}

func (h *Handler) iamUserForAccessKey(ctx context.Context, accessKey string) (*iamUserRecord, bool) {
	if h == nil || h.store == nil || h.store.s == nil || accessKey == "" {
		return nil, false
	}
	records, err := h.store.s.Scan(ctx, "iam:users", "")
	if err != nil {
		return nil, false
	}
	for _, record := range records {
		var user iamUserRecord
		if err := json.Unmarshal([]byte(record.Value), &user); err != nil {
			continue
		}
		for _, key := range user.AccessKeys {
			if key.AccessKeyID == accessKey && !strings.EqualFold(key.Status, "Inactive") {
				return &user, true
			}
		}
	}
	return nil, false
}

func (h *Handler) accountID() string {
	if h != nil && h.cfg != nil && h.cfg.AccountID != "" {
		return h.cfg.AccountID
	}
	return "000000000000"
}

func sigV4AccessKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		for _, part := range strings.Split(auth, ",") {
			part = strings.TrimSpace(part)
			part = strings.TrimPrefix(part, "AWS4-HMAC-SHA256 ")
			if strings.HasPrefix(part, "Credential=") {
				credential := strings.TrimPrefix(part, "Credential=")
				if idx := strings.IndexByte(credential, '/'); idx > 0 {
					return credential[:idx]
				}
			}
		}
	}
	credential := r.URL.Query().Get("X-Amz-Credential")
	if idx := strings.IndexByte(credential, '/'); idx > 0 {
		return credential[:idx]
	}
	return ""
}

// ─── Query Execution Engine ──────────────────────────────────────────────────

// resolveContext carries field-level information through the resolution chain.
// It provides the context needed by different data source resolvers (especially
// Lambda, which receives arguments, source, and field info in its event payload).
type resolveContext struct {
	FieldName  string
	ParentType string
	Arguments  map[string]any
	Source     any // parent object for nested resolvers (nil at root)
	Variables  map[string]any
	Headers    http.Header
	DomainName string
	Stash      map[string]any
	PrevResult any
	Field      *ast.Field     // AST field for sub-selection access
	Identity   map[string]any // authenticated caller's identity for $context.identity
}

type batchField struct {
	field    *ast.Field
	alias    string
	resolver *Resolver
	ds       *DataSource
}

// executeQuery walks the selection set and resolves fields using stored resolvers.
func (h *Handler) executeQuery(r *http.Request, api *GraphqlAPI, schema *ParsedSchema, doc *ast.QueryDocument, operationName string, variables map[string]any, identity map[string]any) *ExecuteResult {
	op, opErr := selectOperation(doc, operationName)
	if opErr != nil {
		return &ExecuteResult{Errors: []GraphQLError{*opErr}}
	}

	rootTypeName := schema.QueryType
	if op.Operation == ast.Mutation {
		rootTypeName = schema.MutationType
	}
	if rootTypeName == "" {
		return &ExecuteResult{Errors: []GraphQLError{{Message: "No root type for operation"}}}
	}

	astSchema := schema.Opaque.(*ast.Schema)
	introspectionDisabled := api.IntrospectionConfig == "DISABLED"

	// Separate introspection meta-fields (__schema, __type) from regular fields
	// so they can be resolved against the schema without touching the resolver store.
	var regularSelections ast.SelectionSet
	introspData := map[string]any{}
	var introspErrors []GraphQLError

	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			regularSelections = append(regularSelections, sel)
			continue
		}
		switch field.Name {
		case "__schema", "__type":
			alias := field.Alias
			if alias == "" {
				alias = field.Name
			}
			if introspectionDisabled {
				introspErrors = append(introspErrors, GraphQLError{
					Message: "GraphQL introspection is not allowed, but the query contained " + field.Name,
				})
				introspData[alias] = nil
			} else {
				introspData[alias] = resolveIntrospectionField(field, astSchema, variables, doc.Fragments)
			}
		default:
			regularSelections = append(regularSelections, sel)
		}
	}

	data, errors := h.resolveSelectionSet(r, api, regularSelections, rootTypeName, nil, variables, identity)
	errors = append(introspErrors, errors...)

	// Merge introspection results into the data map.
	for k, v := range introspData {
		data[k] = v
	}

	// After a mutation resolves, notify matching subscriptions.
	if op.Operation == ast.Mutation && h.subscriptions != nil && len(errors) == 0 {
		for fieldName, value := range data {
			if valueMap, ok := value.(map[string]any); ok {
				h.subscriptions.Publish(r.Context(), api.ApiId, fieldName, valueMap)
			}
		}
	}

	dataJSON, _ := json.Marshal(data)
	return &ExecuteResult{
		Data:   json.RawMessage(dataJSON),
		Errors: errors,
	}
}

// resolveSelectionSet resolves all fields in a selection set for a given parent type.
// source is the parent object (nil for root-level queries).
func (h *Handler) resolveSelectionSet(r *http.Request, api *GraphqlAPI, selections ast.SelectionSet, parentType string, source any, variables map[string]any, identity map[string]any) (map[string]any, []GraphQLError) {
	data := make(map[string]any)
	var errors []GraphQLError

	for _, sel := range selections {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}

		alias := field.Alias
		if alias == "" {
			alias = field.Name
		}

		// __typename is a meta-field that returns the name of the current type.
		// It is never backed by a resolver — handle it before resolver dispatch.
		if field.Name == "__typename" {
			data[alias] = parentType
			continue
		}
		if h.deniesField(api, identity, parentType, field.Name) {
			data[alias] = nil
			continue
		}

		resolver, resolverErr := h.store.GetResolver(r.Context(), api.ApiId, parentType, field.Name)
		if resolverErr != nil {
			errors = append(errors, GraphQLError{Message: "resolver lookup error: " + resolverErr.Error()})
			continue
		}

		if resolver == nil {
			// No resolver for this field. If the source (parent) has a value
			// for this field, use it (structural resolution).
			if sourceMap, ok := source.(map[string]any); ok {
				val, exists := sourceMap[field.Name]
				if exists {
					data[alias] = h.filterSubFields(val, field, api, identity, fieldTypeName(field))
					continue
				}
			}
			data[alias] = nil
			continue
		}

		rctx := &resolveContext{
			FieldName:  field.Name,
			ParentType: parentType,
			Arguments:  extractArguments(field, variables),
			Source:     source,
			Variables:  variables,
			Headers:    r.Header,
			Field:      field,
			Identity:   identity,
		}

		result, resolveErr := h.resolveField(r, api, resolver, rctx)
		if resolveErr != nil {
			// Enrich error with the field path for accurate client debugging.
			enriched := *resolveErr
			if len(enriched.Path) == 0 {
				enriched.Path = []any{alias}
			}
			errors = append(errors, enriched)
			data[alias] = nil
			continue
		}

		// If the field has sub-selections, handle nested resolution.
		if len(field.SelectionSet) > 0 && result != nil {
			resolved, childErrs := h.resolveNestedField(r, api, field, result, variables, identity)
			errors = append(errors, childErrs...)
			data[alias] = resolved
		} else {
			data[alias] = result
		}
	}
	return data, errors
}

// resolveNestedField resolves sub-fields of a resolved object.
// It checks if any child field has its own resolver; if so, it recurses.
// Otherwise, it applies structural (filter) resolution.
func (h *Handler) resolveNestedField(r *http.Request, api *GraphqlAPI, field *ast.Field, result any, variables map[string]any, identity map[string]any) (any, []GraphQLError) {
	// Determine the child type name from the field's definition.
	childType := fieldTypeName(field)

	switch v := result.(type) {
	case map[string]any:
		// Check if any child field has a resolver.
		hasChildResolvers := false
		for _, sel := range field.SelectionSet {
			sf, ok := sel.(*ast.Field)
			if !ok {
				continue
			}
			cr, _ := h.store.GetResolver(r.Context(), api.ApiId, childType, sf.Name)
			if cr != nil {
				hasChildResolvers = true
				break
			}
		}
		if hasChildResolvers {
			return h.resolveSelectionSet(r, api, field.SelectionSet, childType, v, variables, identity)
		}
		return h.filterSubFields(v, field, api, identity, childType), nil

	case []any:
		if batched, ok, batchErrs := h.resolveBatchedListSelectionSet(r, api, field.SelectionSet, childType, v, variables, identity); ok {
			return batched, batchErrs
		}
		// Array of objects — resolve each element.
		out := make([]any, len(v))
		var errors []GraphQLError
		for i, elem := range v {
			resolved, childErrs := h.resolveNestedField(r, api, field, elem, variables, identity)
			errors = append(errors, childErrs...)
			out[i] = resolved
		}
		return out, errors

	default:
		return result, nil
	}
}

func (h *Handler) resolveBatchedListSelectionSet(r *http.Request, api *GraphqlAPI, selections ast.SelectionSet, parentType string, items []any, variables map[string]any, identity map[string]any) ([]any, bool, []GraphQLError) {
	if len(items) == 0 || len(selections) == 0 {
		return nil, false, nil
	}
	batchFields := []batchField{}
	for _, sel := range selections {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		resolver, ds, ok := h.batchableDirectLambdaResolver(r, api, parentType, field)
		if !ok {
			continue
		}
		alias := field.Alias
		if alias == "" {
			alias = field.Name
		}
		batchFields = append(batchFields, batchField{field: field, alias: alias, resolver: resolver, ds: ds})
	}
	if len(batchFields) == 0 {
		return nil, false, nil
	}

	out := make([]any, len(items))
	var errors []GraphQLError
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			out[i] = item
			continue
		}
		resolved, childErrs := h.resolveSelectionSetWithoutBatchFields(r, api, selections, parentType, obj, variables, identity, batchFields)
		errors = append(errors, childErrs...)
		out[i] = resolved
	}

	for _, bf := range batchFields {
		for i := range out {
			if m, ok := out[i].(map[string]any); ok {
				m[bf.alias] = nil
			}
		}
		for start := 0; start < len(items); start += bf.resolver.MaxBatchSize {
			end := start + bf.resolver.MaxBatchSize
			if end > len(items) {
				end = len(items)
			}
			rctxs := make([]*resolveContext, 0, end-start)
			indices := make([]int, 0, end-start)
			for i := start; i < end; i++ {
				obj, ok := items[i].(map[string]any)
				if !ok || h.deniesField(api, identity, parentType, bf.field.Name) {
					continue
				}
				rctxs = append(rctxs, &resolveContext{
					FieldName:  bf.field.Name,
					ParentType: parentType,
					Arguments:  extractArguments(bf.field, variables),
					Source:     obj,
					Variables:  variables,
					Headers:    r.Header,
					Field:      bf.field,
					Identity:   identity,
				})
				indices = append(indices, i)
			}
			if len(rctxs) == 0 {
				continue
			}
			var results []any
			var batchErrs []GraphQLError
			if bf.resolver.RequestMappingTemplate != "" {
				results, batchErrs = h.resolveMappedLambdaBatchDataSource(r, api, bf.resolver, bf.ds, rctxs)
			} else if h.jsEvaluator != nil && isAppSyncJSRuntime(bf.resolver.Runtime) && bf.resolver.Code != "" {
				results, batchErrs = h.resolveJSLambdaBatchDataSource(r, api, bf.resolver, bf.ds, rctxs)
			} else {
				results, batchErrs = h.resolveDirectLambdaBatchDataSource(r.Context(), bf.ds, rctxs)
			}
			errors = append(errors, batchErrs...)
			for j, result := range results {
				idx := indices[j]
				if m, ok := out[idx].(map[string]any); ok {
					m[bf.alias] = h.filterSubFields(result, bf.field, api, identity, fieldTypeName(bf.field))
				}
			}
		}
	}

	return out, true, errors
}

func (h *Handler) resolveSelectionSetWithoutBatchFields(r *http.Request, api *GraphqlAPI, selections ast.SelectionSet, parentType string, source map[string]any, variables map[string]any, identity map[string]any, batchFields []batchField) (map[string]any, []GraphQLError) {
	filtered := make(ast.SelectionSet, 0, len(selections))
	for _, sel := range selections {
		field, ok := sel.(*ast.Field)
		if !ok {
			filtered = append(filtered, sel)
			continue
		}
		isBatch := false
		for _, bf := range batchFields {
			if bf.field == field {
				isBatch = true
				break
			}
		}
		if !isBatch {
			filtered = append(filtered, sel)
		}
	}
	return h.resolveSelectionSet(r, api, filtered, parentType, source, variables, identity)
}

func (h *Handler) batchableDirectLambdaResolver(r *http.Request, api *GraphqlAPI, parentType string, field *ast.Field) (*Resolver, *DataSource, bool) {
	if field == nil || h.store == nil {
		return nil, nil, false
	}
	resolver, err := h.store.GetResolver(r.Context(), api.ApiId, parentType, field.Name)
	if err != nil || resolver == nil || resolver.MaxBatchSize <= 0 || resolver.Kind == "PIPELINE" || resolver.ResponseMappingTemplate != "" || resolver.DataSourceName == "" {
		return nil, nil, false
	}
	if resolver.Code != "" && !isAppSyncJSRuntime(resolver.Runtime) {
		return nil, nil, false
	}
	ds, err := h.store.GetDataSource(r.Context(), api.ApiId, resolver.DataSourceName)
	if err != nil || ds == nil || ds.Type != "AWS_LAMBDA" {
		return nil, nil, false
	}
	return resolver, ds, true
}

// fieldTypeName extracts the named type from a field's type definition.
// Unwraps NonNull and List wrappers to get the underlying type name.
func fieldTypeName(field *ast.Field) string {
	if field.Definition == nil || field.Definition.Type == nil {
		return ""
	}
	t := field.Definition.Type
	for t.Elem != nil {
		t = t.Elem
	}
	return t.NamedType
}

// extractArguments converts AST field arguments into a map, resolving variables.
func extractArguments(field *ast.Field, variables map[string]any) map[string]any {
	if len(field.Arguments) == 0 {
		return nil
	}
	args := make(map[string]any, len(field.Arguments))
	for _, arg := range field.Arguments {
		args[arg.Name] = astValueToGo(arg.Value, variables)
	}
	return args
}

// astValueToGo converts an AST value to a Go native type. Handles variables,
// literals, lists, and objects.
func astValueToGo(v *ast.Value, variables map[string]any) any {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case ast.Variable:
		if variables != nil {
			return variables[v.Raw]
		}
		return nil
	case ast.IntValue, ast.FloatValue:
		var n json.Number
		n = json.Number(v.Raw)
		if f, err := n.Float64(); err == nil {
			return f
		}
		return v.Raw
	case ast.BooleanValue:
		return v.Raw == "true"
	case ast.NullValue:
		return nil
	case ast.ListValue:
		list := make([]any, len(v.Children))
		for i, child := range v.Children {
			list[i] = astValueToGo(child.Value, variables)
		}
		return list
	case ast.ObjectValue:
		obj := make(map[string]any, len(v.Children))
		for _, child := range v.Children {
			obj[child.Name] = astValueToGo(child.Value, variables)
		}
		return obj
	case ast.StringValue, ast.BlockValue, ast.EnumValue:
		return v.Raw
	}
	return v.Raw
}

// selectOperation picks the operation to execute from a multi-operation document.
// If the document has a single operation, operationName may be empty.
// If the document has multiple operations, operationName is required per the GraphQL spec.
func selectOperation(doc *ast.QueryDocument, operationName string) (*ast.OperationDefinition, *GraphQLError) {
	if len(doc.Operations) == 0 {
		return nil, &GraphQLError{Message: "No operation found in query"}
	}
	if operationName == "" {
		if len(doc.Operations) > 1 {
			return nil, &GraphQLError{Message: "Must provide operation name if query contains multiple operations."}
		}
		return doc.Operations[0], nil
	}
	for _, o := range doc.Operations {
		if o.Name == operationName {
			return o, nil
		}
	}
	return nil, &GraphQLError{Message: "Unknown operation named \"" + operationName + "\"."}
}

// resolveField dispatches a single field resolution based on the resolver's data source.
func (h *Handler) resolveField(r *http.Request, api *GraphqlAPI, resolver *Resolver, rctx *resolveContext) (any, *GraphQLError) {
	// PIPELINE resolvers execute an ordered sequence of functions.
	if resolver.Kind == "PIPELINE" {
		return h.resolvePipeline(r, api, resolver, rctx)
	}

	// Check if this is an APPSYNC_JS resolver.
	if h.jsEvaluator != nil && isAppSyncJSRuntime(resolver.Runtime) && resolver.Code != "" {
		return h.resolveFieldJS(r, api, resolver, rctx, nil)
	}

	// Check if this resolver uses VTL mapping templates.
	if h.vtlEvaluator != nil && resolver.RequestMappingTemplate != "" {
		return h.resolveFieldVTL(r, api, resolver, rctx)
	}

	// UNIT resolver — single data source call.
	if resolver.DataSourceName == "" {
		return nil, nil
	}

	ds, err := h.store.GetDataSource(r.Context(), api.ApiId, resolver.DataSourceName)
	if err != nil {
		return nil, &GraphQLError{Message: "data source lookup error: " + err.Error()}
	}
	if ds == nil {
		return nil, &GraphQLError{Message: "data source " + resolver.DataSourceName + " not found"}
	}

	return h.resolveDataSource(r, ds, resolver.RequestMappingTemplate, rctx)
}

// resolveDataSource dispatches resolution to the appropriate backend based on data source type.
func (h *Handler) resolveDataSource(r *http.Request, ds *DataSource, requestTemplate string, rctx *resolveContext) (any, *GraphQLError) {
	switch ds.Type {
	case "NONE":
		return resolveNoneTemplate(requestTemplate)
	case "HTTP":
		return h.resolveHTTPTemplate(r.Context(), ds, requestTemplate)
	case "AWS_LAMBDA":
		return h.resolveLambdaDataSource(r.Context(), ds, requestTemplate, rctx)
	case "AMAZON_DYNAMODB":
		return h.resolveDynamoDBDataSource(r.Context(), ds, requestTemplate, rctx)
	default:
		return nil, &GraphQLError{Message: "data source type " + ds.Type + " not yet supported"}
	}
}

// resolvePipeline executes a PIPELINE resolver: runs each function in order,
// passing results from one to the next. The last function's result is returned.
func (h *Handler) resolvePipeline(r *http.Request, api *GraphqlAPI, resolver *Resolver, rctx *resolveContext) (any, *GraphQLError) {
	var pc struct {
		Functions []string `json:"functions"`
	}
	if len(resolver.PipelineConfig) > 0 {
		if err := json.Unmarshal(resolver.PipelineConfig, &pc); err != nil {
			return nil, &GraphQLError{Message: "invalid pipelineConfig: " + err.Error()}
		}
	}
	if len(pc.Functions) == 0 {
		return nil, nil
	}

	// If the pipeline resolver uses APPSYNC_JS, execute with JS-aware pipeline.
	isJSPipeline := h.jsEvaluator != nil && isAppSyncJSRuntime(resolver.Runtime) && resolver.Code != ""
	stash := map[string]any{}

	// For JS pipelines, call the resolver's request() first.
	if isJSPipeline {
		ctx := h.buildJSContext(r, api, rctx, nil, stash, nil)
		reqResult, err := h.jsEvaluator.Evaluate(resolver.Code, "request", ctx)
		if err != nil {
			return nil, &GraphQLError{Message: "pipeline request() error: " + err.Error()}
		}
		if reqResult.Error != nil {
			return nil, jsEvalErrorToGraphQL(reqResult.Error)
		}
		// Propagate stash mutations from the ctx.
		syncStash(ctx, stash)
	}

	// For VTL pipelines, evaluate the resolver's request mapping template first.
	if !isJSPipeline && h.vtlEvaluator != nil && resolver.RequestMappingTemplate != "" {
		reqCtx := h.buildVTLContext(r, api, rctx, nil, stash, nil)
		_, err := h.vtlEvaluator.Evaluate(resolver.RequestMappingTemplate, reqCtx)
		if err != nil {
			if vtlErr, ok := err.(*vtlError); ok {
				return nil, &GraphQLError{Message: vtlErr.Message}
			}
			return nil, &GraphQLError{Message: "VTL pipeline request template error: " + err.Error()}
		}
		if s, ok := reqCtx["stash"].(map[string]any); ok {
			for k, v := range s {
				stash[k] = v
			}
		}
	}

	var lastResult any
	for _, fnID := range pc.Functions {
		fn, err := h.store.GetFunction(r.Context(), api.ApiId, fnID)
		if err != nil {
			return nil, &GraphQLError{Message: "function lookup error: " + err.Error()}
		}
		if fn == nil {
			return nil, &GraphQLError{Message: "pipeline function " + fnID + " not found"}
		}

		// Check if this function uses APPSYNC_JS.
		if h.jsEvaluator != nil && isAppSyncJSRuntime(fn.Runtime) && fn.Code != "" {
			result, gqlErr := h.resolveFunctionJS(r, api, fn, rctx, stash, lastResult)
			if gqlErr != nil {
				return nil, gqlErr
			}
			lastResult = result
			continue
		}

		// VTL/template-based function.
		if h.vtlEvaluator != nil && fn.RequestMappingTemplate != "" {
			result, gqlErr := h.resolveFunctionVTL(r, api, fn, rctx, stash, lastResult)
			if gqlErr != nil {
				return nil, gqlErr
			}
			lastResult = result
			continue
		}

		if fn.DataSourceName == "" {
			continue
		}

		ds, err := h.store.GetDataSource(r.Context(), api.ApiId, fn.DataSourceName)
		if err != nil {
			return nil, &GraphQLError{Message: "data source lookup error: " + err.Error()}
		}
		if ds == nil {
			return nil, &GraphQLError{Message: "data source " + fn.DataSourceName + " not found"}
		}

		result, gqlErr := h.resolveDataSource(r, ds, fn.RequestMappingTemplate, rctx)
		if gqlErr != nil {
			return nil, gqlErr
		}
		lastResult = result
	}

	// For VTL pipelines, evaluate the resolver's response template with prev.result.
	isVTLPipeline := h.vtlEvaluator != nil && resolver.ResponseMappingTemplate != "" && !isJSPipeline
	if isVTLPipeline {
		respCtx := h.buildVTLContext(r, api, rctx, nil, stash, lastResult)
		respCtx["prev"] = map[string]any{"result": lastResult}
		respOutput, err := h.vtlEvaluator.Evaluate(resolver.ResponseMappingTemplate, respCtx)
		if err != nil {
			return nil, &GraphQLError{Message: "VTL pipeline response error: " + err.Error()}
		}
		var parsed any
		if err := json.Unmarshal([]byte(respOutput), &parsed); err != nil {
			return respOutput, nil
		}
		return parsed, nil
	}

	// For JS pipelines, call the resolver's response() with ctx.prev.result.
	if isJSPipeline {
		ctx := h.buildJSContext(r, api, rctx, nil, stash, lastResult)
		respResult, err := h.jsEvaluator.Evaluate(resolver.Code, "response", ctx)
		if err != nil {
			return nil, &GraphQLError{Message: "pipeline response() error: " + err.Error()}
		}
		if respResult.Error != nil {
			return nil, jsEvalErrorToGraphQL(respResult.Error)
		}
		var parsed any
		if err := json.Unmarshal([]byte(respResult.EvaluationResult), &parsed); err != nil {
			return respResult.EvaluationResult, nil
		}
		return parsed, nil
	}

	return lastResult, nil
}

// ─── APPSYNC_JS Resolver Execution ───────────────────────────────────────────

// isAppSyncJSRuntime checks if a resolver/function uses the APPSYNC_JS runtime.
func isAppSyncJSRuntime(runtimeJSON json.RawMessage) bool {
	if len(runtimeJSON) == 0 {
		return false
	}
	var rt struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(runtimeJSON, &rt); err != nil {
		return false
	}
	return rt.Name == "APPSYNC_JS"
}

// buildJSContext builds the context object passed to JS request/response functions.
func (h *Handler) buildJSContext(r *http.Request, api *GraphqlAPI, rctx *resolveContext, dataSourceResult any, stash map[string]any, prevResult any) map[string]any {
	ctx := map[string]any{
		"arguments": rctx.Arguments,
		"source":    rctx.Source,
		"stash":     stash,
		"info": map[string]any{
			"fieldName":           rctx.FieldName,
			"parentTypeName":      rctx.ParentType,
			"selectionSetGraphQL": selectionSetToGraphQL(vtlFieldSelectionSet(rctx.Field)),
		},
		"request": map[string]any{
			"headers": flattenHeaders(rctx.Headers),
		},
	}
	if dataSourceResult != nil {
		ctx["result"] = dataSourceResult
	}
	if prevResult != nil {
		ctx["prev"] = map[string]any{"result": prevResult}
	} else {
		ctx["prev"] = map[string]any{"result": nil}
	}

	// Inject environment variables as ctx.env.
	if r != nil && api != nil {
		ev, _ := h.store.GetEnvironmentVariables(r.Context(), api.ApiId)
		if ev != nil && ev.EnvironmentVariables != nil {
			env := make(map[string]any, len(ev.EnvironmentVariables))
			for k, v := range ev.EnvironmentVariables {
				env[k] = v
			}
			ctx["env"] = env
		} else {
			ctx["env"] = map[string]any{}
		}
	} else {
		ctx["env"] = map[string]any{}
	}

	// Inject identity if available on the resolve context.
	if rctx.Identity != nil {
		ctx["identity"] = publicResolverIdentity(rctx.Identity)
	}

	return ctx
}

// syncStash reads any stash mutations from the JS context back into the Go stash map.
func syncStash(ctx map[string]any, stash map[string]any) {
	if s, ok := ctx["stash"].(map[string]any); ok {
		for k, v := range s {
			stash[k] = v
		}
	}
}

func withPipelineContext(rctx *resolveContext, stash map[string]any, prevResult any) *resolveContext {
	if rctx == nil {
		return &resolveContext{Stash: stash, PrevResult: prevResult}
	}
	copy := *rctx
	copy.Stash = stash
	copy.PrevResult = prevResult
	return &copy
}

// resolveFieldJS executes a UNIT resolver that uses APPSYNC_JS runtime.
// Flow: request(ctx) → data source → response(ctx with result).
func (h *Handler) resolveFieldJS(r *http.Request, api *GraphqlAPI, resolver *Resolver, rctx *resolveContext, stash map[string]any) (any, *GraphQLError) {
	if stash == nil {
		stash = map[string]any{}
	}

	// 1. Call request(ctx) to get the data source request payload.
	reqCtx := h.buildJSContext(r, api, rctx, nil, stash, nil)
	reqResult, err := h.jsEvaluator.Evaluate(resolver.Code, "request", reqCtx)
	if err != nil {
		return nil, &GraphQLError{Message: "JS request() error: " + err.Error()}
	}
	if reqResult.Error != nil {
		return nil, jsEvalErrorToGraphQL(reqResult.Error)
	}
	syncStash(reqCtx, stash)

	// 2. Execute the data source with the request result.
	var dsResult any
	if resolver.DataSourceName != "" {
		ds, dsErr := h.store.GetDataSource(r.Context(), api.ApiId, resolver.DataSourceName)
		if dsErr != nil {
			return nil, &GraphQLError{Message: "data source lookup error: " + dsErr.Error()}
		}
		if ds == nil {
			return nil, &GraphQLError{Message: "data source " + resolver.DataSourceName + " not found"}
		}

		// For NONE data sources, the request() return value's payload is the result.
		if ds.Type == "NONE" {
			var reqPayload any
			if err := json.Unmarshal([]byte(reqResult.EvaluationResult), &reqPayload); err == nil {
				if m, ok := reqPayload.(map[string]any); ok {
					dsResult = m["payload"]
				}
			}
		} else {
			// For other data sources, use the request result as the template.
			dsResult2, gqlErr := h.resolveDataSource(r, ds, reqResult.EvaluationResult, rctx)
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = dsResult2
		}
	}

	// 3. Call response(ctx) with the data source result.
	respCtx := h.buildJSContext(r, api, rctx, dsResult, stash, nil)
	respResult, err := h.jsEvaluator.Evaluate(resolver.Code, "response", respCtx)
	if err != nil {
		return nil, &GraphQLError{Message: "JS response() error: " + err.Error()}
	}
	if respResult.Error != nil {
		return nil, jsEvalErrorToGraphQL(respResult.Error)
	}

	// Parse the response result.
	var parsed any
	if err := json.Unmarshal([]byte(respResult.EvaluationResult), &parsed); err != nil {
		return respResult.EvaluationResult, nil
	}
	return parsed, nil
}

// resolveFunctionJS executes a pipeline function that uses APPSYNC_JS runtime.
func (h *Handler) resolveFunctionJS(r *http.Request, api *GraphqlAPI, fn *FunctionConfiguration, rctx *resolveContext, stash map[string]any, prevResult any) (any, *GraphQLError) {
	// 1. Call request(ctx).
	reqCtx := h.buildJSContext(r, api, rctx, nil, stash, prevResult)
	reqResult, err := h.jsEvaluator.Evaluate(fn.Code, "request", reqCtx)
	if err != nil {
		return nil, &GraphQLError{Message: "JS function request() error: " + err.Error()}
	}
	if reqResult.Error != nil {
		return nil, jsEvalErrorToGraphQL(reqResult.Error)
	}
	syncStash(reqCtx, stash)

	// 2. Execute the data source.
	var dsResult any
	if fn.DataSourceName != "" {
		ds, dsErr := h.store.GetDataSource(r.Context(), api.ApiId, fn.DataSourceName)
		if dsErr != nil {
			return nil, &GraphQLError{Message: "data source lookup error: " + dsErr.Error()}
		}
		if ds == nil {
			return nil, &GraphQLError{Message: "data source " + fn.DataSourceName + " not found"}
		}
		if ds.Type == "NONE" {
			var reqPayload any
			if err := json.Unmarshal([]byte(reqResult.EvaluationResult), &reqPayload); err == nil {
				if m, ok := reqPayload.(map[string]any); ok {
					dsResult = m["payload"]
				}
			}
		} else {
			dsResult2, gqlErr := h.resolveDataSource(r, ds, reqResult.EvaluationResult, withPipelineContext(rctx, stash, prevResult))
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = dsResult2
		}
	}

	// 3. Call response(ctx) with the data source result.
	respCtx := h.buildJSContext(r, api, rctx, dsResult, stash, prevResult)
	respResult, err := h.jsEvaluator.Evaluate(fn.Code, "response", respCtx)
	if err != nil {
		return nil, &GraphQLError{Message: "JS function response() error: " + err.Error()}
	}
	if respResult.Error != nil {
		return nil, jsEvalErrorToGraphQL(respResult.Error)
	}
	syncStash(respCtx, stash)

	// Parse the response result.
	var parsed any
	if err := json.Unmarshal([]byte(respResult.EvaluationResult), &parsed); err != nil {
		return respResult.EvaluationResult, nil
	}
	return parsed, nil
}

// ─── VTL Resolver Execution ──────────────────────────────────────────────────

// buildVTLContext builds the $context map for VTL template evaluation.
func (h *Handler) buildVTLContext(r *http.Request, api *GraphqlAPI, rctx *resolveContext, dataSourceResult any, stash map[string]any, prevResult any) map[string]any {
	ctx := map[string]any{
		"arguments": rctx.Arguments,
		"source":    rctx.Source,
		"stash":     stash,
		"info": map[string]any{
			"fieldName":           rctx.FieldName,
			"parentTypeName":      rctx.ParentType,
			"selectionSetList":    vtlSelectionSetList(rctx.Field),
			"selectionSetGraphQL": selectionSetToGraphQL(vtlFieldSelectionSet(rctx.Field)),
		},
		"request": map[string]any{
			"headers": flattenHeaders(rctx.Headers),
		},
	}
	if dataSourceResult != nil {
		ctx["result"] = dataSourceResult
	}
	if prevResult != nil {
		ctx["prev"] = map[string]any{"result": prevResult}
	}
	if rctx.Identity != nil {
		ctx["identity"] = publicResolverIdentity(rctx.Identity)
	}

	// Load environment variables if available.
	ev, _ := h.store.GetEnvironmentVariables(r.Context(), api.ApiId)
	if ev != nil && ev.EnvironmentVariables != nil {
		env := make(map[string]any, len(ev.EnvironmentVariables))
		for k, v := range ev.EnvironmentVariables {
			env[k] = v
		}
		ctx["env"] = env
	} else {
		ctx["env"] = map[string]any{}
	}

	return ctx
}

// vtlSelectionSetList returns the list of field names from a field's selection set.
func vtlSelectionSetList(field *ast.Field) []any {
	if field == nil || len(field.SelectionSet) == 0 {
		return []any{}
	}
	var names []any
	for _, sel := range field.SelectionSet {
		if sf, ok := sel.(*ast.Field); ok {
			names = append(names, sf.Name)
		}
	}
	return names
}

// vtlFieldSelectionSet returns the AST selection set for a field, or nil.
func vtlFieldSelectionSet(field *ast.Field) ast.SelectionSet {
	if field == nil {
		return nil
	}
	return field.SelectionSet
}

// resolveFieldVTL executes a UNIT resolver that uses VTL mapping templates.
func (h *Handler) resolveFieldVTL(r *http.Request, api *GraphqlAPI, resolver *Resolver, rctx *resolveContext) (any, *GraphQLError) {
	stash := map[string]any{}

	// 1. Evaluate the request mapping template.
	reqCtx := h.buildVTLContext(r, api, rctx, nil, stash, nil)
	reqOutput, err := h.vtlEvaluator.Evaluate(resolver.RequestMappingTemplate, reqCtx)
	if err != nil {
		return nil, vtlErrorToGraphQL(err, "VTL request template error")
	}

	// 2. Dispatch to data source (if one is configured).
	var dsResult any
	if resolver.DataSourceName != "" {
		ds, dsErr := h.store.GetDataSource(r.Context(), api.ApiId, resolver.DataSourceName)
		if dsErr != nil {
			return nil, &GraphQLError{Message: "data source lookup error: " + dsErr.Error()}
		}
		if ds == nil {
			return nil, &GraphQLError{Message: "data source " + resolver.DataSourceName + " not found"}
		}

		switch ds.Type {
		case "NONE":
			result, gqlErr := resolveNoneTemplate(reqOutput)
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = result
		default:
			result, gqlErr := h.resolveDataSource(r, ds, reqOutput, rctx)
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = result
		}
	} else {
		// No data source — parse the request template output as the result.
		var parsed any
		if err := json.Unmarshal([]byte(reqOutput), &parsed); err == nil {
			if m, ok := parsed.(map[string]any); ok {
				dsResult = m["payload"]
			} else {
				dsResult = parsed
			}
		} else {
			dsResult = reqOutput
		}
	}

	// 3. Evaluate the response mapping template (if present).
	if resolver.ResponseMappingTemplate != "" {
		respCtx := h.buildVTLContext(r, api, rctx, dsResult, stash, nil)
		respOutput, err := h.vtlEvaluator.Evaluate(resolver.ResponseMappingTemplate, respCtx)
		if err != nil {
			return nil, vtlErrorToGraphQL(err, "VTL response template error")
		}
		var parsed any
		if err := json.Unmarshal([]byte(respOutput), &parsed); err != nil {
			return respOutput, nil
		}
		return parsed, nil
	}

	return dsResult, nil
}

// resolveFunctionVTL executes a pipeline function that uses VTL mapping templates.
func (h *Handler) resolveFunctionVTL(r *http.Request, api *GraphqlAPI, fn *FunctionConfiguration, rctx *resolveContext, stash map[string]any, prevResult any) (any, *GraphQLError) {
	// 1. Evaluate request mapping template.
	reqCtx := h.buildVTLContext(r, api, rctx, nil, stash, prevResult)
	reqOutput, err := h.vtlEvaluator.Evaluate(fn.RequestMappingTemplate, reqCtx)
	if err != nil {
		return nil, vtlErrorToGraphQL(err, "VTL function request template error")
	}
	// Sync stash back.
	if s, ok := reqCtx["stash"].(map[string]any); ok {
		for k, v := range s {
			stash[k] = v
		}
	}

	// 2. Execute the data source.
	var dsResult any
	if fn.DataSourceName != "" {
		ds, dsErr := h.store.GetDataSource(r.Context(), api.ApiId, fn.DataSourceName)
		if dsErr != nil {
			return nil, &GraphQLError{Message: "data source lookup error: " + dsErr.Error()}
		}
		if ds == nil {
			return nil, &GraphQLError{Message: "data source " + fn.DataSourceName + " not found"}
		}
		if ds.Type == "NONE" {
			result, gqlErr := resolveNoneTemplate(reqOutput)
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = result
		} else {
			result, gqlErr := h.resolveDataSource(r, ds, reqOutput, withPipelineContext(rctx, stash, prevResult))
			if gqlErr != nil {
				return nil, gqlErr
			}
			dsResult = result
		}
	}

	// 3. Evaluate response mapping template (if present).
	if fn.ResponseMappingTemplate != "" {
		respCtx := h.buildVTLContext(r, api, rctx, dsResult, stash, prevResult)
		respOutput, err := h.vtlEvaluator.Evaluate(fn.ResponseMappingTemplate, respCtx)
		if err != nil {
			return nil, vtlErrorToGraphQL(err, "VTL function response template error")
		}
		if s, ok := respCtx["stash"].(map[string]any); ok {
			for k, v := range s {
				stash[k] = v
			}
		}
		var parsed any
		if err := json.Unmarshal([]byte(respOutput), &parsed); err != nil {
			return respOutput, nil
		}
		return parsed, nil
	}

	return dsResult, nil
}

// resolveNoneTemplate handles NONE data sources — the requestMappingTemplate payload
// becomes the resolver result. This is how AppSync "local resolvers" work.
func resolveNoneTemplate(template string) (any, *GraphQLError) {
	if template == "" {
		return nil, nil
	}

	// For NONE data sources, the requestMappingTemplate is a JSON object
	// with a "payload" field. The payload becomes $context.result.
	var tmpl struct {
		Payload any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(template), &tmpl); err != nil {
		return template, nil
	}
	return tmpl.Payload, nil
}

// resolveHTTPTemplate handles HTTP data sources by proxying to the configured endpoint.
// The requestMappingTemplate specifies resourcePath, method, and params (headers, body).
func (h *Handler) resolveHTTPTemplate(ctx context.Context, ds *DataSource, requestTemplate string) (any, *GraphQLError) {
	// Parse the HTTP config to get the endpoint.
	var httpCfg struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(ds.HttpConfig, &httpCfg); err != nil || httpCfg.Endpoint == "" {
		return nil, &GraphQLError{Message: "HTTP data source missing endpoint configuration"}
	}

	// Parse the request mapping template to get resourcePath, method, and params.
	var tmpl struct {
		ResourcePath string `json:"resourcePath"`
		Method       string `json:"method"`
		Params       struct {
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"params"`
	}
	if requestTemplate != "" {
		if err := json.Unmarshal([]byte(requestTemplate), &tmpl); err != nil {
			return nil, &GraphQLError{Message: "invalid HTTP request mapping template: " + err.Error()}
		}
	}
	if tmpl.Method == "" {
		tmpl.Method = http.MethodGet
	}
	if tmpl.ResourcePath == "" {
		tmpl.ResourcePath = "/"
	}

	url := strings.TrimRight(httpCfg.Endpoint, "/") + tmpl.ResourcePath

	var bodyReader io.Reader
	if tmpl.Params.Body != "" {
		bodyReader = strings.NewReader(tmpl.Params.Body)
	}

	req, err := http.NewRequestWithContext(ctx, tmpl.Method, url, bodyReader)
	if err != nil {
		return nil, &GraphQLError{Message: "failed to create HTTP request: " + err.Error()}
	}
	for k, v := range tmpl.Params.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &GraphQLError{Message: "HTTP data source request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	// Read the response body (bounded to 1 MiB to prevent OOM).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, &GraphQLError{Message: "failed to read HTTP response: " + err.Error()}
	}

	// Try to parse as JSON; if it fails, return as raw string.
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return string(body), nil
	}
	return result, nil
}

// resolveLambdaDataSource handles AWS_LAMBDA data sources by invoking the
// configured Lambda function synchronously. The function receives an AppSync
// resolver event with arguments, source, and field info.
func (h *Handler) resolveLambdaDataSource(ctx context.Context, ds *DataSource, requestTemplate string, rctx *resolveContext) (any, *GraphQLError) {
	if h.invoker == nil {
		return nil, &GraphQLError{Message: "Lambda invocation not available (Lambda service not enabled)"}
	}

	// Extract the function name from the Lambda config.
	var lambdaCfg struct {
		LambdaFunctionArn string `json:"lambdaFunctionArn"`
	}
	if err := json.Unmarshal(ds.LambdaConfig, &lambdaCfg); err != nil || lambdaCfg.LambdaFunctionArn == "" {
		return nil, &GraphQLError{Message: "AWS_LAMBDA data source missing lambdaFunctionArn configuration"}
	}

	functionName := lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn)

	var payload []byte
	asyncEventInvocation := false
	if strings.TrimSpace(requestTemplate) != "" {
		requestObject, gqlErr := parseLambdaRequestObject(requestTemplate, false)
		if gqlErr != nil {
			return nil, gqlErr
		}
		asyncEventInvocation = requestObject.InvocationType == "Event"
		payload = []byte(requestTemplate)
	} else {
		// AWS direct Lambda resolvers receive the AppSync resolver context when
		// no request mapping template is configured.
		event := h.buildDirectLambdaResolverEvent(rctx)
		var err error
		payload, err = json.Marshal(event)
		if err != nil {
			return nil, &GraphQLError{Message: "failed to marshal Lambda event: " + err.Error()}
		}
	}

	outcome, err := h.invoker.Invoke(ctx, functionName, payload)
	if err != nil {
		return nil, &GraphQLError{Message: "Lambda invocation failed: " + err.Error()}
	}
	if asyncEventInvocation {
		return nil, nil
	}
	if outcome == nil {
		return nil, &GraphQLError{Message: "Lambda function " + functionName + " not found or unavailable"}
	}

	// If the function returned an error, propagate it.
	if outcome.FunctionError != "" {
		var errPayload struct {
			ErrorMessage string `json:"errorMessage"`
			ErrorType    string `json:"errorType"`
		}
		if json.Unmarshal(outcome.Payload, &errPayload) == nil && errPayload.ErrorMessage != "" {
			return nil, &GraphQLError{Message: errPayload.ErrorMessage}
		}
		return nil, &GraphQLError{Message: "Lambda function error: " + outcome.FunctionError}
	}

	// Parse the response payload.
	var result any
	if len(outcome.Payload) > 0 {
		if err := json.Unmarshal(outcome.Payload, &result); err != nil {
			return string(outcome.Payload), nil
		}
	}
	return result, nil
}

type lambdaRequestObject struct {
	Operation      string `json:"operation"`
	InvocationType string `json:"invocationType"`
}

func parseLambdaRequestObject(requestTemplate string, allowBatch bool) (*lambdaRequestObject, *GraphQLError) {
	var requestObject lambdaRequestObject
	if err := json.Unmarshal([]byte(requestTemplate), &requestObject); err != nil {
		return nil, &GraphQLError{Message: "invalid Lambda request mapping template output"}
	}
	if requestObject.Operation == "BatchInvoke" {
		if allowBatch {
			return &requestObject, nil
		}
		return nil, &GraphQLError{Message: "Lambda BatchInvoke request mapping is not yet supported"}
	}
	if requestObject.Operation != "Invoke" {
		return nil, &GraphQLError{Message: "invalid Lambda request operation"}
	}
	if requestObject.InvocationType != "" && requestObject.InvocationType != "RequestResponse" && requestObject.InvocationType != "Event" {
		return nil, &GraphQLError{Message: "invalid Lambda invocationType"}
	}
	return &requestObject, nil
}

func (h *Handler) resolveDirectLambdaBatchDataSource(ctx context.Context, ds *DataSource, rctxs []*resolveContext) ([]any, []GraphQLError) {
	if len(rctxs) == 0 {
		return []any{}, nil
	}
	if h.invoker == nil {
		return nil, []GraphQLError{{Message: "Lambda invocation not available (Lambda service not enabled)"}}
	}
	var lambdaCfg struct {
		LambdaFunctionArn string `json:"lambdaFunctionArn"`
	}
	if err := json.Unmarshal(ds.LambdaConfig, &lambdaCfg); err != nil || lambdaCfg.LambdaFunctionArn == "" {
		return nil, []GraphQLError{{Message: "AWS_LAMBDA data source missing lambdaFunctionArn configuration"}}
	}

	events := make([]map[string]any, 0, len(rctxs))
	for _, rctx := range rctxs {
		events = append(events, h.buildDirectLambdaResolverEvent(rctx))
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return nil, []GraphQLError{{Message: "failed to marshal Lambda batch event: " + err.Error()}}
	}
	outcome, err := h.invoker.Invoke(ctx, lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn), payload)
	if err != nil {
		return nil, []GraphQLError{{Message: "Lambda invocation failed: " + err.Error()}}
	}
	if outcome == nil {
		return nil, []GraphQLError{{Message: "Lambda function " + lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn) + " not found or unavailable"}}
	}
	if outcome.FunctionError != "" {
		return nil, []GraphQLError{{Message: "Lambda function error: " + outcome.FunctionError}}
	}

	return parseLambdaBatchInvokeResponse(outcome.Payload, len(rctxs))
}

func (h *Handler) resolveMappedLambdaBatchDataSource(r *http.Request, api *GraphqlAPI, resolver *Resolver, ds *DataSource, rctxs []*resolveContext) ([]any, []GraphQLError) {
	if len(rctxs) == 0 {
		return []any{}, nil
	}
	if h.vtlEvaluator == nil {
		return nil, []GraphQLError{{Message: "VTL evaluator not available for Lambda BatchInvoke"}}
	}
	var lambdaCfg struct {
		LambdaFunctionArn string `json:"lambdaFunctionArn"`
	}
	if err := json.Unmarshal(ds.LambdaConfig, &lambdaCfg); err != nil || lambdaCfg.LambdaFunctionArn == "" {
		return nil, []GraphQLError{{Message: "AWS_LAMBDA data source missing lambdaFunctionArn configuration"}}
	}

	payloads := make([]any, 0, len(rctxs))
	var merged map[string]any
	invocationType := ""
	for _, rctx := range rctxs {
		output, err := h.vtlEvaluator.Evaluate(resolver.RequestMappingTemplate, h.buildVTLContext(r, api, rctx, nil, map[string]any{}, nil))
		if err != nil {
			return nil, []GraphQLError{*vtlErrorToGraphQL(err, "VTL request template error")}
		}
		requestObject, gqlErr := parseLambdaRequestObject(output, true)
		if gqlErr != nil {
			return nil, []GraphQLError{*gqlErr}
		}
		if requestObject.Operation != "BatchInvoke" {
			return nil, []GraphQLError{{Message: "Lambda batched resolver request must use BatchInvoke"}}
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(output), &obj); err != nil {
			return nil, []GraphQLError{{Message: "invalid Lambda request mapping template output"}}
		}
		if merged == nil {
			merged = obj
			invocationType = requestObject.InvocationType
		} else if requestObject.InvocationType != invocationType {
			return nil, []GraphQLError{{Message: "Lambda BatchInvoke invocationType mismatch"}}
		}
		payloads = append(payloads, obj["payload"])
	}
	if merged == nil {
		return []any{}, nil
	}
	merged["payload"] = payloads
	payload, err := json.Marshal(merged)
	if err != nil {
		return nil, []GraphQLError{{Message: "failed to marshal Lambda batch event: " + err.Error()}}
	}
	outcome, err := h.invoker.Invoke(r.Context(), lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn), payload)
	if err != nil {
		return nil, []GraphQLError{{Message: "Lambda invocation failed: " + err.Error()}}
	}
	if invocationType == "Event" {
		return make([]any, len(rctxs)), nil
	}
	if outcome == nil {
		return nil, []GraphQLError{{Message: "Lambda function " + lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn) + " not found or unavailable"}}
	}
	if outcome.FunctionError != "" {
		return nil, []GraphQLError{{Message: "Lambda function error: " + outcome.FunctionError}}
	}
	return parseLambdaBatchInvokeResponse(outcome.Payload, len(rctxs))
}

func (h *Handler) resolveJSLambdaBatchDataSource(r *http.Request, api *GraphqlAPI, resolver *Resolver, ds *DataSource, rctxs []*resolveContext) ([]any, []GraphQLError) {
	if len(rctxs) == 0 {
		return []any{}, nil
	}
	if h.jsEvaluator == nil {
		return nil, []GraphQLError{{Message: "JS evaluator not available for Lambda BatchInvoke"}}
	}
	var lambdaCfg struct {
		LambdaFunctionArn string `json:"lambdaFunctionArn"`
	}
	if err := json.Unmarshal(ds.LambdaConfig, &lambdaCfg); err != nil || lambdaCfg.LambdaFunctionArn == "" {
		return nil, []GraphQLError{{Message: "AWS_LAMBDA data source missing lambdaFunctionArn configuration"}}
	}

	payloads := make([]any, 0, len(rctxs))
	var merged map[string]any
	invocationType := ""
	stashes := make([]map[string]any, len(rctxs))
	for i, rctx := range rctxs {
		stash := map[string]any{}
		stashes[i] = stash
		reqResult, err := h.jsEvaluator.Evaluate(resolver.Code, "request", h.buildJSContext(r, api, rctx, nil, stash, nil))
		if err != nil {
			return nil, []GraphQLError{{Message: "JS request() error: " + err.Error()}}
		}
		if reqResult.Error != nil {
			return nil, []GraphQLError{*jsEvalErrorToGraphQL(reqResult.Error)}
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(reqResult.EvaluationResult), &obj); err != nil {
			return nil, []GraphQLError{{Message: "invalid Lambda request mapping template output"}}
		}
		requestObject, gqlErr := parseLambdaRequestObject(reqResult.EvaluationResult, true)
		if gqlErr != nil {
			return nil, []GraphQLError{*gqlErr}
		}
		if requestObject.Operation != "BatchInvoke" {
			return nil, []GraphQLError{{Message: "Lambda batched resolver request must use BatchInvoke"}}
		}
		if merged == nil {
			merged = obj
			invocationType = requestObject.InvocationType
		} else if requestObject.InvocationType != invocationType {
			return nil, []GraphQLError{{Message: "Lambda BatchInvoke invocationType mismatch"}}
		}
		payloads = append(payloads, obj["payload"])
	}
	if merged == nil {
		return []any{}, nil
	}
	merged["payload"] = payloads
	payload, err := json.Marshal(merged)
	if err != nil {
		return nil, []GraphQLError{{Message: "failed to marshal Lambda batch event: " + err.Error()}}
	}
	outcome, err := h.invoker.Invoke(r.Context(), lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn), payload)
	if err != nil {
		return nil, []GraphQLError{{Message: "Lambda invocation failed: " + err.Error()}}
	}
	if invocationType == "Event" {
		return make([]any, len(rctxs)), nil
	}
	if outcome == nil {
		return nil, []GraphQLError{{Message: "Lambda function " + lambdaFunctionNameFromARN(lambdaCfg.LambdaFunctionArn) + " not found or unavailable"}}
	}
	if outcome.FunctionError != "" {
		return nil, []GraphQLError{{Message: "Lambda function error: " + outcome.FunctionError}}
	}

	results, errs := parseLambdaBatchInvokeResponse(outcome.Payload, len(rctxs))
	if len(errs) > 0 {
		return results, errs
	}
	parsed := make([]any, len(results))
	for i, result := range results {
		respCtx := h.buildJSContext(r, api, rctxs[i], result, stashes[i], nil)
		respResult, err := h.jsEvaluator.Evaluate(resolver.Code, "response", respCtx)
		if err != nil {
			return nil, []GraphQLError{{Message: "JS response() error: " + err.Error()}}
		}
		if respResult.Error != nil {
			return nil, []GraphQLError{*jsEvalErrorToGraphQL(respResult.Error)}
		}
		if err := json.Unmarshal([]byte(respResult.EvaluationResult), &parsed[i]); err != nil {
			parsed[i] = respResult.EvaluationResult
		}
	}
	return parsed, nil
}

func parseLambdaBatchInvokeResponse(payload []byte, wantLen int) ([]any, []GraphQLError) {
	var rawResults []any
	if err := json.Unmarshal(payload, &rawResults); err != nil {
		return nil, []GraphQLError{{Message: "Lambda BatchInvoke response must be a list"}}
	}
	if len(rawResults) != wantLen {
		return nil, []GraphQLError{{Message: "Lambda BatchInvoke response length does not match request length"}}
	}
	results := make([]any, len(rawResults))
	var errs []GraphQLError
	for i, item := range rawResults {
		if m, ok := item.(map[string]any); ok {
			if msg, _ := m["errorMessage"].(string); msg != "" {
				err := GraphQLError{Message: msg}
				if typ, _ := m["errorType"].(string); typ != "" {
					err.Extensions = map[string]any{"errorType": typ}
				}
				errs = append(errs, err)
				results[i] = nil
				continue
			}
			if data, ok := m["data"]; ok {
				results[i] = data
				continue
			}
		}
		results[i] = item
	}
	return results, errs
}

// resolveDynamoDBDataSource handles AMAZON_DYNAMODB data sources by invoking
// DynamoDB operations via the local DynamoDB emulator.
//
// The requestMappingTemplate is a JSON object specifying the DynamoDB operation
// and its parameters. This is a simplified version of the real AppSync VTL
// template output — it accepts the resolved DynamoDB operation directly:
//
//	{
//	  "operation": "GetItem",
//	  "key": {"id": {"S": "123"}},
//	  "table": "my-table"       // optional override; defaults to dynamodbConfig.tableName
//	}
//
// Supported operations: GetItem, PutItem, DeleteItem, Query, Scan.
func (h *Handler) resolveDynamoDBDataSource(ctx context.Context, ds *DataSource, requestTemplate string, rctx *resolveContext) (any, *GraphQLError) {
	if h.dynamoInvoker == nil {
		return nil, &GraphQLError{Message: "DynamoDB invocation not available (DynamoDB service not enabled)"}
	}

	// Parse the DynamoDB config to get the default table name.
	var dynamoCfg struct {
		TableName string `json:"tableName"`
	}
	if err := json.Unmarshal(ds.DynamodbConfig, &dynamoCfg); err != nil || dynamoCfg.TableName == "" {
		return nil, &GraphQLError{Message: "AMAZON_DYNAMODB data source missing tableName in dynamodbConfig"}
	}

	// Parse the request mapping template.
	var tmpl dynamoDBTemplate
	if requestTemplate == "" {
		return nil, &GraphQLError{Message: "AMAZON_DYNAMODB resolver requires a requestMappingTemplate"}
	}
	if err := json.Unmarshal([]byte(requestTemplate), &tmpl); err != nil {
		return nil, &GraphQLError{Message: "invalid DynamoDB request mapping template: " + err.Error()}
	}
	if tmpl.Operation == "" {
		return nil, &GraphQLError{Message: "DynamoDB request mapping template missing 'operation'"}
	}

	// Use the template table override, or fall back to the data source config.
	tableName := tmpl.Table
	if tableName == "" {
		tableName = dynamoCfg.TableName
	}

	// Build arguments, substituting $context.arguments values into the template.
	tmpl.substituteArguments(rctx.Arguments)

	return h.executeDynamoDBOperation(ctx, tmpl.Operation, tableName, &tmpl)
}

// dynamoDBTemplate represents the parsed requestMappingTemplate for a DynamoDB resolver.
type dynamoDBTemplate struct {
	Operation string `json:"operation"`
	Table     string `json:"table,omitempty"`

	// GetItem / DeleteItem / PutItem
	Key  map[string]any `json:"key,omitempty"`
	Item map[string]any `json:"item,omitempty"`

	// PutItem / DeleteItem / UpdateItem
	ConditionExpression       string            `json:"condition,omitempty"`
	ExpressionAttributeNames  map[string]string `json:"expressionNames,omitempty"`
	ExpressionAttributeValues map[string]any    `json:"expressionValues,omitempty"`

	// Query / Scan
	KeyConditionExpression string `json:"query,omitempty"`
	FilterExpression       string `json:"filter,omitempty"`
	IndexName              string `json:"index,omitempty"`
	Limit                  int    `json:"limit,omitempty"`
	ScanIndexForward       *bool  `json:"scanIndexForward,omitempty"`
	ProjectionExpression   string `json:"projection,omitempty"`

	// UpdateItem
	UpdateExpression string `json:"update,omitempty"`

	// BatchGetItem: map of table name → { keys: [...] }
	Tables map[string]batchTableSpec `json:"tables,omitempty"`

	// TransactGetItems / TransactWriteItems
	TransactItems []transactItemSpec `json:"transactItems,omitempty"`
}

// batchTableSpec describes one table's participation in a BatchGetItem or BatchWriteItem.
type batchTableSpec struct {
	// BatchGetItem: list of key maps to fetch.
	Keys []map[string]any `json:"keys,omitempty"`
	// BatchWriteItem: items to put.
	PutRequest []map[string]any `json:"putRequest,omitempty"`
	// BatchWriteItem: keys of items to delete.
	DeleteRequest []map[string]any `json:"deleteRequest,omitempty"`
}

// transactItemSpec is a single item in a TransactGetItems or TransactWriteItems list.
type transactItemSpec struct {
	// Table overrides the data source default table.
	Table string `json:"table,omitempty"`
	// For TransactGetItems.
	Key map[string]any `json:"key,omitempty"`
	// For TransactWriteItems: "PutItem", "DeleteItem", "UpdateItem", "ConditionCheck".
	Operation string `json:"operation,omitempty"`
	// PutItem item body.
	Item map[string]any `json:"item,omitempty"`
	// Optional condition expression.
	ConditionExpression string            `json:"condition,omitempty"`
	ExpressionNames     map[string]string `json:"expressionNames,omitempty"`
	ExpressionValues    map[string]any    `json:"expressionValues,omitempty"`
}

// substituteArguments replaces string values of the form "$context.arguments.X"
// in the template Key and Item maps with actual argument values, converted to
// DynamoDB attribute value format.
func (t *dynamoDBTemplate) substituteArguments(args map[string]any) {
	if args == nil {
		return
	}
	substituteMap(t.Key, args)
	substituteMap(t.Item, args)
	substituteAttrValues(t.ExpressionAttributeValues, args)
}

// substituteMap replaces "$context.arguments.X" references in a DynamoDB
// attribute value map with the resolved argument converted to DynamoDB JSON.
func substituteMap(m map[string]any, args map[string]any) {
	for k, v := range m {
		typed, ok := v.(map[string]any)
		if !ok {
			continue
		}
		// Check each DynamoDB type descriptor for a $context.arguments reference.
		for typKey, typVal := range typed {
			s, ok := typVal.(string)
			if !ok {
				continue
			}
			if !strings.HasPrefix(s, "$context.arguments.") {
				continue
			}
			argName := strings.TrimPrefix(s, "$context.arguments.")
			if argVal, found := args[argName]; found {
				typed[typKey] = argVal
				m[k] = typed
			}
		}
	}
}

// substituteAttrValues replaces "$context.arguments.X" references in expression
// attribute values with the resolved argument converted to DynamoDB JSON.
func substituteAttrValues(m map[string]any, args map[string]any) {
	for k, v := range m {
		typed, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for typKey, typVal := range typed {
			s, ok := typVal.(string)
			if !ok {
				continue
			}
			if !strings.HasPrefix(s, "$context.arguments.") {
				continue
			}
			argName := strings.TrimPrefix(s, "$context.arguments.")
			if argVal, found := args[argName]; found {
				typed[typKey] = argVal
				m[k] = typed
			}
		}
	}
}

// executeDynamoDBOperation builds and dispatches the DynamoDB JSON wire request.
func (h *Handler) executeDynamoDBOperation(ctx context.Context, operation, tableName string, tmpl *dynamoDBTemplate) (any, *GraphQLError) {
	var ddbOp string
	var reqBody any

	switch operation {
	case "GetItem":
		ddbOp = "GetItem"
		reqBody = map[string]any{
			"TableName": tableName,
			"Key":       tmpl.Key,
		}
		if tmpl.ProjectionExpression != "" {
			reqBody.(map[string]any)["ProjectionExpression"] = tmpl.ProjectionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			reqBody.(map[string]any)["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}

	case "PutItem":
		ddbOp = "PutItem"
		item := tmpl.Item
		if item == nil {
			item = map[string]any{}
		}
		// Merge key into item (PutItem requires the full item including keys).
		for k, v := range tmpl.Key {
			if _, exists := item[k]; !exists {
				item[k] = v
			}
		}
		req := map[string]any{
			"TableName": tableName,
			"Item":      item,
		}
		if tmpl.ConditionExpression != "" {
			req["ConditionExpression"] = tmpl.ConditionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			req["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}
		if len(tmpl.ExpressionAttributeValues) > 0 {
			req["ExpressionAttributeValues"] = tmpl.ExpressionAttributeValues
		}
		reqBody = req

	case "DeleteItem":
		ddbOp = "DeleteItem"
		req := map[string]any{
			"TableName": tableName,
			"Key":       tmpl.Key,
		}
		if tmpl.ConditionExpression != "" {
			req["ConditionExpression"] = tmpl.ConditionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			req["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}
		if len(tmpl.ExpressionAttributeValues) > 0 {
			req["ExpressionAttributeValues"] = tmpl.ExpressionAttributeValues
		}
		reqBody = req

	case "Query":
		ddbOp = "Query"
		req := map[string]any{
			"TableName":                 tableName,
			"KeyConditionExpression":    tmpl.KeyConditionExpression,
			"ExpressionAttributeValues": tmpl.ExpressionAttributeValues,
		}
		if tmpl.FilterExpression != "" {
			req["FilterExpression"] = tmpl.FilterExpression
		}
		if tmpl.IndexName != "" {
			req["IndexName"] = tmpl.IndexName
		}
		if tmpl.Limit > 0 {
			req["Limit"] = tmpl.Limit
		}
		if tmpl.ScanIndexForward != nil {
			req["ScanIndexForward"] = *tmpl.ScanIndexForward
		}
		if tmpl.ProjectionExpression != "" {
			req["ProjectionExpression"] = tmpl.ProjectionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			req["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}
		reqBody = req

	case "Scan":
		ddbOp = "Scan"
		req := map[string]any{
			"TableName": tableName,
		}
		if tmpl.FilterExpression != "" {
			req["FilterExpression"] = tmpl.FilterExpression
		}
		if tmpl.IndexName != "" {
			req["IndexName"] = tmpl.IndexName
		}
		if tmpl.Limit > 0 {
			req["Limit"] = tmpl.Limit
		}
		if tmpl.ProjectionExpression != "" {
			req["ProjectionExpression"] = tmpl.ProjectionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			req["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}
		if len(tmpl.ExpressionAttributeValues) > 0 {
			req["ExpressionAttributeValues"] = tmpl.ExpressionAttributeValues
		}
		reqBody = req

	case "UpdateItem":
		ddbOp = "UpdateItem"
		req := map[string]any{
			"TableName": tableName,
			"Key":       tmpl.Key,
		}
		if tmpl.UpdateExpression != "" {
			req["UpdateExpression"] = tmpl.UpdateExpression
		}
		if tmpl.ConditionExpression != "" {
			req["ConditionExpression"] = tmpl.ConditionExpression
		}
		if len(tmpl.ExpressionAttributeNames) > 0 {
			req["ExpressionAttributeNames"] = tmpl.ExpressionAttributeNames
		}
		if len(tmpl.ExpressionAttributeValues) > 0 {
			req["ExpressionAttributeValues"] = tmpl.ExpressionAttributeValues
		}
		reqBody = req

	case "BatchGetItem":
		// Build RequestItems map: { TableName: { Keys: [...] } }
		requestItems := make(map[string]any, len(tmpl.Tables))
		for tbl, spec := range tmpl.Tables {
			requestItems[tbl] = map[string]any{
				"Keys": spec.Keys,
			}
		}
		ddbOp = "BatchGetItem"
		reqBody = map[string]any{"RequestItems": requestItems}

	case "BatchWriteItem":
		// Build RequestItems map: { TableName: [ {PutRequest:{Item:...}}, ... ] }
		requestItems := make(map[string]any, len(tmpl.Tables))
		for tbl, spec := range tmpl.Tables {
			var writeReqs []map[string]any
			for _, item := range spec.PutRequest {
				writeReqs = append(writeReqs, map[string]any{
					"PutRequest": map[string]any{"Item": item},
				})
			}
			for _, key := range spec.DeleteRequest {
				writeReqs = append(writeReqs, map[string]any{
					"DeleteRequest": map[string]any{"Key": key},
				})
			}
			requestItems[tbl] = writeReqs
		}
		ddbOp = "BatchWriteItem"
		reqBody = map[string]any{"RequestItems": requestItems}

	case "TransactGetItems":
		// Build TransactItems list: [ {Get: {TableName, Key}}, ... ]
		items := make([]map[string]any, 0, len(tmpl.TransactItems))
		for _, ti := range tmpl.TransactItems {
			tbl := ti.Table
			if tbl == "" {
				tbl = tableName
			}
			items = append(items, map[string]any{
				"Get": map[string]any{
					"TableName": tbl,
					"Key":       ti.Key,
				},
			})
		}
		ddbOp = "TransactGetItems"
		reqBody = map[string]any{"TransactItems": items}

	case "TransactWriteItems":
		// Build TransactItems list with Put/Delete/Update/ConditionCheck actions.
		items := make([]map[string]any, 0, len(tmpl.TransactItems))
		for _, ti := range tmpl.TransactItems {
			tbl := ti.Table
			if tbl == "" {
				tbl = tableName
			}
			op := ti.Operation
			if op == "" {
				op = "PutItem"
			}
			action := make(map[string]any)
			action["TableName"] = tbl
			if ti.Key != nil {
				action["Key"] = ti.Key
			}
			if ti.Item != nil {
				// Merge key into item so DynamoDB has the full item.
				item := make(map[string]any, len(ti.Item)+len(ti.Key))
				for k, v := range ti.Item {
					item[k] = v
				}
				for k, v := range ti.Key {
					if _, exists := item[k]; !exists {
						item[k] = v
					}
				}
				action["Item"] = item
			}
			if ti.ConditionExpression != "" {
				action["ConditionExpression"] = ti.ConditionExpression
			}
			if len(ti.ExpressionNames) > 0 {
				action["ExpressionAttributeNames"] = ti.ExpressionNames
			}
			if len(ti.ExpressionValues) > 0 {
				action["ExpressionAttributeValues"] = ti.ExpressionValues
			}
			var actionKey string
			switch op {
			case "PutItem":
				actionKey = "Put"
			case "DeleteItem":
				actionKey = "Delete"
			case "UpdateItem":
				actionKey = "Update"
			case "ConditionCheck":
				actionKey = "ConditionCheck"
			default:
				actionKey = "Put"
			}
			items = append(items, map[string]any{actionKey: action})
		}
		ddbOp = "TransactWriteItems"
		reqBody = map[string]any{"TransactItems": items}

	default:
		return nil, &GraphQLError{Message: "unsupported DynamoDB operation: " + operation}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, &GraphQLError{Message: "failed to marshal DynamoDB request: " + err.Error()}
	}

	respBytes, err := h.dynamoInvoker.Invoke(ctx, ddbOp, payload)
	if err != nil {
		return nil, &GraphQLError{Message: "DynamoDB invocation failed: " + err.Error()}
	}

	return h.parseDynamoDBResponse(operation, respBytes)
}

// parseDynamoDBResponse converts a raw DynamoDB JSON response into a GraphQL-
// friendly value. DynamoDB items use typed attribute values ({"S":"foo"}) which
// are unwrapped into plain Go values for the GraphQL result.
func (h *Handler) parseDynamoDBResponse(operation string, respBytes []byte) (any, *GraphQLError) {
	switch operation {
	case "GetItem":
		var resp struct {
			Item map[string]any `json:"Item"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB GetItem response: " + err.Error()}
		}
		if resp.Item == nil {
			return nil, nil
		}
		return unwrapDynamoDBItem(resp.Item), nil

	case "PutItem":
		// PutItem returns the key attributes — return them unwrapped.
		var resp struct {
			Attributes map[string]any `json:"Attributes"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB PutItem response: " + err.Error()}
		}
		if resp.Attributes != nil {
			return unwrapDynamoDBItem(resp.Attributes), nil
		}
		// PutItem with no ReturnValues returns empty — return the original key/item as confirmation.
		return nil, nil

	case "DeleteItem":
		var resp struct {
			Attributes map[string]any `json:"Attributes"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB DeleteItem response: " + err.Error()}
		}
		if resp.Attributes != nil {
			return unwrapDynamoDBItem(resp.Attributes), nil
		}
		return nil, nil

	case "Query", "Scan":
		var resp struct {
			Items        []map[string]any `json:"Items"`
			Count        int              `json:"Count"`
			ScannedCount int              `json:"ScannedCount"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB " + operation + " response: " + err.Error()}
		}
		items := make([]any, 0, len(resp.Items))
		for _, item := range resp.Items {
			items = append(items, unwrapDynamoDBItem(item))
		}
		return items, nil

	case "UpdateItem":
		var resp struct {
			Attributes map[string]any `json:"Attributes"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB UpdateItem response: " + err.Error()}
		}
		if resp.Attributes != nil {
			return unwrapDynamoDBItem(resp.Attributes), nil
		}
		return nil, nil

	case "BatchGetItem":
		// BatchGetItem response: { "Responses": { "TableName": [ item, ... ] } }
		var resp struct {
			Responses map[string][]map[string]any `json:"Responses"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB BatchGetItem response: " + err.Error()}
		}
		var items []any
		for _, tableItems := range resp.Responses {
			for _, item := range tableItems {
				items = append(items, unwrapDynamoDBItem(item))
			}
		}
		return items, nil

	case "BatchWriteItem":
		// BatchWriteItem returns UnprocessedItems on partial failures; we ignore those here.
		return nil, nil

	case "TransactGetItems":
		// TransactGetItems response: { "Responses": [ { "Item": {...} }, ... ] }
		var resp struct {
			Responses []struct {
				Item map[string]any `json:"Item"`
			} `json:"Responses"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, &GraphQLError{Message: "failed to parse DynamoDB TransactGetItems response: " + err.Error()}
		}
		items := make([]any, 0, len(resp.Responses))
		for _, r := range resp.Responses {
			if r.Item != nil {
				items = append(items, unwrapDynamoDBItem(r.Item))
			}
		}
		return items, nil

	case "TransactWriteItems":
		// TransactWriteItems returns empty on success.
		return nil, nil

	default:
		// Unknown operation — return raw JSON.
		var result any
		if err := json.Unmarshal(respBytes, &result); err != nil {
			return string(respBytes), nil
		}
		return result, nil
	}
}

// unwrapDynamoDBItem converts a DynamoDB-typed attribute map into a plain map.
// DynamoDB JSON: {"name": {"S": "Alice"}, "age": {"N": "30"}} → {"name": "Alice", "age": 30}.
func unwrapDynamoDBItem(item map[string]any) map[string]any {
	result := make(map[string]any, len(item))
	for k, v := range item {
		result[k] = unwrapDynamoDBAttr(v)
	}
	return result
}

// unwrapDynamoDBAttr converts a single DynamoDB typed attribute to a plain value.
func unwrapDynamoDBAttr(v any) any {
	typed, ok := v.(map[string]any)
	if !ok {
		return v
	}

	// String
	if s, ok := typed["S"]; ok {
		return s
	}
	// Number — DynamoDB sends numbers as strings; parse to float64 for JSON.
	if n, ok := typed["N"]; ok {
		if ns, ok := n.(string); ok {
			if f, err := strconv.ParseFloat(ns, 64); err == nil {
				// Return as int if it's a whole number for nicer JSON.
				if f == float64(int64(f)) {
					return int64(f)
				}
				return f
			}
			return ns
		}
		return n
	}
	// Boolean
	if b, ok := typed["BOOL"]; ok {
		return b
	}
	// Null
	if _, ok := typed["NULL"]; ok {
		return nil
	}
	// List
	if l, ok := typed["L"]; ok {
		if list, ok := l.([]any); ok {
			result := make([]any, len(list))
			for i, elem := range list {
				result[i] = unwrapDynamoDBAttr(elem)
			}
			return result
		}
		return l
	}
	// Map
	if m, ok := typed["M"]; ok {
		if mp, ok := m.(map[string]any); ok {
			return unwrapDynamoDBItem(mp)
		}
		return m
	}
	// String Set
	if ss, ok := typed["SS"]; ok {
		return ss
	}
	// Number Set
	if ns, ok := typed["NS"]; ok {
		return ns
	}
	// Binary (return as-is)
	if b, ok := typed["B"]; ok {
		return b
	}
	// Binary Set
	if bs, ok := typed["BS"]; ok {
		return bs
	}

	// Unknown type — return as-is.
	return v
}

// lambdaFunctionNameFromARN extracts the function name from a Lambda ARN.
// Input: "arn:aws:lambda:us-east-1:000000000000:function:my-fn" → "my-fn".
func lambdaFunctionNameFromARN(arn string) string {
	if idx := strings.Index(arn, ":function:"); idx >= 0 {
		name := arn[idx+len(":function:"):]
		if colonIdx := strings.IndexByte(name, ':'); colonIdx >= 0 {
			name = name[:colonIdx]
		}
		return name
	}
	// Fallback: if not an ARN, treat the whole string as a function name.
	return arn
}

// vtlErrorToGraphQL converts a VTL evaluation error into a GraphQLError.
// If the error is a *vtlError (from $util.error), its ErrorType and Data
// are propagated into extensions.errorType and extensions.data respectively.
func vtlErrorToGraphQL(err error, prefix string) *GraphQLError {
	if vtlErr, ok := err.(*vtlError); ok {
		gqlErr := &GraphQLError{Message: vtlErr.Message}
		if vtlErr.ErrorType != "" || vtlErr.Data != nil {
			gqlErr.Extensions = map[string]any{}
			if vtlErr.ErrorType != "" {
				gqlErr.Extensions["errorType"] = vtlErr.ErrorType
			}
			if vtlErr.Data != nil {
				gqlErr.Extensions["data"] = vtlErr.Data
			}
		}
		return gqlErr
	}
	return &GraphQLError{Message: prefix + ": " + err.Error()}
}

// jsEvalErrorToGraphQL converts a JS EvaluationError into a GraphQLError,
// propagating any errorType and data into the extensions field.
func jsEvalErrorToGraphQL(evalErr *EvaluationError) *GraphQLError {
	gqlErr := &GraphQLError{Message: evalErr.Message}
	if evalErr.ErrorType != "" || evalErr.Data != nil {
		gqlErr.Extensions = map[string]any{}
		if evalErr.ErrorType != "" {
			gqlErr.Extensions["errorType"] = evalErr.ErrorType
		}
		if evalErr.Data != nil {
			gqlErr.Extensions["data"] = evalErr.Data
		}
	}
	return gqlErr
}

// selectionNames returns the list of sub-field names from a field's selection set.
func selectionNames(field *ast.Field) []string {
	if field == nil || len(field.SelectionSet) == 0 {
		return nil
	}
	names := make([]string, 0, len(field.SelectionSet))
	for _, sel := range field.SelectionSet {
		if f, ok := sel.(*ast.Field); ok {
			names = append(names, f.Name)
		}
	}
	return names
}

func (h *Handler) buildDirectLambdaResolverEvent(rctx *resolveContext) map[string]any {
	selectionSetList := selectionNames(rctx.Field)
	if selectionSetList == nil {
		selectionSetList = []string{}
	}
	arguments := rctx.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	variables := rctx.Variables
	if variables == nil {
		variables = map[string]any{}
	}
	headers := flattenHeaders(rctx.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	stash := rctx.Stash
	if stash == nil {
		stash = map[string]any{}
	}

	var domainName any
	if rctx.DomainName != "" {
		domainName = rctx.DomainName
	}

	return map[string]any{
		"arguments": arguments,
		"identity":  publicResolverIdentity(rctx.Identity),
		"source":    rctx.Source,
		"info": map[string]any{
			"fieldName":           rctx.FieldName,
			"parentTypeName":      rctx.ParentType,
			"selectionSetList":    selectionSetList,
			"selectionSetGraphQL": selectionSetToGraphQL(vtlFieldSelectionSet(rctx.Field)),
			"variables":           variables,
		},
		"request": map[string]any{
			"headers":    headers,
			"domainName": domainName,
		},
		"stash": stash,
		"prev":  map[string]any{"result": rctx.PrevResult},
	}
}

func publicResolverIdentity(identity map[string]any) map[string]any {
	if identity == nil {
		return nil
	}
	if _, ok := identity[lambdaAuthorizerDeniedFieldsKey]; !ok {
		return identity
	}
	public := make(map[string]any, len(identity)-1)
	for k, v := range identity {
		if k != lambdaAuthorizerDeniedFieldsKey {
			public[k] = v
		}
	}
	return public
}

func (h *Handler) deniesField(api *GraphqlAPI, identity map[string]any, typeName, fieldName string) bool {
	if identity == nil {
		return false
	}
	raw, ok := identity[lambdaAuthorizerDeniedFieldsKey]
	if !ok {
		return false
	}
	denied, ok := raw.([]string)
	if !ok {
		return false
	}
	short := typeName + "." + fieldName
	suffix := "/types/" + typeName + "/fields/" + fieldName
	full := ""
	if api != nil && api.ApiId != "" {
		region := "us-east-1"
		if h != nil && h.cfg != nil && h.cfg.Region != "" {
			region = h.cfg.Region
		}
		full = "arn:aws:appsync:" + region + ":" + h.accountID() + ":apis/" + api.ApiId + suffix
	}
	for _, item := range denied {
		if item == short || item == full {
			return true
		}
	}
	return false
}

// flattenHeaders converts http.Header (multi-valued) to a single-valued map
// keyed by lowercased header name.
//
// Go's net/http canonicalises incoming header names via
// textproto.CanonicalMIMEHeaderKey, so a client's "x-api-key" arrives in
// r.Header as "X-Api-Key". AWS AppSync does not do that: the documented shape
// of $context.request.headers / ctx.request.headers uses the lowercase name
// (the docs' examples read ctx.request.headers.custom for a "custom:nadia"
// header, and x-api-key is likewise addressed in lowercase). Leaking Go's
// canonical casing would break resolvers written against the AWS docs, so we
// lowercase on the way out.
//
// The cookie header is stripped: "AWS AppSync doesn't expose the cookie header
// in $context.request.headers" (same reference).
//
// Known divergence: for a repeated header we keep only the first value. The AWS
// docs instead describe an array ("You could then access these as an array,
// such as ctx.request.headers.custom[1]"), which would need this map to become
// map[string]any. See docs/plans/header-casing-audit.md.
func flattenHeaders(h http.Header) map[string]string {
	if h == nil {
		return nil
	}
	flat := make(map[string]string, len(h))
	for k, vs := range h {
		if strings.EqualFold(k, "cookie") {
			continue
		}
		if len(vs) > 0 {
			flat[strings.ToLower(k)] = vs[0]
		}
	}
	return flat
}

// filterSubFields filters a resolved value to only include fields requested in
// the GraphQL selection set, while applying Lambda authorizer deniedFields.
func (h *Handler) filterSubFields(result any, field *ast.Field, api *GraphqlAPI, identity map[string]any, parentType string) any {
	if len(field.SelectionSet) == 0 || result == nil {
		return result
	}

	obj, ok := result.(map[string]any)
	if !ok {
		return result
	}

	filtered := make(map[string]any, len(field.SelectionSet))
	for _, sel := range field.SelectionSet {
		subField, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		alias := subField.Alias
		if alias == "" {
			alias = subField.Name
		}
		if h.deniesField(api, identity, parentType, subField.Name) {
			filtered[alias] = nil
			continue
		}
		val, exists := obj[subField.Name]
		if exists {
			filtered[alias] = h.filterSubFields(val, subField, api, identity, fieldTypeName(subField))
		} else {
			filtered[alias] = nil
		}
	}
	return filtered
}

// ─── GraphQL error response helpers ──────────────────────────────────────────

// writeGraphQLErrors writes a GraphQL-style error response with 200 status.
func writeGraphQLErrors(w http.ResponseWriter, r *http.Request, errs []GraphQLError) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"data":   nil,
		"errors": errs,
	})
}

// writeGraphQLAuthError writes an authorization failure on the data-plane
// GraphQL endpoint as AppSync's GraphQL error envelope —
// {"errors":[{"errorType":...,"message":...}]} — with Content-Type:
// application/json and the failure's own HTTP status, instead of the AWS
// JSON error envelope (protocol.WriteJSONError's {"__type":...}) that the
// management-plane /v1/apis operations correctly use. A real AppSync 401
// response for a rejected request carries no "data" key and no
// locations/path — the request never reaches query parsing/execution, so
// there is nothing to attach a location or field path to. The shape is the
// one AWS documents for classified AppSync errors (errorType alongside
// message) in
// https://docs.aws.amazon.com/appsync/latest/devguide/troubleshooting-and-common-mistakes.html,
// applied to the authorization failures described in
// https://docs.aws.amazon.com/appsync/latest/devguide/security-authz.html;
// it is what Amplify's client reads (errors[0].errorType) to detect an auth
// failure, which is how #1732 was found.
func writeGraphQLAuthError(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	// Preserve the request-tracing behavior protocol.WriteJSONError provided:
	// record the AWS error on the response writer before writing the body, so
	// the emulator's debug trace/inbox still shows the classified failure even
	// though the wire body is now the GraphQL envelope rather than protocol's
	// own JSON error envelope.
	if rec, ok := w.(interface{ RecordAWSError(*protocol.AWSError) }); ok {
		rec.RecordAWSError(aerr)
	}
	writeJSON(w, r, aerr.HTTPStatus, map[string]any{
		"errors": []GraphQLError{{Message: aerr.Message, ErrorType: aerr.Code}},
	})
}

// writeGQLValidationErrors converts gqlparser validation errors to a GraphQL error response.
func writeGQLValidationErrors(w http.ResponseWriter, r *http.Request, errs gqlerror.List) {
	gqlErrors := make([]GraphQLError, 0, len(errs))
	for _, e := range errs {
		ge := GraphQLError{Message: e.Message}
		for _, loc := range e.Locations {
			ge.Locations = append(ge.Locations, SourceLocation{Line: loc.Line, Column: loc.Column})
		}
		gqlErrors = append(gqlErrors, ge)
	}
	writeGraphQLErrors(w, r, gqlErrors)
}
