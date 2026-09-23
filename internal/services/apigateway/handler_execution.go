package apigateway

// handler_execution.go — API Gateway request execution engine.
//
// This file implements the actual API gateway proxy: incoming HTTP requests
// are matched against configured resources/routes, the backend integration
// is invoked (Lambda proxy, MOCK), and the response is written back.
//
// Execution routes:
//   REST v1: /restapis/{restApiId}/{stageName}/_user_request_/{path}
//   HTTP v2: /_overcast/apigateway/connections/{apiId}/{stageName}/{path}
//
// Supported integration types:
//   AWS_PROXY  — Lambda proxy integration (v1 and v2)
//   AWS        — Lambda non-proxy integration (v1)
//   HTTP_PROXY — HTTP proxy integration (v1 and v2)
//   HTTP       — HTTP integration (v1, passthrough)
//   MOCK       — returns a static response based on integration responses
//
// TODO(priority:P3): implement VTL request/response template mapping

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// proxyHTTPClient is used for HTTP_PROXY integrations. A 30-second timeout
// prevents unresponsive backends from holding a goroutine forever.
var proxyHTTPClient = &http.Client{Timeout: 30 * time.Second}

// ---- REST API v1 execution ------------------------------------------------

// ExecuteRestAPI handles incoming requests to a deployed REST API stage.
// Route: /restapis/{restApiId}/{stageName}/_user_request_/*.
func (h *Handler) ExecuteRestAPI(w http.ResponseWriter, r *http.Request) {
	// Collection disabled: skip the status-capturing writer allocation and
	// every clock read below entirely — matches the plan's "disabled
	// collection adds no allocations" acceptance criterion (see
	// metrics_apigateway.go's benchmarks).
	var sw *statusCapturingResponseWriter
	var start time.Time
	var apiName, stageName, resourcePath string
	var integrationLatency time.Duration
	if h.metrics != nil {
		start = h.clk.Now()
		sw = &statusCapturingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		w = sw
		defer func() {
			h.recordRestAPIOutcome(r.Context(), apiName, stageName, resourcePath, r.Method, sw.status, h.clk.Now().Sub(start), integrationLatency)
		}()
	}

	apiID := chi.URLParam(r, "restApiId")
	requestPath := chi.URLParam(r, "*")
	if requestPath == "" {
		requestPath = "/"
	} else if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}

	// 1. Verify API exists. Path-style invoke URLs (
	//   /restapis/{id}/{stage}/_user_request_/...) carry no region hint —
	// no SigV4, no Host. If the API isn't in the request's region, fall back
	// to a cross-region scan and re-bind the request context to the resolved
	// region so all subsequent store reads (stage, resources, methods,
	// integrations, API keys, usage plans) see the same partition.
	api, aerr := h.store.getRestAPI(r.Context(), apiID)
	if aerr != nil {
		if region := h.store.findRestAPIRegion(r.Context(), apiID); region != "" {
			r = r.WithContext(middleware.ContextWithRegion(r.Context(), region))
			api, aerr = h.store.getRestAPI(r.Context(), apiID)
		}
	}
	if aerr != nil {
		writeGatewayError(w, http.StatusForbidden, "Forbidden")
		return
	}
	apiName = api.Name

	// 1b. Load stage for stage variables.
	stageName = chi.URLParam(r, "stageName")
	var stageVars map[string]string
	stage, serr := h.store.getStage(r.Context(), apiID, stageName)
	if serr == nil && stage != nil {
		stageVars = stage.Variables
	}

	// 2. Find matching resource by path. Served from the route cache — this
	// runs per proxied request, and the resource tree only moves on writes.
	resources, aerr := h.store.listResourcesCached(r.Context(), api.ID)
	if aerr != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	resource := matchResource(resources, requestPath)
	if resource == nil {
		writeGatewayError(w, http.StatusForbidden, "Missing Authentication Token")
		return
	}
	resourcePath = resource.Path

	// 3. Find matching method.
	method, ok := resource.ResourceMethods[r.Method]
	if !ok {
		// Try ANY fallback.
		method, ok = resource.ResourceMethods["ANY"]
	}
	if !ok {
		writeGatewayError(w, http.StatusForbidden, "Missing Authentication Token")
		return
	}

	// 3b. Enforce Cognito authorizer before forwarding to the integration.
	if !h.checkRestCognitoAuthorizer(w, r, apiID, method) {
		return
	}

	// 3b-ii. Enforce a Lambda TOKEN/REQUEST authorizer (authorizationType
	// CUSTOM). AWS_IAM methods are intentionally not enforced here — see the
	// package comment in handler_lambda_auth.go.
	nr, authorized := h.checkRestLambdaAuthorizer(w, r, apiID, resource, method, requestPath, stageVars)
	if !authorized {
		return
	}
	r = nr

	// 3c. Enforce API key requirement (apiKeyRequired=true on the method).
	// AWS responds with 403 Forbidden when the x-api-key header is missing,
	// invalid, disabled, or not associated (via a usage plan) with this stage.
	if method.APIKeyRequired {
		if !h.checkAPIKey(w, r, apiID, stageName) {
			return
		}
	}

	// 4. Get integration.
	integration := method.MethodIntegration
	if integration == nil {
		writeGatewayError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	// 5. Substitute stage variables in integration URI.
	effectiveIntegration := integration
	if len(stageVars) > 0 && integration.URI != "" {
		resolved := substituteStageVars(integration.URI, stageVars)
		if resolved != integration.URI {
			// Copy to avoid mutating the stored integration.
			cp := *integration
			cp.URI = resolved
			effectiveIntegration = &cp
		}
	}

	// 6. Dispatch to integration.
	var integrationStart time.Time
	if h.metrics != nil {
		integrationStart = h.clk.Now()
	}
	switch effectiveIntegration.Type {
	case "AWS_PROXY":
		h.executeRestLambdaProxy(w, r, api, resource, method, effectiveIntegration, stageVars, requestPath)
	case "AWS":
		h.executeRestLambdaNonProxy(w, r, api, effectiveIntegration, requestPath)
	case "HTTP_PROXY":
		h.executeHTTPProxy(w, r, effectiveIntegration)
	case "HTTP":
		h.executeHTTPProxy(w, r, effectiveIntegration)
	case "MOCK":
		h.executeRestMock(w, integration)
	default:
		writeGatewayError(w, http.StatusInternalServerError,
			fmt.Sprintf("Integration type %s not yet supported", integration.Type))
		return
	}
	if h.metrics != nil {
		integrationLatency = h.clk.Now().Sub(integrationStart)
	}
}

