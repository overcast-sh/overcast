package apigateway_test

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ---- REST API v1: CRUD ----------------------------------------------------

func TestCreateRestApi_basic(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we create a REST API
	resp := apiCall(t, srv, http.MethodPost, "/restapis", map[string]any{
		"name":        "test-api",
		"description": "A test REST API",
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	helpers.AssertRequestID(t, resp)

	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "test-api" {
		t.Errorf("expected name %q, got %q", "test-api", result["name"])
	}
	if result["description"] != "A test REST API" {
		t.Errorf("expected description %q, got %q", "A test REST API", result["description"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
	if result["rootResourceId"] == nil || result["rootResourceId"] == "" {
		t.Error("expected non-empty rootResourceId")
	}
	if result["createdDate"] == nil {
		t.Error("expected createdDate to be present")
	}
}

func TestCreateRestApi_missingName(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we create a REST API without a name
	resp := apiCall(t, srv, http.MethodPost, "/restapis", map[string]any{
		"description": "no name",
	})

	// Then it should fail with 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
}

func TestGetRestApi_exists(t *testing.T) {
	// Given a created REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "get-test")

	// When we get it by ID
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID, nil)

	// Then it should return the API
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] != apiID {
		t.Errorf("expected id %q, got %q", apiID, result["id"])
	}
	if result["name"] != "get-test" {
		t.Errorf("expected name %q, got %q", "get-test", result["name"])
	}
}

func TestGetRestApi_notFound(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we get a non-existent API
	resp := apiCall(t, srv, http.MethodGet, "/restapis/nonexistent", nil)

	// Then it should return 404 NotFoundException
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "NotFoundException")
}

func TestGetRestApis_empty(t *testing.T) {
	// Given a running server with no APIs
	srv := helpers.NewTestServer(t)

	// When we list REST APIs
	resp := apiCall(t, srv, http.MethodGet, "/restapis", nil)

	// Then it should return an empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) != 0 {
		t.Errorf("expected empty list, got %d items", len(result.Item))
	}
}

func TestGetRestApis_multiple(t *testing.T) {
	// Given two created REST APIs
	srv := helpers.NewTestServer(t)
	createRestAPI(t, srv, "api-one")
	createRestAPI(t, srv, "api-two")

	// When we list REST APIs
	resp := apiCall(t, srv, http.MethodGet, "/restapis", nil)

	// Then it should return both
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Item))
	}
}

func TestDeleteRestApi_exists(t *testing.T) {
	// Given a created REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "delete-test")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID, nil)

	// Then it should return 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And it should no longer exist
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestUpdateRestApi_basic(t *testing.T) {
	// Given a created REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "update-test")

	// When we update its name
	resp := apiCall(t, srv, http.MethodPatch, "/restapis/"+apiID, map[string]any{
		"patchOperations": []map[string]any{
			{"op": "replace", "path": "/name", "value": "updated-name"},
		},
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "updated-name" {
		t.Errorf("expected updated name %q, got %q", "updated-name", result["name"])
	}
}

// ---- REST API v1: Resources -----------------------------------------------

func TestCreateResource_basic(t *testing.T) {
	// Given a REST API with a root resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "resource-test")

	// When we create a child resource
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/resources/"+rootID, map[string]any{
		"pathPart": "users",
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["pathPart"] != "users" {
		t.Errorf("expected pathPart %q, got %q", "users", result["pathPart"])
	}
	if result["path"] != "/users" {
		t.Errorf("expected path %q, got %q", "/users", result["path"])
	}
}

func TestGetResources_includesRoot(t *testing.T) {
	// Given a REST API with root + 1 child resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "list-resources")
	createResource(t, srv, apiID, rootID, "items")

	// When we list resources
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/resources", nil)

	// Then it should include root and the child
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) != 2 {
		t.Errorf("expected 2 resources (root + child), got %d", len(result.Item))
	}
}

func TestDeleteResource_basic(t *testing.T) {
	// Given a REST API with a child resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "delete-res")
	resID := createResource(t, srv, apiID, rootID, "orders")

	// When we delete the child resource
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID+"/resources/"+resID, nil)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And get should return 404
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/resources/"+resID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: Methods -------------------------------------------------

func TestPutMethod_basic(t *testing.T) {
	// Given a resource on a REST API
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "method-test")
	resID := createResource(t, srv, apiID, rootID, "pets")

	// When we put a GET method
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{
			"authorizationType": "NONE",
		},
	)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["httpMethod"] != "GET" {
		t.Errorf("expected httpMethod %q, got %q", "GET", result["httpMethod"])
	}
	if result["authorizationType"] != "NONE" {
		t.Errorf("expected authorizationType %q, got %q", "NONE", result["authorizationType"])
	}
}

func TestGetMethod_basic(t *testing.T) {
	// Given a method on a resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "getmethod")
	resID := createResource(t, srv, apiID, rootID, "items")
	putMethod(t, srv, apiID, resID, "POST")

	// When we get the method
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST",
		nil,
	)

	// Then it should return the method
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["httpMethod"] != "POST" {
		t.Errorf("expected httpMethod %q, got %q", "POST", result["httpMethod"])
	}
}

// ---- REST API v1: Integrations --------------------------------------------

func TestPutIntegration_mock(t *testing.T) {
	// Given a method on a resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "integ-test")
	resID := createResource(t, srv, apiID, rootID, "mock")
	putMethod(t, srv, apiID, resID, "GET")

	// When we put a MOCK integration
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration",
		map[string]any{
			"type": "MOCK",
		},
	)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["type"] != "MOCK" {
		t.Errorf("expected type %q, got %q", "MOCK", result["type"])
	}
}

func TestPutIntegration_awsProxy(t *testing.T) {
	// Given a method on a resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "lambda-integ")
	resID := createResource(t, srv, apiID, rootID, "proxy")
	putMethod(t, srv, apiID, resID, "ANY")

	// When we put an AWS_PROXY integration
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:my-fn/invocations"
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/ANY/integration",
		map[string]any{
			"type":                  "AWS_PROXY",
			"integrationHttpMethod": "POST",
			"uri":                   uri,
		},
	)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["type"] != "AWS_PROXY" {
		t.Errorf("expected type %q, got %q", "AWS_PROXY", result["type"])
	}
}

// ---- REST API v1: Method / Integration Responses --------------------------

func TestPutMethodResponse_basic(t *testing.T) {
	// Given a method on a resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "method-resp")
	resID := createResource(t, srv, apiID, rootID, "resp")
	putMethod(t, srv, apiID, resID, "GET")

	// When we put a method response for 200
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/responses/200",
		map[string]any{},
	)

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["statusCode"] != "200" {
		t.Errorf("expected statusCode %q, got %v", "200", result["statusCode"])
	}
}

func TestPutIntegrationResponse_basic(t *testing.T) {
	// Given a method with a MOCK integration
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "integ-resp")
	resID := createResource(t, srv, apiID, rootID, "ir")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")

	// When we put an integration response for 200
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration/responses/200",
		map[string]any{
			"responseTemplates": map[string]any{
				"application/json": `{"message":"hello"}`,
			},
		},
	)

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["statusCode"] != "200" {
		t.Errorf("expected statusCode %q, got %v", "200", result["statusCode"])
	}
}

// ---- REST API v1: Stages & Deployments ------------------------------------

func TestCreateDeployment_basic(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "deploy-test")

	// When we create a deployment
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/deployments", map[string]any{
		"description": "first deploy",
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty deployment id")
	}

	// AWS returns createdDate as epoch seconds, not milliseconds. The CLI
	// hard-fails on a milliseconds value ("Unable to calculate correct
	// timezone offset"), so assert the magnitude of the wire value.
	created, ok := result["createdDate"].(float64)
	if !ok {
		t.Fatalf("createdDate should be a number, got %T (%v)", result["createdDate"], result["createdDate"])
	}
	if created > 1e11 {
		t.Errorf("createdDate %.0f looks like milliseconds; AWS returns epoch seconds", created)
	}
	if created < 1e9 {
		t.Errorf("createdDate %.0f is suspiciously small for an epoch-seconds value", created)
	}
}

func TestCreateStage_basic(t *testing.T) {
	// Given a REST API with a deployment
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "stage-test")
	depID := createDeployment(t, srv, apiID)

	// When we create a stage
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/stages", map[string]any{
		"stageName":    "dev",
		"deploymentId": depID,
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["stageName"] != "dev" {
		t.Errorf("expected stageName %q, got %q", "dev", result["stageName"])
	}
}

func TestGetStage_basic(t *testing.T) {
	// Given a REST API with a stage
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "getstage")
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "prod")

	// When we get the stage
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/stages/prod", nil)

	// Then it should return the stage
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["stageName"] != "prod" {
		t.Errorf("expected stageName %q, got %q", "prod", result["stageName"])
	}

	// AWS returns timestamps as epoch seconds (10-digit number), not
	// milliseconds. SDK clients deserialize via Date() and treat the
	// integer as seconds, so a milliseconds value yields year ~58000.
	created, ok := result["createdDate"].(float64)
	if !ok {
		t.Fatalf("createdDate should be a number, got %T (%v)", result["createdDate"], result["createdDate"])
	}
	// A current timestamp in seconds is ~1.7e9; in milliseconds it would
	// be ~1.7e12. Assert we're in the seconds range.
	if created > 1e11 {
		t.Errorf("createdDate %.0f looks like milliseconds; AWS returns epoch seconds", created)
	}
	if created < 1e9 {
		t.Errorf("createdDate %.0f is suspiciously small for an epoch-seconds value", created)
	}
}

func TestDeleteStage_basic(t *testing.T) {
	// Given a REST API with a stage
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "delstage")
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "staging")

	// When we delete the stage
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID+"/stages/staging", nil)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/stages/staging", nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- HTTP API v2: CRUD ----------------------------------------------------

func TestCreateV2Api_basic(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we create an HTTP API
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis", map[string]any{
		"name":         "my-http-api",
		"protocolType": "HTTP",
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	helpers.AssertRequestID(t, resp)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "my-http-api" {
		t.Errorf("expected name %q, got %q", "my-http-api", result["name"])
	}
	if result["protocolType"] != "HTTP" {
		t.Errorf("expected protocolType %q, got %q", "HTTP", result["protocolType"])
	}
	if result["apiId"] == nil || result["apiId"] == "" {
		t.Error("expected non-empty apiId")
	}
}

func TestCreateV2Api_missingFields(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Missing name
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis", map[string]any{
		"protocolType": "HTTP",
	})
	helpers.AssertStatus(t, resp, http.StatusBadRequest)

	// Missing protocolType
	resp = apiCall(t, srv, http.MethodPost, "/v2/apis", map[string]any{
		"name": "test",
	})
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetV2Api_exists(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "get-v2")

	// When we get it
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID, nil)

	// Then it should return the API
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["apiId"] != apiID {
		t.Errorf("expected apiId %q, got %q", apiID, result["apiId"])
	}
}

func TestGetV2Api_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/nonexistent", nil)
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "NotFoundException")
}

