package apigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/state"
)

// routingLambdaInvoker returns a canned response per function name and
// records every invocation (call count and last payload), so a test can
// exercise a chain of two invocations (authorizer, then backend) and assert
// on each independently.
type routingLambdaInvoker struct {
	mu        sync.Mutex
	responses map[string]lambdaInvokeCannedResponse
	calls     map[string]int
	payloads  map[string][]byte
}

type lambdaInvokeCannedResponse struct {
	payload       []byte
	functionError string
}

func (i *routingLambdaInvoker) Invoke(_ context.Context, functionName string, payload []byte) (*events.InvokeOutcome, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.calls == nil {
		i.calls = map[string]int{}
	}
	if i.payloads == nil {
		i.payloads = map[string][]byte{}
	}
	i.calls[functionName]++
	i.payloads[functionName] = append([]byte(nil), payload...)
	resp, ok := i.responses[functionName]
	if !ok {
		return &events.InvokeOutcome{Payload: []byte(`{"statusCode":204}`)}, nil
	}
	return &events.InvokeOutcome{Payload: resp.payload, FunctionError: resp.functionError}, nil
}

func (i *routingLambdaInvoker) callCount(functionName string) int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls[functionName]
}

func (i *routingLambdaInvoker) lastPayload(t *testing.T, functionName string) map[string]any {
	t.Helper()
	i.mu.Lock()
	raw := i.payloads[functionName]
	i.mu.Unlock()
	if raw == nil {
		t.Fatalf("function %q was never invoked", functionName)
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("unmarshal captured payload for %q: %v", functionName, err)
	}
	return event
}

// newTestHandler builds a Handler suitable for exercising the authorizer
// checks directly: cfg is required because executeAPISourceARN (used to
// build methodArn/routeArn) reads h.cfg.Region.
func newTestHandler(invoker events.FunctionSyncInvoker) *Handler {
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	return &Handler{
		invoker:          invoker,
		clk:              clock.NewMock(),
		cfg:              cfg,
		store:            newAPIGatewayStore(state.NewMemoryStore(), cfg.Region),
		lambdaAuthzCache: newLambdaAuthorizerCache(),
	}
}

func allowPolicyPayload(methodArnPlaceholder string) []byte {
	body, _ := json.Marshal(map[string]any{
		"principalId": "user-allow",
		"policyDocument": map[string]any{
			"Version": "2012-10-17",
			"Statement": []map[string]any{{
				"Action":   "execute-api:Invoke",
				"Effect":   "Allow",
				"Resource": methodArnPlaceholder,
			}},
		},
		"context": map[string]any{"stringKey": "value", "numberKey": 1},
	})
	return body
}

func denyPolicyPayload(methodArnPlaceholder string) []byte {
	body, _ := json.Marshal(map[string]any{
		"principalId": "user-deny",
		"policyDocument": map[string]any{
			"Version": "2012-10-17",
			"Statement": []map[string]any{{
				"Action":   "execute-api:Invoke",
				"Effect":   "Deny",
				"Resource": methodArnPlaceholder,
			}},
		},
	})
	return body
}

// ---- REST v1: TOKEN authorizer ---------------------------------------------

