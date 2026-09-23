package apigateway

// handler_lambda_auth.go — Lambda authorizer enforcement during request
// execution.
//
// Supports:
//   REST v1:  authorizationType CUSTOM, authorizer type TOKEN or REQUEST.
//   HTTP v2:  authorizationType CUSTOM, authorizer type REQUEST (HTTP APIs
//             have no Lambda TOKEN authorizer — only JWT and Lambda REQUEST).
//
// Both TOKEN and REQUEST authorizers, and both HTTP API authorizer payload
// format versions, return either an IAM policy ({principalId, policyDocument,
// context}) or — payload format 2.0 only, and only when the authorizer has
// EnableSimpleResponses — a simple response ({isAuthorized, context}). An IAM
// policy is evaluated with the same allow/deny algorithm the rest of Overcast
// uses for identity policies (internal/iampolicy), against the single
// synthetic "execute-api:Invoke" action on the request's methodArn/routeArn —
// exactly the shape AWS documents authorizer-returned policies as using.
//
// Verified against AWS docs (2026-09-23):
//   - https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-use-lambda-authorizer.html
//     (REST v1 TOKEN/REQUEST event and IAM-policy response shapes)
//   - https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-lambda-authorizer.html
//     (HTTP v2 REQUEST event shapes per payload format version, response
//     shapes, and that format 2.0's context lands at requestContext.authorizer.lambda
//     while format 1.0 — like REST v1 — lands flat under requestContext.authorizer)
//
// AWS_IAM is intentionally NOT enforced per-method here. AWS_IAM authorizes a
// SigV4-signed caller's IAM identity against execute-api:Invoke on the
// method's ARN, which needs a verified SigV4 signature; Overcast's existing
// SigV4/IAM posture (internal/middleware.IAMEnforce, gated by
// OVERCAST_ENFORCE_IAM) evaluates policies for AWS control-plane operations
// that the generated operation registry names, and ExecuteRestAPI/ExecuteV2API
// are emulator-only routes with no such name — enforcing AWS_IAM here would
// mean either inventing a second, parallel IAM enforcement path or teaching
// the general one about a route it cannot classify. Per the project's
// project-wide IAM stance (README's "not a security boundary"; IAM policies
// are stored but enforcement is everywhere opt-in and best-effort), AWS_IAM
// methods and routes execute unauthenticated, the same as NONE.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// errAuthorizerUnauthorized is the sentinel invokeLambdaAuthorizer returns
// when the authorizer function itself answered 401 by throwing exactly
// "Unauthorized" — the one error message AWS's default authorizer gateway
// response maps to 401 rather than the general 500 configuration-error
// response. See "You can also directly return {"errorMessage":"Unauthorized"}
// from your Lambda function to return a 401 error to your clients."
// (http-api-lambda-authorizer.html § Identity sources).
var errAuthorizerUnauthorized = errors.New("apigateway: authorizer returned Unauthorized")

// ---- REST v1: TOKEN / REQUEST authorizers ----------------------------------

