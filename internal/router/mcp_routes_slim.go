//go:build slim

package router

import (
	"github.com/go-chi/chi/v5"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	mcpproviders "github.com/overcast-sh/overcast/internal/mcp/providers"
	"github.com/overcast-sh/overcast/internal/state"
	"go.uber.org/zap"
)

// registerMCPRoutes is intentionally a no-op for slim builds. It still
// returns the *mcpproviders.RuntimeProvider type so router.go's call site
// does not need its own build-tag split — a nil provider here means the
// later SetRouter wiring is simply skipped.
func registerMCPRoutes(_ chi.Router, _ *config.Config, _ state.Store, _ *events.Bus, _ *zap.Logger, _ <-chan struct{}) *mcpproviders.RuntimeProvider {
	return nil
}
