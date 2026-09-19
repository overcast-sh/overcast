// Package lambda_test contains integration tests for the Lambda control-plane emulator.
//
// TDD contract: every handler must have a failing test here before implementation.
//
// Run: go test ./tests/integration/lambda/...
package lambda_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── helpers ────────────────────────────────────────────────────────────────

type getFunctionResponse struct {
	Configuration functionConfiguration `json:"Configuration"`
	Code          *getFunctionCode      `json:"Code,omitempty"`
}

type getFunctionCode struct {
	Location       string `json:"Location"`
	RepositoryType string `json:"RepositoryType"`
}

// createFunction is a test helper that creates a function and returns the ARN.
func createFunction(t *testing.T, srv *helpers.TestServer, name string) functionConfiguration {
	t.Helper()
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: name,
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)
	return cfg
}

// ─── CreateFunction ──────────────────────────────────────────────────────────

func TestCreateFunction_success(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When CreateFunction is called with valid params
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "my-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Description:  "test function",
		Timeout:      30,
		MemorySize:   128,
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()

	// Then 201 + FunctionConfiguration returned
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.FunctionName != "my-fn" {
		t.Errorf("FunctionName = %q, want %q", cfg.FunctionName, "my-fn")
	}
	if cfg.FunctionArn == "" {
		t.Error("FunctionArn must be set")
	}
	if cfg.Runtime != "nodejs20.x" {
		t.Errorf("Runtime = %q, want %q", cfg.Runtime, "nodejs20.x")
	}
	if cfg.Handler != "index.handler" {
		t.Errorf("Handler = %q, want %q", cfg.Handler, "index.handler")
	}
	if cfg.State != "Active" {
		t.Errorf("State = %q, want Active", cfg.State)
	}
	if cfg.PackageType != "Zip" {
		t.Errorf("PackageType = %q, want Zip", cfg.PackageType)
	}
	if len(cfg.Architectures) == 0 {
		t.Error("Architectures must be non-empty")
	}
	if cfg.RevisionId == "" {
		t.Error("RevisionId must be set")
	}
	if cfg.LastModified == "" {
		t.Error("LastModified must be set")
	}
}

func TestCreateFunction_defaults(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When CreateFunction is called with minimal params (no Timeout / MemorySize)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "minimal-fn",
		Runtime:      "nodejs22.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)

	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)
	// AWS defaults: Timeout=3s, MemorySize=128MB
	if cfg.Timeout != 3 {
		t.Errorf("default Timeout = %d, want 3", cfg.Timeout)
	}
	if cfg.MemorySize != 128 {
		t.Errorf("default MemorySize = %d, want 128", cfg.MemorySize)
	}
}

func TestCreateFunction_nodejs24Runtime(t *testing.T) {
	// Given: a fresh server with Docker disabled for deterministic control-plane testing.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called with the current Node.js Lambda runtime emitted by CDK.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "nodejs24-fn",
		Runtime:      "nodejs24.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()

	// Then: the control plane accepts the runtime and persists the function.
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)
	if cfg.Runtime != "nodejs24.x" {
		t.Fatalf("Runtime = %q, want nodejs24.x", cfg.Runtime)
	}
}

func TestCreateFunction_missingName(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When CreateFunction is called without FunctionName
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		Runtime: "nodejs20.x",
		Handler: "index.handler",
		Role:    "arn:aws:iam::000000000000:role/r",
		Code:    &lambdaCode{},
	})
	defer resp.Body.Close()

	// Then 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestCreateFunction_missingRole(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestCreateFunction_zipMissingCode(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called for a Zip function without Code.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "missing-code-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
	})

	// Then: AWS rejects the request as invalid.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestCreateFunction_zipMissingRuntime(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called for a Zip function without Runtime.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "missing-runtime-fn",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})

	// Then: AWS rejects the request as invalid.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestCreateFunction_zipMissingHandler(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called for a Zip function without Handler.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "missing-handler-fn",
		Runtime:      "nodejs20.x",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})

	// Then: AWS rejects the request as invalid.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestCreateFunction_imageWithRuntime(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called for an Image function that also specifies Runtime.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "image-runtime-fn",
		PackageType:  "Image",
		Runtime:      "nodejs20.x",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{ImageUri: "000000000000.dkr.ecr.us-east-1.amazonaws.com/fn:latest"},
	})

	// Then: AWS rejects Runtime for image package functions.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestCreateFunction_invalidPackageType(t *testing.T) {
	// Given: a fresh server.
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called with an unsupported PackageType value.
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "bad-package-fn",
		PackageType:  "Layer",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})

	// Then: AWS rejects the invalid enum value.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestCreateFunction_invalidBounds(t *testing.T) {
	// Given: invalid documented boundary values for CreateFunction.
	cases := []struct {
		name string
		req  createFunctionReq
	}{
		{
			name: "timeout above maximum",
			req: createFunctionReq{
				FunctionName: "timeout-too-large-fn",
				Runtime:      "nodejs20.x",
				Handler:      "index.handler",
				Role:         "arn:aws:iam::000000000000:role/r",
				Timeout:      901,
				Code:         &lambdaCode{},
			},
		},
		{
			name: "memory below minimum",
			req: createFunctionReq{
				FunctionName: "memory-too-small-fn",
				Runtime:      "nodejs20.x",
				Handler:      "index.handler",
				Role:         "arn:aws:iam::000000000000:role/r",
				MemorySize:   127,
				Code:         &lambdaCode{},
			},
		},
		{
			name: "invalid architecture",
			req: createFunctionReq{
				FunctionName:  "bad-arch-fn",
				Runtime:       "nodejs20.x",
				Handler:       "index.handler",
				Role:          "arn:aws:iam::000000000000:role/r",
				Architectures: []string{"sparc"},
				Code:          &lambdaCode{},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a fresh server.
			srv := helpers.NewTestServer(t)

			// When: CreateFunction is called with the invalid boundary value.
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), tc.req)

			// Then: AWS rejects the request as invalid.
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
		})
	}
}

func TestCreateFunction_duplicate(t *testing.T) {
	// Given a function already exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "dupe-fn")

	// When CreateFunction is called again with the same name
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "dupe-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()

	// Then 409 ResourceConflictException
	helpers.AssertStatus(t, resp, http.StatusConflict)
	var errBody map[string]string
	decodeJSON(t, resp, &errBody)
	if errBody["__type"] != "ResourceConflictException" {
		t.Errorf("expected ResourceConflictException, got %v", errBody)
	}
}

func TestCreateFunction_deprecatedRuntime(t *testing.T) {
	// AWS blocks CreateFunction only once a runtime passes its "block function
	// create" date, which comes after the deprecation date. nodejs14.x passed
	// it on 2024-01-09 and is refused with AWS's message; nodejs18.x is
	// deprecated but not blocked until 2027-02-01, so AWS — and therefore
	// Overcast — still accepts it.
	srv := helpers.NewTestServer(t)

	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "create-blocked-fn",
		Runtime:      "nodejs14.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errBody map[string]string
	decodeJSON(t, resp, &errBody)
	want := "The runtime parameter of nodejs14.x is no longer supported for creating or updating AWS Lambda functions. " +
		"We recommend you use the new runtime (nodejs24.x) while creating or updating functions."
	if errBody["__type"] != "InvalidParameterValueException" || errBody["message"] != want {
		t.Errorf("got %v, want InvalidParameterValueException with message %q", errBody, want)
	}

	stillCreatable := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "deprecated-but-creatable-fn",
		Runtime:      "nodejs18.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/r",
		Code:         &lambdaCode{},
	})
	defer stillCreatable.Body.Close()
	helpers.AssertStatus(t, stillCreatable, http.StatusCreated)
}

func TestCreateFunction_newlySupportedRuntimes(t *testing.T) {
	// One newly mapped runtime per family, over the wire. Each must create;
	// none may answer 501.
	srv := helpers.NewTestServer(t)
	for _, tc := range []struct{ runtime, handler string }{
		{"python3.14", "lambda_function.handler"},
		{"java25", "example.Handler::handleRequest"},
		{"java17.al2023", "example.Handler::handleRequest"},
		{"dotnet10", "Function::FunctionHandler"},
		{"ruby3.4", "lambda_function.lambda_handler"},
		{"ruby4.0", "lambda_function.lambda_handler"},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
				FunctionName: "new-runtime-" + tc.runtime,
				Runtime:      tc.runtime,
				Handler:      tc.handler,
				Role:         "arn:aws:iam::000000000000:role/r",
				Code:         &lambdaCode{},
			})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusCreated)
		})
	}
}

// ─── GetFunction ─────────────────────────────────────────────────────────────

func TestGetFunction_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	created := createFunction(t, srv, "get-fn")

	// When GetFunction is called
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-fn"), nil)
	defer resp.Body.Close()

	// Then 200 + GetFunctionResponse with Configuration + Code location
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out getFunctionResponse
	decodeJSON(t, resp, &out)

	if out.Configuration.FunctionName != "get-fn" {
		t.Errorf("FunctionName = %q", out.Configuration.FunctionName)
	}
	if out.Configuration.FunctionArn != created.FunctionArn {
		t.Errorf("FunctionArn mismatch: %q vs %q", out.Configuration.FunctionArn, created.FunctionArn)
	}
	if out.Code == nil {
		t.Error("Code block must be present")
	}
	if out.Code != nil && out.Code.RepositoryType != "S3" {
		t.Errorf("RepositoryType = %q, want S3", out.Code.RepositoryType)
	}
}

func TestGetFunction_notFound(t *testing.T) {
	// Given a fresh server (no functions)
	srv := helpers.NewTestServer(t)

	// When GetFunction is called for a non-existent function
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/no-such-fn"), nil)
	defer resp.Body.Close()

	// Then 404 ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── GetFunctionConfiguration ────────────────────────────────────────────────

func TestGetFunctionConfiguration_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "cfg-fn")

	// When GetFunctionConfiguration is called (GET /functions/{name}/configuration)
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/cfg-fn/configuration"), nil)
	defer resp.Body.Close()

	// Then 200 + FunctionConfiguration (flat, no Code block)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.FunctionName != "cfg-fn" {
		t.Errorf("FunctionName = %q", cfg.FunctionName)
	}
}

func TestGetFunctionConfiguration_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/no-such/configuration"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── DeleteFunction ──────────────────────────────────────────────────────────

func TestDeleteFunction_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "del-fn")

	// When DeleteFunction is called
	resp := doJSON(t, http.MethodDelete, lambdaURL(srv, "/functions/del-fn"), nil)
	defer resp.Body.Close()

	// Then 204 No Content
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And GetFunction returns 404
	resp2 := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/del-fn"), nil)
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestDeleteFunction_notFound(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When DeleteFunction is called for a non-existent function
	resp := doJSON(t, http.MethodDelete, lambdaURL(srv, "/functions/no-such"), nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── UpdateFunctionCode ──────────────────────────────────────────────────────

func TestUpdateFunctionCode_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	created := createFunction(t, srv, "update-code-fn")

	// When UpdateFunctionCode is called
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/update-code-fn/code"), map[string]any{
		"ZipFile": []byte("fake-zip-content"),
	})
	defer resp.Body.Close()

	// Then 200 + FunctionConfiguration with updated RevisionId
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.FunctionName != "update-code-fn" {
		t.Errorf("FunctionName = %q", cfg.FunctionName)
	}
	if cfg.RevisionId == created.RevisionId {
		t.Error("RevisionId must change after UpdateFunctionCode")
	}
}

func TestUpdateFunctionCode_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/no-such/code"), map[string]any{
		"ZipFile": []byte("fake"),
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateFunctionCode_rejectsRelativeHotReloadPath(t *testing.T) {
	// Given a function tagged with a relative hot-reload path
	srv := helpers.NewTestServer(t, helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-reload-relative",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": "relative/path"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusCreated)

	// When UpdateFunctionCode is called
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-reload-relative/code"), map[string]any{
		"ZipFile": []byte("fake-zip-content"),
	})
	defer resp.Body.Close()

	// Then a clear 400 validation error is returned
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "absolute path") {
		t.Fatalf("expected absolute-path validation error, got: %s", string(body))
	}
}

func TestUpdateFunctionCode_allowsHotReloadWhenLayersAttached(t *testing.T) {
	// Given a hot-reload-enabled function with an attached layer
	srv := helpers.NewTestServer(t, helpers.WithLambdaHotReload())
	createResp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "hot-reload-layered",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		Tags:         map[string]string{"overcast:hot-reload-path": "/workspace"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusCreated)

	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/hot-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip")},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-reload-layered/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When UpdateFunctionCode is called
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/hot-reload-layered/code"), map[string]any{
		"ZipFile": []byte("fake-zip-content"),
	})
	defer resp.Body.Close()

	// Then the update succeeds.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)
	if cfg.FunctionName != "hot-reload-layered" {
		t.Fatalf("FunctionName = %q, want hot-reload-layered", cfg.FunctionName)
	}
}

// ─── UpdateFunctionConfiguration ─────────────────────────────────────────────

func TestUpdateFunctionConfiguration_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "update-cfg-fn")

	// When UpdateFunctionConfiguration is called with new Timeout and MemorySize
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/update-cfg-fn/configuration"), map[string]any{
		"Timeout":     60,
		"MemorySize":  256,
		"Description": "updated",
		"Environment": map[string]any{
			"Variables": map[string]string{"FOO": "bar"},
		},
	})
	defer resp.Body.Close()

	// Then 200 + updated FunctionConfiguration
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.Timeout != 60 {
		t.Errorf("Timeout = %d, want 60", cfg.Timeout)
	}
	if cfg.MemorySize != 256 {
		t.Errorf("MemorySize = %d, want 256", cfg.MemorySize)
	}
}

func TestUpdateFunctionConfiguration_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/no-such/configuration"), map[string]any{
		"Timeout": 30,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateFunctionConfiguration_layersChangesRevisionID(t *testing.T) {
	// Given a function and a published layer.
	srv := helpers.NewTestServer(t)
	created := createFunction(t, srv, "update-cfg-layers-revision")

	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/revision-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip")},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	// When layers are attached via UpdateFunctionConfiguration.
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/update-cfg-layers-revision/configuration"), map[string]any{
		"Layers": []string{lv.LayerVersionArn},
	})
	defer attachResp.Body.Close()
	helpers.AssertStatus(t, attachResp, http.StatusOK)
	var attached functionConfiguration
	decodeJSON(t, attachResp, &attached)

	// Then RevisionId changes from create-time value.
	if attached.RevisionId == "" {
		t.Fatal("RevisionId must be non-empty after layer attach")
	}
	if attached.RevisionId == created.RevisionId {
		t.Fatalf("expected RevisionId to change after layer attach; got unchanged %q", attached.RevisionId)
	}

	// When layers are cleared via UpdateFunctionConfiguration.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/update-cfg-layers-revision/configuration"), map[string]any{
		"Layers": []string{},
	})
	defer clearResp.Body.Close()
	helpers.AssertStatus(t, clearResp, http.StatusOK)
	var cleared functionConfiguration
	decodeJSON(t, clearResp, &cleared)

	// Then RevisionId changes again.
	if cleared.RevisionId == "" {
		t.Fatal("RevisionId must be non-empty after layer clear")
	}
	if cleared.RevisionId == attached.RevisionId {
		t.Fatalf("expected RevisionId to change after layer clear; got unchanged %q", cleared.RevisionId)
	}
}