// checkRestLambdaAuthorizer enforces a CUSTOM (Lambda TOKEN or REQUEST)
// authorizer for a REST v1 method. It returns the request to continue
// processing with — unchanged, or carrying the authorizer's context for the
// downstream proxy event's requestContext.authorizer — and whether execution
// may proceed. On failure it writes the response itself and returns false;
// the caller must stop processing.
func (h *Handler) checkRestLambdaAuthorizer(
	w http.ResponseWriter, r *http.Request,
	apiID string, resource *Resource, method *Method,
	requestPath string, stageVars map[string]string,
) (*http.Request, bool) {
	if method.AuthorizationType != "CUSTOM" || method.AuthorizerID == "" {
		return r, true
	}
	if h.invoker == nil {
		// Lambda service not wired — allow through, matching the Cognito
		// authorizer's permissive degradation in handler_auth.go.
		return r, true
	}
	auth, aerr := h.store.getAuthorizer(r.Context(), apiID, method.AuthorizerID)
	if aerr != nil {
		return r, true
	}

	methodArn := h.executeAPISourceARN(r, apiID, resource.Path)
	values, resolved := identitySourceValues(r, auth.IdentitySource)
	if !resolved {
		writeGatewayError(w, http.StatusUnauthorized, "Unauthorized")
		return r, false
	}

	cachingEnabled := h.lambdaAuthzCache != nil && auth.AuthorizerResultTTLInSeconds > 0 && strings.TrimSpace(auth.IdentitySource) != ""
	cacheKey := lambdaAuthorizerCacheKey(auth.ID, values)
	if cachingEnabled {
		if entry, hit := h.lambdaAuthzCache.get(cacheKey, h.clk.Now()); hit {
			if !entry.allowed {
				writeLambdaAuthorizerDenied(w)
				return r, false
			}
			return attachAuthorizerContext(r, entry.requestCtx), true
		}
	}

	var identityToken string
	if len(values) > 0 {
		identityToken = values[0]
	}

	var payload []byte
	var err error
	if strings.EqualFold(auth.Type, "REQUEST") {
		payload, err = json.Marshal(h.buildRestRequestAuthorizerEvent(r, apiID, resource, methodArn, requestPath, stageVars, identitySourceString(auth.IdentitySource, values)))
	} else {
		payload, err = json.Marshal(tokenAuthorizerEvent{
			Type:               "TOKEN",
			AuthorizationToken: identityToken,
			MethodArn:          methodArn,
		})
	}
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Internal server error")
		return r, false
	}

	functionName := lambdaFunctionNameFromURI(auth.AuthorizerURI)
	allowed, requestCtx, invokeErr := h.invokeLambdaAuthorizer(r.Context(), functionName, payload, methodArn, false)
	if invokeErr != nil {
		writeLambdaAuthorizerInvokeError(w, invokeErr)
		return r, false
	}

	if cachingEnabled {
		h.lambdaAuthzCache.set(cacheKey, allowed, requestCtx, h.clk.Now().Add(time.Duration(auth.AuthorizerResultTTLInSeconds)*time.Second))
	}
	if !allowed {
		writeLambdaAuthorizerDenied(w)
		return r, false
	}
	return attachAuthorizerContext(r, requestCtx), true
}

// buildRestRequestAuthorizerEvent builds the input event for a REST v1
// REQUEST-type Lambda authorizer: the same request-shape fields as the Lambda
// proxy event, plus type/methodArn/identitySource/authorizationToken, and
// without body/isBase64Encoded — REST v1 authorizers never see a body.
func (h *Handler) buildRestRequestAuthorizerEvent(
	r *http.Request, apiID string, resource *Resource, methodArn, requestPath string,
	stageVars map[string]string, identitySource string,
) requestAuthorizerEvent {
	headers, multiHeaders := requestAuthorizerHeaders(r, false)
	query, multiQuery := requestAuthorizerQuery(r)
	pathParams := extractPathParams(resource.Path, requestPath)
	if len(pathParams) == 0 {
		pathParams = nil
	}
	return requestAuthorizerEvent{
		Type:                            "REQUEST",
		MethodArn:                       methodArn,
		IdentitySource:                  identitySource,
		AuthorizationToken:              identitySource,
		Resource:                        resource.Path,
		Path:                            requestPath,
		HTTPMethod:                      r.Method,
		Headers:                         headers,
		MultiValueHeaders:               multiHeaders,
		QueryStringParameters:           query,
		MultiValueQueryStringParameters: multiQuery,
		PathParameters:                  pathParams,
		StageVariables:                  stageVars,
		RequestContext: v1RequestContext{
			AccountID:        h.accountID(),
			APIID:            apiID,
			ResourceID:       resource.ID,
			Stage:            chi.URLParam(r, "stageName"),
			RequestID:        protocol.NewRequestID(),
			Identity:         v1Identity{SourceIP: clientIP(r)},
			HTTPMethod:       r.Method,
			Protocol:         requestProtocol(r),
			Path:             requestPath,
			ResourcePath:     resource.Path,
			RequestTime:      h.clk.Now().Format("02/Jan/2006:15:04:05 +0000"),
			RequestTimeEpoch: h.clk.Now().UnixMilli(),
		},
	}
}