func TestGetV2Apis_multiple(t *testing.T) {
	// Given two HTTP APIs
	srv := helpers.NewTestServer(t)
	createV2API(t, srv, "v2-one")
	createV2API(t, srv, "v2-two")

	// When we list them
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []any `json:"items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Items))
	}
}

func TestDeleteV2Api_basic(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "delete-v2")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/v2/apis/"+apiID, nil)

	// Then it should succeed with 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And it should be gone
	resp2 := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- HTTP API v2: Routes --------------------------------------------------

func TestCreateV2Route_basic(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "route-test")

	// When we create a route
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/routes", map[string]any{
		"routeKey": "GET /items",
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["routeKey"] != "GET /items" {
		t.Errorf("expected routeKey %q, got %q", "GET /items", result["routeKey"])
	}
	if result["routeId"] == nil || result["routeId"] == "" {
		t.Error("expected non-empty routeId")
	}
}

func TestGetV2Routes_multiple(t *testing.T) {
	// Given an HTTP API with two routes
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "list-routes")
	createV2Route(t, srv, apiID, "GET /users")
	createV2Route(t, srv, apiID, "POST /users")

	// When we list routes
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/routes", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []any `json:"items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 2 {
		t.Errorf("expected 2 routes, got %d", len(result.Items))
	}
}

func TestDeleteV2Route_basic(t *testing.T) {
	// Given an HTTP API with a route
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "del-route")
	routeID := createV2Route(t, srv, apiID, "GET /health")

	// When we delete the route
	resp := apiCall(t, srv, http.MethodDelete, "/v2/apis/"+apiID+"/routes/"+routeID, nil)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/routes/"+routeID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- HTTP API v2: Integrations --------------------------------------------

func TestCreateV2Integration_basic(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-integ")

	// When we create an integration
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
		"integrationType":      "AWS_PROXY",
		"integrationUri":       "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
		"payloadFormatVersion": "2.0",
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["integrationType"] != "AWS_PROXY" {
		t.Errorf("expected integrationType %q, got %q", "AWS_PROXY", result["integrationType"])
	}
}

// ---- HTTP API v2: Stages & Deployments ------------------------------------

func TestCreateV2Stage_basic(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-stage")

	// When we create a stage
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/stages", map[string]any{
		"stageName": "dev",
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["stageName"] != "dev" {
		t.Errorf("expected stageName %q, got %q", "dev", result["stageName"])
	}
}

func TestGetV2Stages_multiple(t *testing.T) {
	// Given an HTTP API with two stages
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-stages")
	createV2Stage(t, srv, apiID, "dev")
	createV2Stage(t, srv, apiID, "prod")

	// When we list stages
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/stages", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []any `json:"items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 2 {
		t.Errorf("expected 2 stages, got %d", len(result.Items))
	}
}

func TestDeleteV2Stage_basic(t *testing.T) {
	// Given an HTTP API with a stage
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-del-stage")
	createV2Stage(t, srv, apiID, "test")

	// When we delete the stage
	resp := apiCall(t, srv, http.MethodDelete, "/v2/apis/"+apiID+"/stages/test", nil)

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/stages/test", nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: MOCK execution ------------------------------------------

func TestExecuteRestAPI_mockIntegration(t *testing.T) {
	// Given a REST API with a resource, method, MOCK integration, and responses
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "exec-mock")
	resID := createResource(t, srv, apiID, rootID, "hello")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"message":"hello world"}`)

	// Deploy and create a stage
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When we execute a GET request against the deployed API
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/test/_user_request_/hello",
		nil,
	)

	// Then it should return the mock response
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != `{"message":"hello world"}` {
		t.Errorf("expected mock body %q, got %q", `{"message":"hello world"}`, body)
	}
}

func TestExecuteRestAPI_noMatchingResource(t *testing.T) {
	// Given a REST API with only a root resource (no /nonexistent path)
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "exec-nomatch")
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "live")

	// When we try to execute a request to a non-matching path
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/live/_user_request_/nonexistent",
		nil,
	)

	// Then it should return a gateway error (no matching resource)
	// The exact status may vary, but it should not be 200
	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 status for unmatched resource, got %d", resp.StatusCode)
	}
}

// ---- Execution: HTTP_PROXY integration ------------------------------------

func TestExecuteRestAPI_httpProxyIntegration(t *testing.T) {
	// Given an upstream HTTP server that echoes a response
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "true")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"source":"upstream","method":"%s"}`, r.Method)
	}))
	defer upstream.Close()

	// And a REST API with HTTP_PROXY integration pointing to the upstream
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "http-proxy")
	resID := createResource(t, srv, apiID, rootID, "proxy")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegrationWithMethod(t, srv, apiID, resID, "GET", "HTTP_PROXY", upstream.URL, "GET")

	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When we execute a GET request through the proxy
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/test/_user_request_/proxy",
		nil,
	)

	// Then it should return the upstream response
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "X-Upstream", "true")
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"source":"upstream"`) {
		t.Errorf("expected upstream body, got %q", body)
	}
}

func TestExecuteRestAPI_httpProxyForwardsMethod(t *testing.T) {
	// Given an upstream that reports the method it receives
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"method":"%s"}`, r.Method)
	}))
	defer upstream.Close()

	// And a REST API with HTTP_PROXY integration using POST as the integration method
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "http-proxy-method")
	resID := createResource(t, srv, apiID, rootID, "forward")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegrationWithMethod(t, srv, apiID, resID, "GET", "HTTP_PROXY", upstream.URL, "POST")

	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When we execute a GET (which should be forwarded as POST to the integration)
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/test/_user_request_/forward",
		nil,
	)

	// Then the upstream should have received a POST
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"method":"POST"`) {
		t.Errorf("expected POST method at upstream, got %q", body)
	}
}

func TestExecuteRestAPI_httpIntegration(t *testing.T) {
	// Given an upstream HTTP server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"source":"http-non-proxy"}`)
	}))
	defer upstream.Close()

	// And a REST API with HTTP (non-proxy) integration
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "http-integration")
	resID := createResource(t, srv, apiID, rootID, "http")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegrationWithMethod(t, srv, apiID, resID, "GET", "HTTP", upstream.URL, "GET")

	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When we execute a request
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/test/_user_request_/http",
		nil,
	)

	// Then it should return the upstream response (passthrough without VTL)
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"source":"http-non-proxy"`) {
		t.Errorf("expected upstream body, got %q", body)
	}
}

// ---- Execution: stage variables -------------------------------------------

func TestExecuteRestAPI_stageVariableSubstitution(t *testing.T) {
	// Given an upstream HTTP server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resolved":"true"}`)
	}))
	defer upstream.Close()

	// And a REST API with HTTP_PROXY integration that uses a stage variable in the URI
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "stagevar-test")
	resID := createResource(t, srv, apiID, rootID, "vars")
	putMethod(t, srv, apiID, resID, "GET")
	// Use ${stageVariables.backendUrl} in the integration URI
	putIntegrationWithMethod(t, srv, apiID, resID, "GET", "HTTP_PROXY",
		"${stageVariables.backendUrl}", "GET")

	depID := createDeployment(t, srv, apiID)

	// Create a stage with the variable pointing to the upstream
	createStageWithVars(t, srv, apiID, depID, "test", map[string]string{
		"backendUrl": upstream.URL,
	})

	// When we execute the request
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/test/_user_request_/vars",
		nil,
	)

	// Then the stage variable should be resolved and the request should reach the upstream
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"resolved":"true"`) {
		t.Errorf("expected resolved body, got %q", body)
	}
}

// ---- Execution: HTTP v2 HTTP_PROXY ----------------------------------------

func TestExecuteV2API_httpProxyIntegration(t *testing.T) {
	// Given an upstream HTTP server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-V2-Upstream", "yes")
		fmt.Fprintf(w, `{"v2":"proxy","path":"%s"}`, r.URL.Path)
	}))
	defer upstream.Close()

	// And an HTTP API v2 with HTTP_PROXY integration
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-http-proxy")

	// Create integration
	integResp := apiCall(t, srv, http.MethodPost,
		"/v2/apis/"+apiID+"/integrations",
		map[string]any{
			"integrationType":   "HTTP_PROXY",
			"integrationUri":    upstream.URL,
			"integrationMethod": "GET",
		},
	)
	helpers.AssertStatus(t, integResp, http.StatusCreated)
	var integResult map[string]any
	helpers.DecodeJSON(t, integResp, &integResult)
	integID := integResult["integrationId"].(string)

	// Create route pointing to integration
	createV2RouteWithTarget(t, srv, apiID, "GET /v2proxy", "integrations/"+integID)

	// Create and deploy
	createV2Stage(t, srv, apiID, "test")

	// When we execute the request
	resp := apiCall(t, srv, http.MethodGet,
		"/v2/apis/"+apiID+"/stages/test/v2proxy",
		nil,
	)

	// Then it should return the upstream response
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "X-V2-Upstream", "yes")
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"v2":"proxy"`) {
		t.Errorf("expected upstream v2 body, got %q", body)
	}
}

// ---- REST API v1: Authorizers ---------------------------------------------

func TestCreateAuthorizer_basic(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "auth-test")

	// When we create a TOKEN authorizer
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/authorizers", map[string]any{
		"name":                         "my-authorizer",
		"type":                         "TOKEN",
		"authorizerUri":                "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:auth-fn/invocations",
		"identitySource":               "method.request.header.Authorization",
		"authorizerResultTtlInSeconds": 300,
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "my-authorizer" {
		t.Errorf("expected name %q, got %q", "my-authorizer", result["name"])
	}
	if result["type"] != "TOKEN" {
		t.Errorf("expected type %q, got %q", "TOKEN", result["type"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
}

func TestGetAuthorizer_basic(t *testing.T) {
	// Given a REST API with an authorizer
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "auth-get")
	authID := createAuthorizer(t, srv, apiID, "my-auth", "TOKEN")

	// When we get the authorizer
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/authorizers/"+authID, nil)

	// Then it should return it
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] != authID {
		t.Errorf("expected id %q, got %q", authID, result["id"])
	}
}

func TestGetAuthorizer_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "auth-nf")
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/authorizers/nonexistent", nil)
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetAuthorizers_multiple(t *testing.T) {
	// Given a REST API with two authorizers
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "auth-list")
	createAuthorizer(t, srv, apiID, "auth-one", "TOKEN")
	createAuthorizer(t, srv, apiID, "auth-two", "REQUEST")

	// When we list authorizers
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/authorizers", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) != 2 {
		t.Errorf("expected 2 authorizers, got %d", len(result.Item))
	}
}

func TestDeleteAuthorizer_basic(t *testing.T) {
	// Given a REST API with an authorizer
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "auth-del")
	authID := createAuthorizer(t, srv, apiID, "to-delete", "TOKEN")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID+"/authorizers/"+authID, nil)

	// Then it should succeed with 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/authorizers/"+authID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: API Keys ------------------------------------------------

