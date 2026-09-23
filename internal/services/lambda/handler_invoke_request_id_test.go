package lambda

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// answeringInstance is a RuntimeInstance that answers at once, as the
// invocation it names.
type answeringInstance struct{ requestID string }

func (a *answeringInstance) Invoke(context.Context, []byte, InvokeOptions) (*InvokeResult, error) {
	return &InvokeResult{StatusCode: 200, Payload: []byte(`{"ok":true}`), RequestID: a.requestID}, nil
}

func (a *answeringInstance) LogStreamName() string  { return "2026/09/23/[$LATEST]abc" }
func (a *answeringInstance) Healthy() bool          { return true }
func (a *answeringInstance) FunctionName() string   { return "fn" }
func (a *answeringInstance) ConfigIdentity() string { return "identity" }
func (a *answeringInstance) ContainerID() string    { return "" }
func (a *answeringInstance) InstanceID() string     { return "instance-1" }
func (a *answeringInstance) Close() error           { return nil }

// TestInvokeFunction_answersWithTheInvocationsRequestID pins that a synchronous
// Invoke's x-amzn-RequestId is the invocation's own request id — the one its
// START and REPORT lines carry.
//
// It used to be absent: the request-id middleware sets no response header and
// the Invoke success path never set one, so every SDK caller read an empty
// request id, and Step Functions' lambda:invoke handed the next state
// "SdkResponseMetadata": {"RequestId": ""} — nothing to find the logs by.
func TestInvokeFunction_answersWithTheInvocationsRequestID(t *testing.T) {
	// Given: a function whose invocation runs as request "req-7f3a".
	clk := clock.NewMock()
	ls := newLambdaStore(state.NewMemoryStore(), "us-east-1", clk)
	fn := &Function{
		Name:       "fn",
		Runtime:    "nodejs22.x",
		Handler:    "index.handler",
		ARN:        "arn:aws:lambda:us-east-1:000000000000:function:fn",
		MemorySize: 128,
		Timeout:    3,
		State:      "Active",
	}
	if aerr := ls.putFunction(context.Background(), fn); aerr != nil {
		t.Fatalf("putFunction: %v", aerr)
	}
	h := &Handler{
		cfg:      &config.Config{Region: "us-east-1"},
		log:      serviceutil.NewServiceLogger(zap.NewNop(), "lambda"),
		clk:      clk,
		ls:       ls,
		runtimes: newRuntimeRegistry([]Runtime{&stubRuntime{inst: &answeringInstance{requestID: "req-7f3a"}}}),
	}

	// When: it is invoked synchronously.
	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions/fn/invocations", strings.NewReader(`{}`))
	req = withFunctionNameParam(req, "fn")
	rec := httptest.NewRecorder()
	h.InvokeFunction(rec, req)

	// Then: the response names that invocation.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-amzn-RequestId"); got != "req-7f3a" {
		t.Errorf("x-amzn-RequestId = %q, want the invocation's request id %q", got, "req-7f3a")
	}
}