// executeRestLambdaProxy builds a Lambda proxy event (v1) and invokes the function.
func (h *Handler) executeRestLambdaProxy(
	w http.ResponseWriter, r *http.Request,
	api *RestAPI, resource *Resource, _ *Method,
	integration *Integration, stageVars map[string]string, requestPath string,
) {
	log := h.log.WithRecorder(r.Context())
	if h.invoker == nil {
		writeGatewayError(w, http.StatusServiceUnavailable, "Lambda service not available")
		return
	}

	functionName := lambdaFunctionNameFromURI(integration.URI)
	if functionName == "" {
		writeGatewayError(w, http.StatusInternalServerError, "Invalid integration URI")
		return
	}

	// Build the API Gateway v1 proxy event.
	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024)) // 6 MiB payload limit
	if err != nil {
		writeGatewayError(w, http.StatusBadRequest, "Could not read request body")
		return
	}

	// REST proxy events carry the LAST value of a repeated name in `headers` /
	// `queryStringParameters` and every value in the multiValue* maps — see the
	// documented input format, where `-H 'header2: value1' -H 'header2: value2'`
	// yields `"header2": "value2"` alongside `["value1","value2"]`.
	// https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html
	headers := make(map[string]string, len(r.Header))
	multiValueHeaders := make(map[string][]string, len(r.Header))
	for k, vals := range r.Header {
		headers[k] = vals[len(vals)-1]
		multiValueHeaders[k] = vals
	}

	var queryParams map[string]string
	var multiValueQueryParams map[string][]string
	if rawQuery := r.URL.Query(); len(rawQuery) > 0 {
		queryParams = make(map[string]string, len(rawQuery))
		multiValueQueryParams = make(map[string][]string, len(rawQuery))
		for k, vals := range rawQuery {
			queryParams[k] = vals[len(vals)-1]
			multiValueQueryParams[k] = vals
		}
	}

	pathParams := extractPathParams(resource.Path, requestPath)
	if len(pathParams) == 0 {
		pathParams = nil
	}

	// REST APIs base64-encode the request body into the proxy event only when
	// the request's Content-Type matches one of the API's configured
	// binaryMediaTypes — see api-gateway-payload-encodings-workflow.html.
	// Without a match the body is passed through as a UTF-8 string, as it was
	// before binaryMediaTypes support existed here.
	isBinaryRequest := matchesBinaryMediaType(r.Header.Get("Content-Type"), api.BinaryMediaTypes)

	reqCtx := v1RequestContext{
		AccountID:        h.accountID(),
		APIID:            api.ID,
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
	}
	if authCtx, ok := lambdaAuthorizerContextFromRequest(r); ok {
		reqCtx.Authorizer = authCtx
	}

	proxyEvent := lambdaV1ProxyEvent{
		Resource:                        resource.Path,
		Path:                            requestPath,
		HTTPMethod:                      r.Method,
		Headers:                         headers,
		MultiValueHeaders:               multiValueHeaders,
		QueryStringParameters:           queryParams,
		MultiValueQueryStringParameters: multiValueQueryParams,
		PathParameters:                  pathParams,
		StageVariables:                  stageVars,
		RequestContext:                  reqCtx,
		Body:                            proxyEventBody(body, isBinaryRequest),
		IsBase64Encoded:                 isBinaryRequest,
	}

	payload, err := json.Marshal(proxyEvent)
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Failed to build proxy event")
		return
	}

	if !h.authorizeLambdaIntegration(w, r, functionName, api.ID, resource.Path) {
		return
	}

	outcome, err := h.invoker.Invoke(r.Context(), functionName, payload)
	if err != nil {
		log.Error("lambda invocation failed",
			zap.String("function", functionName),
			zap.Error(err),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}
	if outcome == nil {
		log.Warn("lambda function not available",
			zap.String("function", functionName),
		)
		writeGatewayError(w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}

	if outcome.FunctionError != "" {
		log.Warn("lambda function error",
			zap.String("function", functionName),
			zap.String("error", outcome.FunctionError),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}

	var proxyResp lambdaProxyResponse
	if err := json.Unmarshal(outcome.Payload, &proxyResp); err != nil {
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}

	if !writeLambdaProxyResponse(w, &proxyResp) {
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
	}
}

// executeRestMock returns a response based on integration response configuration.
func (h *Handler) executeRestMock(w http.ResponseWriter, integration *Integration) {
	statusCode := 200
	body := ""

	if len(integration.IntegrationResponses) > 0 {
		for sc, iresp := range integration.IntegrationResponses {
			statusCode = parseStatusCode(sc)
			if tmpl, ok := iresp.ResponseTemplates["application/json"]; ok {
				body = tmpl
			}
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(body))
}

// ---- HTTP API v2 execution ------------------------------------------------

// ExecuteV2API handles incoming requests to a deployed HTTP API stage.
func (h *Handler) ExecuteV2API(w http.ResponseWriter, r *http.Request) {
	// Collection disabled: skip the allocation/clock reads — see
	// ExecuteRestAPI's identical guard.
	var sw *statusCapturingResponseWriter
	var start time.Time
	var stageName, routeKey string
	var integrationLatency time.Duration
	apiID := chi.URLParam(r, "apiId")
	if h.metrics != nil {
		start = h.clk.Now()
		sw = &statusCapturingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		w = sw
		defer func() {
			h.recordV2APIOutcome(r.Context(), apiID, stageName, routeKey, r.Method, sw.status, h.clk.Now().Sub(start), integrationLatency)
		}()
	}

	requestPath := chi.URLParam(r, "*")
	if requestPath == "" {
		requestPath = "/"
	} else if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}

	// 1. Verify API exists. See ExecuteRestAPI for the cross-region rationale.
	api, aerr := h.store.getV2API(r.Context(), apiID)
	if aerr != nil {
		if region := h.store.findV2APIRegion(r.Context(), apiID); region != "" {
			r = r.WithContext(middleware.ContextWithRegion(r.Context(), region))
			api, aerr = h.store.getV2API(r.Context(), apiID)
		}
	}
	if aerr != nil {
		writeGatewayError(w, http.StatusNotFound, "Not Found")
		return
	}

	// 1b. Load stage for stage variables.
	stageName = chi.URLParam(r, "stageName")
	var stageVars map[string]string
	stg, serr := h.store.getV2Stage(r.Context(), apiID, stageName)
	if serr == nil && stg != nil {
		stageVars = stg.StageVariables
	}

	// 2. Find matching route. Served from the route cache — this runs per
	// proxied request, and routes only move on writes.
	routes, aerr := h.store.listV2RoutesCached(r.Context(), api.ApiID)
	if aerr != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	route := matchV2Route(routes, r.Method, requestPath)
	if route == nil {
		writeGatewayError(w, http.StatusNotFound, "Not Found")
		return
	}
	routeKey = route.RouteKey

	// 2b. Enforce JWT authorizer before forwarding to the integration.
	if !h.checkV2JWTAuthorizer(w, r, apiID, route) {
		return
	}

	// 2c. Enforce a Lambda REQUEST authorizer (authorizationType CUSTOM).
	// AWS_IAM routes are intentionally not enforced here — see the package
	// comment in handler_lambda_auth.go.
	nr, authorized := h.checkV2LambdaAuthorizer(w, r, apiID, api, route, requestPath, stageVars)
	if !authorized {
		return
	}
	r = nr

	// 3. Resolve integration.
	var integrationID string
	if strings.HasPrefix(route.Target, "integrations/") {
		integrationID = strings.TrimPrefix(route.Target, "integrations/")
	}

	if integrationID == "" {
		writeGatewayError(w, http.StatusInternalServerError, "No integration configured")
		return
	}

	integ, aerr := h.store.getV2Integration(r.Context(), apiID, integrationID)
	if aerr != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Integration not found")
		return
	}

	// 4. Substitute stage variables in integration URI.
	if len(stageVars) > 0 && integ.IntegrationURI != "" {
		resolved := substituteStageVars(integ.IntegrationURI, stageVars)
		if resolved != integ.IntegrationURI {
			cp := *integ
			cp.IntegrationURI = resolved
			integ = &cp
		}
	}

	// 5. Dispatch by integration type.
	var integrationStart time.Time
	if h.metrics != nil {
		integrationStart = h.clk.Now()
	}
	switch integ.IntegrationType {
	case "AWS_PROXY":
		h.executeV2LambdaProxy(w, r, api, route, integ, stageVars, requestPath)
	case "HTTP_PROXY":
		h.executeV2HTTPProxy(w, r, integ)
	default:
		writeGatewayError(w, http.StatusInternalServerError,
			fmt.Sprintf("Integration type %s not yet supported", integ.IntegrationType))
		return
	}
	if h.metrics != nil {
		integrationLatency = h.clk.Now().Sub(integrationStart)
	}
}

// executeV2LambdaProxy builds a Lambda proxy event (v2 / payload format 2.0) and invokes.
func (h *Handler) executeV2LambdaProxy(
	w http.ResponseWriter, r *http.Request,
	api *APIV2, route *RouteV2, integ *IntegrationV2,
	stageVars map[string]string, requestPath string,
) {
	log := h.log.WithRecorder(r.Context())
	if h.invoker == nil {
		writeGatewayError(w, http.StatusServiceUnavailable, "Lambda service not available")
		return
	}

	functionName := lambdaFunctionNameFromURI(integ.IntegrationURI)
	if functionName == "" {
		writeGatewayError(w, http.StatusInternalServerError, "Invalid integration URI")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024))
	if err != nil {
		writeGatewayError(w, http.StatusBadRequest, "Could not read request body")
		return
	}

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

	// HTTP APIs need no binaryMediaTypes configuration: any request body whose
	// Content-Type isn't recognised as text is base64-encoded into the proxy
	// event with isBase64Encoded set true. See "Handling binary data using
	// Amazon API Gateway HTTP APIs" (AWS Compute Blog) — content-type driven
	// binary detection, no opt-in list required.
	isBinaryRequest := !isTextContentType(r.Header.Get("Content-Type"))

	var payload []byte
	if integ.PayloadFormatVersion == "2.0" {
		reqCtx := v2RequestContext{
			AccountID: h.accountID(),
			APIID:     api.ApiID,
			// Folded: a Host is case-insensitive, so the domain reported to
			// handler code must not vary with how the caller typed it.
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
		}
		if authCtx, ok := lambdaAuthorizerContextFromRequest(r); ok {
			reqCtx.Authorizer = authCtx
		}
		event := lambdaV2ProxyEvent{
			Version:               "2.0",
			RouteKey:              route.RouteKey,
			RawPath:               requestPath,
			RawQueryString:        r.URL.RawQuery,
			Headers:               headers,
			QueryStringParameters: queryParams,
			PathParameters:        pathParams,
			StageVariables:        stageVars,
			Cookies:               cookies,
			RequestContext:        reqCtx,
			Body:                  proxyEventBody(body, isBinaryRequest),
			IsBase64Encoded:       isBinaryRequest,
		}
		payload, err = json.Marshal(event)
	} else {
		// Default to 1.0 format (same shape as REST v1 proxy event). HTTP APIs
		// lowercase header names in BOTH payload formats ("All headernames are
		// lowercased" — http-api-develop-integrations-lambda.html), and format
		// 1.0 puts a single value in `headers`/`queryStringParameters` with the
		// full list in the multiValue* maps, rather than the comma-joined form
		// that format 2.0 uses.
		queryParamsMulti := make(map[string][]string)
		queryParamsV1 := make(map[string]string)
		for k, vals := range r.URL.Query() {
			queryParamsMulti[k] = vals
			queryParamsV1[k] = vals[len(vals)-1]
		}
		if len(queryParamsV1) == 0 {
			queryParamsV1 = nil
			queryParamsMulti = nil
		}
		headersMulti := make(map[string][]string, len(r.Header))
		headersV1 := make(map[string]string, len(r.Header))
		for k, vals := range r.Header {
			lower := strings.ToLower(k)
			headersMulti[lower] = vals
			headersV1[lower] = vals[len(vals)-1]
		}
		reqCtx := v1RequestContext{
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
		}
		if authCtx, ok := lambdaAuthorizerContextFromRequest(r); ok {
			reqCtx.Authorizer = authCtx
		}
		event := lambdaV1ProxyEvent{
			Resource:                        route.RouteKey,
			Path:                            requestPath,
			HTTPMethod:                      r.Method,
			Headers:                         headersV1,
			MultiValueHeaders:               headersMulti,
			QueryStringParameters:           queryParamsV1,
			MultiValueQueryStringParameters: queryParamsMulti,
			PathParameters:                  pathParams,
			StageVariables:                  stageVars,
			RequestContext:                  reqCtx,
			Body:                            proxyEventBody(body, isBinaryRequest),
			IsBase64Encoded:                 isBinaryRequest,
		}
		payload, err = json.Marshal(event)
	}

	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "Failed to build proxy event")
		return
	}

	if !h.authorizeLambdaIntegration(w, r, functionName, api.ApiID, requestPath) {
		return
	}

	outcome, invokeErr := h.invoker.Invoke(r.Context(), functionName, payload)
	if invokeErr != nil {
		log.Error("lambda invocation failed",
			zap.String("function", functionName),
			zap.Error(invokeErr),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}
	if outcome == nil {
		log.Warn("lambda function not available",
			zap.String("function", functionName),
		)
		writeGatewayError(w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}

	if outcome.FunctionError != "" {
		log.Warn("lambda function error",
			zap.String("function", functionName),
			zap.String("error", outcome.FunctionError),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}

	var proxyResp lambdaProxyResponse
	if err := json.Unmarshal(outcome.Payload, &proxyResp); err != nil {
		// Payload format 2.0 allows simple string responses.
		if integ.PayloadFormatVersion == "2.0" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(outcome.Payload)
			return
		}
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}

	if !writeLambdaProxyResponse(w, &proxyResp) {
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
	}
}

// proxyEventBody renders the request body for a Lambda proxy event. When
// base64Encoded is true (the request's Content-Type was classified as
// binary — see matchesBinaryMediaType / isTextContentType) the bytes are
// base64-encoded, matching what AWS places in the event when isBase64Encoded
// is true. An empty body is always encoded as JSON null, not an empty string.
func proxyEventBody(body []byte, base64Encoded bool) *string {
	if len(body) == 0 {
		return nil
	}
	if base64Encoded {
		s := base64.StdEncoding.EncodeToString(body)
		return &s
	}
	s := string(body)
	return &s
}

// matchesBinaryMediaType reports whether contentType matches one of a REST
// API's configured binaryMediaTypes entries, honouring AWS's wildcard forms
// ("*/*", "image/*"). A request whose Content-Type matches is base64-encoded
// into the Lambda proxy event body with isBase64Encoded set true.
//
// Per AWS docs (api-gateway-payload-encodings-workflow.html): "Binary data,
// A binary data type, Set with matching media types, Undefined -> Binary
// data" — the encoded-for-Lambda column of that table is the base64 form,
// which is how binary payloads always cross the JSON proxy-event boundary.
func matchesBinaryMediaType(contentType string, binaryMediaTypes []string) bool {
	if contentType == "" || len(binaryMediaTypes) == 0 {
		return false
	}
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.TrimSpace(mediaType)
	for _, bmt := range binaryMediaTypes {
		if bmt == "*/*" || bmt == mediaType {
			return true
		}
		if prefix, ok := strings.CutSuffix(bmt, "/*"); ok && strings.HasPrefix(mediaType, prefix+"/") {
			return true
		}
	}
	return false
}

// isTextContentType reports whether contentType is one of the media types
// HTTP APIs (v2) treat as text, passed through in the Lambda proxy event body
// without base64 encoding. Unlike REST APIs, HTTP APIs need no
// binaryMediaTypes configuration: any Content-Type not recognised as text is
// treated as binary and base64-encoded, per "Handling binary data using
// Amazon API Gateway HTTP APIs" (AWS Compute Blog) — content-type driven
// binary detection. A request with no Content-Type at all is treated as text,
// matching the pre-existing behaviour for a body sent without one.
func isTextContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-www-form-urlencoded", "application/ld+json",
		"application/xhtml+xml", "application/graphql":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

// ---- Event types ----------------------------------------------------------

// lambdaV1ProxyEvent is the API Gateway REST (v1) Lambda proxy request format.
type lambdaV1ProxyEvent struct {
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	PathParameters                  map[string]string   `json:"pathParameters"`
	StageVariables                  map[string]string   `json:"stageVariables"`
	RequestContext                  v1RequestContext    `json:"requestContext"`
	Body                            *string             `json:"body"`
	IsBase64Encoded                 bool                `json:"isBase64Encoded"`
}

type v1RequestContext struct {
	AccountID        string     `json:"accountId"`
	APIID            string     `json:"apiId"`
	ResourceID       string     `json:"resourceId,omitempty"`
	Stage            string     `json:"stage"`
	RequestID        string     `json:"requestId"`
	Identity         v1Identity `json:"identity"`
	HTTPMethod       string     `json:"httpMethod"`
	Protocol         string     `json:"protocol"`
	Path             string     `json:"path"`
	ResourcePath     string     `json:"resourcePath"`
	RequestTime      string     `json:"requestTime"`
	RequestTimeEpoch int64      `json:"requestTimeEpoch"`
	// Authorizer carries a Lambda TOKEN/REQUEST authorizer's principalId and
	// custom context (flattened, per AWS's documented shape) — nil unless
	// checkRestLambdaAuthorizer / checkV2LambdaAuthorizer (payload format 1.0)
	// allowed the request. See handler_lambda_auth.go.
	Authorizer map[string]any `json:"authorizer,omitempty"`
}

type v1Identity struct {
	SourceIP string `json:"sourceIp"`
}

// lambdaV2ProxyEvent is the API Gateway HTTP API (v2) payload format 2.0.
type lambdaV2ProxyEvent struct {
	Version               string            `json:"version"`
	RouteKey              string            `json:"routeKey"`
	RawPath               string            `json:"rawPath"`
	RawQueryString        string            `json:"rawQueryString"`
	Headers               map[string]string `json:"headers"`
	QueryStringParameters map[string]string `json:"queryStringParameters,omitempty"`
	PathParameters        map[string]string `json:"pathParameters,omitempty"`
	StageVariables        map[string]string `json:"stageVariables,omitempty"`
	Cookies               []string          `json:"cookies,omitempty"`
	RequestContext        v2RequestContext  `json:"requestContext"`
	Body                  *string           `json:"body,omitempty"`
	IsBase64Encoded       bool              `json:"isBase64Encoded"`
}

type v2RequestContext struct {
	AccountID    string `json:"accountId"`
	APIID        string `json:"apiId"`
	DomainName   string `json:"domainName"`
	DomainPrefix string `json:"domainPrefix"`
	HTTP         v2HTTP `json:"http"`
	RequestID    string `json:"requestId"`
	RouteKey     string `json:"routeKey"`
	Stage        string `json:"stage"`
	Time         string `json:"time"`
	TimeEpoch    int64  `json:"timeEpoch"`
	// Authorizer carries a Lambda REQUEST authorizer's context — nested under
	// "lambda" for payload format 2.0 (either response shape), flat
	// (principalId + context, format 1.0's REST-compatible shape) otherwise.
	// Nil unless checkV2LambdaAuthorizer allowed the request. See
	// handler_lambda_auth.go.
	Authorizer map[string]any `json:"authorizer,omitempty"`
}

type v2HTTP struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Protocol  string `json:"protocol"`
	SourceIP  string `json:"sourceIp"`
	UserAgent string `json:"userAgent"`
}

