package lambdaweb

import (
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func TestTypedOps_coverEveryModeledOperation(t *testing.T) {
	// Given: the service's operation registry
	ops := newTestService(t).typedOps()

	// Then: every operation the pinned lambda-web model declares is registered
	// as typed, not raw, and nothing else is
	modeled := 0
	awsapi.WalkOperations(func(o awsapi.Operation) bool {
		if o.Service != "lambda-web" {
			return true
		}
		modeled++
		if _, ok := ops[o.Name]; !ok {
			t.Errorf("modeled operation %s has no typed op", o.Name)
		}
		return true
	})
	if modeled != 20 || len(ops) != modeled {
		t.Errorf("modeled = %d, registered = %d, want 20 each", modeled, len(ops))
	}
	for name, o := range ops {
		if o.Name() != name {
			t.Errorf("op %q has Name() %q", name, o.Name())
		}
		if _, raw := o.(*op.Raw); raw {
			t.Errorf("%s registered as raw", name)
		}
	}
}