// ─── ListFunctions ───────────────────────────────────────────────────────────

func TestListFunctions_empty(t *testing.T) {
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions"), nil)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Functions []any `json:"Functions"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Functions) != 0 {
		t.Errorf("expected empty list, got %d", len(out.Functions))
	}
}

func TestListFunctions_returnsAll(t *testing.T) {
	// Given three functions exist
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "fn-a")
	createFunction(t, srv, "fn-b")
	createFunction(t, srv, "fn-c")

	// When ListFunctions is called
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions"), nil)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Functions []functionConfiguration `json:"Functions"`
	}
	decodeJSON(t, resp, &out)

	if len(out.Functions) != 3 {
		t.Errorf("expected 3 functions, got %d", len(out.Functions))
	}
}

// ─── wire types for versions + aliases ──────────────────────────────────────

type publishVersionReq struct {
	Description string `json:"Description,omitempty"`
	CodeSha256  string `json:"CodeSha256,omitempty"`
}

type versionConfiguration struct {
	FunctionName string `json:"FunctionName"`
	FunctionArn  string `json:"FunctionArn"`
	Version      string `json:"Version"`
	Description  string `json:"Description"`
	Runtime      string `json:"Runtime"`
	Handler      string `json:"Handler"`
	RevisionId   string `json:"RevisionId"`
	CodeSha256   string `json:"CodeSha256"`
}

type listVersionsResponse struct {
	Versions []versionConfiguration `json:"Versions"`
}

type aliasConfig struct {
	AliasArn        string `json:"AliasArn"`
	Name            string `json:"Name"`
	FunctionVersion string `json:"FunctionVersion"`
	Description     string `json:"Description"`
	RevisionId      string `json:"RevisionId"`
}

type listAliasesResponse struct {
	Aliases []aliasConfig `json:"Aliases"`
}

type createAliasReq struct {
	Name            string `json:"Name"`
	FunctionVersion string `json:"FunctionVersion"`
	Description     string `json:"Description,omitempty"`
}

type updateAliasReq struct {
	FunctionVersion string `json:"FunctionVersion"`
	Description     string `json:"Description,omitempty"`
}

// ─── PublishVersion ──────────────────────────────────────────────────────────

func TestPublishVersion_success(t *testing.T) {
	// Given a created function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "pub-fn")

	// When POST /functions/{name}/versions with empty body
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/pub-fn/versions"), publishVersionReq{})
	defer resp.Body.Close()

	// Then 201, Version="1", FunctionArn ends with ":1", CodeSha256 non-empty, RevisionId non-empty
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var ver versionConfiguration
	decodeJSON(t, resp, &ver)

	if ver.Version != "1" {
		t.Errorf("Version = %q, want %q", ver.Version, "1")
	}
	if ver.FunctionArn == "" {
		t.Error("FunctionArn must be set")
	}
	if len(ver.FunctionArn) < 2 || ver.FunctionArn[len(ver.FunctionArn)-2:] != ":1" {
		t.Errorf("FunctionArn %q should end with \":1\"", ver.FunctionArn)
	}
	if ver.CodeSha256 == "" {
		t.Error("CodeSha256 must be non-empty")
	}
	if ver.RevisionId == "" {
		t.Error("RevisionId must be non-empty")
	}
}

func TestPublishVersion_incrementing(t *testing.T) {
	// Given a created function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "inc-fn")

	// When PublishVersion is called twice
	resp1 := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/inc-fn/versions"), publishVersionReq{})
	helpers.AssertStatus(t, resp1, http.StatusCreated)
	var ver1 versionConfiguration
	decodeJSON(t, resp1, &ver1)

	resp2 := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/inc-fn/versions"), publishVersionReq{})
	helpers.AssertStatus(t, resp2, http.StatusCreated)
	var ver2 versionConfiguration
	decodeJSON(t, resp2, &ver2)

	// Then first response has Version="1", second has Version="2"
	if ver1.Version != "1" {
		t.Errorf("first Version = %q, want \"1\"", ver1.Version)
	}
	if ver2.Version != "2" {
		t.Errorf("second Version = %q, want \"2\"", ver2.Version)
	}
}

func TestPublishVersion_withDescription(t *testing.T) {
	// Given a created function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "desc-fn")

	// When POST /functions/{name}/versions with {"Description":"my release"}
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/desc-fn/versions"), publishVersionReq{
		Description: "my release",
	})
	defer resp.Body.Close()

	// Then 201, Description="my release"
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var ver versionConfiguration
	decodeJSON(t, resp, &ver)

	if ver.Description != "my release" {
		t.Errorf("Description = %q, want %q", ver.Description, "my release")
	}
}

func TestPublishVersion_unknownFunction(t *testing.T) {
	// Given no function exists
	srv := helpers.NewTestServer(t)

	// When POST /functions/nonexistent/versions
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/nonexistent/versions"), publishVersionReq{})
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── ListVersionsByFunction ──────────────────────────────────────────────────

func TestListVersionsByFunction_empty(t *testing.T) {
	// Given a function with no published versions
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "list-ver-fn")

	// When GET /functions/{name}/versions
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/list-ver-fn/versions"), nil)
	defer resp.Body.Close()

	// Then 200, Versions contains exactly one entry with Version="$LATEST"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listVersionsResponse
	decodeJSON(t, resp, &out)

	if len(out.Versions) != 1 {
		t.Fatalf("expected 1 version ($LATEST), got %d", len(out.Versions))
	}
	if out.Versions[0].Version != "$LATEST" {
		t.Errorf("Version = %q, want \"$LATEST\"", out.Versions[0].Version)
	}
}

func TestListVersionsByFunction_afterPublish(t *testing.T) {
	// Given a function with two published versions
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "list-pub-fn")

	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-pub-fn/versions"), publishVersionReq{})
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-pub-fn/versions"), publishVersionReq{})

	// When GET /functions/{name}/versions
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/list-pub-fn/versions"), nil)
	defer resp.Body.Close()

	// Then 200, Versions has 3 entries (2 numbered + $LATEST)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listVersionsResponse
	decodeJSON(t, resp, &out)

	if len(out.Versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(out.Versions))
	}

	versionSet := make(map[string]bool)
	for _, v := range out.Versions {
		versionSet[v.Version] = true
	}
	for _, want := range []string{"$LATEST", "1", "2"} {
		if !versionSet[want] {
			t.Errorf("version %q not found in response", want)
		}
	}
}

// ─── Aliases ─────────────────────────────────────────────────────────────────

func TestCreateAlias_success(t *testing.T) {
	// Given a function with one published version
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/alias-fn/versions"), publishVersionReq{})

	// When POST /functions/{name}/aliases with {Name:"prod", FunctionVersion:"1"}
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/alias-fn/aliases"), createAliasReq{
		Name:            "prod",
		FunctionVersion: "1",
	})
	defer resp.Body.Close()

	// Then 201, AliasArn contains function name and "prod", Name="prod", FunctionVersion="1", RevisionId non-empty
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var alias aliasConfig
	decodeJSON(t, resp, &alias)

	if alias.Name != "prod" {
		t.Errorf("Name = %q, want \"prod\"", alias.Name)
	}
	if alias.FunctionVersion != "1" {
		t.Errorf("FunctionVersion = %q, want \"1\"", alias.FunctionVersion)
	}
	if alias.AliasArn == "" {
		t.Error("AliasArn must be set")
	}
	if alias.RevisionId == "" {
		t.Error("RevisionId must be non-empty")
	}
}

func TestCreateAlias_duplicate(t *testing.T) {
	// Given a function with alias "prod" already created
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "dup-alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/dup-alias-fn/versions"), publishVersionReq{})

	first := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/dup-alias-fn/aliases"), createAliasReq{
		Name:            "prod",
		FunctionVersion: "1",
	})
	helpers.AssertStatus(t, first, http.StatusCreated)
	first.Body.Close()

	// When POST /functions/{name}/aliases with same Name
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/dup-alias-fn/aliases"), createAliasReq{
		Name:            "prod",
		FunctionVersion: "1",
	})
	defer resp.Body.Close()

	// Then 409
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestGetAlias_success(t *testing.T) {
	// Given a function with alias "prod" pointing to version "1"
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/get-alias-fn/versions"), publishVersionReq{})
	created := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/get-alias-fn/aliases"), createAliasReq{
		Name:            "prod",
		FunctionVersion: "1",
	})
	helpers.AssertStatus(t, created, http.StatusCreated)
	created.Body.Close()

	// When GET /functions/{name}/aliases/prod
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/get-alias-fn/aliases/prod"), nil)
	defer resp.Body.Close()

	// Then 200, Name="prod", FunctionVersion="1"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var alias aliasConfig
	decodeJSON(t, resp, &alias)

	if alias.Name != "prod" {
		t.Errorf("Name = %q, want \"prod\"", alias.Name)
	}
	if alias.FunctionVersion != "1" {
		t.Errorf("FunctionVersion = %q, want \"1\"", alias.FunctionVersion)
	}
}

func TestGetAlias_notFound(t *testing.T) {
	// Given a function exists but the alias does not
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "no-alias-fn")

	// When GET /functions/{name}/aliases/nonexistent
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/no-alias-fn/aliases/nonexistent"), nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListAliases_success(t *testing.T) {
	// Given a function with aliases "prod" and "staging"
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "list-alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-alias-fn/versions"), publishVersionReq{})
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-alias-fn/versions"), publishVersionReq{})

	r1 := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-alias-fn/aliases"), createAliasReq{
		Name: "prod", FunctionVersion: "1",
	})
	helpers.AssertStatus(t, r1, http.StatusCreated)
	r1.Body.Close()

	r2 := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/list-alias-fn/aliases"), createAliasReq{
		Name: "staging", FunctionVersion: "2",
	})
	helpers.AssertStatus(t, r2, http.StatusCreated)
	r2.Body.Close()

	// When GET /functions/{name}/aliases
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/list-alias-fn/aliases"), nil)
	defer resp.Body.Close()

	// Then 200, both aliases present in Aliases array
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listAliasesResponse
	decodeJSON(t, resp, &out)

	nameSet := make(map[string]bool)
	for _, a := range out.Aliases {
		nameSet[a.Name] = true
	}
	for _, want := range []string{"prod", "staging"} {
		if !nameSet[want] {
			t.Errorf("alias %q not found in response", want)
		}
	}
}

func TestDeleteAlias_success(t *testing.T) {
	// Given a function with alias "prod"
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "del-alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/del-alias-fn/versions"), publishVersionReq{})

	created := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/del-alias-fn/aliases"), createAliasReq{
		Name: "prod", FunctionVersion: "1",
	})
	helpers.AssertStatus(t, created, http.StatusCreated)
	created.Body.Close()

	// When DELETE /functions/{name}/aliases/prod
	resp := doJSON(t, http.MethodDelete, lambdaURL(srv, "/functions/del-alias-fn/aliases/prod"), nil)
	defer resp.Body.Close()

	// Then 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And GET /functions/{name}/aliases/prod returns 404
	check := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/del-alias-fn/aliases/prod"), nil)
	defer check.Body.Close()
	helpers.AssertStatus(t, check, http.StatusNotFound)
}

func TestDeleteAlias_notFound(t *testing.T) {
	// Given a function exists but the alias does not
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "del-nf-fn")

	// When DELETE /functions/fn/aliases/nonexistent
	resp := doJSON(t, http.MethodDelete, lambdaURL(srv, "/functions/del-nf-fn/aliases/nonexistent"), nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateAlias_success(t *testing.T) {
	// Given function with alias "prod" pointing to "1" and version "2" published
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "upd-alias-fn")
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/upd-alias-fn/versions"), publishVersionReq{})
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/upd-alias-fn/versions"), publishVersionReq{})

	created := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions/upd-alias-fn/aliases"), createAliasReq{
		Name: "prod", FunctionVersion: "1",
	})
	helpers.AssertStatus(t, created, http.StatusCreated)
	var origAlias aliasConfig
	decodeJSON(t, created, &origAlias)

	// When PUT /functions/{name}/aliases/prod with {FunctionVersion:"2"}
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/upd-alias-fn/aliases/prod"), updateAliasReq{
		FunctionVersion: "2",
	})
	defer resp.Body.Close()

	// Then 200, FunctionVersion="2", RevisionId changed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var updated aliasConfig
	decodeJSON(t, resp, &updated)

	if updated.FunctionVersion != "2" {
		t.Errorf("FunctionVersion = %q, want \"2\"", updated.FunctionVersion)
	}
	if updated.RevisionId == "" {
		t.Error("RevisionId must be non-empty")
	}
	if updated.RevisionId == "" {
		t.Error("RevisionId must be non-empty")
	}
	if updated.RevisionId == origAlias.RevisionId {
		t.Error("RevisionId should change after update")
	}
}

// ─── wire types for layers ────────────────────────────────────────────────────

type listLayerVersionsResponse struct {
	LayerVersions []layerVersionResponse `json:"LayerVersions"`
	NextMarker    *string                `json:"NextMarker,omitempty"`
}

type listLayersEntry struct {
	LayerName             string               `json:"LayerName"`
	LayerArn              string               `json:"LayerArn"`
	LatestMatchingVersion layerVersionResponse `json:"LatestMatchingVersion"`
}

type listLayersResponse struct {
	Layers     []listLayersEntry `json:"Layers"`
	NextMarker *string           `json:"NextMarker,omitempty"`
}

// ─── PublishLayerVersion ──────────────────────────────────────────────────────

func TestPublishLayerVersion_success(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When PublishLayerVersion is called with a zip and compatible runtimes
	resp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{
		Description:        "test layer",
		Content:            layerContent{ZipFile: []byte("PK\x05\x06" + string(make([]byte, 18)))},
		CompatibleRuntimes: []string{"nodejs20.x", "nodejs22.x"},
	})

	// Then 201 is returned with a populated LayerVersionArn and Version=1
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, resp, &lv)
	if lv.LayerVersionArn == "" {
		t.Error("LayerVersionArn must be non-empty")
	}
	if lv.LayerArn == "" {
		t.Error("LayerArn must be non-empty")
	}
	if lv.Version != 1 {
		t.Errorf("expected Version=1, got %d", lv.Version)
	}
	if lv.Description != "test layer" {
		t.Errorf("unexpected description %q", lv.Description)
	}
	if len(lv.CompatibleRuntimes) != 2 {
		t.Errorf("expected 2 CompatibleRuntimes, got %d", len(lv.CompatibleRuntimes))
	}
	if lv.CreatedDate == "" {
		t.Error("CreatedDate must be non-empty")
	}
	if lv.Content.CodeSize == 0 {
		t.Error("Content.CodeSize must be non-zero")
	}
}

func TestPublishLayerVersion_incrementing(t *testing.T) {
	// Given a server with one layer version already published
	srv := helpers.NewTestServer(t)
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip1")},
	})

	// When a second version is published
	resp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip2")},
	})

	// Then Version=2 is returned
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, resp, &lv)
	if lv.Version != 2 {
		t.Errorf("expected Version=2, got %d", lv.Version)
	}
}

// ─── GetLayerVersion ─────────────────────────────────────────────────────────