// clientIP returns the bare IP from r.RemoteAddr (which is "host:port"),
// preferring X-Forwarded-For when present. Falls back to "127.0.0.1" so the
// field is always a valid IP literal — Powertools / pydantic schemas reject
// non-IP values like "[::1]:54321".
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" && net.ParseIP(ip) != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if net.ParseIP(host) != nil {
		return host
	}
	return "127.0.0.1"
}

// requestProtocol normalises r.Proto into the form Powertools/pydantic expects
// (e.g. "HTTP/1.1"). Defaults to "HTTP/1.1" when missing.
func requestProtocol(r *http.Request) string {
	if r.Proto != "" {
		return r.Proto
	}
	return "HTTP/1.1"
}

// lambdaProxyResponse is the unified response format from Lambda proxy integration.
// StatusCode is a pointer so a response that omits the (required) field can
// be told apart from one that explicitly sets it to a value — see
// writeLambdaProxyResponse.
type lambdaProxyResponse struct {
	StatusCode        *int                `json:"statusCode"`
	Headers           map[string]string   `json:"headers,omitempty"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders,omitempty"`
	Body              string              `json:"body,omitempty"`
	IsBase64Encoded   bool                `json:"isBase64Encoded,omitempty"`
	Cookies           []string            `json:"cookies,omitempty"`
}