// ---- HTTP v2: REQUEST authorizers ------------------------------------------

// checkV2LambdaAuthorizer enforces a CUSTOM (Lambda REQUEST) authorizer for
// an HTTP v2 route. Same contract as checkRestLambdaAuthorizer.
func (h *Handler) checkV2LambdaAuthorizer(
	w http.ResponseWriter, r *http.Request,
	apiID string, api *APIV2, route *RouteV2,
	requestPath string, stageVars map[string]string,
) (*http.Request, bool) {
	if route.AuthorizationType != "CUSTOM" || route.AuthorizerID == "" {
		return r, true
	}
	if h.invoker == nil {
		return r, true
	}
	auth, aerr := h.store.getV2Authorizer(r.Context(), apiID, route.AuthorizerID)
	if aerr != nil {
		return r, true
	}

	methodArn := h.executeAPISourceARN(r, apiID, requestPath)
	values, resolved := identitySourceValues(r, auth.IdentitySource)
	if !resolved {
		writeGatewayError(w, http.StatusUnauthorized, "Unauthorized")
		return r, false
	}

	cachingEnabled := h.lambdaAuthzCache != nil && auth.AuthorizerResultTTLInSeconds > 0 && strings.TrimSpace(auth.IdentitySource) != ""
	cacheKey := lambdaAuthorizerCacheKey(auth.AuthorizerID, values)
	if cachingEnabled {
		if entry, hit := h.lambdaAuthzCache.get(cacheKey, h.clk.Now()); hit {
			if !entry.allowed {
				writeLambdaAuthorizerDenied(w)
				return r, false
			}
			return attachAuthorizerContext(r, entry.requestCtx), true
		}
	}

	// Format 2.0 is a distinct event shape (routeArn, no multiValueHeaders,
	// identitySource as a list) from format 1.0's REST-compatible shape.
	// Unspecified defaults to "1.0" — CreateAuthorizer requires the field
	// explicitly outside the console, so an authorizer this store already
	// holds without one predates that requirement.
	isFormat2 := auth.AuthorizerPayloadFormatVersion == "2.0"
	simpleResponse := isFormat2 && auth.EnableSimpleResponses

	var payload []byte
	var err error
	if isFormat2 {
		payload, err = json.Marshal(h.buildV2RequestAuthorizerEventFormat2(r, api, route, methodArn, requestPath, stageVars, values))
	} else {
		payload, err = json.Marshal(h.buildV2RequestAuthorizerEventFormat1(r, api, route, methodArn, requestPath, stageVars, identitySourceString(auth.IdentitySource, values)))
	}
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Internal server error")
		return r, false
	}

	functionName := lambdaFunctionNameFromURI(auth.AuthorizerURI)
	allowed, requestCtx, invokeErr := h.invokeLambdaAuthorizer(r.Context(), functionName, payload, methodArn, simpleResponse)
	if invokeErr != nil {
		writeLambdaAuthorizerInvokeError(w, invokeErr)
		return r, false
	}

	if isFormat2 {
		// Format 2.0's context always lands nested under "lambda" —
		// requestContext.authorizer.lambda — for both response shapes.
		// See http-api-lambda-authorizer.html and the package comment above.
		requestCtx = map[string]any{"lambda": requestCtx["context"]}
	}

	if cachingEnabled {
		h.lambdaAuthzCache.set(cacheKey, allowed, requestCtx, h.clk.Now().Add(time.Duration(auth.AuthorizerResultTTLInSeconds)*time.Second))
	}
	if !allowed {
		writeLambdaAuthorizerDenied(w)
		return r, false
	}
	return attachAuthorizerContext(r, requestCtx), true
}