func TestGetLayerVersion_success(t *testing.T) {
	// Given a layer version that was published
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{
		Description: "v1",
		Content:     layerContent{ZipFile: []byte("zip")},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var published layerVersionResponse
	decodeJSON(t, resp, &published)

	// When GetLayerVersion is called for version 1
	resp2 := doJSON(t, http.MethodGet, layerURL(srv, "/layers/my-layer/versions/1"), nil)

	// Then the same layer version is returned
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var got layerVersionResponse
	decodeJSON(t, resp2, &got)
	if got.LayerVersionArn != published.LayerVersionArn {
		t.Errorf("ARN mismatch: got %q, want %q", got.LayerVersionArn, published.LayerVersionArn)
	}
	if got.Description != "v1" {
		t.Errorf("unexpected description %q", got.Description)
	}
}

func TestGetLayerVersion_notFound(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When GetLayerVersion is called for a non-existent layer
	resp := doJSON(t, http.MethodGet, layerURL(srv, "/layers/missing-layer/versions/1"), nil)

	// Then 404 is returned
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetLayerVersionMetadata_detectsExternalExtensions(t *testing.T) {
	// Given: a published layer with an executable external extension file.
	srv := helpers.NewTestServer(t)
	zipBytes := makeZipWithMode(t, "extensions/bootstrap", "#!/bin/sh\n", 0o755)
	resp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/extension-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: zipBytes},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)

	// When: emulator layer metadata is requested.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/lambda/layers/extension-layer/versions/1/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}
	metaResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer metaResp.Body.Close()

	// Then: the extension executable is surfaced for developer tooling.
	helpers.AssertStatus(t, metaResp, http.StatusOK)
	var meta struct {
		HasExternalExtensions bool     `json:"hasExternalExtensions"`
		ExternalExtensions    []string `json:"externalExtensions"`
	}
	decodeJSON(t, metaResp, &meta)
	if !meta.HasExternalExtensions || len(meta.ExternalExtensions) != 1 || meta.ExternalExtensions[0] != "bootstrap" {
		t.Fatalf("metadata = %+v", meta)
	}
}

// ─── ListLayerVersions ────────────────────────────────────────────────────────

func TestListLayerVersions_empty(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When ListLayerVersions is called for a layer that has no versions
	resp := doJSON(t, http.MethodGet, layerURL(srv, "/layers/no-such-layer/versions"), nil)

	// Then 200 is returned with an empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var list listLayerVersionsResponse
	decodeJSON(t, resp, &list)
	if len(list.LayerVersions) != 0 {
		t.Errorf("expected empty list, got %d items", len(list.LayerVersions))
	}
}

func TestListLayerVersions_afterPublish(t *testing.T) {
	// Given a layer with two published versions
	srv := helpers.NewTestServer(t)
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("v1")}})
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("v2")}})

	// When ListLayerVersions is called
	resp := doJSON(t, http.MethodGet, layerURL(srv, "/layers/my-layer/versions"), nil)

	// Then both versions are returned in descending order (newest first, per AWS spec)
	helpers.AssertStatus(t, resp, http.StatusOK)
	var list listLayerVersionsResponse
	decodeJSON(t, resp, &list)
	if len(list.LayerVersions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(list.LayerVersions))
	}
	if list.LayerVersions[0].Version != 2 {
		t.Errorf("expected newest version first, got version %d first", list.LayerVersions[0].Version)
	}
}

// ─── ListLayers ───────────────────────────────────────────────────────────────

func TestListLayers_empty(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When ListLayers is called
	resp := doJSON(t, http.MethodGet, layerURL(srv, "/layers"), nil)

	// Then 200 is returned with an empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var list listLayersResponse
	decodeJSON(t, resp, &list)
	if len(list.Layers) != 0 {
		t.Errorf("expected empty list, got %d items", len(list.Layers))
	}
}

func TestListLayers_afterPublish(t *testing.T) {
	// Given two distinct layers published
	srv := helpers.NewTestServer(t)
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/layer-a/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("a")}})
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/layer-b/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("b")}})
	// Publish a second version of layer-a so LatestMatchingVersion is version 2
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/layer-a/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("a2")}})

	// When ListLayers is called
	resp := doJSON(t, http.MethodGet, layerURL(srv, "/layers"), nil)

	// Then both distinct layers appear, each with their latest version
	helpers.AssertStatus(t, resp, http.StatusOK)
	var list listLayersResponse
	decodeJSON(t, resp, &list)
	if len(list.Layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(list.Layers))
	}
	for _, l := range list.Layers {
		if l.LayerName == "layer-a" && l.LatestMatchingVersion.Version != 2 {
			t.Errorf("layer-a: expected latest version 2, got %d", l.LatestMatchingVersion.Version)
		}
	}
}

// ─── DeleteLayerVersion ───────────────────────────────────────────────────────

func TestDeleteLayerVersion_success(t *testing.T) {
	// Given a published layer version
	srv := helpers.NewTestServer(t)
	doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{Content: layerContent{ZipFile: []byte("zip")}})

	// When DeleteLayerVersion is called
	resp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/my-layer/versions/1"), nil)

	// Then 204 is returned
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And subsequent GetLayerVersion returns 404
	resp2 := doJSON(t, http.MethodGet, layerURL(srv, "/layers/my-layer/versions/1"), nil)
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestDeleteLayerVersion_notFound(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When DeleteLayerVersion is called for a non-existent layer
	resp := doJSON(t, http.MethodDelete, layerURL(srv, "/layers/no-layer/versions/99"), nil)

	// Then 404 is returned
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Attach layer to function ─────────────────────────────────────────────────

func TestUpdateFunctionConfiguration_attachLayer(t *testing.T) {
	// Given a function and a published layer
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "fn-with-layer")
	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/my-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip")},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	// When UpdateFunctionConfiguration is called with Layers
	type updateWithLayersReq struct {
		Layers []string `json:"Layers"`
	}
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/fn-with-layer/configuration"), updateWithLayersReq{
		Layers: []string{lv.LayerVersionArn},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then GetFunctionConfiguration returns the attached layer
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn      string `json:"Arn"`
			CodeSize int64  `json:"CodeSize"`
		} `json:"Layers,omitempty"`
	}
	resp2 := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/fn-with-layer/configuration"), nil)
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var config funcConfigWithLayers
	decodeJSON(t, resp2, &config)
	if len(config.Layers) != 1 {
		t.Fatalf("expected 1 attached layer, got %d", len(config.Layers))
	}
	if config.Layers[0].Arn != lv.LayerVersionArn {
		t.Errorf("layer ARN mismatch: got %q, want %q", config.Layers[0].Arn, lv.LayerVersionArn)
	}
}

func TestUpdateFunctionConfiguration_replaceLayers(t *testing.T) {
	// Given a function with one attached layer and a second published layer.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "fn-replace-layers")

	lvResp1 := doJSON(t, http.MethodPost, layerURL(srv, "/layers/replace-layer-a/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip-a")},
	})
	helpers.AssertStatus(t, lvResp1, http.StatusCreated)
	var lv1 layerVersionResponse
	decodeJSON(t, lvResp1, &lv1)

	lvResp2 := doJSON(t, http.MethodPost, layerURL(srv, "/layers/replace-layer-b/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip-b")},
	})
	helpers.AssertStatus(t, lvResp2, http.StatusCreated)
	var lv2 layerVersionResponse
	decodeJSON(t, lvResp2, &lv2)

	type updateWithLayersReq struct {
		Layers []string `json:"Layers"`
	}
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/fn-replace-layers/configuration"), updateWithLayersReq{
		Layers: []string{lv1.LayerVersionArn},
	})
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When Layers is updated to a different single layer.
	replaceResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/fn-replace-layers/configuration"), updateWithLayersReq{
		Layers: []string{lv2.LayerVersionArn},
	})
	helpers.AssertStatus(t, replaceResp, http.StatusOK)

	// Then configuration readback returns only the replacement layer.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn      string `json:"Arn"`
			CodeSize int64  `json:"CodeSize"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/fn-replace-layers/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 1 {
		t.Fatalf("expected 1 attached layer after replace, got %d", len(cfg.Layers))
	}
	if cfg.Layers[0].Arn != lv2.LayerVersionArn {
		t.Fatalf("expected replacement layer ARN %q, got %q", lv2.LayerVersionArn, cfg.Layers[0].Arn)
	}

	getFnResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/fn-replace-layers"), nil)
	helpers.AssertStatus(t, getFnResp, http.StatusOK)
	var fnResp struct {
		Configuration funcConfigWithLayers `json:"Configuration"`
	}
	decodeJSON(t, getFnResp, &fnResp)
	if len(fnResp.Configuration.Layers) != 1 {
		t.Fatalf("expected GetFunction to report 1 attached layer after replace, got %d", len(fnResp.Configuration.Layers))
	}
	if fnResp.Configuration.Layers[0].Arn != lv2.LayerVersionArn {
		t.Fatalf("expected GetFunction replacement layer ARN %q, got %q", lv2.LayerVersionArn, fnResp.Configuration.Layers[0].Arn)
	}
}

func TestUpdateFunctionConfiguration_clearLayers(t *testing.T) {
	// Given a function with one attached layer.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "fn-clear-layers")

	lvResp := doJSON(t, http.MethodPost, layerURL(srv, "/layers/clear-layer/versions"), publishLayerVersionReq{
		Content: layerContent{ZipFile: []byte("zip")},
	})
	helpers.AssertStatus(t, lvResp, http.StatusCreated)
	var lv layerVersionResponse
	decodeJSON(t, lvResp, &lv)

	type updateWithLayersReq struct {
		Layers []string `json:"Layers"`
	}
	attachResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/fn-clear-layers/configuration"), updateWithLayersReq{
		Layers: []string{lv.LayerVersionArn},
	})
	helpers.AssertStatus(t, attachResp, http.StatusOK)

	// When Layers is updated to an empty list.
	clearResp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/fn-clear-layers/configuration"), map[string]any{
		"Layers": []string{},
	})
	helpers.AssertStatus(t, clearResp, http.StatusOK)

	// Then both GetFunctionConfiguration and GetFunction report no attached layers.
	type funcConfigWithLayers struct {
		functionConfiguration
		Layers []struct {
			Arn      string `json:"Arn"`
			CodeSize int64  `json:"CodeSize"`
		} `json:"Layers,omitempty"`
	}
	getCfgResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/fn-clear-layers/configuration"), nil)
	helpers.AssertStatus(t, getCfgResp, http.StatusOK)
	var cfg funcConfigWithLayers
	decodeJSON(t, getCfgResp, &cfg)
	if len(cfg.Layers) != 0 {
		t.Fatalf("expected 0 attached layers after clear, got %d", len(cfg.Layers))
	}

	getFnResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/fn-clear-layers"), nil)
	helpers.AssertStatus(t, getFnResp, http.StatusOK)
	var fnResp struct {
		Configuration funcConfigWithLayers `json:"Configuration"`
	}
	decodeJSON(t, getFnResp, &fnResp)
	if len(fnResp.Configuration.Layers) != 0 {
		t.Fatalf("expected GetFunction to report 0 attached layers after clear, got %d", len(fnResp.Configuration.Layers))
	}
}

func makeZipWithMode(t *testing.T, name, content string, mode os.FileMode) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: name, Method: zip.Deflate}
	fh.SetMode(mode)
	f, err := w.CreateHeader(fh)
	if err != nil {
		t.Fatalf("zip.CreateHeader: %v", err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		t.Fatalf("zip.Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	return buf.Bytes()
}

func TestInvoke_functionNotFound(t *testing.T) {
	// Does not require Docker — function lookup happens before runtime dispatch.
	srv := helpers.NewTestServer(t)

	// When InvokeFunction is called for a non-existent function
	resp := invokeFunction(t, srv, "no-such-function", nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── InvokeWithResponseStream ────────────────────────────────────────────────

func TestInvokeWithResponseStream_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, streamingURL(srv, "no-such-fn"), bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestInvokeWithResponseStream_badInvocationType(t *testing.T) {
	srv := helpers.NewTestServer(t)

	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "stream-type-fn",
		"Runtime":      "nodejs20.x",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/test",
	})

	req, _ := http.NewRequest(http.MethodPost, streamingURL(srv, "stream-type-fn"), bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Invocation-Type", "Event")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── Event Source Mappings ───────────────────────────────────────────────────

// esmURL builds the event-source-mappings base URL.
func esmURL(srv *helpers.TestServer) string {
	return lambdaURL(srv, "/event-source-mappings")
}

// sqsARN builds a fake SQS queue ARN for tests.
func sqsARN(name string) string {
	return "arn:aws:sqs:us-east-1:000000000000:" + name
}

// dynamoDBStreamARN builds a fake DynamoDB Stream ARN for tests.
func dynamoDBStreamARN(table string) string {
	return "arn:aws:dynamodb:us-east-1:000000000000:table/" + table + "/stream/2024-01-01T00:00:00.000"
}

// createESM creates an event source mapping and returns its decoded body.
func createESM(t *testing.T, srv *helpers.TestServer, body map[string]any) map[string]any {
	t.Helper()
	resp := doJSON(t, http.MethodPost, esmURL(srv), body)
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var out map[string]any
	decodeJSON(t, resp, &out)
	return out
}

// createSQSQueue creates an SQS queue via the test server and returns the URL.
func createSQSQueue(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"QueueName": name})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("createSQSQueue: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("createSQSQueue: status %d", resp.StatusCode)
	}
	var result struct {
		QueueUrl string `json:"QueueUrl"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return result.QueueUrl
}

// sqsReceiveMessages attempts to receive up to 10 messages from the queue and
// returns how many were returned. Does not delete them.
func sqsReceiveMessages(t *testing.T, srv *helpers.TestServer, queueURL string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"QueueUrl":            queueURL,
		"MaxNumberOfMessages": 10,
		"WaitTimeSeconds":     0,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.ReceiveMessage")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sqsReceiveMessages: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		Messages []map[string]any `json:"Messages"`
	}
	helpers.DecodeJSON(t, resp, &result)
	return len(result.Messages)
}

// sqsSendMessage sends a single message to an SQS queue via the test server.
func sqsSendMessage(t *testing.T, srv *helpers.TestServer, queueURL, messageBody string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"QueueUrl":    queueURL,
		"MessageBody": messageBody,
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.SendMessage")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sqsSendMessage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sqsSendMessage: status %d", resp.StatusCode)
	}
}

func TestListEventSourceMappings_empty(t *testing.T) {
	// Given: a server with no ESMs
	srv := helpers.NewTestServer(t)

	// When: we list all ESMs
	resp := doJSON(t, http.MethodGet, esmURL(srv), nil)
	defer resp.Body.Close()

	// Then: 200 with an empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		EventSourceMappings []any `json:"EventSourceMappings"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.EventSourceMappings) != 0 {
		t.Errorf("expected empty list, got %d items", len(out.EventSourceMappings))
	}
}

func TestCreateEventSourceMapping_sqsSource(t *testing.T) {
	// Given: a Lambda function and an SQS queue ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-fn")
	queueARN := sqsARN("esm-queue")

	// When: we create an ESM mapping the SQS queue to the function
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "esm-fn",
		"EventSourceArn": queueARN,
		"BatchSize":      5,
	})

	// Then: the ESM is created with expected fields
	if esm["UUID"] == "" {
		t.Error("expected UUID to be set")
	}
	wantARN := "arn:aws:lambda:us-east-1:000000000000:event-source-mapping:" + esm["UUID"].(string)
	if esm["EventSourceMappingArn"] != wantARN {
		t.Errorf("EventSourceMappingArn: got %v, want %s", esm["EventSourceMappingArn"], wantARN)
	}
	if esm["EventSourceArn"] != queueARN {
		t.Errorf("EventSourceArn: got %v, want %s", esm["EventSourceArn"], queueARN)
	}
	if esm["State"] != "Enabled" {
		t.Errorf("State: got %v, want Enabled", esm["State"])
	}
	if esm["BatchSize"] != float64(5) {
		t.Errorf("BatchSize: got %v, want 5", esm["BatchSize"])
	}
	if esm["FunctionArn"] == "" {
		t.Error("expected FunctionArn to be set")
	}
}

