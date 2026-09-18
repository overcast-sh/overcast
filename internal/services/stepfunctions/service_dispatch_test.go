package stepfunctions

import (
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil/dispatchtest"
	"github.com/overcast-sh/overcast/internal/state"
)

// TestDispatch_everyModeledOperationIsImplemented pins that Step Functions
// implements its whole modeled API: every operation the pinned AWS models
// carry for sfn reaches a handler on both the legacy X-Amz-Target table and
// the typed (codec) table. An operation AWS adds would fail here until it is
// implemented or consciously left as a 501.
func TestDispatch_everyModeledOperationIsImplemented(t *testing.T) {
	// Given: the service as the router builds it
	svc := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), zap.NewNop(), clock.NewMock())

	// When: we walk the modeled Step Functions operations
	modeled := 0
	awsapi.WalkOperations(func(op awsapi.Operation) bool {
		if op.SDKID != "SFN" {
			return true
		}
		modeled++
		// Then: each has a handler on both dispatch paths
		if _, ok := svc.handler.ops[op.Name]; !ok {
			t.Errorf("%s has no X-Amz-Target handler", op.Name)
		}
		if _, ok := svc.handler.typedOp[op.Name]; !ok {
			t.Errorf("%s has no typed handler", op.Name)
		}
		return true
	})
	if modeled == 0 {
		t.Fatal("the AWS models carry no Step Functions operations — the SDKID filter is wrong")
	}
}

// TestDispatch_unknownOperation pins that a name AWS does not model keeps the
// 400 UnknownOperationException, in the request's own wire format (#1645).
// Every modeled operation is implemented (see above), so the 501 half of the
// house rule has no Step Functions operation left to exercise.
func TestDispatch_unknownOperation(t *testing.T) {
	// Given: an operation name the Step Functions model does not carry
	const unknown = "NotAStepFunctionsOperation"
	svc := New(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), zap.NewNop(), clock.NewMock())

	// When / Then: it is refused as unknown over every protocol
	cases := dispatchtest.UnimplementedVsUnknown("", unknown, "UnknownOperationException")
	var unknownCases []dispatchtest.Refusal
	for _, c := range cases {
		if c.Operation == unknown {
			unknownCases = append(unknownCases, c)
		}
	}
	dispatchtest.AssertRefusals(t, svc.Dispatch, "AWSStepFunctions.", unknownCases)
}