func TestCreateApiKey_basic(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we create an API key
	resp := apiCall(t, srv, http.MethodPost, "/apikeys", map[string]any{
		"name":    "my-api-key",
		"enabled": true,
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "my-api-key" {
		t.Errorf("expected name %q, got %q", "my-api-key", result["name"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
	if result["value"] == nil || result["value"] == "" {
		t.Error("expected non-empty value")
	}
}

func TestGetApiKey_basic(t *testing.T) {
	// Given an API key
	srv := helpers.NewTestServer(t)
	keyID := createApiKey(t, srv, "get-key")

	// When we get it
	resp := apiCall(t, srv, http.MethodGet, "/apikeys/"+keyID, nil)

	// Then it should return it
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] != keyID {
		t.Errorf("expected id %q, got %q", keyID, result["id"])
	}
}

func TestGetApiKey_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := apiCall(t, srv, http.MethodGet, "/apikeys/nonexistent", nil)
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetApiKeys_multiple(t *testing.T) {
	// Given two API keys
	srv := helpers.NewTestServer(t)
	createApiKey(t, srv, "key-one")
	createApiKey(t, srv, "key-two")

	// When we list them
	resp := apiCall(t, srv, http.MethodGet, "/apikeys", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) < 2 {
		t.Errorf("expected at least 2 api keys, got %d", len(result.Item))
	}
}

func TestDeleteApiKey_basic(t *testing.T) {
	// Given an API key
	srv := helpers.NewTestServer(t)
	keyID := createApiKey(t, srv, "del-key")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/apikeys/"+keyID, nil)

	// Then it should succeed with 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/apikeys/"+keyID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: Usage Plans ---------------------------------------------

func TestCreateUsagePlan_basic(t *testing.T) {
	// Given a running server
	srv := helpers.NewTestServer(t)

	// When we create a usage plan
	resp := apiCall(t, srv, http.MethodPost, "/usageplans", map[string]any{
		"name":        "my-plan",
		"description": "A test usage plan",
		"throttle": map[string]any{
			"rateLimit":  100.0,
			"burstLimit": 50,
		},
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "my-plan" {
		t.Errorf("expected name %q, got %q", "my-plan", result["name"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
}

// AWS treats `name` as optional on CreateUsagePlan and auto-generates one
// when omitted. CDK's RestApi.addUsagePlan({ apiStages: [...] }) relies on
// this behaviour. Regression test for the previous 400-on-empty-name bug.
func TestCreateUsagePlan_nameOptional(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := apiCall(t, srv, http.MethodPost, "/usageplans", map[string]any{
		"description": "no-name plan",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)

	id, _ := result["id"].(string)
	if id == "" {
		t.Fatalf("expected non-empty id, got %+v", result)
	}
	name, _ := result["name"].(string)
	if name == "" {
		t.Errorf("expected auto-generated name, got empty")
	}
}

func TestGetUsagePlan_basic(t *testing.T) {
	// Given a usage plan
	srv := helpers.NewTestServer(t)
	planID := createUsagePlan(t, srv, "get-plan")

	// When we get it
	resp := apiCall(t, srv, http.MethodGet, "/usageplans/"+planID, nil)

	// Then it should return it
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] != planID {
		t.Errorf("expected id %q, got %q", planID, result["id"])
	}
}

func TestGetUsagePlan_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := apiCall(t, srv, http.MethodGet, "/usageplans/nonexistent", nil)
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetUsagePlans_multiple(t *testing.T) {
	// Given two usage plans
	srv := helpers.NewTestServer(t)
	createUsagePlan(t, srv, "plan-one")
	createUsagePlan(t, srv, "plan-two")

	// When we list them
	resp := apiCall(t, srv, http.MethodGet, "/usageplans", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Item) < 2 {
		t.Errorf("expected at least 2 usage plans, got %d", len(result.Item))
	}
}

func TestDeleteUsagePlan_basic(t *testing.T) {
	// Given a usage plan
	srv := helpers.NewTestServer(t)
	planID := createUsagePlan(t, srv, "del-plan")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/usageplans/"+planID, nil)

	// Then it should succeed with 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/usageplans/"+planID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestUsagePlanKeys_createAndList(t *testing.T) {
	// Given a usage plan and an API key
	srv := helpers.NewTestServer(t)
	planID := createUsagePlan(t, srv, "plan-with-keys")
	keyID := createApiKey(t, srv, "plan-key")

	// When we associate the key with the plan
	resp := apiCall(t, srv, http.MethodPost, "/usageplans/"+planID+"/keys", map[string]any{
		"keyId":   keyID,
		"keyType": "API_KEY",
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["id"] != keyID {
		t.Errorf("expected key id %q, got %q", keyID, result["id"])
	}

	// And listing keys should include it
	resp2 := apiCall(t, srv, http.MethodGet, "/usageplans/"+planID+"/keys", nil)
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var listResult struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp2, &listResult)
	if len(listResult.Item) != 1 {
		t.Errorf("expected 1 key in plan, got %d", len(listResult.Item))
	}
}

func TestDeleteUsagePlanKey_basic(t *testing.T) {
	// Given a usage plan with an associated API key
	srv := helpers.NewTestServer(t)
	planID := createUsagePlan(t, srv, "plan-del-key")
	keyID := createApiKey(t, srv, "del-plan-key")
	associateKeyWithPlan(t, srv, planID, keyID)

	// When we remove the key from the plan
	resp := apiCall(t, srv, http.MethodDelete, "/usageplans/"+planID+"/keys/"+keyID, nil)

	// Then it should succeed with 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And the plan should have no keys
	resp2 := apiCall(t, srv, http.MethodGet, "/usageplans/"+planID+"/keys", nil)
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var listResult struct {
		Item []any `json:"item"`
	}
	helpers.DecodeJSON(t, resp2, &listResult)
	if len(listResult.Item) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(listResult.Item))
	}
}

// ---- HTTP API v2: Authorizers ---------------------------------------------

func TestCreateV2Authorizer_basic(t *testing.T) {
	// Given an HTTP API
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-auth")

	// When we create a JWT authorizer
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/authorizers", map[string]any{
		"name":           "jwt-auth",
		"authorizerType": "JWT",
		"identitySource": "$request.header.Authorization",
		"jwtConfiguration": map[string]any{
			"audience": []string{"api-client"},
			"issuer":   "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc123",
		},
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "jwt-auth" {
		t.Errorf("expected name %q, got %q", "jwt-auth", result["name"])
	}
	if result["authorizerId"] == nil || result["authorizerId"] == "" {
		t.Error("expected non-empty authorizerId")
	}
}

func TestGetV2Authorizer_basic(t *testing.T) {
	// Given an HTTP API with an authorizer
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-auth-get")
	authID := createV2Authorizer(t, srv, apiID, "my-jwt")

	// When we get it
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/authorizers/"+authID, nil)

	// Then it should return it
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["authorizerId"] != authID {
		t.Errorf("expected authorizerId %q, got %q", authID, result["authorizerId"])
	}
}

func TestGetV2Authorizers_multiple(t *testing.T) {
	// Given an HTTP API with two authorizers
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-auth-list")
	createV2Authorizer(t, srv, apiID, "auth-a")
	createV2Authorizer(t, srv, apiID, "auth-b")

	// When we list them
	resp := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/authorizers", nil)

	// Then both should be returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Items []any `json:"items"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Items) != 2 {
		t.Errorf("expected 2 authorizers, got %d", len(result.Items))
	}
}

func TestDeleteV2Authorizer_basic(t *testing.T) {
	// Given an HTTP API with an authorizer
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-auth-del")
	authID := createV2Authorizer(t, srv, apiID, "to-del")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/v2/apis/"+apiID+"/authorizers/"+authID, nil)

	// Then it should succeed with 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/v2/apis/"+apiID+"/authorizers/"+authID, nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: Models --------------------------------------------------

func TestCreateModel_basic(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")

	// When we create a model
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/models", map[string]any{
		"name":        "Empty",
		"contentType": "application/json",
		"schema":      `{"type":"object"}`,
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "Empty" {
		t.Errorf("expected name %q, got %q", "Empty", result["name"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
	if result["contentType"] != "application/json" {
		t.Errorf("expected contentType %q, got %q", "application/json", result["contentType"])
	}
}

func TestCreateModel_missingName(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")

	// When we create a model without a name
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/models", map[string]any{
		"contentType": "application/json",
	})

	// Then it should fail with 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetModel_basic(t *testing.T) {
	// Given a REST API with a model
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")
	modelName := createModel(t, srv, apiID, "Empty", "application/json")

	// When we get it by name
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/models/"+modelName, nil)

	// Then it should return the model
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != modelName {
		t.Errorf("expected name %q, got %q", modelName, result["name"])
	}
}

func TestGetModel_notFound(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")

	// When we get a non-existent model
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/models/NotExist", nil)

	// Then it should return 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertJSONError(t, resp, "NotFoundException")
}

func TestGetModels_multiple(t *testing.T) {
	// Given a REST API with two models
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")
	createModel(t, srv, apiID, "Empty", "application/json")
	createModel(t, srv, apiID, "Error", "application/json")

	// When we list models
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/models", nil)

	// Then both should appear
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	items := result["item"].([]any)
	if len(items) != 2 {
		t.Errorf("expected 2 models, got %d", len(items))
	}
}

func TestDeleteModel_basic(t *testing.T) {
	// Given a REST API with a model
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "model-test")
	createModel(t, srv, apiID, "Empty", "application/json")

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID+"/models/Empty", nil)

	// Then it should return 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/models/Empty", nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: Request Validators --------------------------------------

func TestCreateRequestValidator_basic(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "rv-test")

	// When we create a request validator
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/requestvalidators", map[string]any{
		"name":                      "body-only",
		"validateRequestBody":       true,
		"validateRequestParameters": false,
	})

	// Then it should succeed with 201
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["name"] != "body-only" {
		t.Errorf("expected name %q, got %q", "body-only", result["name"])
	}
	if result["id"] == nil || result["id"] == "" {
		t.Error("expected non-empty id")
	}
	if result["validateRequestBody"] != true {
		t.Error("expected validateRequestBody to be true")
	}
}

func TestCreateRequestValidator_missingName(t *testing.T) {
	// Given a REST API
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "rv-test")

	// When we create a request validator without a name
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/requestvalidators", map[string]any{
		"validateRequestBody": true,
	})

	// Then it should fail with 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetRequestValidators_multiple(t *testing.T) {
	// Given a REST API with two request validators
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "rv-test")
	createRequestValidator(t, srv, apiID, "body-only", true, false)
	createRequestValidator(t, srv, apiID, "params-only", false, true)

	// When we list request validators
	resp := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/requestvalidators", nil)

	// Then both should appear
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	items := result["item"].([]any)
	if len(items) != 2 {
		t.Errorf("expected 2 validators, got %d", len(items))
	}
}

func TestDeleteRequestValidator_basic(t *testing.T) {
	// Given a REST API with a request validator
	srv := helpers.NewTestServer(t)
	apiID := createRestAPI(t, srv, "rv-test")
	rvID := createRequestValidator(t, srv, apiID, "body-only", true, false)

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/restapis/"+apiID+"/requestvalidators/"+rvID, nil)

	// Then it should return 202
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// And getting the list should show 0 validators
	resp2 := apiCall(t, srv, http.MethodGet, "/restapis/"+apiID+"/requestvalidators", nil)
	var result map[string]any
	helpers.DecodeJSON(t, resp2, &result)
	items := result["item"].([]any)
	if len(items) != 0 {
		t.Errorf("expected 0 validators after delete, got %d", len(items))
	}
}

// ---- REST API v1: GetMethodResponse / DeleteMethodResponse ----------------

func TestGetMethodResponse_basic(t *testing.T) {
	// Given a resource with a method that has a response
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "mresp-test")
	resID := createResource(t, srv, apiID, rootID, "users")
	putMethod(t, srv, apiID, resID, http.MethodGet)

	// PUT a method response
	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/responses/200",
		map[string]any{"statusCode": "200"},
	)

	// When we GET it
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/responses/200", nil)

	// Then it should return the method response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["statusCode"] != "200" {
		t.Errorf("expected statusCode %q, got %v", "200", result["statusCode"])
	}
}