// ---- Helpers --------------------------------------------------------------

// matchResource finds the resource whose path matches the request path.
// Supports exact matches and simple {proxy+} / {param} patterns.
func matchResource(resources []*Resource, requestPath string) *Resource {
	// First try exact match.
	for _, res := range resources {
		if res.Path == requestPath {
			return res
		}
	}

	// Try parametric matches (e.g. /users/{id}, /{proxy+}).
	var bestMatch *Resource
	bestScore := -1
	for _, res := range resources {
		score := pathMatchScore(res.Path, requestPath)
		if score > bestScore {
			bestScore = score
			bestMatch = res
		}
	}
	if bestScore > 0 {
		return bestMatch
	}
	return nil
}

// pathMatchScore computes a match score between a resource path template and
// a concrete request path. Returns -1 for no match; higher is better.
func pathMatchScore(template, requestPath string) int {
	if template == "/" {
		return 0
	}

	tParts := strings.Split(strings.Trim(template, "/"), "/")
	rParts := strings.Split(strings.Trim(requestPath, "/"), "/")

	score := 0
	for i, tPart := range tParts {
		if strings.HasSuffix(tPart, "+}") {
			// Greedy path parameter — must have at least one remaining segment.
			if i >= len(rParts) || (i == 0 && rParts[0] == "") {
				return -1
			}
			return score + 1
		}
		if i >= len(rParts) {
			return -1
		}
		if strings.HasPrefix(tPart, "{") && strings.HasSuffix(tPart, "}") {
			score++
		} else if tPart == rParts[i] {
			score += 2 // Exact segment match is higher priority.
		} else {
			return -1
		}
	}
	if len(rParts) > len(tParts) {
		return -1
	}
	return score
}