// buildV2RequestAuthorizerEventFormat1 builds the payload-format-1.0 input
// event: the REST-compatible shape (methodArn, resource/path/httpMethod,
// lowercased headers per HTTP API convention).
func (h *Handler) buildV2RequestAuthorizerEventFormat1(
	r *http.Request, api *APIV2, route *RouteV2, methodArn, requestPath string,
	stageVars map[string]string, identitySource string,
) requestAuthorizerEvent {
	headers, multiHeaders := requestAuthorizerHeaders(r, true)
	query, multiQuery := requestAuthorizerQuery(r)
	pathParams := extractV2PathParams(route.RouteKey, requestPath)
	return requestAuthorizerEvent{
		Type:                            "REQUEST",
		MethodArn:                       methodArn,
		IdentitySource:                  identitySource,
		AuthorizationToken:              identitySource,
		Resource:                        route.RouteKey,
		Path:                            requestPath,
		HTTPMethod:                      r.Method,
		Headers:                         headers,
		MultiValueHeaders:               multiHeaders,
		QueryStringParameters:           query,
		MultiValueQueryStringParameters: multiQuery,
		PathParameters:                  pathParams,
		StageVariables:                  stageVars,
		RequestContext: v1RequestContext{
			AccountID:        h.accountID(),
			APIID:            api.ApiID,
			Stage:            chi.URLParam(r, "stageName"),
			RequestID:        protocol.NewRequestID(),
			Identity:         v1Identity{SourceIP: clientIP(r)},
			HTTPMethod:       r.Method,
			Protocol:         requestProtocol(r),
			Path:             requestPath,
			ResourcePath:     route.RouteKey,
			RequestTime:      h.clk.Now().Format("02/Jan/2006:15:04:05 +0000"),
			RequestTimeEpoch: h.clk.Now().UnixMilli(),
		},
	}
}

// requestAuthorizerEventV2 is the payload-format-2.0 input event for an HTTP
// v2 REQUEST authorizer: the same shape as the payload-format-2.0 Lambda
// proxy event, plus type/routeArn/identitySource, minus body/isBase64Encoded.
type requestAuthorizerEventV2 struct {
	Version               string            `json:"version"`
	Type                  string            `json:"type"`
	RouteArn              string            `json:"routeArn"`
	IdentitySource        []string          `json:"identitySource,omitempty"`
	RouteKey              string            `json:"routeKey"`
	RawPath               string            `json:"rawPath"`
	RawQueryString        string            `json:"rawQueryString"`
	Headers               map[string]string `json:"headers"`
	QueryStringParameters map[string]string `json:"queryStringParameters,omitempty"`
	PathParameters        map[string]string `json:"pathParameters,omitempty"`
	StageVariables        map[string]string `json:"stageVariables,omitempty"`
	Cookies               []string          `json:"cookies,omitempty"`
	RequestContext        v2RequestContext  `json:"requestContext"`
}