func TestEventSourceMappingLegacyRecordBackfillsARNOnResponsesAndUpdate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const id = "84d6e94f-372a-4c5e-9d39-6fdf46e15432"
	legacy := `{"UUID":"` + id + `","FunctionArn":"arn:aws:lambda:us-east-1:000000000000:function:legacy","EventSourceArn":"arn:aws:sqs:us-east-1:000000000000:legacy","State":"Enabled","BatchSize":10}`
	if err := srv.Store.Set(context.Background(), "lambda:esm", "us-east-1/"+id, legacy); err != nil {
		t.Fatalf("seed legacy event source mapping: %v", err)
	}
	wantARN := "arn:aws:lambda:us-east-1:000000000000:event-source-mapping:" + id

	get := doJSON(t, http.MethodGet, lambdaURL(srv, "/event-source-mappings/"+id), nil)
	var got map[string]any
	decodeJSON(t, get, &got)
	if got["EventSourceMappingArn"] != wantARN {
		t.Errorf("GetEventSourceMapping ARN = %v, want %q", got["EventSourceMappingArn"], wantARN)
	}

	list := doJSON(t, http.MethodGet, esmURL(srv), nil)
	var listed struct {
		Mappings []map[string]any `json:"EventSourceMappings"`
	}
	decodeJSON(t, list, &listed)
	if len(listed.Mappings) != 1 || listed.Mappings[0]["EventSourceMappingArn"] != wantARN {
		t.Errorf("ListEventSourceMappings = %#v, want backfilled ARN", listed.Mappings)
	}

	update := doJSON(t, http.MethodPut, lambdaURL(srv, "/event-source-mappings/"+id), map[string]any{
		"BatchSize": 20, "MaximumBatchingWindowInSeconds": 1,
	})
	helpers.AssertStatus(t, update, http.StatusAccepted)
	decodeJSON(t, update, &got)
	if got["EventSourceMappingArn"] != wantARN {
		t.Errorf("UpdateEventSourceMapping ARN = %v, want %q", got["EventSourceMappingArn"], wantARN)
	}
	raw, found, err := srv.Store.Get(context.Background(), "lambda:esm", "us-east-1/"+id)
	if err != nil || !found || !strings.Contains(raw, wantARN) {
		t.Errorf("updated legacy record = found %v, err %v, raw %q", found, err, raw)
	}
}

func TestCreateEventSourceMapping_dynamodbStreamSource(t *testing.T) {
	// Given: a Lambda function and a DynamoDB Stream ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-ddb-fn")
	streamARN := dynamoDBStreamARN("MyTable")

	// When: we create an ESM mapping the DynamoDB stream to the function
	esm := createESM(t, srv, map[string]any{
		"FunctionName":     "esm-ddb-fn",
		"EventSourceArn":   streamARN,
		"StartingPosition": "TRIM_HORIZON",
	})

	// Then: the ESM is created with Enabled state and the right source ARN
	if esm["UUID"] == "" {
		t.Error("expected UUID to be set")
	}
	if esm["EventSourceArn"] != streamARN {
		t.Errorf("EventSourceArn: got %v, want %s", esm["EventSourceArn"], streamARN)
	}
	if esm["State"] != "Enabled" {
		t.Errorf("State: got %v, want Enabled", esm["State"])
	}
	if esm["StartingPosition"] != "TRIM_HORIZON" {
		t.Errorf("StartingPosition: got %v, want TRIM_HORIZON", esm["StartingPosition"])
	}
}

func TestCreateEventSourceMapping_defaultBatchSize(t *testing.T) {
	// Given: a function and SQS queue ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-default-fn")

	// When: we create an ESM without specifying BatchSize
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "esm-default-fn",
		"EventSourceArn": sqsARN("default-queue"),
	})

	// Then: BatchSize defaults to 10
	if esm["BatchSize"] != float64(10) {
		t.Errorf("BatchSize: got %v, want 10", esm["BatchSize"])
	}
}

func TestCreateEventSourceMapping_disabledInitially(t *testing.T) {
	// Given: a function and SQS queue ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-disabled-fn")

	// When: we create an ESM with Enabled: false
	resp := doJSON(t, http.MethodPost, esmURL(srv), map[string]any{
		"FunctionName":   "esm-disabled-fn",
		"EventSourceArn": sqsARN("disabled-queue"),
		"Enabled":        false,
	})
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var esm map[string]any
	decodeJSON(t, resp, &esm)

	// Then: State is Disabled
	if esm["State"] != "Disabled" {
		t.Errorf("State: got %v, want Disabled", esm["State"])
	}
}

func TestCreateEventSourceMapping_functionNotFound(t *testing.T) {
	// Given: no function named "ghost-fn"
	srv := helpers.NewTestServer(t)

	// When: we create an ESM for it
	resp := doJSON(t, http.MethodPost, esmURL(srv), map[string]any{
		"FunctionName":   "ghost-fn",
		"EventSourceArn": sqsARN("ghost-queue"),
	})
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestCreateEventSourceMapping_invalidSource(t *testing.T) {
	// Given: a function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-invalid-fn")

	// When: we create an ESM with a kinesis ARN (unsupported)
	resp := doJSON(t, http.MethodPost, esmURL(srv), map[string]any{
		"FunctionName":   "esm-invalid-fn",
		"EventSourceArn": "arn:aws:kinesis:us-east-1:000000000000:stream/my-stream",
	})
	defer resp.Body.Close()

	// Then: 400 ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestCreateEventSourceMapping_missingFunctionName(t *testing.T) {
	// Given: a server
	srv := helpers.NewTestServer(t)

	// When: we create an ESM without FunctionName
	resp := doJSON(t, http.MethodPost, esmURL(srv), map[string]any{
		"EventSourceArn": sqsARN("my-queue"),
	})
	defer resp.Body.Close()

	// Then: 400
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestCreateFunction_crossRegionLayer(t *testing.T) {
	// Given: a Lambda function creation request with a layer ARN from a different region
	srv := helpers.NewTestServer(t)

	// When: CreateFunction specifies a layer ARN in eu-west-1 but the server is us-east-1
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "cross-region-layer-fn",
		"Runtime":      "python3.12",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/test-role",
		"Code":         map[string]any{"ZipFile": "UEsDBBQAAAAIAA=="},
		"Layers":       []string{"arn:aws:lambda:eu-west-1:000000000000:layer:my-layer:1"},
	})
	defer resp.Body.Close()

	// Then: real AWS rejects cross-region layers with InvalidParameterValueException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["__type"] != "InvalidParameterValueException" {
		t.Errorf("error type: got %v, want InvalidParameterValueException", body["__type"])
	}
}

func TestUpdateFunctionConfiguration_crossRegionLayer(t *testing.T) {
	// Given: an existing function in us-east-1
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "cross-region-update-fn")

	// When: UpdateFunctionConfiguration specifies a layer ARN in eu-west-1
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/cross-region-update-fn/configuration"),
		map[string]any{
			"Layers": []string{"arn:aws:lambda:eu-west-1:000000000000:layer:my-layer:1"},
		})
	defer resp.Body.Close()

	// Then: real AWS rejects cross-region layers with InvalidParameterValueException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["__type"] != "InvalidParameterValueException" {
		t.Errorf("error type: got %v, want InvalidParameterValueException", body["__type"])
	}
}

func TestCreateEventSourceMapping_crossRegion(t *testing.T) {
	// Given: a function in the test server's default region (us-east-1)
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "cross-region-fn")

	// When: we try to map it to an SQS queue in a different region (eu-west-1)
	resp := doJSON(t, http.MethodPost, esmURL(srv), map[string]any{
		"FunctionName":   "cross-region-fn",
		"EventSourceArn": "arn:aws:sqs:eu-west-1:000000000000:some-queue",
	})
	defer resp.Body.Close()

	// Then: real AWS rejects cross-region ESMs with InvalidParameterValueException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["__type"] != "InvalidParameterValueException" {
		t.Errorf("error type: got %v, want InvalidParameterValueException", body["__type"])
	}
}

func TestGetEventSourceMapping_success(t *testing.T) {
	// Given: an existing ESM
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "get-esm-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "get-esm-fn",
		"EventSourceArn": sqsARN("get-esm-queue"),
	})
	id := esm["UUID"].(string)

	// When: we get it by UUID
	resp := doJSON(t, http.MethodGet, esmURL(srv)+"/"+id, nil)
	defer resp.Body.Close()

	// Then: 200 with the same UUID and fields
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)
	if got["UUID"] != id {
		t.Errorf("UUID: got %v, want %s", got["UUID"], id)
	}
	if got["State"] != "Enabled" {
		t.Errorf("State: got %v, want Enabled", got["State"])
	}
}

func TestGetEventSourceMapping_notFound(t *testing.T) {
	// Given: no ESM with this UUID
	srv := helpers.NewTestServer(t)

	// When: we get a non-existent UUID
	resp := doJSON(t, http.MethodGet, esmURL(srv)+"/00000000-0000-0000-0000-000000000000", nil)
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateEventSourceMapping_disable(t *testing.T) {
	// Given: an enabled ESM
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "upd-esm-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "upd-esm-fn",
		"EventSourceArn": sqsARN("upd-esm-queue"),
	})
	id := esm["UUID"].(string)

	// When: we disable it
	resp := doJSON(t, http.MethodPut, esmURL(srv)+"/"+id, map[string]any{
		"Enabled": false,
	})
	defer resp.Body.Close()

	// Then: 202 with State Disabled
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)
	if got["State"] != "Disabled" {
		t.Errorf("State: got %v, want Disabled", got["State"])
	}
}

func TestUpdateEventSourceMapping_changeBatchSize(t *testing.T) {
	// Given: an ESM with BatchSize 5
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "batch-esm-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "batch-esm-fn",
		"EventSourceArn": sqsARN("batch-esm-queue"),
		"BatchSize":      5,
	})
	id := esm["UUID"].(string)

	// When: we update BatchSize to 20
	resp := doJSON(t, http.MethodPut, esmURL(srv)+"/"+id, map[string]any{
		"BatchSize": 20, "MaximumBatchingWindowInSeconds": 1,
	})
	defer resp.Body.Close()

	// Then: 202 with new BatchSize
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)
	if got["BatchSize"] != float64(20) {
		t.Errorf("BatchSize: got %v, want 20", got["BatchSize"])
	}
	if got["MaximumBatchingWindowInSeconds"] != float64(1) {
		t.Errorf("MaximumBatchingWindowInSeconds: got %v, want 1", got["MaximumBatchingWindowInSeconds"])
	}
}

func TestUpdateEventSourceMapping_notFound(t *testing.T) {
	// Given: no ESM
	srv := helpers.NewTestServer(t)

	// When: we update a non-existent UUID
	resp := doJSON(t, http.MethodPut, esmURL(srv)+"/no-such-uuid", map[string]any{
		"BatchSize": 5,
	})
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestDeleteEventSourceMapping_success(t *testing.T) {
	// Given: an ESM
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "del-esm-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "del-esm-fn",
		"EventSourceArn": sqsARN("del-esm-queue"),
	})
	id := esm["UUID"].(string)

	// When: we delete it
	resp := doJSON(t, http.MethodDelete, esmURL(srv)+"/"+id, nil)
	defer resp.Body.Close()

	// Then: 200 with the deleted ESM body
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)
	if got["UUID"] != id {
		t.Errorf("UUID mismatch: got %v, want %s", got["UUID"], id)
	}

	// And: subsequent GET returns 404
	resp2 := doJSON(t, http.MethodGet, esmURL(srv)+"/"+id, nil)
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusNotFound)
}

func TestDeleteEventSourceMapping_notFound(t *testing.T) {
	// Given: no ESM
	srv := helpers.NewTestServer(t)

	// When: we delete a non-existent UUID
	resp := doJSON(t, http.MethodDelete, esmURL(srv)+"/no-such-uuid", nil)
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListEventSourceMappings_byFunctionName(t *testing.T) {
	// Given: two ESMs for different functions
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-fn-a")
	createFunction(t, srv, "esm-fn-b")
	createESM(t, srv, map[string]any{
		"FunctionName":   "esm-fn-a",
		"EventSourceArn": sqsARN("queue-a"),
	})
	createESM(t, srv, map[string]any{
		"FunctionName":   "esm-fn-b",
		"EventSourceArn": sqsARN("queue-b"),
	})

	// When: we list by FunctionName
	resp := doJSON(t, http.MethodGet, esmURL(srv)+"?FunctionName=esm-fn-a", nil)
	defer resp.Body.Close()

	// Then: only esm-fn-a's mapping is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		EventSourceMappings []map[string]any `json:"EventSourceMappings"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.EventSourceMappings) != 1 {
		t.Fatalf("expected 1 ESM, got %d", len(out.EventSourceMappings))
	}
	arnGot, _ := out.EventSourceMappings[0]["EventSourceArn"].(string)
	if arnGot != sqsARN("queue-a") {
		t.Errorf("EventSourceArn: got %q, want %q", arnGot, sqsARN("queue-a"))
	}
}

func TestESMDelivery_sqsToLambda(t *testing.T) {
	// Given: a function, an SQS queue, and an ESM connecting them.
	// The stub NodeRuntime returns success (no FunctionError), so messages
	// will be deleted after the ESM poller delivers them.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "delivery-fn")
	queueURL := createSQSQueue(t, srv, "delivery-queue")
	createESM(t, srv, map[string]any{
		"FunctionName":   "delivery-fn",
		"EventSourceArn": sqsARN("delivery-queue"),
		"BatchSize":      10,
	})

	// When: we send a message to the queue
	sqsSendMessage(t, srv, queueURL, `{"hello":"world"}`)

	// Then: after the ESM poll interval (1s + safety margin), the message is consumed.
	// The visibility timeout used by the poller is 30s, so the message will be
	// invisible (not deleted) if delivery fails — the queue would appear empty either
	// way from ReceiveMessage's perspective.  We rely on the server receiving 0 messages
	// on a fresh receive (either deleted or still invisible due to visibility window).
	time.Sleep(2 * time.Second)
	remaining := sqsReceiveMessages(t, srv, queueURL)
	if remaining != 0 {
		t.Errorf("expected 0 visible messages after ESM delivery (got %d)", remaining)
	}
}