func TestCheckRestLambdaAuthorizer_tokenAllow(t *testing.T) {
	// Given: a TOKEN authorizer whose policy allows the wildcard resource.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{
		AuthorizationType: "CUSTOM",
		AuthorizerID:      "auth1",
	}
	if aerr := h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	}); aerr != nil {
		t.Fatalf("seeding authorizer: %v", aerr)
	}

	// When: the method's authorizer is checked.
	nr, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	// Then: the request is authorized, the authorizer received type=TOKEN
	// with the raw header value, and its context is ready to flow downstream.
	if !ok {
		t.Fatalf("expected authorization to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}
	event := invoker.lastPayload(t, "my-authorizer")
	if event["type"] != "TOKEN" {
		t.Fatalf("expected type TOKEN, got %#v", event["type"])
	}
	if event["authorizationToken"] != "secret-token" {
		t.Fatalf("expected authorizationToken %q, got %#v", "secret-token", event["authorizationToken"])
	}
	authCtx, ok := lambdaAuthorizerContextFromRequest(nr)
	if !ok {
		t.Fatalf("expected authorizer context attached to the request")
	}
	if authCtx["principalId"] != "user-allow" || authCtx["stringKey"] != "value" {
		t.Fatalf("expected principalId and context merged, got %#v", authCtx)
	}
}

func TestCheckRestLambdaAuthorizer_tokenDenyAnswers403WithCapitalMessage(t *testing.T) {
	// Given: a TOKEN authorizer whose policy denies the wildcard resource.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: denyPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	})

	// When: the method's authorizer is checked.
	_, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	// Then: AWS's 403, with the capitalised "Message" key this response
	// alone uses.
	if ok {
		t.Fatalf("expected authorization to fail")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	want := `{"Message":"User is not authorized to access this resource with an explicit deny"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("expected body %q, got %q", want, got)
	}
}

func TestCheckRestLambdaAuthorizer_missingIdentitySourceAnswers401WithoutInvoking(t *testing.T) {
	// Given: a TOKEN authorizer whose identitySource header is absent.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil) // no Authorization header

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	})

	// When: the method's authorizer is checked.
	_, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	// Then: 401 Unauthorized, and API Gateway never invokes the function —
	// "If the client's request doesn't include the identity sources, API
	// Gateway doesn't invoke your Lambda authorizer."
	if ok {
		t.Fatalf("expected authorization to fail")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	if got := rec.Body.String(); got != `{"message":"Unauthorized"}` {
		t.Fatalf("expected body %q, got %q", `{"message":"Unauthorized"}`, got)
	}
	if n := invoker.callCount("my-authorizer"); n != 0 {
		t.Fatalf("expected the authorizer to never be invoked, got %d calls", n)
	}
}

func TestCheckRestLambdaAuthorizer_throwsUnauthorizedAnswers401(t *testing.T) {
	// Given: a TOKEN authorizer function that throws exactly "Unauthorized".
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: []byte(`{"errorMessage":"Unauthorized","errorType":"Error"}`), functionError: "Unhandled"},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	})

	_, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	if ok {
		t.Fatalf("expected authorization to fail")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	if got := rec.Body.String(); got != `{"message":"Unauthorized"}` {
		t.Fatalf("expected body %q, got %q", `{"message":"Unauthorized"}`, got)
	}
}

func TestCheckRestLambdaAuthorizer_throwsOtherErrorAnswers500(t *testing.T) {
	// Given: a TOKEN authorizer function that throws an unrelated error.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: []byte(`{"errorMessage":"boom","errorType":"Error"}`), functionError: "Unhandled"},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	})

	_, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	if ok {
		t.Fatalf("expected authorization to fail")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	if got := rec.Body.String(); got != `{"message":"Internal server error"}` {
		t.Fatalf("expected body %q, got %q", `{"message":"Internal server error"}`, got)
	}
}

// ---- REST v1: REQUEST authorizer, caching, context propagation ------------

func TestCheckRestLambdaAuthorizer_requestTypeSendsHeadersAndQuery(t *testing.T) {
	// Given: a REQUEST authorizer.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello?name=world", nil)
	req.Header.Set("HeaderAuth1", "headerValue1")

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "REQUEST", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.HeaderAuth1,method.request.querystring.name",
	})

	_, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)
	if !ok {
		t.Fatalf("expected authorization to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}

	event := invoker.lastPayload(t, "my-authorizer")
	if event["type"] != "REQUEST" {
		t.Fatalf("expected type REQUEST, got %#v", event["type"])
	}
	headers, ok := event["headers"].(map[string]any)
	if !ok || headers["Headerauth1"] != "headerValue1" {
		t.Fatalf("expected headers.Headerauth1, got %#v", event["headers"])
	}
	query, ok := event["queryStringParameters"].(map[string]any)
	if !ok || query["name"] != "world" {
		t.Fatalf("expected queryStringParameters.name, got %#v", event["queryStringParameters"])
	}
}

func TestCheckRestLambdaAuthorizer_cachesWithinTTL(t *testing.T) {
	// Given: a TOKEN authorizer with a 300-second cache TTL.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization", AuthorizerResultTTLInSeconds: 300,
	})

	// When: two requests carry the same identity source value.
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
		req.Header.Set("Authorization", "secret-token")
		if _, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil); !ok {
			t.Fatalf("call %d: expected authorization to succeed", i)
		}
	}

	// Then: the authorizer function is invoked only once.
	if n := invoker.callCount("my-authorizer"); n != 1 {
		t.Fatalf("expected 1 authorizer invocation with caching, got %d", n)
	}
}

func TestCheckRestLambdaAuthorizer_contextFlowsIntoProxyEvent(t *testing.T) {
	// Given: an allowing REQUEST authorizer, and a downstream Lambda proxy
	// integration on the same resource.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	api := &RestAPI{ID: apiID}
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putAuthorizer(context.Background(), apiID, &Authorizer{
		ID: "auth1", Type: "TOKEN", AuthorizerURI: "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "method.request.header.Authorization",
	})
	integration := &Integration{URI: "arn:aws:lambda:us-east-1:000000000000:function:backend"}

	// When: the authorizer is checked, and its (possibly rewritten) request
	// is used to invoke the backend proxy integration.
	nr, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)
	if !ok {
		t.Fatalf("expected authorization to succeed")
	}
	h.executeRestLambdaProxy(rec, nr, api, resource, method, integration, nil, "/hello")

	// Then: the backend's proxy event carries the authorizer's principalId
	// and context under requestContext.authorizer.
	event := invoker.lastPayload(t, "backend")
	reqCtx, ok := event["requestContext"].(map[string]any)
	if !ok {
		t.Fatalf("expected requestContext in the proxy event, got %#v", event["requestContext"])
	}
	authCtx, ok := reqCtx["authorizer"].(map[string]any)
	if !ok {
		t.Fatalf("expected requestContext.authorizer, got %#v", reqCtx["authorizer"])
	}
	if authCtx["principalId"] != "user-allow" || authCtx["stringKey"] != "value" {
		t.Fatalf("expected principalId and context to flow through, got %#v", authCtx)
	}
}

// ---- HTTP v2: REQUEST authorizer, both payload formats ---------------------

func TestCheckV2LambdaAuthorizer_format1IAMPolicyAllow(t *testing.T) {
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	api := &APIV2{ApiID: apiID}
	route := &RouteV2{RouteKey: "GET /hello", AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putV2Authorizer(context.Background(), apiID, &AuthorizerV2{
		AuthorizerID: "auth1", AuthorizerType: "REQUEST",
		AuthorizerURI:  "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "$request.header.Authorization", AuthorizerPayloadFormatVersion: "1.0",
	})

	nr, ok := h.checkV2LambdaAuthorizer(rec, req, apiID, api, route, "/hello", nil)
	if !ok {
		t.Fatalf("expected authorization to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}

	event := invoker.lastPayload(t, "my-authorizer")
	if event["type"] != "REQUEST" {
		t.Fatalf("expected type REQUEST, got %#v", event["type"])
	}
	headers, ok := event["headers"].(map[string]any)
	if !ok || headers["authorization"] != "secret-token" {
		t.Fatalf("expected lowercased headers.authorization, got %#v", event["headers"])
	}
	authCtx, ok := lambdaAuthorizerContextFromRequest(nr)
	if !ok || authCtx["principalId"] != "user-allow" {
		t.Fatalf("expected flat authorizer context, got %#v", authCtx)
	}
}

func TestCheckV2LambdaAuthorizer_format2SimpleResponseDeny(t *testing.T) {
	simple, _ := json.Marshal(map[string]any{
		"isAuthorized": false,
		"context":      map[string]any{"reason": "no"},
	})
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: simple},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	api := &APIV2{ApiID: apiID}
	route := &RouteV2{RouteKey: "GET /hello", AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putV2Authorizer(context.Background(), apiID, &AuthorizerV2{
		AuthorizerID: "auth1", AuthorizerType: "REQUEST",
		AuthorizerURI:  "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "$request.header.Authorization", AuthorizerPayloadFormatVersion: "2.0",
		EnableSimpleResponses: true,
	})

	_, ok := h.checkV2LambdaAuthorizer(rec, req, apiID, api, route, "/hello", nil)

	if ok {
		t.Fatalf("expected authorization to fail")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	event := invoker.lastPayload(t, "my-authorizer")
	if event["routeArn"] == nil || event["routeArn"] == "" {
		t.Fatalf("expected routeArn on the format-2.0 event, got %#v", event["routeArn"])
	}
}

func TestCheckV2LambdaAuthorizer_format2SimpleResponseAllowContextNestsUnderLambda(t *testing.T) {
	simple, _ := json.Marshal(map[string]any{
		"isAuthorized": true,
		"context":      map[string]any{"stringKey": "value"},
	})
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: simple},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	api := &APIV2{ApiID: apiID}
	route := &RouteV2{RouteKey: "GET /hello", AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putV2Authorizer(context.Background(), apiID, &AuthorizerV2{
		AuthorizerID: "auth1", AuthorizerType: "REQUEST",
		AuthorizerURI:  "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "$request.header.Authorization", AuthorizerPayloadFormatVersion: "2.0",
		EnableSimpleResponses: true,
	})

	nr, ok := h.checkV2LambdaAuthorizer(rec, req, apiID, api, route, "/hello", nil)
	if !ok {
		t.Fatalf("expected authorization to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}

	authCtx, ok := lambdaAuthorizerContextFromRequest(nr)
	if !ok {
		t.Fatalf("expected authorizer context attached")
	}
	lambdaCtx, ok := authCtx["lambda"].(map[string]any)
	if !ok {
		t.Fatalf("expected format-2.0 context nested under \"lambda\", got %#v", authCtx)
	}
	if lambdaCtx["stringKey"] != "value" {
		t.Fatalf("expected stringKey in the nested context, got %#v", lambdaCtx)
	}
}

func TestCheckV2LambdaAuthorizer_format2IAMPolicyWithoutSimpleResponses(t *testing.T) {
	// Given: a format-2.0 authorizer that returns an IAM policy rather than a
	// simple response (EnableSimpleResponses false) — AWS allows both
	// response shapes at format 2.0.
	invoker := &routingLambdaInvoker{responses: map[string]lambdaInvokeCannedResponse{
		"my-authorizer": {payload: allowPolicyPayload("arn:aws:execute-api:*:*:*")},
	}}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)
	req.Header.Set("Authorization", "secret-token")

	apiID := "api123"
	api := &APIV2{ApiID: apiID}
	route := &RouteV2{RouteKey: "GET /hello", AuthorizationType: "CUSTOM", AuthorizerID: "auth1"}
	_ = h.store.putV2Authorizer(context.Background(), apiID, &AuthorizerV2{
		AuthorizerID: "auth1", AuthorizerType: "REQUEST",
		AuthorizerURI:  "arn:aws:lambda:us-east-1:000000000000:function:my-authorizer",
		IdentitySource: "$request.header.Authorization", AuthorizerPayloadFormatVersion: "2.0",
		EnableSimpleResponses: false,
	})

	nr, ok := h.checkV2LambdaAuthorizer(rec, req, apiID, api, route, "/hello", nil)
	if !ok {
		t.Fatalf("expected authorization to succeed, got status %d body %q", rec.Code, rec.Body.String())
	}
	authCtx, ok := lambdaAuthorizerContextFromRequest(nr)
	if !ok {
		t.Fatalf("expected authorizer context attached")
	}
	if _, nested := authCtx["lambda"]; !nested {
		t.Fatalf("expected format-2.0 IAM-policy context to also nest under \"lambda\", got %#v", authCtx)
	}
}

// ---- AWS_IAM: intentionally not enforced here ------------------------------

func TestCheckRestLambdaAuthorizer_awsIAMIsNotEnforced(t *testing.T) {
	// Given: a method whose authorizationType is AWS_IAM — a type this check
	// deliberately does not act on (see the package comment).
	invoker := &routingLambdaInvoker{}
	h := newTestHandler(invoker)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/hello", nil)

	apiID := "api123"
	resource := &Resource{ID: "res123", Path: "/hello"}
	method := &Method{AuthorizationType: "AWS_IAM"}

	nr, ok := h.checkRestLambdaAuthorizer(rec, req, apiID, resource, method, "/hello", nil)

	if !ok {
		t.Fatalf("expected AWS_IAM to pass through unenforced, got status %d", rec.Code)
	}
	if nr != req {
		t.Fatalf("expected the request to be returned unchanged")
	}
	if n := invoker.callCount("my-authorizer"); n != 0 {
		t.Fatalf("expected no Lambda invocation for AWS_IAM, got %d", n)
	}
}
