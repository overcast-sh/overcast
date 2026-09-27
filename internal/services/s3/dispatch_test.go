package s3

import (
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/s3route"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestDispatch_everyNamedOperationHasAHandler(t *testing.T) {
	// Given: the handler's operation table
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", DataDir: t.TempDir()}
	h := newHandler(cfg, state.NewMemoryStore(), serviceutil.NewServiceLogger(zap.NewNop(), "s3"), clock.New(), events.NewBus())

	// When/Then: every operation s3route can name is served, and nothing else
	// is in the table
	named := s3route.Operations()
	for _, operation := range named {
		if h.operations[operation] == nil {
			t.Errorf("s3route names %s, which has no handler", operation)
		}
	}
	if len(h.operations) != len(named) {
		t.Errorf("%d handlers for %d operations s3route can name", len(h.operations), len(named))
	}
}