func TestCreateEventSourceMapping_withDestinationConfig(t *testing.T) {
	// Given: a function and a DynamoDB Stream ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-dest-fn")
	streamARN := dynamoDBStreamARN("dest-table")
	dlqARN := sqsARN("esm-on-failure-queue")

	// When: we create an ESM with DestinationConfig and MaximumRetryAttempts
	maxRetries := 2
	esm := createESM(t, srv, map[string]any{
		"FunctionName":         "esm-dest-fn",
		"EventSourceArn":       streamARN,
		"StartingPosition":     "TRIM_HORIZON",
		"MaximumRetryAttempts": maxRetries,
		"DestinationConfig": map[string]any{
			"OnFailure": map[string]any{
				"Destination": dlqARN,
			},
		},
	})

	// Then: the ESM stores and returns the DestinationConfig
	if esm["MaximumRetryAttempts"] != float64(maxRetries) {
		t.Errorf("MaximumRetryAttempts: got %v, want %d", esm["MaximumRetryAttempts"], maxRetries)
	}
	dest, ok := esm["DestinationConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected DestinationConfig to be present")
	}
	onFailure, ok := dest["OnFailure"].(map[string]any)
	if !ok {
		t.Fatal("expected DestinationConfig.OnFailure to be present")
	}
	if onFailure["Destination"] != dlqARN {
		t.Errorf("DestinationConfig.OnFailure.Destination: got %v, want %s", onFailure["Destination"], dlqARN)
	}

	// When: we GET the ESM by UUID
	resp := doJSON(t, http.MethodGet, esmURL(srv)+"/"+esm["UUID"].(string), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)

	// Then: DestinationConfig is preserved on read
	dest2, ok := got["DestinationConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected DestinationConfig on GET")
	}
	onFailure2, ok := dest2["OnFailure"].(map[string]any)
	if !ok {
		t.Fatal("expected DestinationConfig.OnFailure on GET")
	}
	if onFailure2["Destination"] != dlqARN {
		t.Errorf("GET DestinationConfig.OnFailure.Destination: got %v, want %s", onFailure2["Destination"], dlqARN)
	}
}

func TestCreateEventSourceMapping_withScalingConfig(t *testing.T) {
	// Given: a function and an SQS queue ARN
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-scaling-fn")
	qArn := sqsARN("scaling-queue")

	// When: we create an ESM with ScalingConfig.MaximumConcurrency = 5
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "esm-scaling-fn",
		"EventSourceArn": qArn,
		"ScalingConfig": map[string]any{
			"MaximumConcurrency": 5,
		},
	})

	// Then: the ESM stores and returns the ScalingConfig
	sc, ok := esm["ScalingConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected ScalingConfig to be present")
	}
	if sc["MaximumConcurrency"] != float64(5) {
		t.Errorf("ScalingConfig.MaximumConcurrency: got %v, want 5", sc["MaximumConcurrency"])
	}

	// When: we GET the ESM by UUID
	resp := doJSON(t, http.MethodGet, esmURL(srv)+"/"+esm["UUID"].(string), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)

	// Then: ScalingConfig is preserved
	sc2, ok := got["ScalingConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected ScalingConfig on GET")
	}
	if sc2["MaximumConcurrency"] != float64(5) {
		t.Errorf("GET ScalingConfig.MaximumConcurrency: got %v, want 5", sc2["MaximumConcurrency"])
	}
}

func TestUpdateEventSourceMapping_changeScalingConfig(t *testing.T) {
	// Given: an ESM without ScalingConfig
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "esm-update-sc-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName":   "esm-update-sc-fn",
		"EventSourceArn": sqsARN("sc-update-queue"),
	})
	id := esm["UUID"].(string)

	// When: we update ScalingConfig to MaximumConcurrency=3
	resp := doJSON(t, http.MethodPut, esmURL(srv)+"/"+id, map[string]any{
		"ScalingConfig": map[string]any{
			"MaximumConcurrency": 3,
		},
	})
	defer resp.Body.Close()

	// Then: 202 with ScalingConfig set
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var got map[string]any
	helpers.DecodeJSON(t, resp, &got)
	sc, ok := got["ScalingConfig"].(map[string]any)
	if !ok {
		t.Fatal("expected ScalingConfig in update response")
	}
	if sc["MaximumConcurrency"] != float64(3) {
		t.Errorf("ScalingConfig.MaximumConcurrency: got %v, want 3", sc["MaximumConcurrency"])
	}
}

// ─── PackageType: Image ───────────────────────────────────────────────────────

func TestCreateFunction_imagePackageType_success(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When CreateFunction is called with PackageType=Image and an ImageUri
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "image-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"},
	})
	defer resp.Body.Close()

	// Then 201 + PackageType=Image, ImageUri set, Runtime omitted
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.PackageType != "Image" {
		t.Errorf("PackageType = %q, want Image", cfg.PackageType)
	}
	if cfg.ImageUri != "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest" {
		t.Errorf("ImageUri = %q, want ECR URI", cfg.ImageUri)
	}
	if cfg.FunctionName != "image-fn" {
		t.Errorf("FunctionName = %q, want image-fn", cfg.FunctionName)
	}
	if cfg.FunctionArn == "" {
		t.Error("FunctionArn must be set")
	}
	if cfg.State != "Active" {
		t.Errorf("State = %q, want Active", cfg.State)
	}
}

func TestCreateFunction_imagePackageType_missingImageUri(t *testing.T) {
	// Given a fresh server
	srv := helpers.NewTestServer(t)

	// When CreateFunction is called with PackageType=Image but no ImageUri
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "image-fn-bad",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{},
	})
	defer resp.Body.Close()

	// Then 400 InvalidParameterValueException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetFunction_imageFunction_returnsECRRepositoryType(t *testing.T) {
	// Given an image function
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "image-get-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// When GetFunction is called
	resp2 := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/image-get-fn"), nil)
	defer resp2.Body.Close()

	// Then Code.RepositoryType = "ECR" (not "S3")
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out getFunctionResponse
	decodeJSON(t, resp2, &out)

	if out.Configuration.PackageType != "Image" {
		t.Errorf("PackageType = %q, want Image", out.Configuration.PackageType)
	}
	if out.Code == nil {
		t.Fatal("Code block must be present")
	}
	if out.Code.RepositoryType != "ECR" {
		t.Errorf("RepositoryType = %q, want ECR", out.Code.RepositoryType)
	}
	if out.Configuration.ImageUri != "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest" {
		t.Errorf("ImageUri = %q", out.Configuration.ImageUri)
	}
}

func TestUpdateFunctionCode_imageFunction_updatesImageUri(t *testing.T) {
	// Given an image function
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "image-update-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:v1"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created functionConfiguration
	decodeJSON(t, resp, &created)

	// When UpdateFunctionCode is called with a new ImageUri
	resp2 := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/image-update-fn/code"), map[string]any{
		"ImageUri": "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:v2",
	})
	defer resp2.Body.Close()

	// Then 200 + updated ImageUri and new RevisionId
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp2, &cfg)

	if cfg.ImageUri != "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:v2" {
		t.Errorf("ImageUri = %q, want :v2", cfg.ImageUri)
	}
	if cfg.RevisionId == created.RevisionId {
		t.Error("RevisionId must change after UpdateFunctionCode")
	}
}

// ─── VpcConfig ───────────────────────────────────────────────────────────────

// ec2Query is a helper to call EC2 Query-protocol actions from Lambda tests.
func ec2Query(t *testing.T, srv *helpers.TestServer, action string, params map[string]string) *http.Response {
	t.Helper()
	v := make(map[string][]string)
	v["Action"] = []string{action}
	v["Version"] = []string{"2016-11-15"}
	for k, val := range params {
		v[k] = []string{val}
	}
	var pairs []string
	for k, vals := range v {
		for _, val := range vals {
			pairs = append(pairs, k+"="+val)
		}
	}
	body := strings.Join(pairs, "&")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ec2Query %s: %v", action, err)
	}
	return resp
}

func TestCreateFunction_withVpcConfig(t *testing.T) {
	// Given: a fresh server
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called with VpcConfig
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "vpc-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		VpcConfig: &vpcConfigReq{
			SubnetIds:        []string{"subnet-abc123", "subnet-def456"},
			SecurityGroupIds: []string{"sg-111", "sg-222"},
		},
	})
	defer resp.Body.Close()

	// Then: 201 with VpcConfig in response
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.VpcConfig == nil {
		t.Fatal("expected VpcConfig to be set")
	}
	if len(cfg.VpcConfig.SubnetIds) != 2 {
		t.Errorf("expected 2 SubnetIds, got %d", len(cfg.VpcConfig.SubnetIds))
	}
	if len(cfg.VpcConfig.SecurityGroupIds) != 2 {
		t.Errorf("expected 2 SecurityGroupIds, got %d", len(cfg.VpcConfig.SecurityGroupIds))
	}
}

func TestGetFunction_withVpcConfig(t *testing.T) {
	// Given: a function with VpcConfig
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "vpc-get-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		VpcConfig: &vpcConfigReq{
			SubnetIds:        []string{"subnet-aaa"},
			SecurityGroupIds: []string{"sg-bbb"},
		},
	})
	resp.Body.Close()

	// When: GetFunction is called
	getResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/vpc-get-fn"), nil)
	defer getResp.Body.Close()

	// Then: 200 with VpcConfig in Configuration
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var result getFunctionResponse
	decodeJSON(t, getResp, &result)

	if result.Configuration.VpcConfig == nil {
		t.Fatal("expected VpcConfig in GetFunction response")
	}
	if len(result.Configuration.VpcConfig.SubnetIds) != 1 {
		t.Errorf("expected 1 SubnetId, got %d", len(result.Configuration.VpcConfig.SubnetIds))
	}
	if result.Configuration.VpcConfig.SubnetIds[0] != "subnet-aaa" {
		t.Errorf("expected subnet-aaa, got %q", result.Configuration.VpcConfig.SubnetIds[0])
	}
}

func TestGetFunctionConfiguration_withVpcConfig(t *testing.T) {
	// Given: a function with VpcConfig
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "vpc-cfg-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		VpcConfig: &vpcConfigReq{
			SubnetIds:        []string{"subnet-x"},
			SecurityGroupIds: []string{"sg-y"},
		},
	})
	resp.Body.Close()

	// When: GetFunctionConfiguration is called
	getResp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/vpc-cfg-fn/configuration"), nil)
	defer getResp.Body.Close()

	// Then: 200 with VpcConfig
	helpers.AssertStatus(t, getResp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, getResp, &cfg)

	if cfg.VpcConfig == nil {
		t.Fatal("expected VpcConfig")
	}
	if cfg.VpcConfig.SubnetIds[0] != "subnet-x" {
		t.Errorf("expected subnet-x, got %q", cfg.VpcConfig.SubnetIds[0])
	}
}

func TestUpdateFunctionConfiguration_addsVpcConfig(t *testing.T) {
	// Given: a function without VpcConfig
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "vpc-update-fn")

	// When: UpdateFunctionConfiguration is called with VpcConfig
	resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/vpc-update-fn/configuration"), map[string]any{
		"VpcConfig": map[string]any{
			"SubnetIds":        []string{"subnet-new1", "subnet-new2"},
			"SecurityGroupIds": []string{"sg-new"},
		},
	})
	defer resp.Body.Close()

	// Then: 200 with VpcConfig set
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.VpcConfig == nil {
		t.Fatal("expected VpcConfig after update")
	}
	if len(cfg.VpcConfig.SubnetIds) != 2 {
		t.Errorf("expected 2 SubnetIds, got %d", len(cfg.VpcConfig.SubnetIds))
	}
	if cfg.VpcConfig.SecurityGroupIds[0] != "sg-new" {
		t.Errorf("expected sg-new, got %q", cfg.VpcConfig.SecurityGroupIds[0])
	}
}

func TestCreateFunction_withVpcConfig_resolvesVpcId(t *testing.T) {
	// Given: an EC2 VPC with a subnet (EC2+Lambda both enabled by default)
	srv := helpers.NewTestServer(t)

	// Create VPC via EC2
	vpcResp := ec2Query(t, srv, "CreateVpc", map[string]string{"CidrBlock": "10.0.0.0/16"})
	defer vpcResp.Body.Close()
	var vpcResult struct {
		Vpc struct {
			VpcID string `xml:"vpcId"`
		} `xml:"vpc"`
	}
	vpcBody, _ := io.ReadAll(vpcResp.Body)
	xml.Unmarshal(vpcBody, &vpcResult) //nolint:errcheck
	vpcID := vpcResult.Vpc.VpcID
	if vpcID == "" {
		t.Fatal("failed to create VPC")
	}

	// Create subnet in the VPC
	subResp := ec2Query(t, srv, "CreateSubnet", map[string]string{
		"VpcId":     vpcID,
		"CidrBlock": "10.0.1.0/24",
	})
	defer subResp.Body.Close()
	var subResult struct {
		Subnet struct {
			SubnetID string `xml:"subnetId"`
		} `xml:"subnet"`
	}
	subBody, _ := io.ReadAll(subResp.Body)
	xml.Unmarshal(subBody, &subResult) //nolint:errcheck
	subnetID := subResult.Subnet.SubnetID
	if subnetID == "" {
		t.Fatal("failed to create subnet")
	}

	// When: CreateFunction is called with VpcConfig referencing the real subnet
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "vpc-resolve-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		VpcConfig: &vpcConfigReq{
			SubnetIds:        []string{subnetID},
			SecurityGroupIds: []string{"sg-test"},
		},
	})
	defer resp.Body.Close()

	// Then: 201 and VpcConfig.VpcId is resolved from the subnet
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	if cfg.VpcConfig == nil {
		t.Fatal("expected VpcConfig")
	}
	if cfg.VpcConfig.VpcId != vpcID {
		t.Errorf("expected VpcId=%s (resolved from subnet), got %q", vpcID, cfg.VpcConfig.VpcId)
	}
}

func TestCreateFunction_withoutVpcConfig_noVpcConfigInResponse(t *testing.T) {
	// Given: a fresh server
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called without VpcConfig
	cfg := createFunction(t, srv, "no-vpc-fn")

	// Then: VpcConfig is nil/absent in response
	if cfg.VpcConfig != nil {
		t.Errorf("expected no VpcConfig, got %+v", cfg.VpcConfig)
	}
}

func TestListFunctions_includesVpcConfig(t *testing.T) {
	// Given: a function with VpcConfig
	srv := helpers.NewTestServer(t)
	doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "vpc-list-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
		VpcConfig: &vpcConfigReq{
			SubnetIds:        []string{"subnet-list1"},
			SecurityGroupIds: []string{"sg-list1"},
		},
	}).Body.Close()

	// When: ListFunctions is called
	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions"), nil)
	defer resp.Body.Close()

	// Then: the function's VpcConfig is included
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Functions []functionConfiguration `json:"Functions"`
	}
	decodeJSON(t, resp, &out)

	var found *functionConfiguration
	for i := range out.Functions {
		if out.Functions[i].FunctionName == "vpc-list-fn" {
			found = &out.Functions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("vpc-list-fn not found in ListFunctions")
	}
	if found.VpcConfig == nil {
		t.Fatal("expected VpcConfig in ListFunctions response")
	}
	if found.VpcConfig.SubnetIds[0] != "subnet-list1" {
		t.Errorf("expected subnet-list1, got %q", found.VpcConfig.SubnetIds[0])
	}
}

// ─── ImageConfig ──────────────────────────────────────────────────────────────