// extractPathParams extracts parameter values from a REST API resource path template.
func extractPathParams(template, requestPath string) map[string]string {
	params := make(map[string]string)
	tParts := strings.Split(strings.Trim(template, "/"), "/")
	rParts := strings.Split(strings.Trim(requestPath, "/"), "/")

	for i, tPart := range tParts {
		if strings.HasPrefix(tPart, "{") && strings.HasSuffix(tPart, "}") {
			paramName := strings.TrimSuffix(strings.TrimPrefix(tPart, "{"), "}")
			paramName = strings.TrimSuffix(paramName, "+")
			if strings.HasSuffix(tPart, "+}") {
				if i < len(rParts) {
					params[paramName] = strings.Join(rParts[i:], "/")
				}
			} else if i < len(rParts) {
				params[paramName] = rParts[i]
			}
		}
	}
	return params
}

// matchV2Route finds the best route for the given method + path among v2 routes.
// AWS prioritises: exact match > most-specific parametric > $default.
func matchV2Route(routes []*RouteV2, method, path string) *RouteV2 {
	methodPath := method + " " + path

	// First try exact match.
	for _, route := range routes {
		if route.RouteKey == methodPath {
			return route
		}
	}

	// Try parametric matches, picking the highest-scoring route.
	var bestRoute *RouteV2
	bestScore := -1
	for _, route := range routes {
		if score := routeV2MatchScore(route.RouteKey, method, path); score > bestScore {
			bestScore = score
			bestRoute = route
		}
	}
	if bestScore > 0 {
		return bestRoute
	}

	// Fallback to $default route.
	for _, route := range routes {
		if route.RouteKey == "$default" {
			return route
		}
	}
	return nil
}