func TestGetMethodResponse_notFound(t *testing.T) {
	// Given a resource with a method but no 200 response
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "mresp-test")
	resID := createResource(t, srv, apiID, rootID, "items")
	putMethod(t, srv, apiID, resID, http.MethodGet)

	// When we GET a non-existent status code
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/responses/404", nil)

	// Then it should return 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestDeleteMethodResponse_basic(t *testing.T) {
	// Given a resource with a method that has a response
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "mresp-test")
	resID := createResource(t, srv, apiID, rootID, "things")
	putMethod(t, srv, apiID, resID, http.MethodPost)

	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/responses/201",
		map[string]any{"statusCode": "201"},
	)

	// When we DELETE it
	resp := apiCall(t, srv, http.MethodDelete,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/responses/201", nil)

	// Then it should return 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/responses/201", nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: GetIntegrationResponse / DeleteIntegrationResponse ------

func TestGetIntegrationResponse_basic(t *testing.T) {
	// Given a resource with a method, integration, and integration response
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "iresp-test")
	resID := createResource(t, srv, apiID, rootID, "widgets")
	putMethod(t, srv, apiID, resID, http.MethodGet)

	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration",
		map[string]any{"type": "MOCK"},
	)
	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration/responses/200",
		map[string]any{"statusCode": "200"},
	)

	// When we GET the integration response
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration/responses/200", nil)

	// Then it should return the integration response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["statusCode"] != "200" {
		t.Errorf("expected statusCode %q, got %v", "200", result["statusCode"])
	}
}

func TestDeleteIntegrationResponse_basic(t *testing.T) {
	// Given a resource with an integration response
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "iresp-test")
	resID := createResource(t, srv, apiID, rootID, "gadgets")
	putMethod(t, srv, apiID, resID, http.MethodPost)

	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/integration",
		map[string]any{"type": "MOCK"},
	)
	apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/integration/responses/201",
		map[string]any{"statusCode": "201"},
	)

	// When we DELETE it
	resp := apiCall(t, srv, http.MethodDelete,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/integration/responses/201", nil)

	// Then it should return 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And getting it should fail
	resp2 := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/integration/responses/201", nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

// ---- REST API v1: UpdateResource ------------------------------------------

func TestUpdateResource_pathPart(t *testing.T) {
	// Given a REST API with a resource
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "upd-res")
	resID := createResource(t, srv, apiID, rootID, "original")

	// When we update the pathPart via patch
	resp := apiCall(t, srv, http.MethodPatch,
		"/restapis/"+apiID+"/resources/"+resID,
		map[string]any{
			"patchOperations": []map[string]any{
				{"op": "replace", "path": "/pathPart", "value": "renamed"},
			},
		},
	)

	// Then it should succeed and return the updated resource
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["pathPart"] != "renamed" {
		t.Errorf("expected pathPart %q, got %q", "renamed", result["pathPart"])
	}
	if result["path"] != "/renamed" {
		t.Errorf("expected path %q, got %q", "/renamed", result["path"])
	}
}

// ---- REST API v1: UpdateMethod --------------------------------------------

func TestUpdateMethod_authorizationType(t *testing.T) {
	// Given a REST API with a method
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "upd-method")
	resID := createResource(t, srv, apiID, rootID, "items")
	putMethod(t, srv, apiID, resID, "GET")

	// When we update the authorizationType
	resp := apiCall(t, srv, http.MethodPatch,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{
			"patchOperations": []map[string]any{
				{"op": "replace", "path": "/authorizationType", "value": "AWS_IAM"},
				{"op": "replace", "path": "/apiKeyRequired", "value": "true"},
			},
		},
	)

	// Then it should return the updated method
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["authorizationType"] != "AWS_IAM" {
		t.Errorf("expected authorizationType %q, got %q", "AWS_IAM", result["authorizationType"])
	}
	if result["apiKeyRequired"] != true {
		t.Errorf("expected apiKeyRequired true, got %v", result["apiKeyRequired"])
	}
}

// ---- REST API v1: UpdateIntegration --------------------------------------

func TestUpdateIntegration_uri(t *testing.T) {
	// Given a REST API with a MOCK integration
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "upd-integ")
	resID := createResource(t, srv, apiID, rootID, "fn")
	putMethod(t, srv, apiID, resID, "POST")
	putIntegration(t, srv, apiID, resID, "POST", "MOCK", "")

	// When we update the URI and type
	resp := apiCall(t, srv, http.MethodPatch,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/POST/integration",
		map[string]any{
			"patchOperations": []map[string]any{
				{"op": "replace", "path": "/uri", "value": "arn:aws:lambda:us-east-1:000000000000:function:updated"},
				{"op": "replace", "path": "/httpMethod", "value": "POST"},
			},
		},
	)

	// Then it should return the updated integration
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["uri"] != "arn:aws:lambda:us-east-1:000000000000:function:updated" {
		t.Errorf("unexpected uri: %v", result["uri"])
	}
}

// ---- HTTP API v2: UpdateRoute --------------------------------------------

func TestUpdateV2Route_routeKey(t *testing.T) {
	// Given an HTTP API with a route
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "upd-route")
	routeID := createV2Route(t, srv, apiID, "GET /old")

	// When we update the routeKey
	resp := apiCall(t, srv, http.MethodPatch,
		"/v2/apis/"+apiID+"/routes/"+routeID,
		map[string]any{"routeKey": "GET /new"},
	)

	// Then it should succeed with the updated routeKey
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["routeKey"] != "GET /new" {
		t.Errorf("expected routeKey %q, got %q", "GET /new", result["routeKey"])
	}
}

// ---- HTTP API v2: UpdateIntegration ---------------------------------------

func TestUpdateV2Integration_uri(t *testing.T) {
	// Given an HTTP API with an integration
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "upd-v2integ")
	resp0 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
		"integrationType": "AWS_PROXY",
		"integrationUri":  "arn:aws:lambda:us-east-1:000000000000:function:original",
	})
	helpers.AssertStatus(t, resp0, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp0, &created)
	integID := created["integrationId"].(string)

	// When we update the integrationUri
	resp := apiCall(t, srv, http.MethodPatch,
		"/v2/apis/"+apiID+"/integrations/"+integID,
		map[string]any{"integrationUri": "arn:aws:lambda:us-east-1:000000000000:function:updated"},
	)

	// Then it should return the updated integration
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["integrationUri"] != "arn:aws:lambda:us-east-1:000000000000:function:updated" {
		t.Errorf("unexpected integrationUri: %v", result["integrationUri"])
	}
}

// ---- HTTP API v2: UpdateStage --------------------------------------------

func TestUpdateV2Stage_description(t *testing.T) {
	// Given an HTTP API with a stage
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "upd-v2stage")
	createV2Stage(t, srv, apiID, "dev")

	// When we update the description
	resp := apiCall(t, srv, http.MethodPatch,
		"/v2/apis/"+apiID+"/stages/dev",
		map[string]any{"description": "updated description"},
	)

	// Then it should succeed with the updated description
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	if result["description"] != "updated description" {
		t.Errorf("expected description %q, got %v", "updated description", result["description"])
	}
}

// ---- REST API v1: Domain Names -------------------------------------------

func TestDomainName_createAndList(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When we create a domain name
	resp := apiCall(t, srv, http.MethodPost, "/domainnames", map[string]any{
		"domainName":     "api.example.com",
		"certificateArn": "arn:aws:acm:us-east-1:000000000000:certificate/abc123",
	})

	// Then it should succeed
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp, &created)
	if created["domainName"] != "api.example.com" {
		t.Errorf("expected domainName %q, got %v", "api.example.com", created["domainName"])
	}

	// And listing should return it
	listResp := apiCall(t, srv, http.MethodGet, "/domainnames", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	items := list["item"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 domain name, got %d", len(items))
	}
}

func TestDomainName_delete(t *testing.T) {
	// Given a domain name
	srv := helpers.NewTestServer(t)
	apiCall(t, srv, http.MethodPost, "/domainnames", map[string]any{"domainName": "del.example.com"})

	// When we delete it
	resp := apiCall(t, srv, http.MethodDelete, "/domainnames/del.example.com", nil)
	helpers.AssertStatus(t, resp, http.StatusAccepted)

	// Then it should not appear in the list
	listResp := apiCall(t, srv, http.MethodGet, "/domainnames", nil)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	items := list["item"].([]any)
	if len(items) != 0 {
		t.Errorf("expected 0 domain names after delete, got %d", len(items))
	}
}