func TestCreateFunction_imageConfig_storedAndReturned(t *testing.T) {
	// Given an image function created with ImageConfig overrides
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "imgcfg-create-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"},
		ImageConfig: &imageConfigReq{
			EntryPoint:       []string{"/entry.sh"},
			Command:          []string{"handler"},
			WorkingDirectory: "/var/task",
		},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created functionConfiguration
	decodeJSON(t, resp, &created)

	// Then ImageConfig is present in the create response
	if created.ImageConfigResponse == nil || created.ImageConfigResponse.ImageConfig == nil {
		t.Fatal("ImageConfigResponse.ImageConfig must be present in CreateFunction response")
	}
	imageConfig := created.ImageConfigResponse.ImageConfig
	if len(imageConfig.EntryPoint) != 1 || imageConfig.EntryPoint[0] != "/entry.sh" {
		t.Errorf("EntryPoint = %v, want [/entry.sh]", imageConfig.EntryPoint)
	}
	if len(imageConfig.Command) != 1 || imageConfig.Command[0] != "handler" {
		t.Errorf("Command = %v, want [handler]", imageConfig.Command)
	}
	if imageConfig.WorkingDirectory != "/var/task" {
		t.Errorf("WorkingDirectory = %q, want /var/task", imageConfig.WorkingDirectory)
	}

	// And ImageConfig is returned by GetFunction
	resp2 := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/imgcfg-create-fn"), nil)
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out getFunctionResponse
	decodeJSON(t, resp2, &out)

	if out.Configuration.ImageConfigResponse == nil || out.Configuration.ImageConfigResponse.ImageConfig == nil {
		t.Fatal("ImageConfigResponse.ImageConfig must be present in GetFunction response")
	}
	if out.Configuration.ImageConfigResponse.ImageConfig.WorkingDirectory != "/var/task" {
		t.Errorf("WorkingDirectory = %q, want /var/task", out.Configuration.ImageConfigResponse.ImageConfig.WorkingDirectory)
	}
}

func TestUpdateFunctionConfiguration_imageConfig_patches(t *testing.T) {
	// Given an image function with no ImageConfig
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "imgcfg-update-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// When UpdateFunctionConfiguration is called with ImageConfig
	resp2 := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/imgcfg-update-fn/configuration"), map[string]any{
		"ImageConfig": map[string]any{
			"Command":          []string{"my-handler"},
			"WorkingDirectory": "/app",
		},
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var updated functionConfiguration
	decodeJSON(t, resp2, &updated)

	// Then ImageConfig reflects the update
	if updated.ImageConfigResponse == nil || updated.ImageConfigResponse.ImageConfig == nil {
		t.Fatal("ImageConfigResponse.ImageConfig must be present in UpdateFunctionConfiguration response")
	}
	imageConfig := updated.ImageConfigResponse.ImageConfig
	if len(imageConfig.Command) != 1 || imageConfig.Command[0] != "my-handler" {
		t.Errorf("Command = %v, want [my-handler]", imageConfig.Command)
	}
	if imageConfig.WorkingDirectory != "/app" {
		t.Errorf("WorkingDirectory = %q, want /app", imageConfig.WorkingDirectory)
	}
}

func TestUpdateFunctionConfiguration_imageConfig_clearable(t *testing.T) {
	// Given an image function with ImageConfig
	srv := helpers.NewTestServer(t)
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "imgcfg-clear-fn",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		PackageType:  "Image",
		Code:         &lambdaCode{ImageUri: "123456789012.dkr.ecr.us-east-1.amazonaws.com/my-fn:latest"},
		ImageConfig: &imageConfigReq{
			Command: []string{"old-handler"},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// When UpdateFunctionConfiguration sends an empty ImageConfig object
	resp2 := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/imgcfg-clear-fn/configuration"), map[string]any{
		"ImageConfig": map[string]any{},
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var updated functionConfiguration
	decodeJSON(t, resp2, &updated)

	// Then ImageConfig is cleared (nil or empty)
	if updated.ImageConfigResponse != nil && updated.ImageConfigResponse.ImageConfig != nil {
		t.Errorf("expected ImageConfigResponse cleared, got %+v", updated.ImageConfigResponse)
	}
}

// ─── Concurrency ──────────────────────────────────────────────────────────────

type concurrencyResponse struct {
	ReservedConcurrentExecutions int `json:"ReservedConcurrentExecutions"`
}

// Reserved concurrency is served on two API versions that are not Lambda's
// 2015-03-31 base — these are the paths the AWS SDKs and CLI actually call:
//
//	PutFunctionConcurrency     PUT    /2017-10-31/functions/{name}/concurrency
//	DeleteFunctionConcurrency  DELETE /2017-10-31/functions/{name}/concurrency
//	GetFunctionConcurrency     GET    /2019-09-30/functions/{name}/concurrency
func putConcurrencyURL(srv *helpers.TestServer, name string) string {
	return srv.URL + "/2017-10-31/functions/" + name + "/concurrency"
}

func deleteConcurrencyURL(srv *helpers.TestServer, name string) string {
	return srv.URL + "/2017-10-31/functions/" + name + "/concurrency"
}

func getConcurrencyURL(srv *helpers.TestServer, name string) string {
	return srv.URL + "/2019-09-30/functions/" + name + "/concurrency"
}

type provisionedConcurrencyResponse struct {
	AllocatedProvisionedConcurrentExecutions int    `json:"AllocatedProvisionedConcurrentExecutions"`
	RequestedProvisionedConcurrentExecutions int    `json:"RequestedProvisionedConcurrentExecutions"`
	AvailableProvisionedConcurrentExecutions int    `json:"AvailableProvisionedConcurrentExecutions"`
	Status                                   string `json:"Status"`
	LastModified                             string `json:"LastModified"`
}

func TestPutFunctionConcurrency_success(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-fn")

	// When PutFunctionConcurrency is called on the AWS API path
	resp := doJSON(t, http.MethodPut, putConcurrencyURL(srv, "concurrency-fn"), map[string]any{
		"ReservedConcurrentExecutions": 50,
	})
	defer resp.Body.Close()

	// Then 200 with the concurrency value
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out concurrencyResponse
	decodeJSON(t, resp, &out)
	if out.ReservedConcurrentExecutions != 50 {
		t.Errorf("ReservedConcurrentExecutions = %d, want 50", out.ReservedConcurrentExecutions)
	}
}

func TestPutFunctionConcurrency_negativeValueReturnsInvalidParameter(t *testing.T) {
	// Given: a function exists.
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-invalid-fn")

	// When: PutFunctionConcurrency receives a value below AWS's minimum.
	resp := doJSON(t, http.MethodPut, putConcurrencyURL(srv, "concurrency-invalid-fn"), map[string]any{
		"ReservedConcurrentExecutions": -1,
	})
	defer resp.Body.Close()

	// Then: Lambda owns the validation and returns its modeled error.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestPutFunctionConcurrency_missingValueReturnsInvalidParameter(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-missing-fn")
	resp := doJSON(t, http.MethodPut, putConcurrencyURL(srv, "concurrency-missing-fn"), map[string]any{})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterValueException")
}

func TestUnsupportedFunctionAndEventSourceConfigurationFailsBeforeMutation(t *testing.T) {
	srv := helpers.NewTestServer(t)
	// TracingConfig, EphemeralStorage and KMSKeyArn are deliberately absent:
	// they are stored and echoed now, and covered by
	// tracing_storage_kms_test.go. DeadLetterConfig is absent for a stronger
	// reason — it is implemented, and dead_letter_config_test.go owns it. So
	// is Publish, which create_function_publish_test.go owns.
	// Everything still listed here would promise behaviour Overcast does not
	// deliver — see CreateFunction's gate.
	createFunctionFields := map[string]any{
		"SnapStart":              map[string]any{"ApplyOn": "PublishedVersions"},
		"CapacityProviderConfig": map[string]any{"LambdaManagedInstancesCapacityProviderConfig": map[string]any{}},
		"DurableConfig":          map[string]any{"RetentionPeriodInDays": 1},
		// Every value here has to be one that actually asks for the feature:
		// an explicit falsy value means "do nothing" and is accepted (see
		// TestUnsupportedFields_ExplicitNoOpValuesAreNotRequests).
		"PublishTo":     "LATEST_PUBLISHED",
		"TenancyConfig": map[string]any{"TenantIsolationMode": "PER_TENANT"},
	}
	for field, value := range createFunctionFields {
		t.Run("function/"+field, func(t *testing.T) {
			name := "unsupported-" + strings.ToLower(field)
			request := map[string]any{
				"FunctionName": name, "Runtime": "python3.12", "Handler": "index.handler",
				"Role": "arn:aws:iam::000000000000:role/lambda-role", "Code": map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA=="}, field: value,
			}
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), request)
			assertLambdaUnsupported(t, resp)
			get := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/"+name+"/configuration"), nil)
			helpers.AssertStatus(t, get, http.StatusNotFound)
			get.Body.Close()
		})
	}
	for field, value := range map[string]any{
		"S3ObjectStorageMode": "Zip",
		"SourceKMSKeyArn":     "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
	} {
		t.Run("function-code/"+field, func(t *testing.T) {
			name := "unsupported-code-" + strings.ToLower(field)
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
				"FunctionName": name, "Runtime": "python3.12", "Handler": "index.handler",
				"Role": "arn:aws:iam::000000000000:role/lambda-role",
				"Code": map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==", field: value},
			})
			assertLambdaUnsupported(t, resp)
		})
	}

	createFunction(t, srv, "unsupported-update-fn")
	baseline := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/unsupported-update-fn/configuration"), map[string]any{
		"Description": "before",
	})
	helpers.AssertStatus(t, baseline, http.StatusOK)
	var baselineConfig functionConfiguration
	decodeJSON(t, baseline, &baselineConfig)
	updateConfigurationFields := map[string]any{
		"SnapStart":              map[string]any{"ApplyOn": "PublishedVersions"},
		"CapacityProviderConfig": map[string]any{"LambdaManagedInstancesCapacityProviderConfig": map[string]any{}},
		"DurableConfig":          map[string]any{"RetentionPeriodInDays": 1},
		"RevisionId":             "revision",
	}
	for field, value := range updateConfigurationFields {
		t.Run("function-update/"+field, func(t *testing.T) {
			resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/unsupported-update-fn/configuration"), map[string]any{
				"Description": "mutated", field: value,
			})
			assertLambdaUnsupported(t, resp)
			get := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/unsupported-update-fn/configuration"), nil)
			var config struct {
				Description string `json:"Description"`
			}
			decodeJSON(t, get, &config)
			if config.Description != "before" {
				t.Fatalf("Description = %q after rejected %s update, want before", config.Description, field)
			}
		})
	}
	for field, value := range map[string]any{
		"DryRun": true, "Publish": true, "PublishTo": "LATEST_PUBLISHED", "RevisionId": "revision",
		"S3ObjectStorageMode": "Zip", "SourceKMSKeyArn": "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
	} {
		t.Run("function-code-update/"+field, func(t *testing.T) {
			resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/unsupported-update-fn/code"), map[string]any{
				"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==", field: value,
			})
			assertLambdaUnsupported(t, resp)
			get := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/unsupported-update-fn/configuration"), nil)
			var config functionConfiguration
			decodeJSON(t, get, &config)
			if config.RevisionId != baselineConfig.RevisionId {
				t.Fatalf("RevisionId = %q after rejected %s code update, want %q", config.RevisionId, field, baselineConfig.RevisionId)
			}
		})
	}

	// Tags is deliberately absent: CreateEventSourceMapping stores them (see
	// TestCreateEventSourceMapping_StoresTags). Every value below is populated
	// because an empty list or object means "do nothing" and is accepted.
	//
	// FunctionResponseTypes is deliberately absent too: the poller honours
	// ["ReportBatchItemFailures"] since #512, so it is accepted rather than
	// refused — see TestCreateEventSourceMapping_ReportBatchItemFailures.
	esmFields := map[string]any{
		"ParallelizationFactor": 2, "StartingPositionTimestamp": 1000,
		"SourceAccessConfigurations": []any{map[string]any{"Type": "BASIC_AUTH", "URI": "arn:aws:secretsmanager:us-east-1:000000000000:secret:kafka"}},
		"SelfManagedEventSource":     map[string]any{"Endpoints": map[string]any{"KafkaBootstrapServers": []string{"host:9092"}}},
		"Topics":                     []string{"topic"}, "Queues": []string{"queue"},
		"KmsKeyArn":     "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
		"MetricsConfig": map[string]any{"Metrics": []string{"EventCount"}}, "ProvisionedPollerConfig": map[string]any{"MinimumPollers": 1, "MaximumPollers": 2},
		"AmazonManagedKafkaEventSourceConfig": map[string]any{"ConsumerGroupId": "group"},
		"DocumentDBEventSourceConfig":         map[string]any{"DatabaseName": "db"},
		"LoggingConfig":                       map[string]any{"SystemLogLevel": "INFO"},
		"SelfManagedKafkaEventSourceConfig":   map[string]any{"ConsumerGroupId": "group"},
	}
	for field, value := range esmFields {
		t.Run("esm/"+field, func(t *testing.T) {
			resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/event-source-mappings/"), map[string]any{field: value})
			assertLambdaUnsupported(t, resp)
		})
	}

	createFunction(t, srv, "unsupported-esm-update-fn")
	esm := createESM(t, srv, map[string]any{
		"FunctionName": "unsupported-esm-update-fn", "EventSourceArn": sqsARN("unsupported-esm-update-queue"),
	})
	id := esm["UUID"].(string)
	for field, value := range map[string]any{
		"AmazonManagedKafkaEventSourceConfig": map[string]any{"ConsumerGroupId": "group"},
		"DocumentDBEventSourceConfig":         map[string]any{"DatabaseName": "db"},
		"LoggingConfig":                       map[string]any{"SystemLogLevel": "INFO"},
		"SelfManagedKafkaEventSourceConfig":   map[string]any{"ConsumerGroupId": "group"},
		"Topics":                              []string{"topic"}, "Queues": []string{"queue"},
	} {
		t.Run("esm-update/"+field, func(t *testing.T) {
			resp := doJSON(t, http.MethodPut, lambdaURL(srv, "/event-source-mappings/"+id), map[string]any{
				"BatchSize": 99, field: value,
			})
			assertLambdaUnsupported(t, resp)
			get := doJSON(t, http.MethodGet, lambdaURL(srv, "/event-source-mappings/"+id), nil)
			var mapping map[string]any
			decodeJSON(t, get, &mapping)
			if mapping["BatchSize"] != float64(10) {
				t.Fatalf("BatchSize = %v after rejected %s update, want 10", mapping["BatchSize"], field)
			}
		})
	}
}

func TestUpdateFunctionConfiguration_explicitEmptyValuesClearOptionals(t *testing.T) {
	srv := helpers.NewTestServer(t)
	create := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "clear-optionals-fn", "Runtime": "python3.12", "Handler": "index.handler",
		"Role": "arn:aws:iam::000000000000:role/lambda-role", "Code": map[string]any{"ZipFile": "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA=="},
		"Description": "remove me", "Environment": map[string]any{"Variables": map[string]string{"A": "B"}},
		"Layers":        []string{"arn:aws:lambda:us-east-1:000000000000:layer:test:1"},
		"LoggingConfig": map[string]any{"LogGroup": "/custom/group", "LogFormat": "Text"},
	})
	helpers.AssertStatus(t, create, http.StatusCreated)
	create.Body.Close()
	update := doJSON(t, http.MethodPut, lambdaURL(srv, "/functions/clear-optionals-fn/configuration"), map[string]any{
		"Description": "", "Environment": map[string]any{"Variables": map[string]string{}}, "Layers": []string{},
	})
	helpers.AssertStatus(t, update, http.StatusOK)
	var config map[string]any
	decodeJSON(t, update, &config)
	if _, ok := config["Description"]; ok {
		t.Error("Description was not cleared")
	}
	if _, ok := config["Environment"]; ok {
		t.Error("Environment was not cleared")
	}
	if _, ok := config["Layers"]; ok {
		t.Error("Layers were not cleared")
	}
	logging, _ := config["LoggingConfig"].(map[string]any)
	if logging["LogGroup"] != "/custom/group" || logging["LogFormat"] != "Text" {
		t.Errorf("omitted LoggingConfig changed = %v", logging)
	}
}