// routeV2Matches checks if a v2 route key (e.g. "GET /users/{id}") matches the request.
//
//nolint:unused // Kept as a small predicate wrapper for route matching callers/tests.
func routeV2Matches(routeKey, method, path string) bool {
	return routeV2MatchScore(routeKey, method, path) > 0
}

// routeV2MatchScore returns a specificity score for how well a v2 route key
// matches the given method + path. Returns -1 for no match; higher is better.
// Exact segments score +2, single path params score +1, greedy {param+} scores +1
// but requires at least one remaining segment.
func routeV2MatchScore(routeKey, method, path string) int {
	parts := strings.SplitN(routeKey, " ", 2)
	if len(parts) != 2 {
		return -1
	}
	routeMethod := parts[0]
	routePath := parts[1]

	if routeMethod != method && routeMethod != "ANY" {
		return -1
	}

	rParts := strings.Split(strings.Trim(routePath, "/"), "/")
	pParts := strings.Split(strings.Trim(path, "/"), "/")

	score := 0
	for i, rp := range rParts {
		if strings.HasSuffix(rp, "+}") {
			// Greedy path parameter — must have at least one remaining segment.
			if i >= len(pParts) || (i == 0 && pParts[0] == "") {
				return -1
			}
			return score + 1
		}
		if i >= len(pParts) {
			return -1
		}
		if strings.HasPrefix(rp, "{") && strings.HasSuffix(rp, "}") {
			score++
		} else if rp == pParts[i] {
			score += 2
		} else {
			return -1
		}
	}
	if len(pParts) != len(rParts) {
		return -1
	}
	return score
}