func TestBasePathMapping_createAndList(t *testing.T) {
	// Given a domain name
	srv := helpers.NewTestServer(t)
	apiCall(t, srv, http.MethodPost, "/domainnames", map[string]any{"domainName": "bpm.example.com"})
	apiID := createRestAPI(t, srv, "bpm-api")

	// When we create a base path mapping
	resp := apiCall(t, srv, http.MethodPost, "/domainnames/bpm.example.com/basepathmappings", map[string]any{
		"basePath":  "v1",
		"restApiId": apiID,
		"stage":     "prod",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	// And list it
	listResp := apiCall(t, srv, http.MethodGet, "/domainnames/bpm.example.com/basepathmappings", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	items := list["item"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 base path mapping, got %d", len(items))
	}
}

// ---- REST API v1: VPC Links ----------------------------------------------

func TestVpcLink_createListDelete(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When we create a VPC link
	resp := apiCall(t, srv, http.MethodPost, "/vpclinks", map[string]any{
		"name":       "my-vpc-link",
		"targetArns": []string{"arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/my-nlb/abc123"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp, &created)
	if created["name"] != "my-vpc-link" {
		t.Errorf("expected name %q, got %v", "my-vpc-link", created["name"])
	}
	if created["status"] != "AVAILABLE" {
		t.Errorf("expected status AVAILABLE, got %v", created["status"])
	}
	id := created["id"].(string)

	// List should return it
	listResp := apiCall(t, srv, http.MethodGet, "/vpclinks", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	items := list["item"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 vpc link, got %d", len(items))
	}

	// Delete should succeed
	delResp := apiCall(t, srv, http.MethodDelete, "/vpclinks/"+id, nil)
	helpers.AssertStatus(t, delResp, http.StatusAccepted)

	// List should be empty
	listResp2 := apiCall(t, srv, http.MethodGet, "/vpclinks", nil)
	var list2 map[string]any
	helpers.DecodeJSON(t, listResp2, &list2)
	if len(list2["item"].([]any)) != 0 {
		t.Errorf("expected 0 vpc links after delete")
	}
}

// ---- REST API v1: Tags ---------------------------------------------------

func TestTags_putGetUntag(t *testing.T) {
	// Given a resource ARN
	srv := helpers.NewTestServer(t)
	arn := "arn:aws:apigateway:us-east-1::/restapis/abc123"

	// When we add tags
	resp := apiCall(t, srv, http.MethodPut, "/tags/"+arn, map[string]any{
		"tags": map[string]string{"env": "test", "team": "backend"},
	})
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Then GET should return them
	getResp := apiCall(t, srv, http.MethodGet, "/tags/"+arn, nil)
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, getResp, &result)
	tags := result["tags"].(map[string]any)
	if tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %v", tags["env"])
	}

	// Untagging should remove a key
	untagResp := apiCall(t, srv, http.MethodDelete, "/tags/"+arn+"?tagKeys=env", nil)
	helpers.AssertStatus(t, untagResp, http.StatusNoContent)

	getResp2 := apiCall(t, srv, http.MethodGet, "/tags/"+arn, nil)
	var result2 map[string]any
	helpers.DecodeJSON(t, getResp2, &result2)
	tags2 := result2["tags"].(map[string]any)
	if _, ok := tags2["env"]; ok {
		t.Error("expected env tag to be removed")
	}
	if tags2["team"] != "backend" {
		t.Errorf("expected team=backend to remain, got %v", tags2["team"])
	}
}

// ---- HTTP API v2: Domain Names -------------------------------------------

func TestV2DomainName_createListDelete(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When we create a v2 domain name
	resp := apiCall(t, srv, http.MethodPost, "/v2/domainnames", map[string]any{
		"domainName": "http.example.com",
		"domainNameConfigurations": []map[string]any{
			{"certificateArn": "arn:aws:acm:us-east-1:000000000000:certificate/xyz", "endpointType": "REGIONAL"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp, &created)
	if created["domainName"] != "http.example.com" {
		t.Errorf("expected domainName %q, got %v", "http.example.com", created["domainName"])
	}

	// List returns it
	listResp := apiCall(t, srv, http.MethodGet, "/v2/domainnames", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Errorf("expected 1 v2 domain name, got %d", len(items))
	}

	// Delete
	delResp := apiCall(t, srv, http.MethodDelete, "/v2/domainnames/http.example.com", nil)
	helpers.AssertStatus(t, delResp, http.StatusNoContent)

	listResp2 := apiCall(t, srv, http.MethodGet, "/v2/domainnames", nil)
	var list2 map[string]any
	helpers.DecodeJSON(t, listResp2, &list2)
	if len(list2["items"].([]any)) != 0 {
		t.Errorf("expected 0 v2 domain names after delete")
	}
}

// ---- HTTP API v2: VPC Links ----------------------------------------------

func TestV2VpcLink_createListDelete(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When we create a v2 VPC link
	resp := apiCall(t, srv, http.MethodPost, "/v2/vpclinks", map[string]any{
		"name":             "my-v2-vpc-link",
		"subnetIds":        []string{"subnet-abc123"},
		"securityGroupIds": []string{"sg-abc123"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp, &created)
	if created["name"] != "my-v2-vpc-link" {
		t.Errorf("expected name %q, got %v", "my-v2-vpc-link", created["name"])
	}
	id := created["vpcLinkId"].(string)

	// List returns it
	listResp := apiCall(t, srv, http.MethodGet, "/v2/vpclinks", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	if len(list["items"].([]any)) != 1 {
		t.Errorf("expected 1 v2 vpc link, got %d", len(list["items"].([]any)))
	}

	// Delete
	delResp := apiCall(t, srv, http.MethodDelete, "/v2/vpclinks/"+id, nil)
	helpers.AssertStatus(t, delResp, http.StatusNoContent)
}

// ---- HTTP API v2: API Mappings ------------------------------------------

func TestV2ApiMapping_createAndList(t *testing.T) {
	// Given a domain name and an HTTP API
	srv := helpers.NewTestServer(t)
	apiCall(t, srv, http.MethodPost, "/v2/domainnames", map[string]any{"domainName": "mapping.example.com"})
	apiID := createV2API(t, srv, "mapping-api")
	createV2Stage(t, srv, apiID, "prod")

	// When we create an API mapping
	resp := apiCall(t, srv, http.MethodPost, "/v2/domainnames/mapping.example.com/apimappings", map[string]any{
		"apiId":         apiID,
		"stage":         "prod",
		"apiMappingKey": "v1",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]any
	helpers.DecodeJSON(t, resp, &created)
	if created["apiId"] != apiID {
		t.Errorf("expected apiId %q, got %v", apiID, created["apiId"])
	}

	// List returns it
	listResp := apiCall(t, srv, http.MethodGet, "/v2/domainnames/mapping.example.com/apimappings", nil)
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var list map[string]any
	helpers.DecodeJSON(t, listResp, &list)
	if len(list["items"].([]any)) != 1 {
		t.Errorf("expected 1 api mapping, got %d", len(list["items"].([]any)))
	}
}

// ---- HTTP API v2: Tags ---------------------------------------------------

func TestV2Tags_putGetUntag(t *testing.T) {
	// Given an ARN
	srv := helpers.NewTestServer(t)
	arn := "arn:aws:apigateway:us-east-1::/apis/abc123"

	// When we add tags
	resp := apiCall(t, srv, http.MethodPost, "/v2/tags/"+arn, map[string]any{
		"Tags": map[string]string{"project": "overcast"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	// GET returns them
	getResp := apiCall(t, srv, http.MethodGet, "/v2/tags/"+arn, nil)
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var result map[string]any
	helpers.DecodeJSON(t, getResp, &result)
	tags := result["tags"].(map[string]any)
	if tags["project"] != "overcast" {
		t.Errorf("expected project=overcast, got %v", tags["project"])
	}

	// Untag removes it
	untagResp := apiCall(t, srv, http.MethodDelete, "/v2/tags/"+arn+"?tagKeys=project", nil)
	helpers.AssertStatus(t, untagResp, http.StatusNoContent)

	getResp2 := apiCall(t, srv, http.MethodGet, "/v2/tags/"+arn, nil)
	var result2 map[string]any
	helpers.DecodeJSON(t, getResp2, &result2)
	if len(result2["tags"].(map[string]any)) != 0 {
		t.Errorf("expected empty tags after untagging, got %v", result2["tags"])
	}
}

// ---- Stubs return 501 -----------------------------------------------------

func TestStubs_return501(t *testing.T) {
	srv := helpers.NewTestServer(t)

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/account"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := apiCall(t, srv, tc.method, tc.path, map[string]any{})
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
			helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
		})
	}
}

// ---- Test helpers ---------------------------------------------------------

// apiCall sends a JSON request to the API Gateway endpoint.
func apiCall(t *testing.T, srv *helpers.TestServer, method, path string, body any) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		b, _ := json.Marshal(body)
		req, err = http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
	} else {
		req, err = http.NewRequest(method, srv.URL+path, nil)
	}
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

// createRestAPI creates a REST API and returns its ID.
func createRestAPI(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/restapis", map[string]any{"name": name})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// createRestAPIWithRoot creates a REST API and returns (apiID, rootResourceID).
func createRestAPIWithRoot(t *testing.T, srv *helpers.TestServer, name string) (string, string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/restapis", map[string]any{"name": name})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string), result["rootResourceId"].(string)
}

// createResource creates a child resource and returns its ID.
func createResource(t *testing.T, srv *helpers.TestServer, apiID, parentID, pathPart string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/restapis/"+apiID+"/resources/"+parentID,
		map[string]any{"pathPart": pathPart},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// putMethod puts a method on a resource.
func putMethod(t *testing.T, srv *helpers.TestServer, apiID, resourceID, httpMethod string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resourceID+"/methods/"+httpMethod,
		map[string]any{"authorizationType": "NONE"},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// putIntegration puts an integration on a method.
func putIntegration(t *testing.T, srv *helpers.TestServer, apiID, resourceID, httpMethod, intType, uri string) {
	t.Helper()
	body := map[string]any{"type": intType}
	if uri != "" {
		body["uri"] = uri
		body["httpMethod"] = "POST"
	}
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resourceID+"/methods/"+httpMethod+"/integration",
		body,
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// putMethodResponse puts a method response on a method.
func putMethodResponse(t *testing.T, srv *helpers.TestServer, apiID, resourceID, httpMethod, statusCode string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resourceID+"/methods/"+httpMethod+"/responses/"+statusCode,
		map[string]any{},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// putIntegrationResponse puts an integration response with a body template.
func putIntegrationResponse(t *testing.T, srv *helpers.TestServer, apiID, resourceID, httpMethod, statusCode, responseBody string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resourceID+"/methods/"+httpMethod+"/integration/responses/"+statusCode,
		map[string]any{
			"responseTemplates": map[string]any{
				"application/json": responseBody,
			},
		},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// createDeployment creates a deployment and returns its ID.
func createDeployment(t *testing.T, srv *helpers.TestServer, apiID string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/restapis/"+apiID+"/deployments",
		map[string]any{},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// createStage creates a stage on a REST API.
func createStage(t *testing.T, srv *helpers.TestServer, apiID, deploymentID, stageName string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/restapis/"+apiID+"/stages",
		map[string]any{
			"stageName":    stageName,
			"deploymentId": deploymentID,
		},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// createV2API creates an HTTP API and returns its ID.
func createV2API(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis", map[string]any{
		"name":         name,
		"protocolType": "HTTP",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["apiId"].(string)
}

// createV2Route creates a route on an HTTP API and returns its ID.
func createV2Route(t *testing.T, srv *helpers.TestServer, apiID, routeKey string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/v2/apis/"+apiID+"/routes",
		map[string]any{"routeKey": routeKey},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["routeId"].(string)
}

// createV2Stage creates a stage on an HTTP API.
func createV2Stage(t *testing.T, srv *helpers.TestServer, apiID, stageName string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/v2/apis/"+apiID+"/stages",
		map[string]any{"stageName": stageName},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// putIntegrationWithMethod puts an integration with an explicit HTTP method.
func putIntegrationWithMethod(t *testing.T, srv *helpers.TestServer, apiID, resourceID, httpMethod, intType, uri, integrationMethod string) {
	t.Helper()
	body := map[string]any{
		"type":       intType,
		"uri":        uri,
		"httpMethod": integrationMethod,
	}
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resourceID+"/methods/"+httpMethod+"/integration",
		body,
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// createStageWithVars creates a stage with stage variables.
func createStageWithVars(t *testing.T, srv *helpers.TestServer, apiID, deploymentID, stageName string, vars map[string]string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/restapis/"+apiID+"/stages",
		map[string]any{
			"stageName":    stageName,
			"deploymentId": deploymentID,
			"variables":    vars,
		},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()
}

// createV2RouteWithTarget creates a route with a specific target.
func createV2RouteWithTarget(t *testing.T, srv *helpers.TestServer, apiID, routeKey, target string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost,
		"/v2/apis/"+apiID+"/routes",
		map[string]any{
			"routeKey": routeKey,
			"target":   target,
		},
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["routeId"].(string)
}

// createAuthorizer creates a REST v1 authorizer and returns its ID.
func createAuthorizer(t *testing.T, srv *helpers.TestServer, apiID, name, authType string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/authorizers", map[string]any{
		"name":           name,
		"type":           authType,
		"identitySource": "method.request.header.Authorization",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// createApiKey creates an API key and returns its ID.
func createApiKey(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/apikeys", map[string]any{
		"name":    name,
		"enabled": true,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// createUsagePlan creates a usage plan and returns its ID.
func createUsagePlan(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/usageplans", map[string]any{
		"name": name,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// associateKeyWithPlan associates an API key with a usage plan.
func associateKeyWithPlan(t *testing.T, srv *helpers.TestServer, planID, keyID string) {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/usageplans/"+planID+"/keys", map[string]any{
		"keyId":   keyID,
		"keyType": "API_KEY",
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
}

// createV2Authorizer creates an HTTP API v2 authorizer and returns its ID.
func createV2Authorizer(t *testing.T, srv *helpers.TestServer, apiID, name string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/authorizers", map[string]any{
		"name":           name,
		"authorizerType": "JWT",
		"identitySource": "$request.header.Authorization",
		"jwtConfiguration": map[string]any{
			"audience": []string{"client"},
			"issuer":   "https://example.com",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["authorizerId"].(string)
}

// createModel creates a model and returns its name.
func createModel(t *testing.T, srv *helpers.TestServer, apiID, name, contentType string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/models", map[string]any{
		"name":        name,
		"contentType": contentType,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["name"].(string)
}

// createRequestValidator creates a request validator and returns its ID.
func createRequestValidator(t *testing.T, srv *helpers.TestServer, apiID, name string, body, params bool) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/requestvalidators", map[string]any{
		"name":                      name,
		"validateRequestBody":       body,
		"validateRequestParameters": params,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var result map[string]any
	helpers.DecodeJSON(t, resp, &result)
	return result["id"].(string)
}

// ---- Cognito authorizer enforcement ---------------------------------------

// cognitoCallInTest issues a Cognito JSON target request against the test server.
func cognitoCallInTest(t *testing.T, srv *helpers.TestServer, op string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+op)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cognitoCall %s: %v", op, err)
	}
	return resp
}

// setupCognitoPool creates a user pool + confirmed user and returns (poolID, accessToken).
// The token is issued by the emulator's real Cognito JWT signer, so API Gateway
// can validate it with the stored RSA key.
func setupCognitoPool(t *testing.T, srv *helpers.TestServer) (poolID, accessToken string) {
	t.Helper()

	r1 := cognitoCallInTest(t, srv, "CreateUserPool", map[string]any{"PoolName": "gw-auth-pool"})
	defer r1.Body.Close()
	helpers.AssertStatus(t, r1, http.StatusOK)
	var p1 struct {
		UserPool struct {
			Id string `json:"Id"`
		} `json:"UserPool"`
	}
	helpers.DecodeJSON(t, r1, &p1)
	poolID = p1.UserPool.Id

	r2 := cognitoCallInTest(t, srv, "CreateUserPoolClient", map[string]any{
		"UserPoolId": poolID, "ClientName": "app",
	})
	defer r2.Body.Close()
	helpers.AssertStatus(t, r2, http.StatusOK)
	var p2 struct {
		UserPoolClient struct {
			ClientId string `json:"ClientId"`
		} `json:"UserPoolClient"`
	}
	helpers.DecodeJSON(t, r2, &p2)
	clientID := p2.UserPoolClient.ClientId

	r3 := cognitoCallInTest(t, srv, "AdminCreateUser", map[string]any{
		"UserPoolId": poolID, "Username": "alice",
	})
	defer r3.Body.Close()
	helpers.AssertStatus(t, r3, http.StatusOK)

	r4 := cognitoCallInTest(t, srv, "AdminSetUserPassword", map[string]any{
		"UserPoolId": poolID, "Username": "alice",
		"Password": "AlicePass1!", "Permanent": true,
	})
	defer r4.Body.Close()
	helpers.AssertStatus(t, r4, http.StatusOK)

	r5 := cognitoCallInTest(t, srv, "InitiateAuth", map[string]any{
		"ClientId": clientID, "AuthFlow": "USER_PASSWORD_AUTH",
		"AuthParameters": map[string]string{
			"USERNAME": "alice", "PASSWORD": "AlicePass1!",
		},
	})
	defer r5.Body.Close()
	helpers.AssertStatus(t, r5, http.StatusOK)
	var tokResult struct {
		AuthenticationResult struct {
			AccessToken string `json:"AccessToken"`
		} `json:"AuthenticationResult"`
	}
	helpers.DecodeJSON(t, r5, &tokResult)
	if tokResult.AuthenticationResult.AccessToken == "" {
		t.Fatal("InitiateAuth returned empty AccessToken")
	}
	return poolID, tokResult.AuthenticationResult.AccessToken
}

// setupRestAPIWithCognitoAuthorizer builds a fully deployed REST API whose GET
// /protected resource has a COGNITO_USER_POOLS authorizer pointing to poolID.
// Returns the (apiID, stageName).
func setupRestAPIWithCognitoAuthorizer(t *testing.T, srv *helpers.TestServer, poolID, region string) (apiID, stageName string) {
	t.Helper()
	apiID, rootID := createRestAPIWithRoot(t, srv, "cognito-gw")
	resID := createResource(t, srv, apiID, rootID, "protected")

	// PUT method with COGNITO_USER_POOLS auth type.
	resp := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{"authorizationType": "NONE"}, // we'll patch next
	)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// Create authorizer with ProviderARN referencing the pool.
	poolARN := "arn:aws:cognito-idp:" + region + ":000000000000:userpool/" + poolID
	r2 := apiCall(t, srv, http.MethodPost, "/restapis/"+apiID+"/authorizers", map[string]any{
		"name":           "cognito-auth",
		"type":           "COGNITO_USER_POOLS",
		"identitySource": "method.request.header.Authorization",
		"providerARNs":   []string{poolARN},
	})
	helpers.AssertStatus(t, r2, http.StatusCreated)
	var authResult map[string]any
	helpers.DecodeJSON(t, r2, &authResult)
	authID := authResult["id"].(string)

	// Patch method to use the authorizer.
	r3 := apiCall(t, srv, http.MethodPatch,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{
			"patchOperations": []map[string]any{
				{"op": "replace", "path": "/authorizationType", "value": "COGNITO_USER_POOLS"},
				{"op": "replace", "path": "/authorizerId", "value": authID},
			},
		},
	)
	helpers.AssertStatus(t, r3, http.StatusOK)
	r3.Body.Close()

	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"ok":true}`)

	depID := createDeployment(t, srv, apiID)
	stageName = "test"
	createStage(t, srv, apiID, depID, stageName)
	return apiID, stageName
}

// fakeRSASignedJWT creates a self-signed RS256 JWT with a fresh key that is NOT
// registered in the emulator — used to test rejection of tokens from unknown pools.
func fakeRSASignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	msg := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pl)
	h := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ─── REST v1 Cognito authorizer tests ────────────────────────────────────────

func TestExecuteRestAPI_cognitoAuthorizer_rejectsMissingToken(t *testing.T) {
	// Given: a REST API with a Cognito authorizer on GET /protected
	srv := helpers.NewTestServer(t)
	poolID, _ := setupCognitoPool(t, srv)
	apiID, stageName := setupRestAPIWithCognitoAuthorizer(t, srv, poolID, "us-east-1")

	// When: request arrives with no Authorization header
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/"+stageName+"/_user_request_/protected",
		nil,
	)
	defer resp.Body.Close()

	// Then: 401 Unauthorized
	helpers.AssertStatus(t, resp, http.StatusUnauthorized)
}

func TestExecuteRestAPI_cognitoAuthorizer_rejectsInvalidToken(t *testing.T) {
	// Given: a REST API with a Cognito authorizer
	srv := helpers.NewTestServer(t)
	poolID, _ := setupCognitoPool(t, srv)
	apiID, stageName := setupRestAPIWithCognitoAuthorizer(t, srv, poolID, "us-east-1")

	// When: request arrives with a token signed by an unrelated key
	expiry := int64(9999999999) // far future
	badToken := fakeRSASignedJWT(t, map[string]any{
		"sub": "attacker",
		"iss": "http://localhost/us-east-1/" + poolID,
		"exp": float64(expiry),
	})
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/"+stageName+"/_user_request_/protected",
		nil,
	)
	resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stageName+"/_user_request_/protected", nil)
	req.Header.Set("Authorization", "Bearer "+badToken)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()

	// Then: 401 Unauthorized (signature mismatch)
	helpers.AssertStatus(t, resp2, http.StatusUnauthorized)
}

func TestExecuteRestAPI_cognitoAuthorizer_acceptsValidToken(t *testing.T) {
	// Given: a REST API with a Cognito authorizer and a real token from the emulator
	srv := helpers.NewTestServer(t)
	poolID, accessToken := setupCognitoPool(t, srv)
	apiID, stageName := setupRestAPIWithCognitoAuthorizer(t, srv, poolID, "us-east-1")

	// When: request arrives with the valid access token
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stageName+"/_user_request_/protected", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 OK (authorizer passed, MOCK integration responds)
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestExecuteRestAPI_noCognitoAuthorizer_passesThrough(t *testing.T) {
	// Given: a REST API with no authorizer on GET /open
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "open-gw")
	resID := createResource(t, srv, apiID, rootID, "open")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"ok":true}`)
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "live")

	// When: unauthenticated request
	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/live/_user_request_/open", nil)
	defer resp.Body.Close()

	// Then: 200 — no auth required
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── HTTP v2 JWT authorizer tests ────────────────────────────────────────────

func TestExecuteV2API_jwtAuthorizer_rejectsMissingToken(t *testing.T) {
	// Given: an HTTP v2 API with a JWT authorizer on GET /protected
	srv := helpers.NewTestServer(t)
	poolID, _ := setupCognitoPool(t, srv)

	// Build the HTTP v2 API with jwt authorizer.
	apiID, issuer := setupV2APIWithJWTAuthorizer(t, srv, poolID, "us-east-1")

	// When: request without Authorization header
	resp := apiCall(t, srv, http.MethodGet,
		"/v2/apis/"+apiID+"/stages/dev/protected",
		nil,
	)
	defer resp.Body.Close()
	_ = issuer

	// Then: 401
	helpers.AssertStatus(t, resp, http.StatusUnauthorized)
}

func TestExecuteV2API_jwtAuthorizer_acceptsValidToken(t *testing.T) {
	// Given: an HTTP v2 API with a JWT authorizer and a valid Cognito access token
	srv := helpers.NewTestServer(t)
	poolID, accessToken := setupCognitoPool(t, srv)
	apiID, _ := setupV2APIWithJWTAuthorizer(t, srv, poolID, "us-east-1")

	// When: request with the valid Bearer token
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/v2/apis/"+apiID+"/stages/dev/protected", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 (MOCK integration responds)
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// setupV2APIWithJWTAuthorizer creates a deployed HTTP API v2 with a JWT
// authorizer on GET /protected. Returns (apiID, issuerURL).
func setupV2APIWithJWTAuthorizer(t *testing.T, srv *helpers.TestServer, poolID, region string) (apiID, issuer string) {
	t.Helper()

	// Create HTTP API v2.
	r1 := apiCall(t, srv, http.MethodPost, "/v2/apis", map[string]any{
		"name":         "jwt-gw",
		"protocolType": "HTTP",
	})
	helpers.AssertStatus(t, r1, http.StatusCreated)
	var apiResult map[string]any
	helpers.DecodeJSON(t, r1, &apiResult)
	apiID = apiResult["apiId"].(string)

	// Issuer format: http://{host}/{poolId} — the path portion of AWS's own
	// https://cognito-idp.{region}.amazonaws.com/{poolId}, on the origin Cognito
	// advertises rather than the one this test happens to dial. A JWT authorizer
	// compares the token's "iss" claim to this string exactly (as real AWS does,
	// see handler_auth.go), so it has to be the issuer Cognito actually mints —
	// ExternalBase(), not srv.URL. See docs/plans/harness-representativeness-audit.md.
	//
	// region is still a parameter because the pool is created in it; it is just
	// no longer part of the issuer, since a pool ID already carries it. See
	// docs/plans/non-canonical-url-namespace.md section 4.9.
	issuer = srv.ExternalBase() + "/" + poolID

	// Create a JWT authorizer.
	r2 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/authorizers", map[string]any{
		"name":           "jwt-auth",
		"authorizerType": "JWT",
		"identitySource": "$request.header.Authorization",
		"jwtConfiguration": map[string]any{
			"issuer":   issuer,
			"audience": []string{},
		},
	})
	helpers.AssertStatus(t, r2, http.StatusCreated)
	var authResult map[string]any
	helpers.DecodeJSON(t, r2, &authResult)
	authID := authResult["authorizerId"].(string)

	// Create a route protected by that authorizer.
	r3 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/routes", map[string]any{
		"routeKey":          "GET /protected",
		"authorizationType": "JWT",
		"authorizerId":      authID,
	})
	helpers.AssertStatus(t, r3, http.StatusCreated)
	var routeResult map[string]any
	helpers.DecodeJSON(t, r3, &routeResult)
	routeID := routeResult["routeId"].(string)

	// Create a MOCK-like integration (HTTP_PROXY to a local echo server for simplicity).
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(echo.Close)

	r4 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
		"integrationType":      "HTTP_PROXY",
		"integrationUri":       echo.URL + "/",
		"integrationMethod":    "GET",
		"payloadFormatVersion": "1.0",
	})
	helpers.AssertStatus(t, r4, http.StatusCreated)
	var intResult map[string]any
	helpers.DecodeJSON(t, r4, &intResult)
	integID := intResult["integrationId"].(string)

	// Patch route to target this integration.
	r5 := apiCall(t, srv, http.MethodPatch, "/v2/apis/"+apiID+"/routes/"+routeID, map[string]any{
		"target": "integrations/" + integID,
	})
	helpers.AssertStatus(t, r5, http.StatusOK)
	r5.Body.Close()

	// Deploy.
	r6 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/deployments", map[string]any{
		"description": "initial",
	})
	helpers.AssertStatus(t, r6, http.StatusCreated)
	var depResult map[string]any
	helpers.DecodeJSON(t, r6, &depResult)
	depID := depResult["deploymentId"].(string)

	r7 := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/stages", map[string]any{
		"stageName":    "dev",
		"deploymentId": depID,
	})
	helpers.AssertStatus(t, r7, http.StatusCreated)
	r7.Body.Close()

	return apiID, issuer
}

// ─── API key enforcement (apiKeyRequired=true) ────────────────────────────

// setupAPIKeyMethod creates an API with a /hello GET method that has
// apiKeyRequired=true plus a MOCK integration that returns 200. It returns
// the API ID and the stage name.
func setupAPIKeyMethod(t *testing.T, srv *helpers.TestServer, label string) (apiID, stageName string) {
	t.Helper()
	apiID, rootID := createRestAPIWithRoot(t, srv, label)
	resID := createResource(t, srv, apiID, rootID, "hello")

	// PutMethod with apiKeyRequired=true.
	r := apiCall(t, srv, http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{"authorizationType": "NONE", "apiKeyRequired": true},
	)
	helpers.AssertStatus(t, r, http.StatusCreated)
	r.Body.Close()

	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"ok":true}`)

	depID := createDeployment(t, srv, apiID)
	stageName = "test"
	createStage(t, srv, apiID, depID, stageName)
	return apiID, stageName
}

// createAPIKey creates an API key with a known value and returns its ID.
func createAPIKey(t *testing.T, srv *helpers.TestServer, name, value string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/apikeys", map[string]any{
		"name":    name,
		"enabled": true,
		"value":   value,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	return out["id"].(string)
}

// createUsagePlanForStage creates a usage plan covering {apiID, stageName} and
// associates the given API key with it.
func createUsagePlanForStage(t *testing.T, srv *helpers.TestServer, name, apiID, stageName, keyID string) string {
	t.Helper()
	resp := apiCall(t, srv, http.MethodPost, "/usageplans", map[string]any{
		"name": name,
		"apiStages": []map[string]any{
			{"apiId": apiID, "stage": stageName},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	planID := out["id"].(string)

	r := apiCall(t, srv, http.MethodPost, "/usageplans/"+planID+"/keys", map[string]any{
		"keyId":   keyID,
		"keyType": "API_KEY",
	})
	helpers.AssertStatus(t, r, http.StatusCreated)
	r.Body.Close()
	return planID
}

func TestExecuteRestAPI_apiKeyRequired_missingHeader(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, stage := setupAPIKeyMethod(t, srv, "key-missing")

	resp := apiCall(t, srv, http.MethodGet,
		"/restapis/"+apiID+"/"+stage+"/_user_request_/hello", nil)
	helpers.AssertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
}

func TestExecuteRestAPI_apiKeyRequired_invalidKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, stage := setupAPIKeyMethod(t, srv, "key-invalid")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stage+"/_user_request_/hello", nil)
	req.Header.Set("x-api-key", "no-such-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	helpers.AssertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
}

func TestExecuteRestAPI_apiKeyRequired_validKeyNoUsagePlan(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, stage := setupAPIKeyMethod(t, srv, "key-noplan")

	const value = "abcdef0123456789abcdef0123456789abcdef01"
	_ = createAPIKey(t, srv, "lonely-key", value)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stage+"/_user_request_/hello", nil)
	req.Header.Set("x-api-key", value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	// Valid key but no usage plan covers the stage → 403 Forbidden.
	helpers.AssertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
}

func TestExecuteRestAPI_apiKeyRequired_validKeyWithUsagePlan(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, stage := setupAPIKeyMethod(t, srv, "key-ok")

	const value = "ZZZZdef0123456789abcdef0123456789abcdef01"
	keyID := createAPIKey(t, srv, "good-key", value)
	createUsagePlanForStage(t, srv, "plan", apiID, stage, keyID)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stage+"/_user_request_/hello", nil)
	req.Header.Set("x-api-key", value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestExecuteRestAPI_apiKeyRequired_disabledKey(t *testing.T) {
	srv := helpers.NewTestServer(t)
	apiID, stage := setupAPIKeyMethod(t, srv, "key-disabled")

	const value = "DDDDdef0123456789abcdef0123456789abcdef01"
	// Create disabled key.
	resp := apiCall(t, srv, http.MethodPost, "/apikeys", map[string]any{
		"name":    "disabled-key",
		"enabled": false,
		"value":   value,
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	keyID := out["id"].(string)
	createUsagePlanForStage(t, srv, "plan-d", apiID, stage, keyID)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/restapis/"+apiID+"/"+stage+"/_user_request_/hello", nil)
	req.Header.Set("x-api-key", value)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	helpers.AssertStatus(t, r2, http.StatusForbidden)
	r2.Body.Close()
}

// TestExecuteRestAPI_crossRegionInvoke verifies that a path-style invoke URL
// (no SigV4, no Host hint) can reach a REST API created in a non-default
// region. This mirrors LocalStack's "API IDs are globally unique within the
// instance" behaviour and is required for tooling that creates resources in
// e.g. ap-southeast-2 but invokes via plain `curl localhost:4566/restapis/...`.
func TestExecuteRestAPI_crossRegionInvoke(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Create the API + resource + method + integration + deployment + stage
	// while pinned to ap-southeast-2 via SigV4 credential scope.
	const region = "ap-southeast-2"
	sigv4 := "AWS4-HMAC-SHA256 Credential=test/20260430/" + region +
		"/apigateway/aws4_request, SignedHeaders=host, Signature=fake"

	doSigned := func(method, path string, body any) *http.Response {
		t.Helper()
		var req *http.Request
		var err error
		if body != nil {
			b, _ := json.Marshal(body)
			req, err = http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		} else {
			req, err = http.NewRequest(method, srv.URL+path, nil)
		}
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", sigv4)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		return resp
	}

	resp := doSigned(http.MethodPost, "/restapis", map[string]any{"name": "cross-region"})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var apiOut map[string]any
	helpers.DecodeJSON(t, resp, &apiOut)
	apiID := apiOut["id"].(string)
	rootID := apiOut["rootResourceId"].(string)

	resp = doSigned(http.MethodPost, "/restapis/"+apiID+"/resources/"+rootID,
		map[string]any{"pathPart": "ping"})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var rOut map[string]any
	helpers.DecodeJSON(t, resp, &rOut)
	resID := rOut["id"].(string)

	resp = doSigned(http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET",
		map[string]any{"authorizationType": "NONE"})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	resp = doSigned(http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration",
		map[string]any{"type": "MOCK"})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	resp = doSigned(http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/responses/200",
		map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	resp = doSigned(http.MethodPut,
		"/restapis/"+apiID+"/resources/"+resID+"/methods/GET/integration/responses/200",
		map[string]any{"responseTemplates": map[string]string{
			"application/json": `{"pong":true}`,
		}})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	resp = doSigned(http.MethodPost, "/restapis/"+apiID+"/deployments", map[string]any{})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var dOut map[string]any
	helpers.DecodeJSON(t, resp, &dOut)
	depID := dOut["id"].(string)

	resp = doSigned(http.MethodPost, "/restapis/"+apiID+"/stages",
		map[string]any{"stageName": "prod", "deploymentId": depID})
	helpers.AssertStatus(t, resp, http.StatusCreated)

	// Now invoke path-style with NO Authorization header. The Region
	// middleware will fall back to "" → handler uses cfg.Region (us-east-1)
	// → cross-region resolver kicks in and finds the API in ap-southeast-2.
	r2, err := http.DefaultClient.Get(srv.URL + "/restapis/" + apiID + "/prod/_user_request_/ping")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	helpers.AssertStatus(t, r2, http.StatusOK)
	body := helpers.ReadBody(t, r2)
	if !strings.Contains(body, `"pong":true`) {
		t.Errorf("expected pong response, got %q", body)
	}
}

// ---- Execution: Host-based invoke (execute-api Host header) --------------

// TestExecuteRestAPI_hostBasedInvoke mirrors TestExecuteRestAPI_mockIntegration
// but invokes via the real AWS Host-routed shape
// ({apiId}.execute-api.{region}.amazonaws.com/{stage}/{path}) instead of the
// path-style /restapis/... URL, proving middleware.HostDispatch's rewrite
// reaches the exact same execution logic.
func TestExecuteRestAPI_hostBasedInvoke(t *testing.T) {
	// Given a REST API with a resource, method, MOCK integration, and responses
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "host-exec-mock")
	resID := createResource(t, srv, apiID, rootID, "hello")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"message":"hello world"}`)
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When we invoke via the execute-api Host header instead of the
	// path-style /restapis/{id}/... URL
	resp := apiCallWithHost(t, srv, http.MethodGet, "/test/hello",
		apiID+".execute-api.us-east-1.amazonaws.com", nil)

	// Then it returns the same mock response as the path-style invoke
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if body != `{"message":"hello world"}` {
		t.Errorf("expected mock body %q, got %q", `{"message":"hello world"}`, body)
	}
}

// TestExecuteRestAPI_hostBasedInvokeEncodedSlashStaysOneSegment covers #2136.
// AWS matches resources against the still-encoded path, so %2F inside a
// segment is not a separator: npm's /@scope%2fpkg must match /{package}, not
// the childless /{package}/{version} beside it. Path-style invoke already did
// this; host-style used to decode the path first and answer 403.
func TestExecuteRestAPI_hostBasedInvokeEncodedSlashStaysOneSegment(t *testing.T) {
	// Given: /{package} with a PUT method and a method-less /{package}/{version}
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "host-exec-encoded-slash")
	pkgID := createResource(t, srv, apiID, rootID, "{package}")
	createResource(t, srv, apiID, pkgID, "{version}")
	putMethod(t, srv, apiID, pkgID, "PUT")
	putIntegration(t, srv, apiID, pkgID, "PUT", "MOCK", "")
	putMethodResponse(t, srv, apiID, pkgID, "PUT", "200")
	putIntegrationResponse(t, srv, apiID, pkgID, "PUT", "200", `{"ok":true}`)
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "prod")

	for _, tc := range []struct{ name, path, host string }{
		{"host-style", "/prod/@scope%2fpkg", apiID + ".execute-api.eu-west-1.localhost.overcast.sh:4566"},
		{"path-style", "/restapis/" + apiID + "/prod/_user_request_/@scope%2fpkg", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: a scoped package name is PUT with its slash percent-encoded
			var resp *http.Response
			if tc.host != "" {
				resp = apiCallWithHost(t, srv, http.MethodPut, tc.path, tc.host, map[string]any{})
			} else {
				resp = apiCall(t, srv, http.MethodPut, tc.path, map[string]any{})
			}

			// Then: it reaches the /{package} PUT method's integration
			helpers.AssertStatus(t, resp, http.StatusOK)
			if body := helpers.ReadBody(t, resp); body != `{"ok":true}` {
				t.Errorf("expected mock body %q, got %q", `{"ok":true}`, body)
			}
		})
	}
}

// TestExecuteV2API_hostBasedInvokeEncodedSlashStaysOneSegment is the HTTP API
// counterpart of the REST test above (#2136): with GET /{package} and
// GET /{package}/{version} both routed, /@scope%2fpkg must pick /{package}.
func TestExecuteV2API_hostBasedInvokeEncodedSlashStaysOneSegment(t *testing.T) {
	// Given: each route proxies to its own upstream, which names itself
	upstreamNamed := func(name string) string {
		u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Route", name)
		}))
		t.Cleanup(u.Close)
		return u.URL
	}
	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-host-encoded-slash")
	for routeKey, name := range map[string]string{
		"GET /{package}":           "package",
		"GET /{package}/{version}": "version",
	} {
		integResp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
			"integrationType":   "HTTP_PROXY",
			"integrationUri":    upstreamNamed(name),
			"integrationMethod": "GET",
		})
		helpers.AssertStatus(t, integResp, http.StatusCreated)
		var integ map[string]any
		helpers.DecodeJSON(t, integResp, &integ)
		createV2RouteWithTarget(t, srv, apiID, routeKey, "integrations/"+integ["integrationId"].(string))
	}
	createV2Stage(t, srv, apiID, "$default")

	for _, tc := range []struct{ name, path, host string }{
		{"host-style", "/@scope%2fpkg", apiID + ".execute-api.us-east-1.localhost:4566"},
		{"path-style", "/v2/apis/" + apiID + "/stages/$default/@scope%2fpkg", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: a scoped package name is requested with its slash encoded
			var resp *http.Response
			if tc.host != "" {
				resp = apiCallWithHost(t, srv, http.MethodGet, tc.path, tc.host, nil)
			} else {
				resp = apiCall(t, srv, http.MethodGet, tc.path, nil)
			}
			defer resp.Body.Close()

			// Then: the one-segment route handles it
			helpers.AssertStatus(t, resp, http.StatusOK)
			helpers.AssertHeader(t, resp, "X-Route", "package")
		})
	}
}