func TestGetFunctionConcurrency_success(t *testing.T) {
	// Given a function with reserved concurrency set
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-get-fn")
	resp := doJSON(t, http.MethodPut, putConcurrencyURL(srv, "concurrency-get-fn"), map[string]any{
		"ReservedConcurrentExecutions": 25,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When GetFunctionConcurrency is called on the AWS API path
	resp2 := doJSON(t, http.MethodGet, getConcurrencyURL(srv, "concurrency-get-fn"), nil)
	defer resp2.Body.Close()

	// Then 200 with the stored value
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out concurrencyResponse
	decodeJSON(t, resp2, &out)
	if out.ReservedConcurrentExecutions != 25 {
		t.Errorf("ReservedConcurrentExecutions = %d, want 25", out.ReservedConcurrentExecutions)
	}
}

func TestGetFunctionConcurrency_notSet_returnsEmptyBody(t *testing.T) {
	// Given a function with no reserved concurrency
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-notset-fn")

	// When GetFunctionConcurrency is called on the AWS API path
	resp := doJSON(t, http.MethodGet, getConcurrencyURL(srv, "concurrency-notset-fn"), nil)
	defer resp.Body.Close()

	// Then 200 with no ReservedConcurrentExecutions, which is what AWS answers.
	// This case used to assert 404: the operation does model
	// ResourceNotFoundException, but for the function rather than for its
	// reservation. reserved_concurrency_test.go owns the detail.
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestDeleteFunctionConcurrency_success(t *testing.T) {
	// Given a function with reserved concurrency set
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-del-fn")
	resp := doJSON(t, http.MethodPut, putConcurrencyURL(srv, "concurrency-del-fn"), map[string]any{
		"ReservedConcurrentExecutions": 10,
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When DeleteFunctionConcurrency is called on the AWS API path
	resp2 := doJSON(t, http.MethodDelete, deleteConcurrencyURL(srv, "concurrency-del-fn"), nil)
	defer resp2.Body.Close()

	// Then 204 No Content
	helpers.AssertStatus(t, resp2, http.StatusNoContent)

	// And GetFunctionConcurrency reports no reservation — 200 with an empty
	// document, as AWS answers. reserved_concurrency_test.go asserts that the
	// member is genuinely absent rather than reported as a zero.
	resp3 := doJSON(t, http.MethodGet, getConcurrencyURL(srv, "concurrency-del-fn"), nil)
	defer resp3.Body.Close()
	helpers.AssertStatus(t, resp3, http.StatusOK)
}

// TestReservedConcurrency_awsPaths_doNotFallThroughToS3 pins the routing
// contract for the reserved-concurrency family. These three operations are
// served on API versions other than Lambda's 2015-03-31 base, so registering
// them on the base silently sends every AWS SDK call to the S3 catch-all,
// where the version segment is parsed as a bucket name and the caller gets
// 404 NoSuchBucket instead of reaching Lambda at all.
func TestReservedConcurrency_awsPaths_doNotFallThroughToS3(t *testing.T) {
	// Given a function exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "concurrency-routing-fn")

	cases := []struct {
		name   string
		method string
		url    string
		body   any
		want   int
	}{
		{"PutFunctionConcurrency", http.MethodPut, putConcurrencyURL(srv, "concurrency-routing-fn"),
			map[string]any{"ReservedConcurrentExecutions": 7}, http.StatusOK},
		{"GetFunctionConcurrency", http.MethodGet, getConcurrencyURL(srv, "concurrency-routing-fn"),
			nil, http.StatusOK},
		{"DeleteFunctionConcurrency", http.MethodDelete, deleteConcurrencyURL(srv, "concurrency-routing-fn"),
			nil, http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When the operation is called on the path AWS serves it on
			resp := doJSON(t, tc.method, tc.url, tc.body)
			defer resp.Body.Close()

			// Then it reaches the Lambda handler
			helpers.AssertStatus(t, resp, tc.want)

			// And the response is not an S3 XML error — the fallthrough symptom
			if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "xml") {
				t.Errorf("Content-Type = %q, want JSON: request fell through to the S3 catch-all", ct)
			}
		})
	}
}

func TestPutProvisionedConcurrencyConfig_success(t *testing.T) {
	// Given a function with a published version
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "prov-concurrency-fn")

	// When PutProvisionedConcurrencyConfig is called with Qualifier
	resp := doJSON(t, http.MethodPut, srv.URL+"/2019-09-30/functions/prov-concurrency-fn/provisioned-concurrency?Qualifier=1", map[string]any{
		"ProvisionedConcurrentExecutions": 5,
	})
	defer resp.Body.Close()

	// Then 202 Accepted, echoing the request. The test server has no Docker,
	// so nothing is allocated and the status stays IN_PROGRESS with a reason —
	// reporting READY here would claim capacity that does not exist.
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	var out provisionedConcurrencyResponse
	decodeJSON(t, resp, &out)
	if out.RequestedProvisionedConcurrentExecutions != 5 {
		t.Errorf("RequestedProvisionedConcurrentExecutions = %d, want 5", out.RequestedProvisionedConcurrentExecutions)
	}
	if out.Status != "FAILED" {
		t.Errorf("Status = %q, want FAILED", out.Status)
	}
	if out.AllocatedProvisionedConcurrentExecutions != 0 {
		t.Errorf("AllocatedProvisionedConcurrentExecutions = %d, want 0", out.AllocatedProvisionedConcurrentExecutions)
	}
}

func TestGetProvisionedConcurrencyConfig_success(t *testing.T) {
	// Given a provisioned concurrency config exists
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "prov-concurrency-get-fn")
	resp := doJSON(t, http.MethodPut, srv.URL+"/2019-09-30/functions/prov-concurrency-get-fn/provisioned-concurrency?Qualifier=prod", map[string]any{
		"ProvisionedConcurrentExecutions": 8,
	})
	helpers.AssertStatus(t, resp, http.StatusAccepted)
	resp.Body.Close()

	// When GetProvisionedConcurrencyConfig is called
	resp2 := doJSON(t, http.MethodGet, srv.URL+"/2019-09-30/functions/prov-concurrency-get-fn/provisioned-concurrency?Qualifier=prod", nil)
	defer resp2.Body.Close()

	// Then 200 with the stored request. Without Docker nothing is allocated,
	// so the status reflects that rather than claiming READY.
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out provisionedConcurrencyResponse
	decodeJSON(t, resp2, &out)
	if out.RequestedProvisionedConcurrentExecutions != 8 {
		t.Errorf("RequestedProvisionedConcurrentExecutions = %d, want 8", out.RequestedProvisionedConcurrentExecutions)
	}
	if out.Status != "FAILED" {
		t.Errorf("Status = %q, want FAILED", out.Status)
	}
}

func TestGetProvisionedConcurrencyConfig_notFound(t *testing.T) {
	// Given a function with no provisioned concurrency
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "prov-concurrency-notfound-fn")

	// When GetProvisionedConcurrencyConfig is called
	resp := doJSON(t, http.MethodGet, srv.URL+"/2019-09-30/functions/prov-concurrency-notfound-fn/provisioned-concurrency?Qualifier=1", nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── GetFunctionCodeSigningConfig ─────────────────────────────────────────────

// AWS serves this one on 2020-06-30, not Lambda's 2015-03-31 base.
func codeSigningConfigURL(srv *helpers.TestServer, name string) string {
	return srv.URL + "/2020-06-30/functions/" + name + "/code-signing-config"
}

func TestGetFunctionCodeSigningConfig_noConfigAssociated(t *testing.T) {
	// Given a function with no code signing config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "codesign-fn")

	// When GetFunctionCodeSigningConfig is called on the AWS API path
	resp := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "codesign-fn"), nil)
	defer resp.Body.Close()

	// Then 404 ResourceNotFoundException (not a 501 — the function exists, it just has no config)
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	var errBody map[string]string
	decodeJSON(t, resp, &errBody)
	if errBody["__type"] != "ResourceNotFoundException" {
		t.Errorf("__type = %q, want ResourceNotFoundException", errBody["__type"])
	}
}

func TestGetFunctionCodeSigningConfig_functionNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "no-such-codesign-fn"), nil)
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── Function URLs: CRUD ───────────────────────────────────────────────────

func TestCreateFunctionUrlConfig_success(t *testing.T) {
	// Given an existing function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-fn")

	// When CreateFunctionUrlConfig is called
	resp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-fn/url"), map[string]any{
		"AuthType": "NONE",
	})
	defer resp.Body.Close()

	// Then 201 with a FunctionUrl on the lambda-url host-route grammar
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionUrlConfigResp
	decodeJSON(t, resp, &cfg)
	if cfg.AuthType != "NONE" {
		t.Errorf("AuthType = %q, want NONE", cfg.AuthType)
	}
	if cfg.InvokeMode != "BUFFERED" {
		t.Errorf("InvokeMode = %q, want BUFFERED (default)", cfg.InvokeMode)
	}
	if !strings.Contains(cfg.FunctionUrl, ".lambda-url.") {
		t.Errorf("FunctionUrl = %q, want it to contain %q", cfg.FunctionUrl, ".lambda-url.")
	}
	if cfg.FunctionArn == "" {
		t.Error("FunctionArn is empty")
	}
}

func TestCreateFunctionUrlConfig_defaultAuthType(t *testing.T) {
	// Given an existing function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-default-auth-fn")

	// When CreateFunctionUrlConfig is called with no AuthType
	resp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-default-auth-fn/url"), map[string]any{})
	defer resp.Body.Close()

	// Then it defaults to AWS_IAM, matching real AWS
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cfg functionUrlConfigResp
	decodeJSON(t, resp, &cfg)
	if cfg.AuthType != "AWS_IAM" {
		t.Errorf("AuthType = %q, want AWS_IAM (default)", cfg.AuthType)
	}
}

func TestCreateFunctionUrlConfig_functionNotFound(t *testing.T) {
	// Given no function exists
	srv := helpers.NewTestServer(t)

	// When CreateFunctionUrlConfig targets a non-existent function
	resp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/no-such-fn/url"), map[string]any{"AuthType": "NONE"})
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestCreateFunctionUrlConfig_duplicateConflict(t *testing.T) {
	// Given a function with an existing (unqualified) function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-dup-fn")
	doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-dup-fn/url"), map[string]any{"AuthType": "NONE"}).Body.Close()

	// When CreateFunctionUrlConfig is called again for the same (unqualified) function
	resp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-dup-fn/url"), map[string]any{"AuthType": "NONE"})
	defer resp.Body.Close()

	// Then 409 Conflict
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestGetFunctionUrlConfig_success(t *testing.T) {
	// Given a function with a function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-get-fn")
	createResp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-get-fn/url"), map[string]any{"AuthType": "NONE"})
	var created functionUrlConfigResp
	decodeJSON(t, createResp, &created)

	// When GetFunctionUrlConfig is called
	resp := doJSON(t, http.MethodGet, lambdaURLv2021(srv, "/functions/url-cfg-get-fn/url"), nil)
	defer resp.Body.Close()

	// Then 200 with the same FunctionUrl
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got functionUrlConfigResp
	decodeJSON(t, resp, &got)
	if got.FunctionUrl != created.FunctionUrl {
		t.Errorf("FunctionUrl = %q, want %q (stable across Get)", got.FunctionUrl, created.FunctionUrl)
	}
}

func TestGetFunctionUrlConfig_notFound(t *testing.T) {
	// Given a function with no function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-noconfig-fn")

	// When GetFunctionUrlConfig is called
	resp := doJSON(t, http.MethodGet, lambdaURLv2021(srv, "/functions/url-cfg-noconfig-fn/url"), nil)
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateFunctionUrlConfig_success(t *testing.T) {
	// Given a function with a NONE-auth function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-update-fn")
	doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-update-fn/url"), map[string]any{"AuthType": "NONE"}).Body.Close()

	// When UpdateFunctionUrlConfig changes AuthType to AWS_IAM
	resp := doJSON(t, http.MethodPut, lambdaURLv2021(srv, "/functions/url-cfg-update-fn/url"), map[string]any{"AuthType": "AWS_IAM"})
	defer resp.Body.Close()

	// Then 200 with the updated AuthType
	helpers.AssertStatus(t, resp, http.StatusOK)
	var got functionUrlConfigResp
	decodeJSON(t, resp, &got)
	if got.AuthType != "AWS_IAM" {
		t.Errorf("AuthType = %q, want AWS_IAM after update", got.AuthType)
	}
}

func TestUpdateFunctionUrlConfig_notFound(t *testing.T) {
	// Given a function with no function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-update-notfound-fn")

	// When UpdateFunctionUrlConfig is called
	resp := doJSON(t, http.MethodPut, lambdaURLv2021(srv, "/functions/url-cfg-update-notfound-fn/url"), map[string]any{"AuthType": "NONE"})
	defer resp.Body.Close()

	// Then 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestDeleteFunctionUrlConfig_success(t *testing.T) {
	// Given a function with a function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-delete-fn")
	doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-delete-fn/url"), map[string]any{"AuthType": "NONE"}).Body.Close()

	// When DeleteFunctionUrlConfig is called
	resp := doJSON(t, http.MethodDelete, lambdaURLv2021(srv, "/functions/url-cfg-delete-fn/url"), nil)
	defer resp.Body.Close()

	// Then 204, and a subsequent Get returns 404
	helpers.AssertStatus(t, resp, http.StatusNoContent)
	getResp := doJSON(t, http.MethodGet, lambdaURLv2021(srv, "/functions/url-cfg-delete-fn/url"), nil)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListFunctionUrlConfigs_success(t *testing.T) {
	// Given a function with an unqualified and a qualified function URL config
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "url-cfg-list-fn")
	doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-list-fn/url"), map[string]any{"AuthType": "NONE"}).Body.Close()
	doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/url-cfg-list-fn/url?Qualifier=$LATEST"), map[string]any{"AuthType": "AWS_IAM"}).Body.Close()

	// When ListFunctionUrlConfigs is called
	resp := doJSON(t, http.MethodGet, lambdaURLv2021(srv, "/functions/url-cfg-list-fn/urls"), nil)
	defer resp.Body.Close()

	// Then both configs are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		FunctionUrlConfigs []functionUrlConfigResp `json:"FunctionUrlConfigs"`
	}
	decodeJSON(t, resp, &result)
	if len(result.FunctionUrlConfigs) != 2 {
		t.Errorf("len(FunctionUrlConfigs) = %d, want 2", len(result.FunctionUrlConfigs))
	}
}

// ─── Function URLs: Host-routed invocation ─────────────────────────────────

func TestInvokeFunctionURL_unknownUrlIdReturnsForbidden(t *testing.T) {
	// Given a server with no function URL configs at all
	srv := helpers.NewTestServer(t)

	// When a request hits a lambda-url Host with an unrecognised urlId
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = "nosuchurlid.lambda-url.us-east-1.amazonaws.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Then 403 Forbidden, not a 500 or a misrouted response
	helpers.AssertStatus(t, resp, http.StatusForbidden)
}

// TestInvokeFunctionURL_hostRoutedAcrossResolvableBases proves the lambda-url
// Host reaches the Lambda function-URL handler on every base Overcast can
// actually resolve, not just ".amazonaws.com".
//
// It asserts on the unknown-urlId 403 rather than a successful invocation so
// it needs no Docker runtime: reaching Lambda's own AWS-shaped 403 is the
// routing proof. Before the addressing-precedence fix these hosts were claimed
// by S3 virtual-hosted addressing, so the request landed on the S3 handler with
// a corrupted path and returned an S3 XML error instead.
//
// TestInvokeFunctionURL_hostRouted_success below could not have caught that,
// even though it does round-trip a minted FunctionUrl and does run in CI.
// Before helpers.NewTestServer defaulted OVERCAST_HOSTNAME, it minted
// "{urlId}.lambda-url.us-east-1.127.0.0.1:PORT" — an IP base, which matches no
// virtual-host rule, so the collision never fired. See
// docs/plans/host-routing-precedence.md.
func TestInvokeFunctionURL_hostRoutedAcrossResolvableBases(t *testing.T) {
	// Given: a server with no function URL configs at all
	srv := helpers.NewTestServer(t)

	bases := []struct{ name, host string }{
		{"amazonaws.com with region", "nosuchurlid.lambda-url.us-east-1.amazonaws.com"},
		{"localhost with region", "nosuchurlid.lambda-url.us-east-1.localhost:4566"},
		{"localhost without region", "nosuchurlid.lambda-url.localhost:4566"},
		{"overcast.sh wildcard with region", "nosuchurlid.lambda-url.us-east-1.localhost.overcast.sh:4566"},
		{"localstack.cloud wildcard with region", "nosuchurlid.lambda-url.us-east-1.localhost.localstack.cloud:4566"},
		// A hostname is case-insensitive, so the same address in any case is
		// the same address. Sent case-sensitively, the label missed
		// hostRouteLabels and S3 virtual-hosted addressing claimed the Host
		// instead — an S3 XML error for a Lambda invoke.
		{"mixed case", "NoSuchUrlId.Lambda-Url.US-East-1.localhost.overcast.sh:4566"},
	}

	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			// When: a request hits that lambda-url Host
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Host = b.host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			// Then: Lambda's 403, proving the request reached Lambda and not S3
			helpers.AssertStatus(t, resp, http.StatusForbidden)
		})
	}
}

