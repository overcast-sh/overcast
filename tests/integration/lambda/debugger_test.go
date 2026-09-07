package lambda_test

// debugger_test.go — what the compute debugger must never do to the AWS API
// surface (docs/plans/compute-debugger.md § 2): the flag it injects and the
// port it names live in the container's environment only.

import (
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestGetFunctionConfiguration_debugTagsNeverReachTheEnvironment(t *testing.T) {
	// Given: the debugger is on and a Node function opts in by tag, with an
	// environment of its own
	srv := helpers.NewTestServer(t, helpers.WithLambdaDebugger())
	resp := doJSON(t, http.MethodPost, lambdaURL(srv, "/functions"), createFunctionReq{
		FunctionName: "debugged-fn",
		Runtime:      "nodejs22.x",
		Handler:      "index.handler",
		Role:         "arn:aws:iam::000000000000:role/lambda-role",
		Code:         &lambdaCode{ZipFile: []byte("PK\x05\x06" + string(make([]byte, 18)))},
		Tags:         map[string]string{"overcast:debug": "true", "overcast:debug-port": "9229"},
		Environment:  &functionEnvironment{Variables: map[string]string{"NODE_OPTIONS": "--enable-source-maps"}},
	})
	helpers.AssertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// When: the configuration is read back
	resp = doJSON(t, http.MethodGet, lambdaURL(srv, "/functions/debugged-fn/configuration"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var cfg functionConfiguration
	decodeJSON(t, resp, &cfg)

	// Then: the environment is exactly what was deployed — the inspector flag
	// and OVERCAST_DEBUG_PORT are the container's, not the function's
	if cfg.Environment == nil {
		t.Fatal("Environment missing from the configuration")
	}
	if got := cfg.Environment.Variables["NODE_OPTIONS"]; got != "--enable-source-maps" {
		t.Errorf("NODE_OPTIONS = %q, want the deployed value alone", got)
	}
	if _, leaked := cfg.Environment.Variables["OVERCAST_DEBUG_PORT"]; leaked {
		t.Error("OVERCAST_DEBUG_PORT leaked into GetFunctionConfiguration")
	}
	if len(cfg.Environment.Variables) != 1 {
		t.Errorf("Environment.Variables = %v, want the one deployed variable", cfg.Environment.Variables)
	}
}