// extractV2PathParams extracts path parameters from a v2 route key.
func extractV2PathParams(routeKey, requestPath string) map[string]string {
	params := make(map[string]string)
	parts := strings.SplitN(routeKey, " ", 2)
	if len(parts) != 2 {
		return params
	}

	rParts := strings.Split(strings.Trim(parts[1], "/"), "/")
	pParts := strings.Split(strings.Trim(requestPath, "/"), "/")

	for i, rp := range rParts {
		if strings.HasPrefix(rp, "{") && strings.HasSuffix(rp, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(rp, "{"), "}")
			name = strings.TrimSuffix(name, "+")
			if strings.HasSuffix(rp, "+}") && i < len(pParts) {
				params[name] = strings.Join(pParts[i:], "/")
			} else if i < len(pParts) {
				params[name] = pParts[i]
			}
		}
	}
	return params
}

// writeLambdaProxyResponse translates a Lambda proxy response to an HTTP
// response. It returns false — writing nothing to w — when resp is malformed:
// a proxy integration output is required to carry statusCode, and a body
// declared isBase64Encoded must actually be valid base64. The caller must
// then answer AWS's own 502 {"message":"Internal server error"} instead, the
// same response API Gateway gives a client for any other execution failure to
// parse the Lambda output (handle-errors-in-lambda-integration.html).
//
// Verified against AWS docs (http-api-develop-integrations-lambda.html /
// api-gateway-simple-proxy-for-lambda-error-handling): the proxy response
// format is {isBase64Encoded, statusCode, headers, body}, with statusCode
// documented as required — nothing there licenses defaulting a missing one to
// 200, or silently writing an undecodable "base64" body back out raw.
func writeLambdaProxyResponse(w http.ResponseWriter, resp *lambdaProxyResponse) bool {
	if resp.StatusCode == nil {
		return false
	}
	status := *resp.StatusCode
	if status < 100 || status > 599 {
		return false
	}

	var decodedBody []byte
	if resp.IsBase64Encoded && resp.Body != "" {
		decoded, err := base64.StdEncoding.DecodeString(resp.Body)
		if err != nil {
			return false
		}
		decodedBody = decoded
	}

	multiValueKeys := make(map[string]struct{}, len(resp.MultiValueHeaders))
	for k := range resp.MultiValueHeaders {
		multiValueKeys[textproto.CanonicalMIMEHeaderKey(k)] = struct{}{}
	}
	for k, v := range resp.Headers {
		if _, overridden := multiValueKeys[textproto.CanonicalMIMEHeaderKey(k)]; overridden {
			continue
		}
		w.Header().Set(k, v)
	}
	for k, vals := range resp.MultiValueHeaders {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	for _, cookie := range resp.Cookies {
		w.Header().Add("Set-Cookie", cookie)
	}

	w.WriteHeader(status)
	if resp.IsBase64Encoded {
		if len(decodedBody) > 0 {
			_, _ = w.Write(decodedBody)
		}
	} else if resp.Body != "" {
		_, _ = w.Write([]byte(resp.Body))
	}
	return true
}

// writeGatewayError writes a JSON error in the API Gateway error format.
func writeGatewayError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: message})
	_, _ = w.Write(body)
}

// parseStatusCode converts a status code string to int, defaulting to 200.
func parseStatusCode(s string) int {
	if s == "" {
		return 200
	}
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return 200
		}
	}
	if n >= 100 && n <= 599 {
		return n
	}
	return 200
}