// TestCreateFunctionUrlConfig_mintedURLRoutesBackToLambda closes the loop
// between minting and routing: the FunctionUrl Overcast hands back must be one
// its own router accepts. Asserting the string shape would pass even if the
// minting helper and the routing grammar drifted apart, so this feeds the
// minted Host straight back in.
func TestCreateFunctionUrlConfig_mintedURLRoutesBackToLambda(t *testing.T) {
	// Given: a server on a wildcard-DNS hostname with a function URL config
	const hostname = "localhost.overcast.sh"
	srv := helpers.NewTestServer(t, helpers.WithHostname(hostname))
	createFunction(t, srv, "minted-url-fn")

	createResp := doJSON(t, http.MethodPost, lambdaURLv2021(srv, "/functions/minted-url-fn/url"), map[string]any{"AuthType": "NONE"})
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	var cfg functionUrlConfigResp
	decodeJSON(t, createResp, &cfg)

	// The minted URL must carry the configured hostname, not a hardcoded one.
	if !strings.Contains(cfg.FunctionUrl, "lambda-url") || !strings.Contains(cfg.FunctionUrl, hostname) {
		t.Fatalf("FunctionUrl %q does not use the lambda-url grammar on %q", cfg.FunctionUrl, hostname)
	}

	u, err := url.Parse(cfg.FunctionUrl)
	if err != nil {
		t.Fatalf("parse FunctionUrl %q: %v", cfg.FunctionUrl, err)
	}

	// When: that exact Host is presented back to the router
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = u.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	// Then: it is claimed by Lambda. The urlId resolves, so this is NOT the
	// unknown-urlId 403 — any non-403 status proves the host-route claim
	// landed; the invocation itself needs a runtime and is covered by
	// TestInvokeFunctionURL_hostRouted_success in tests/integration/lambdadocker.
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("minted FunctionUrl %q was not recognised by its own router (403)", cfg.FunctionUrl)
	}
}

// TestInvokeFunctionURL_hostRoutedInNonDefaultRegion audits the claim in
// getFunctionURLConfigByURLID (store_url.go) that a Host-routed function URL
// invocation needs no region hint.
//
// That claim holds for the FIRST lookup only: the config scan really is
// region-agnostic. But InvokeFunctionURL immediately calls getFunction, which
// is region-scoped (serviceutil.RegionKey, store.go) — so a function URL
// created in a non-default region resolves its config and then fails to find
// the function behind it.
//
// The assertion is deliberately "not 404 Function not found" rather than a
// successful invoke: resolving the function is the step under test, and what
// happens after it needs a container runtime (covered by
// TestInvokeFunctionURL_hostRouted_success in tests/integration/lambdadocker).
// Without a runtime this request ends in a 500, which is a pass here — the
// function was found.
func TestInvokeFunctionURL_hostRoutedInNonDefaultRegion(t *testing.T) {
	// Given: a function and its URL config, both created in eu-west-1
	const region = "eu-west-1"
	srv := helpers.NewTestServer(t)
	regional := "AWS4-HMAC-SHA256 Credential=test/20250101/" + region +
		"/lambda/aws4_request, SignedHeaders=host, Signature=fake"

	createResp := doJSONWithAuth(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "regional-url-fn",
		Runtime:      "nodejs20.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{},
	}, regional)
	helpers.AssertStatus(t, createResp, http.StatusCreated)
	createResp.Body.Close()

	urlResp := doJSONWithAuth(t, http.MethodPost,
		lambdaURLv2021(srv, "/functions/regional-url-fn/url"),
		map[string]any{"AuthType": "NONE"}, regional)
	helpers.AssertStatus(t, urlResp, http.StatusCreated)
	var cfg functionUrlConfigResp
	decodeJSON(t, urlResp, &cfg)

	u, err := url.Parse(cfg.FunctionUrl)
	if err != nil {
		t.Fatalf("parse FunctionUrl %q: %v", cfg.FunctionUrl, err)
	}
	urlID, _, found := strings.Cut(u.Hostname(), ".")
	if !found || urlID == "" {
		t.Fatalf("FunctionUrl %q has no {urlId} subdomain", cfg.FunctionUrl)
	}

	// When: it is invoked over its host-routed URL, whose region segment is
	// the only thing naming eu-west-1 (a function URL invoke carries no SigV4)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/hello", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = urlID + ".lambda-url." + region + ".localhost:4566"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	defer resp.Body.Close()

	// Then: the function behind the URL is resolved
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		t.Errorf("host-routed invoke in %s did not resolve the function: %d — body: %s",
			region, resp.StatusCode, body)
	}
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("host-routed invoke in %s did not resolve the URL config: %d — body: %s",
			region, resp.StatusCode, body)
	}
}

// doJSONWithAuth is doJSON with an explicit Authorization header, for driving
// a control-plane call into a specific region's partition of the store.
func doJSONWithAuth(t *testing.T, method, url string, body any, auth string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// ─── Code signing config association ──────────────────────────────────────────
//
// Code signing is optional on AWS: a function has a configuration only if one
// was passed to CreateFunction or attached with PutFunctionCodeSigningConfig.
// Overcast does not enforce signing (it is not a security boundary), but it
// must still carry the association faithfully, because SDKs and CDK read it
// back. All three operations live on the 2020-06-30 API version.

type codeSigningConfigResponse struct {
	CodeSigningConfigArn string `json:"CodeSigningConfigArn"`
	FunctionName         string `json:"FunctionName"`
}

const testCSCArn = "arn:aws:lambda:us-east-1:000000000000:code-signing-config:csc-0123456789abcdef0"

func TestCreateFunction_storesCodeSigningConfigArn(t *testing.T) {
	// Given a code signing configuration, and a function created against it as
	// CDK does when its codeSigningConfig prop is set
	srv := helpers.NewTestServer(t)
	testCSCArn := createCSC(t, srv).CodeSigningConfig.CodeSigningConfigArn
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), map[string]any{
		"FunctionName": "csc-fn",
		"Runtime":      "nodejs20.x",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/lambda-role",
		"Code":         map[string]any{},

		"CodeSigningConfigArn": testCSCArn,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)

	// When GetFunctionCodeSigningConfig is called
	got := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "csc-fn"), nil)
	defer got.Body.Close()

	// Then the association is returned, not a 404
	helpers.AssertStatus(t, got, http.StatusOK)
	var out codeSigningConfigResponse
	decodeJSON(t, got, &out)
	if out.CodeSigningConfigArn != testCSCArn {
		t.Errorf("CodeSigningConfigArn = %q, want %q", out.CodeSigningConfigArn, testCSCArn)
	}
	if out.FunctionName != "csc-fn" {
		t.Errorf("FunctionName = %q, want %q", out.FunctionName, "csc-fn")
	}
}

func TestPutAndDeleteFunctionCodeSigningConfig(t *testing.T) {
	// Given a function created without one — the overwhelmingly common case
	srv := helpers.NewTestServer(t)
	testCSCArn := createCSC(t, srv).CodeSigningConfig.CodeSigningConfigArn
	createFunction(t, srv, "csc-attach-fn")

	// Then the optional flow still reports "no config", as AWS's own model
	// requires: CodeSigningConfigArn is a required member of the 200 response,
	// so "no config" cannot be expressed as a success.
	none := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "csc-attach-fn"), nil)
	helpers.AssertStatus(t, none, http.StatusNotFound)
	none.Body.Close()

	// When one is attached
	put := doJSON(t, http.MethodPut, codeSigningConfigURL(srv, "csc-attach-fn"), map[string]any{
		"CodeSigningConfigArn": testCSCArn,
	})
	defer put.Body.Close()
	helpers.AssertStatus(t, put, http.StatusOK)
	var putOut codeSigningConfigResponse
	decodeJSON(t, put, &putOut)
	if putOut.CodeSigningConfigArn != testCSCArn {
		t.Errorf("Put returned %q, want %q", putOut.CodeSigningConfigArn, testCSCArn)
	}

	// Then it reads back
	got := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "csc-attach-fn"), nil)
	helpers.AssertStatus(t, got, http.StatusOK)
	var getOut codeSigningConfigResponse
	decodeJSON(t, got, &getOut)
	got.Body.Close()
	if getOut.CodeSigningConfigArn != testCSCArn {
		t.Errorf("Get returned %q, want %q", getOut.CodeSigningConfigArn, testCSCArn)
	}

	// And deleting it returns 204 and restores the unassociated state
	del := doJSON(t, http.MethodDelete, codeSigningConfigURL(srv, "csc-attach-fn"), nil)
	helpers.AssertStatus(t, del, http.StatusNoContent)
	del.Body.Close()

	after := doJSON(t, http.MethodGet, codeSigningConfigURL(srv, "csc-attach-fn"), nil)
	defer after.Body.Close()
	helpers.AssertStatus(t, after, http.StatusNotFound)
}

func TestPutFunctionCodeSigningConfig_rejectsMalformedArn(t *testing.T) {
	// Given a function
	srv := helpers.NewTestServer(t)
	createFunction(t, srv, "csc-bad-arn-fn")

	// When an ARN that does not match AWS's documented pattern is supplied
	resp := doJSON(t, http.MethodPut, codeSigningConfigURL(srv, "csc-bad-arn-fn"), map[string]any{
		"CodeSigningConfigArn": "not-an-arn",
	})
	defer resp.Body.Close()

	// Then 400 InvalidParameterValueException, as AWS declares for this operation
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestFunctionCodeSigningConfig_functionNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, tc := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPut, map[string]any{"CodeSigningConfigArn": testCSCArn}},
		{http.MethodDelete, nil},
	} {
		resp := doJSON(t, tc.method, codeSigningConfigURL(srv, "no-such-fn"), tc.body)
		helpers.AssertStatus(t, resp, http.StatusNotFound)
		resp.Body.Close()
	}
}

// GetFunction must not leak the association: AWS's FunctionConfiguration shape
// has no CodeSigningConfigArn member, so adding one would be a custom field.
func TestGetFunction_doesNotExposeCodeSigningConfigArn(t *testing.T) {
	srv := helpers.NewTestServer(t)
	testCSCArn := createCSC(t, srv).CodeSigningConfig.CodeSigningConfigArn
	createFunction(t, srv, "csc-leak-fn")
	put := doJSON(t, http.MethodPut, codeSigningConfigURL(srv, "csc-leak-fn"), map[string]any{
		"CodeSigningConfigArn": testCSCArn,
	})
	helpers.AssertStatus(t, put, http.StatusOK)
	put.Body.Close()

	resp := doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/csc-leak-fn/configuration"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg map[string]any
	decodeJSON(t, resp, &cfg)
	if _, present := cfg["CodeSigningConfigArn"]; present {
		t.Error("FunctionConfiguration exposed CodeSigningConfigArn; AWS's shape has no such member")
	}
}

// ---- Lambda TagResource / ListTags / UntagResource ----------------------------

func TestLambda_tagResourceAndList(t *testing.T) {
	srv := helpers.NewTestServer(t)
	fn := createFunction(t, srv, "tag-test-fn")

	tagURL := srv.URL + "/2017-03-31/tags/" + url.PathEscape(fn.FunctionArn)
	tagBody := map[string]map[string]string{
		"Tags": {"env": "prod", "team": "platform"},
	}
	resp := doJSON(t, http.MethodPost, tagURL, tagBody)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	listResp := doJSON(t, http.MethodGet, tagURL, nil)
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)

	var list struct {
		Tags map[string]string `json:"Tags"`
	}
	decodeJSON(t, listResp, &list)
	if list.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %q", list.Tags["env"])
	}
	if list.Tags["team"] != "platform" {
		t.Errorf("expected tag team=platform, got %q", list.Tags["team"])
	}
}

func TestLambda_untagResource(t *testing.T) {
	srv := helpers.NewTestServer(t)
	fn := createFunction(t, srv, "untag-test-fn")

	tagURL := srv.URL + "/2017-03-31/tags/" + url.PathEscape(fn.FunctionArn)
	resp := doJSON(t, http.MethodPost, tagURL, map[string]map[string]string{
		"Tags": {"env": "prod", "team": "platform"},
	})
	resp.Body.Close()

	delResp, err := http.NewRequest(http.MethodDelete, tagURL+"?tagKeys=env&tagKeys=team", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	delHTTPResp, err := http.DefaultClient.Do(delResp)
	if err != nil {
		t.Fatalf("untag: %v", err)
	}
	defer delHTTPResp.Body.Close()
	helpers.AssertStatus(t, delHTTPResp, http.StatusNoContent)

	listResp := doJSON(t, http.MethodGet, tagURL, nil)
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)

	var list struct {
		Tags map[string]string `json:"Tags"`
	}
	decodeJSON(t, listResp, &list)
	if len(list.Tags) != 0 {
		t.Errorf("expected 0 tags after untag, got %d", len(list.Tags))
	}
}

func TestLambda_tagResource_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	tagURL := srv.URL + "/2017-03-31/tags/" + url.PathEscape("arn:aws:lambda:us-east-1:000000000000:function:nonexistent")
	resp := doJSON(t, http.MethodPost, tagURL, map[string]map[string]string{
		"Tags": {"env": "prod"},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}