func (h *Handler) buildV2RequestAuthorizerEventFormat2(
	r *http.Request, api *APIV2, route *RouteV2, routeArn, requestPath string,
	stageVars map[string]string, identitySource []string,
) requestAuthorizerEventV2 {
	headers := make(map[string]string, len(r.Header))
	for k, vals := range r.Header {
		headers[strings.ToLower(k)] = strings.Join(vals, ",")
	}
	queryParams := make(map[string]string)
	for k, vals := range r.URL.Query() {
		queryParams[k] = strings.Join(vals, ",")
	}
	cookies := make([]string, 0)
	for _, c := range r.Cookies() {
		cookies = append(cookies, c.String())
	}
	pathParams := extractV2PathParams(route.RouteKey, requestPath)

	return requestAuthorizerEventV2{
		Version:               "2.0",
		Type:                  "REQUEST",
		RouteArn:              routeArn,
		IdentitySource:        identitySource,
		RouteKey:              route.RouteKey,
		RawPath:               requestPath,
		RawQueryString:        r.URL.RawQuery,
		Headers:               headers,
		QueryStringParameters: queryParams,
		PathParameters:        pathParams,
		StageVariables:        stageVars,
		Cookies:               cookies,
		RequestContext: v2RequestContext{
			AccountID:    h.accountID(),
			APIID:        api.ApiID,
			DomainName:   serviceutil.FoldHostname(r.Host),
			DomainPrefix: serviceutil.DomainPrefix(r.Host),
			HTTP: v2HTTP{
				Method:    r.Method,
				Path:      requestPath,
				Protocol:  requestProtocol(r),
				SourceIP:  clientIP(r),
				UserAgent: r.Header.Get("User-Agent"),
			},
			RequestID: protocol.NewRequestID(),
			RouteKey:  route.RouteKey,
			Stage:     chi.URLParam(r, "stageName"),
			Time:      h.clk.Now().Format("02/Jan/2006:15:04:05 +0000"),
			TimeEpoch: h.clk.Now().UnixMilli(),
		},
	}
}

// ---- Shared: invocation, response evaluation, and caching -----------------

// tokenAuthorizerEvent is the input event for a TOKEN-type Lambda authorizer
// (REST v1 only — HTTP APIs have no TOKEN authorizer type).
type tokenAuthorizerEvent struct {
	Type               string `json:"type"`
	AuthorizationToken string `json:"authorizationToken"`
	MethodArn          string `json:"methodArn"`
}