// substituteStageVars replaces ${stageVariables.<name>} in a string with actual values.
func substituteStageVars(uri string, vars map[string]string) string {
	result := uri
	for name, value := range vars {
		placeholder := "${stageVariables." + name + "}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// ---- HTTP_PROXY / HTTP integration ----------------------------------------

// executeHTTPProxy makes an outbound HTTP request (REST v1 HTTP_PROXY or HTTP integration).
func (h *Handler) executeHTTPProxy(w http.ResponseWriter, r *http.Request, integration *Integration) {
	log := h.log.WithRecorder(r.Context())
	if integration.URI == "" {
		writeGatewayError(w, http.StatusInternalServerError, "Integration URI not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024))
	if err != nil {
		writeGatewayError(w, http.StatusBadGateway, "Could not read request body")
		return
	}

	method := r.Method
	if integration.HTTPMethod != "" {
		method = integration.HTTPMethod
	}

	outReq, err := http.NewRequestWithContext(r.Context(), method, integration.URI, strings.NewReader(string(body)))
	if err != nil {
		writeGatewayError(w, http.StatusBadGateway, "Invalid integration URI")
		return
	}

	// Forward a subset of request headers.
	for _, hdr := range []string{"Content-Type", "Accept", "Authorization"} {
		if v := r.Header.Get(hdr); v != "" {
			outReq.Header.Set(hdr, v)
		}
	}

	resp, err := proxyHTTPClient.Do(outReq)
	if err != nil {
		log.Error("HTTP_PROXY integration request failed",
			zap.String("uri", integration.URI),
			zap.Error(err),
		)
		writeGatewayError(w, http.StatusBadGateway, "Integration request failed")
		return
	}
	defer resp.Body.Close()

	// Copy response headers.
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// executeV2HTTPProxy makes an outbound HTTP request (HTTP API v2 HTTP_PROXY integration).
func (h *Handler) executeV2HTTPProxy(w http.ResponseWriter, r *http.Request, integ *IntegrationV2) {
	log := h.log.WithRecorder(r.Context())
	if integ.IntegrationURI == "" {
		writeGatewayError(w, http.StatusInternalServerError, "Integration URI not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024))
	if err != nil {
		writeGatewayError(w, http.StatusBadGateway, "Could not read request body")
		return
	}

	method := r.Method
	if integ.IntegrationMethod != "" {
		method = integ.IntegrationMethod
	}

	outReq, err := http.NewRequestWithContext(r.Context(), method, integ.IntegrationURI, strings.NewReader(string(body)))
	if err != nil {
		writeGatewayError(w, http.StatusBadGateway, "Invalid integration URI")
		return
	}

	// Forward request headers.
	for _, hdr := range []string{"Content-Type", "Accept", "Authorization"} {
		if v := r.Header.Get(hdr); v != "" {
			outReq.Header.Set(hdr, v)
		}
	}

	resp, err := proxyHTTPClient.Do(outReq)
	if err != nil {
		log.Error("HTTP_PROXY v2 integration request failed",
			zap.String("uri", integ.IntegrationURI),
			zap.Error(err),
		)
		writeGatewayError(w, http.StatusBadGateway, "Integration request failed")
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ---- AWS integration (non-proxy Lambda) -----------------------------------

// executeRestLambdaNonProxy invokes Lambda directly (AWS integration type),
// passing the body as-is without the proxy event wrapper.
func (h *Handler) executeRestLambdaNonProxy(
	w http.ResponseWriter, r *http.Request,
	api *RestAPI, integration *Integration, requestPath string,
) {
	log := h.log.WithRecorder(r.Context())
	if h.invoker == nil {
		writeGatewayError(w, http.StatusServiceUnavailable, "Lambda service not available")
		return
	}

	functionName := lambdaFunctionNameFromURI(integration.URI)
	if functionName == "" {
		writeGatewayError(w, http.StatusInternalServerError, "Invalid integration URI")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024))
	if err != nil {
		writeGatewayError(w, http.StatusBadRequest, "Could not read request body")
		return
	}

	// For AWS (non-proxy) integration, pass body directly to Lambda.
	// Path parameters are only accessible via VTL mapping templates in real AWS,
	// which are not yet implemented (TODO P3). Until then, path params are
	// unavailable to the Lambda function — matching real AWS behaviour when no
	// mapping template is configured.
	payload := body
	if len(payload) == 0 {
		// Send empty JSON object if body is empty.
		payload = []byte("{}")
	}

	if !h.authorizeLambdaIntegration(w, r, functionName, api.ID, requestPath) {
		return
	}

	outcome, invokeErr := h.invoker.Invoke(r.Context(), functionName, payload)
	if invokeErr != nil {
		log.Error("lambda invocation failed (AWS integration)",
			zap.String("function", functionName),
			zap.Error(invokeErr),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}
	if outcome == nil {
		log.Warn("lambda function not available (AWS integration)",
			zap.String("function", functionName),
		)
		writeGatewayError(w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}

	if outcome.FunctionError != "" {
		log.Warn("lambda function error (AWS integration)",
			zap.String("function", functionName),
			zap.String("error", outcome.FunctionError),
		)
		writeGatewayError(w, http.StatusBadGateway, "Internal server error")
		return
	}

	// For AWS integration, the raw Lambda output is returned.
	// In real AWS, response templates (VTL) would transform this.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(outcome.Payload)
}
