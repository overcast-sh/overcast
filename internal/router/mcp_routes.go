//go:build !slim

package router

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/mcp"
	mcpproviders "github.com/overcast-sh/overcast/internal/mcp/providers"
	"github.com/overcast-sh/overcast/internal/state"
	"go.uber.org/zap"
)

// registerMCPRoutes mounts the runtime MCP surface at /_overcast/mcp for non-slim builds.
//
// Transport: Streamable HTTP (MCP 2026-07-28).
//   - POST /_overcast/mcp — the only endpoint. JSON-RPC request/response, or a
//     streamed response when the client sends Accept: text/event-stream, on
//     which that request's own notifications arrive ahead of its result.
//     `subscriptions/listen` is a POST like any other and keeps its response
//     stream open for as long as the client wants notifications.
//
// There is no GET stream, no DELETE, and no session. The revision is stateless:
// "There is no negotiation handshake. Every request carries its protocol
// version, and the server accepts or rejects each request independently."
//
// Auth posture: Origin validation is always enforced, and with one endpoint it
// is enforced in one place. When OVERCAST_MCP_REMOTE_EXPOSURE=true, runtime MCP
// additionally requires a configured bearer token (OVERCAST_MCP_AUTH_TOKEN) on
// every request.
//
// The slim build tag excludes this file entirely so overcast-slim never
// exposes /_overcast/mcp.
// registerMCPRoutes returns the RuntimeProvider it built, so the caller can
// wire it to the fully-constructed router once every route is registered —
// see RuntimeProvider.SetRouter's doc comment and the call site in
// router.go, which mirrors cloudformation's initRouter for the same
// circular-dependency reason.
func registerMCPRoutes(r chi.Router, cfg *config.Config, store state.Store, bus *events.Bus, _ *zap.Logger, shutdownCh <-chan struct{}) *mcpproviders.RuntimeProvider {
	// Built eagerly, and served lazily below. The order matters: AttachEventBus
	// subscribes the provider to the bus so it can keep a window of recent
	// events, and a subscription that waited for the first MCP request would
	// have missed everything the emulator did before it.
	provider := mcpproviders.NewRuntimeProvider(cfg, store)
	provider.AttachEventBus(bus)
	root := sync.OnceValue(func() http.Handler {
		return newRuntimeMCPServer(cfg, shutdownCh, provider).RootHandler()
	})
	strip := http.StripPrefix("/_overcast/mcp", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		root().ServeHTTP(w, req)
	}))
	r.Mount("/_overcast/mcp", strip)

	// Explicitly route the base path without trailing slash so clients can use
	// /_overcast/mcp and /_overcast/mcp/ interchangeably.
	//
	// Every method, not just POST: the MCP handler is the one that decides which
	// methods its surface has, and it answers anything but POST with 405 and an
	// Allow header. Enumerating methods here would put a second, quietly
	// divergent copy of that answer in the router.
	r.HandleFunc("/_overcast/mcp", func(w http.ResponseWriter, req *http.Request) {
		rewritten := req.Clone(req.Context())
		urlCopy := *req.URL
		urlCopy.Path = "/"
		urlCopy.RawPath = "/"
		rewritten.URL = &urlCopy
		root().ServeHTTP(w, rewritten)
	})
	return provider
}

// newRuntimeMCPServer builds the runtime MCP server with the host wiring it
// cannot discover for itself.
//
// Separate from registerMCPRoutes so that shutdownCh is a parameter of building
// a server rather than a step someone has to remember afterwards — the same
// shape eventsHandler and domainsWatchHandler already have, where the channel
// is an argument you cannot leave out and still compile. What the compiler
// cannot check is that this function passes it on, which is the one thing
// TestRuntimeMCPRoutes_ServerWatchesTheShutdownChannel exists for.
//
// A `subscriptions/listen` stream is a long-lived handler and needs the same
// pre-shutdown channel those two get. Without it the stream ends only when the
// client disconnects, and a shutdown that waits for in-flight handlers waits
// for that forever.
func newRuntimeMCPServer(cfg *config.Config, shutdownCh <-chan struct{}, providers ...mcp.ToolProvider) *mcp.Server {
	srv := mcp.NewServer(slog.Default(), providers...)
	srv.SetShutdownSignal(shutdownCh)
	if cfg.MCPRemoteExposure || cfg.MCPAuthToken != "" {
		srv.SetBearerAuthToken(cfg.MCPAuthToken)
	}
	return srv
}