// TestExecuteV2API_hostBasedInvokeDefaultStage proves a $default HTTP API
// stage is reachable through its Host-routed invoke URL with NO stage
// segment in the path — matching real AWS's $default semantics — via the
// same execute-api Host grammar used for REST v1 above.
func TestExecuteV2API_hostBasedInvokeDefaultStage(t *testing.T) {
	// Given an HTTP API v2 with an HTTP_PROXY integration deployed to $default
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-V2-Upstream", "yes")
		fmt.Fprintf(w, `{"v2":"proxy","path":"%s"}`, r.URL.Path)
	}))
	defer upstream.Close()

	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-host-default")

	integResp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
		"integrationType":   "HTTP_PROXY",
		"integrationUri":    upstream.URL,
		"integrationMethod": "GET",
	})
	helpers.AssertStatus(t, integResp, http.StatusCreated)
	var integResult map[string]any
	helpers.DecodeJSON(t, integResp, &integResult)
	integID := integResult["integrationId"].(string)

	createV2RouteWithTarget(t, srv, apiID, "GET /v2proxy", "integrations/"+integID)
	createV2Stage(t, srv, apiID, "$default")

	// When we invoke via the execute-api Host header with NO stage segment
	// in the path — the $default stage shape
	resp := apiCallWithHost(t, srv, http.MethodGet, "/v2proxy",
		apiID+".execute-api.us-east-1.amazonaws.com", nil)

	// Then it reaches the $default stage's integration
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "X-V2-Upstream", "yes")
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"v2":"proxy"`) {
		t.Errorf("expected upstream v2 body, got %q", body)
	}
}

// TestExecuteV2API_hostBasedInvokeNamedStage proves a non-default, named v2
// stage is reachable through its Host-routed invoke URL WITH a stage segment
// in the path, and that the segment is stripped before reaching the route.
func TestExecuteV2API_hostBasedInvokeNamedStage(t *testing.T) {
	// Given an HTTP API v2 deployed to a named ("prod") stage, not $default
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"v2":"named-stage"}`)
	}))
	defer upstream.Close()

	srv := helpers.NewTestServer(t)
	apiID := createV2API(t, srv, "v2-host-named")

	integResp := apiCall(t, srv, http.MethodPost, "/v2/apis/"+apiID+"/integrations", map[string]any{
		"integrationType":   "HTTP_PROXY",
		"integrationUri":    upstream.URL,
		"integrationMethod": "GET",
	})
	helpers.AssertStatus(t, integResp, http.StatusCreated)
	var integResult map[string]any
	helpers.DecodeJSON(t, integResp, &integResult)
	integID := integResult["integrationId"].(string)

	createV2RouteWithTarget(t, srv, apiID, "GET /v2proxy", "integrations/"+integID)
	createV2Stage(t, srv, apiID, "prod")

	// When we invoke via the execute-api Host header WITH the "prod" stage
	// segment in the path
	resp := apiCallWithHost(t, srv, http.MethodGet, "/prod/v2proxy",
		apiID+".execute-api.us-east-1.amazonaws.com", nil)

	// Then it reaches the named stage's integration
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := helpers.ReadBody(t, resp)
	if !strings.Contains(body, `"v2":"named-stage"`) {
		t.Errorf("expected named-stage upstream body, got %q", body)
	}
}