// requestAuthorizerEvent is the input event for a REQUEST-type Lambda
// authorizer using the REST-compatible shape: REST v1 always, and HTTP v2
// when its authorizer uses payload format 1.0.
type requestAuthorizerEvent struct {
	Type                            string              `json:"type"`
	MethodArn                       string              `json:"methodArn"`
	IdentitySource                  string              `json:"identitySource,omitempty"`
	AuthorizationToken              string              `json:"authorizationToken,omitempty"`
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders,omitempty"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters,omitempty"`
	PathParameters                  map[string]string   `json:"pathParameters"`
	StageVariables                  map[string]string   `json:"stageVariables"`
	RequestContext                  v1RequestContext    `json:"requestContext"`
}

// lambdaAuthorizerIAMResponse is the {principalId, policyDocument, context}
// response shape TOKEN/REQUEST authorizers return: REST v1 always, and HTTP
// v2 whenever the authorizer isn't using a payload-format-2.0 simple response.
type lambdaAuthorizerIAMResponse struct {
	PrincipalID    string          `json:"principalId"`
	PolicyDocument json.RawMessage `json:"policyDocument"`
	Context        map[string]any  `json:"context,omitempty"`
}

// lambdaAuthorizerSimpleResponse is the {isAuthorized, context} response
// shape available only to HTTP v2 payload-format-2.0 authorizers with
// EnableSimpleResponses set.
type lambdaAuthorizerSimpleResponse struct {
	IsAuthorized bool           `json:"isAuthorized"`
	Context      map[string]any `json:"context,omitempty"`
}

// invokeLambdaAuthorizer invokes the authorizer function and interprets its
// response, returning whether the request is allowed and the context map to
// place at requestContext.authorizer (already principalId-merged for an IAM
// policy response; the caller re-shapes it under "lambda" for HTTP v2 format
// 2.0 — see checkV2LambdaAuthorizer).
//
// simpleResponse selects the response contract: false expects an IAM policy
// (REST TOKEN/REQUEST, and HTTP v2 REQUEST without EnableSimpleResponses);
// true expects the format-2.0 simple response.
func (h *Handler) invokeLambdaAuthorizer(
	ctx context.Context, functionName string, payload []byte, methodArn string, simpleResponse bool,
) (allowed bool, requestCtx map[string]any, err error) {
	outcome, invokeErr := h.invoker.Invoke(ctx, functionName, payload)
	if invokeErr != nil {
		return false, nil, fmt.Errorf("authorizer invocation failed: %w", invokeErr)
	}
	if outcome == nil {
		return false, nil, errors.New("authorizer function not available")
	}
	if outcome.FunctionError != "" {
		if authorizerThrewUnauthorized(outcome.Payload) {
			return false, nil, errAuthorizerUnauthorized
		}
		return false, nil, fmt.Errorf("authorizer function error: %s", outcome.FunctionError)
	}

	if simpleResponse {
		var resp lambdaAuthorizerSimpleResponse
		if err := json.Unmarshal(outcome.Payload, &resp); err != nil {
			return false, nil, fmt.Errorf("malformed authorizer response: %w", err)
		}
		return resp.IsAuthorized, map[string]any{"context": resp.Context}, nil
	}

	var resp lambdaAuthorizerIAMResponse
	if err := json.Unmarshal(outcome.Payload, &resp); err != nil {
		return false, nil, fmt.Errorf("malformed authorizer response: %w", err)
	}
	if len(resp.PolicyDocument) == 0 {
		return false, nil, errors.New("authorizer response has no policyDocument")
	}
	stmts, perr := iampolicy.ParseDocument(string(resp.PolicyDocument), iampolicy.SourceRef{Type: iampolicy.SourceTypeInputPolicy, ID: "authorizer"})
	if perr != nil {
		return false, nil, fmt.Errorf("malformed authorizer policyDocument: %w", perr)
	}
	result := iampolicy.Evaluate(iampolicy.Input{
		Request:  iampolicy.Request{Action: "execute-api:Invoke", Resource: methodArn},
		Identity: stmts,
	})
	requestCtx = map[string]any{"context": mergeAuthorizerContext(resp.PrincipalID, resp.Context)}
	return result.Decision == iampolicy.DecisionAllowed, requestCtx, nil
}

// authorizerThrewUnauthorized reports whether the Lambda function's error
// payload is exactly the standard error shape carrying errorMessage
// "Unauthorized" — the one authorizer error AWS's default gateway response
// maps to 401 rather than 500.
func authorizerThrewUnauthorized(payload []byte) bool {
	var lambdaErr struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(payload, &lambdaErr); err != nil {
		return false
	}
	return lambdaErr.ErrorMessage == "Unauthorized"
}

// mergeAuthorizerContext builds the flat {principalId, ...context} map that
// requestContext.authorizer carries for REST v1 and HTTP v2 payload format
// 1.0.
func mergeAuthorizerContext(principalID string, ctx map[string]any) map[string]any {
	merged := make(map[string]any, len(ctx)+1)
	if principalID != "" {
		merged["principalId"] = principalID
	}
	for k, v := range ctx {
		merged[k] = v
	}
	return merged
}

// writeLambdaAuthorizerInvokeError answers 401 for an authorizer that threw
// exactly "Unauthorized", and 500 for anything else API Gateway cannot invoke
// or parse: an unreachable function, a runtime error with any other message,
// or a response in the wrong shape.
//
// Verified against AWS docs (http-api-lambda-authorizer.html §
// Troubleshooting): "If API Gateway can't invoke your Lambda authorizer, or
// your Lambda authorizer returns a response in an invalid format, clients
// receive a 500 Internal Server Error".
func writeLambdaAuthorizerInvokeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAuthorizerUnauthorized) {
		writeGatewayError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	writeGatewayError(w, http.StatusInternalServerError, "Internal server error")
}

// writeLambdaAuthorizerDenied writes API Gateway's 403 for an authorizer
// policy that denies (explicitly, or by no statement matching the
// methodArn/routeArn — AWS's own wording covers both under "explicit deny").
// Note the capitalised "Message" key: this response uses a different
// envelope from every other 4xx/5xx in this package, which all use lowercase
// "message".
func writeLambdaAuthorizerDenied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	body, _ := json.Marshal(struct {
		Message string `json:"Message"`
	}{Message: "User is not authorized to access this resource with an explicit deny"})
	_, _ = w.Write(body)
}

// ---- Identity source resolution ---------------------------------------------

type identitySourceKind int

const (
	identitySourceHeader identitySourceKind = iota
	identitySourceQuery
)

// identitySourceValues resolves every entry of a comma-separated
// identitySource expression to its concrete request value. ok is false when
// any entry cannot be resolved (the named header or query parameter is
// absent) — API Gateway does not invoke the authorizer function in that case
// and the client gets 401 directly ("If the client's request doesn't include
// the identity sources, API Gateway doesn't invoke your Lambda authorizer,
// and the client receives a 401 error.")
//
//	REST v1 formats:  "method.request.header.X", "method.request.querystring.X"
//	HTTP v2 formats:  "$request.header.x", "$request.querystring.x"
//
// An unrecognised expression form (a context/stage-variable identity source,
// which AWS also allows) is skipped rather than failing the request — it
// contributes nothing to the cache key or the token/event fields Overcast
// builds, but does not block an otherwise satisfiable identity source list.
// A TOKEN authorizer's identitySource always names exactly one header.
func identitySourceValues(r *http.Request, identitySource string) (values []string, ok bool) {
	identitySource = strings.TrimSpace(identitySource)
	if identitySource == "" {
		return nil, true
	}
	for _, part := range strings.Split(identitySource, ",") {
		part = strings.TrimSpace(part)
		name, kind, recognised := parseIdentitySourceExpression(part)
		if !recognised {
			continue
		}
		var value string
		switch kind {
		case identitySourceHeader:
			value = r.Header.Get(name)
		case identitySourceQuery:
			value = r.URL.Query().Get(name)
		}
		if value == "" {
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func parseIdentitySourceExpression(expr string) (name string, kind identitySourceKind, ok bool) {
	switch {
	case strings.HasPrefix(expr, "method.request.header."):
		return expr[len("method.request.header."):], identitySourceHeader, true
	case strings.HasPrefix(expr, "method.request.querystring."):
		return expr[len("method.request.querystring."):], identitySourceQuery, true
	case strings.HasPrefix(expr, "$request.header."):
		return expr[len("$request.header."):], identitySourceHeader, true
	case strings.HasPrefix(expr, "$request.querystring."):
		return expr[len("$request.querystring."):], identitySourceQuery, true
	}
	return "", 0, false
}

// identitySourceString renders the resolved identity source values the way
// AWS's own authorizationToken/identitySource event fields do: comma-joined,
// falling back to the raw configured expression when nothing resolved (an
// empty or unresolvable identitySource, which authorizers may legitimately
// have).
func identitySourceString(configured string, values []string) string {
	if len(values) == 0 {
		return configured
	}
	return strings.Join(values, ",")
}

// requestAuthorizerHeaders mirrors the header handling of the Lambda proxy
// event builders (executeRestLambdaProxy / executeV2LambdaProxy): last value
// wins in the single-valued map, every value is kept in the multi-valued one,
// and HTTP API v2 (lowercase=true) folds header names to lowercase.
func requestAuthorizerHeaders(r *http.Request, lowercase bool) (map[string]string, map[string][]string) {
	headers := make(map[string]string, len(r.Header))
	multi := make(map[string][]string, len(r.Header))
	for k, vals := range r.Header {
		key := k
		if lowercase {
			key = strings.ToLower(k)
		}
		headers[key] = vals[len(vals)-1]
		multi[key] = vals
	}
	return headers, multi
}

// requestAuthorizerQuery mirrors the query-string handling of the Lambda
// proxy event builders: last value wins in the single-valued map, every value
// is kept in the multi-valued one, both nil when there is no query string.
func requestAuthorizerQuery(r *http.Request) (map[string]string, map[string][]string) {
	rawQuery := r.URL.Query()
	if len(rawQuery) == 0 {
		return nil, nil
	}
	single := make(map[string]string, len(rawQuery))
	multi := make(map[string][]string, len(rawQuery))
	for k, vals := range rawQuery {
		single[k] = vals[len(vals)-1]
		multi[k] = vals
	}
	return single, multi
}

// ---- requestContext.authorizer attachment ----------------------------------

// lambdaAuthorizerContextKey is the request-context key an allowed
// authorizer's context map is stashed under, for the downstream Lambda proxy
// event builder to pick up. Unexported empty-struct type: standard Go
// context-key idiom, collides with nothing else in this package.
type lambdaAuthorizerContextKey struct{}

// attachAuthorizerContext returns r carrying ctx for the downstream proxy
// event to read via lambdaAuthorizerContextFromRequest. Returns r unchanged
// when ctx is empty, so a route with no authorizer configured allocates
// nothing extra on the request path.
func attachAuthorizerContext(r *http.Request, ctx map[string]any) *http.Request {
	if len(ctx) == 0 {
		return r
	}
	// Unwrap the invocation helpers' {"context": ...} / {"lambda": ...}
	// envelope: the value stored here is exactly what
	// v1RequestContext.Authorizer / v2RequestContext.Authorizer serialise.
	var flat map[string]any
	if inner, ok := ctx["context"].(map[string]any); ok {
		flat = inner
	} else {
		flat = ctx
	}
	return r.WithContext(context.WithValue(r.Context(), lambdaAuthorizerContextKey{}, flat))
}

// lambdaAuthorizerContextFromRequest reads back what attachAuthorizerContext
// stored, for the Lambda proxy event builders to place at
// requestContext.authorizer.
func lambdaAuthorizerContextFromRequest(r *http.Request) (map[string]any, bool) {
	ctx, ok := r.Context().Value(lambdaAuthorizerContextKey{}).(map[string]any)
	return ctx, ok
}

// ---- Result caching ---------------------------------------------------------

// lambdaAuthorizerCache holds authorizer decisions keyed on (authorizer ID,
// resolved identity source values), matching AWS's own cache key: "API
// Gateway uses the authorizer's identity sources as the cache key" — the
// same cached decision serves every route/method that uses the authorizer,
// which is why the key carries no route or method.
//
// No background eviction: entries are checked against their expiry lazily on
// read, matching usage.go's in-memory, no-goroutine style for the same
// per-handler-instance data (see handler.go's "no background goroutine"
// note on usageTracker).
type lambdaAuthorizerCache struct {
	mu      sync.Mutex
	entries map[string]lambdaAuthorizerCacheEntry
}

type lambdaAuthorizerCacheEntry struct {
	allowed    bool
	requestCtx map[string]any
	expiresAt  time.Time
}

func newLambdaAuthorizerCache() *lambdaAuthorizerCache {
	return &lambdaAuthorizerCache{entries: make(map[string]lambdaAuthorizerCacheEntry)}
}

func (c *lambdaAuthorizerCache) get(key string, now time.Time) (lambdaAuthorizerCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expiresAt) {
		return lambdaAuthorizerCacheEntry{}, false
	}
	return entry, true
}

func (c *lambdaAuthorizerCache) set(key string, allowed bool, requestCtx map[string]any, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = lambdaAuthorizerCacheEntry{allowed: allowed, requestCtx: requestCtx, expiresAt: expiresAt}
}

func lambdaAuthorizerCacheKey(authorizerID string, identityValues []string) string {
	return authorizerID + "\x1f" + strings.Join(identityValues, "\x1f")
}