// TestExecuteByHost_unknownApiIDReturnsForbidden proves an unresolvable
// apiId under the execute-api Host grammar gets a real API-Gateway-shaped
// error (matching a real custom domain with no base path mapping) instead of
// silently falling through to some other service's handler.
func TestExecuteByHost_unknownApiIDReturnsForbidden(t *testing.T) {
	// Given a server with no APIs created at all
	srv := helpers.NewTestServer(t)

	// When we invoke via a Host that matches the grammar but names an
	// apiId that doesn't exist
	resp := apiCallWithHost(t, srv, http.MethodGet, "/prod/anything",
		"nosuchapi.execute-api.us-east-1.amazonaws.com", nil)

	// Then we get 403 Forbidden, not a 500 or a misrouted response
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// TestExecuteRestAPI_hostBasedInvokeAcrossResolvableBases is the regression
// test for the addressing-precedence bug: every host-routed invoke URL on a
// base Overcast can actually resolve used to return 403, because S3
// virtual-hosted addressing claimed the Host first and prepended a bogus
// bucket segment to the path. Only ".amazonaws.com" worked — the one base that
// does not resolve to the emulator without a hosts-file entry, and the only
// one the other tests in this file use.
//
// See docs/plans/host-routing-precedence.md.
func TestExecuteRestAPI_hostBasedInvokeAcrossResolvableBases(t *testing.T) {
	// Given: a REST API with a MOCK integration deployed to stage "test"
	srv := helpers.NewTestServer(t)
	apiID, rootID := createRestAPIWithRoot(t, srv, "host-exec-bases")
	resID := createResource(t, srv, apiID, rootID, "hello")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"message":"hello world"}`)
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// Every base a client can actually reach the emulator on, with and
	// without the optional region segment.
	bases := []struct{ name, host string }{
		{"amazonaws.com with region", apiID + ".execute-api.us-east-1.amazonaws.com"},
		{"localhost with region", apiID + ".execute-api.us-east-1.localhost:4566"},
		{"localhost without region", apiID + ".execute-api.localhost:4566"},
		{"overcast.sh wildcard with region", apiID + ".execute-api.us-east-1.localhost.overcast.sh:4566"},
		{"overcast.sh wildcard without region", apiID + ".execute-api.localhost.overcast.sh:4566"},
		{"localstack.cloud wildcard with region", apiID + ".execute-api.us-east-1.localhost.localstack.cloud:4566"},
		// A hostname is case-insensitive, so the same address in any case is
		// the same address. Sent case-sensitively, the label missed
		// hostRouteLabels and S3 virtual-hosted addressing claimed the Host
		// instead — an S3 XML error for an API Gateway invoke.
		{"mixed case", apiID + ".Execute-Api.US-East-1.localhost.overcast.sh:4566"},
	}

	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			// When: we invoke via the execute-api Host header on that base
			resp := apiCallWithHost(t, srv, http.MethodGet, "/test/hello", b.host, nil)

			// Then: it reaches the same mock integration as every other base
			helpers.AssertStatus(t, resp, http.StatusOK)
			if body := helpers.ReadBody(t, resp); body != `{"message":"hello world"}` {
				t.Errorf("Host %q: expected mock body %q, got %q", b.host, `{"message":"hello world"}`, body)
			}
		})
	}
}

// TestExecuteRestAPI_hostBasedInvokeOnConfiguredHostname proves the same for a
// custom OVERCAST_HOSTNAME, which is the deployment shape docs/networking.md
// recommends for Windows and for sibling containers.
func TestExecuteRestAPI_hostBasedInvokeOnConfiguredHostname(t *testing.T) {
	// Given: a server configured with a custom external hostname
	const hostname = "dev.example.test"
	srv := helpers.NewTestServer(t, helpers.WithHostname(hostname))
	apiID, rootID := createRestAPIWithRoot(t, srv, "host-exec-configured")
	resID := createResource(t, srv, apiID, rootID, "hello")
	putMethod(t, srv, apiID, resID, "GET")
	putIntegration(t, srv, apiID, resID, "GET", "MOCK", "")
	putMethodResponse(t, srv, apiID, resID, "GET", "200")
	putIntegrationResponse(t, srv, apiID, resID, "GET", "200", `{"message":"hello world"}`)
	depID := createDeployment(t, srv, apiID)
	createStage(t, srv, apiID, depID, "test")

	// When: we invoke on the configured base
	resp := apiCallWithHost(t, srv, http.MethodGet, "/test/hello",
		apiID+".execute-api.us-east-1."+hostname+":4566", nil)

	// Then: it reaches the integration
	helpers.AssertStatus(t, resp, http.StatusOK)
	if body := helpers.ReadBody(t, resp); body != `{"message":"hello world"}` {
		t.Errorf("expected mock body, got %q", body)
	}
}

// TestExecuteRestAPI_bucketNamedLikeServiceIsNotClaimed proves the collision
// boundary: a bucket named exactly like a host-route label is NOT a host
// route, because {id} must be non-empty in every real AWS host-routed shape.
func TestExecuteRestAPI_bucketNamedLikeServiceIsNotClaimed(t *testing.T) {
	// Given: a server with a bucket named "execute-api" and no APIs at all
	srv := helpers.NewTestServer(t)
	putResp := apiCallWithHost(t, srv, http.MethodPut, "/", "execute-api.localhost:4566", nil)
	helpers.AssertStatus(t, putResp, http.StatusOK)

	// When: we address that bucket virtual-hosted style
	resp := apiCallWithHost(t, srv, http.MethodGet, "/", "execute-api.localhost:4566", nil)

	// Then: S3 answers (a bucket listing), not API Gateway's 403 Forbidden
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// apiCallWithHost is apiCall but sets an explicit Host header, for exercising
// Host-routed (execute-api) invoke paths.
func apiCallWithHost(t *testing.T, srv *helpers.TestServer, method, path, host string, body any) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		b, _ := json.Marshal(body)
		req, err = http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
	} else {
		req, err = http.NewRequest(method, srv.URL+path, nil)
	}
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}
