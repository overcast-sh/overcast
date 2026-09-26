package router

import (
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/trace"
)

// requestChain builds the per-request middleware chain. It is per-request
// plumbing, and none of it dispatches: the Athena engine gateway serves it in
// front of handlers that route by signing name alone (engine_api.go), so a
// middleware that sends a request to a service belongs after it, as
// queryGetMiddleware does. IAM enforcement is the one link that differs
// between the two, which is why the chain is built per router.
type requestChain struct {
	cfg      *config.Config
	store    state.Store
	logger   *zap.Logger
	clk      clock.Clock
	traceBuf *trace.Buffer
	// bus and hostRoutes are filled in after the chain is built, and read at
	// request time, so they only need to be complete before Serve starts.
	bus        **events.Bus
	hostRoutes *[]middleware.HostRouteRow
}

// api is the chain of the API clients call, whose AWS Query dispatch is
// queries: IAM enforcement reads its decisions, so a Query call is authorised
// as the operation it is served as (#2229).
func (c requestChain) api(queries middleware.QueryRouter) chi.Middlewares {
	return c.build(c.cfg.EnforceIAM, queries)
}

// engine is the chain of the Athena engine gateway, which serves no Query
// operation (its Glue POST / answers one with 501), and enforces no IAM
// policy.
//
// The engine signs every call with the key the gateway minted, which no
// principal holds, so enforcing would deny every Glue and S3 call it makes
// (#2237). The gateway has already authenticated the key as the engine's, and
// the engine runs only queries whose StartQueryExecution was authorised on the
// API. What that leaves unchecked is the Glue and S3 access AWS checks as the
// principal that started the query: the engine's calls carry no trace of
// which query, or whose, they serve (#2258).
func (c requestChain) engine() chi.Middlewares {
	return c.build(false, nil)
}

func (c requestChain) build(enforceIAM bool, queries middleware.QueryRouter) chi.Middlewares {
	return chi.Middlewares{
		middleware.RealIP,
		middleware.CORS,
		middleware.DrainBody,
		// HostAddressing owns the whole Host-header decision: S3 virtual-hosted
		// addressing AND host-routed services (execute-api / lambda-url /
		// appsync-api). They are one middleware, not two, because the two
		// schemes share a hostname space — when they were registered separately
		// both claimed the same request and each rewrote the path the other had
		// already rewritten. See docs/plans/host-routing-precedence.md.
		middleware.HostAddressing(c.cfg.Hostname, c.hostRoutes, c.logger),
		middleware.RequestID,
		middleware.Recovery(c.logger),
		middleware.DebugTrace(c.cfg, c.traceBuf, c.clk),
		middleware.Logger(c.logger, c.clk),
		// NotReady short-circuits with a 503 while the storage backend is still
		// completing a one-time startup migration (storage-plan.md item — see
		// internal/middleware/notready.go) — placed after Logger so a rejected
		// request is still observable in logs, and before every other
		// middleware below so none of that work (event recording, SigV4, IAM,
		// region/protocol detection) runs for a request about to be rejected
		// anyway.
		middleware.NotReady(c.store),
		middleware.RequestEvents(c.bus, c.clk),
		middleware.SigV4(c.cfg.SigV4Validate, middleware.NewSecretResolver(c.store), c.logger, c.clk),
		middleware.IAMEnforce(enforceIAM, c.store, c.logger, queries),
		middleware.Region,
		// ClientEndpoint stamps the origin the caller dialled, so services that
		// hand back resource URLs (SQS queue URLs above all) mint them on an
		// origin that caller can reach. See internal/middleware/clientendpoint.go
		// for why a single server-wide hostname cannot serve host CLIs and
		// sibling containers at once.
		middleware.ClientEndpoint,
		// Environment preflight (deploy-failure-diagnosis.md W4): "the client's
		// endpoint is pointed somewhere other than the developer thinks", the
		// one shape of it Overcast can see for itself — see
		// endpointpreflight.go's doc comment. Placed right after ClientEndpoint
		// since both inspect the same Host header.
		middleware.WarnRealAWSHost(c.cfg, c.logger),
		// Protocol-detection middleware (Smithy alignment, see
		// docs/plans/smithy.md). Always-on as of Phase 6 completion.
		middleware.Protocol(codec.DefaultIdentifiers()),
	}
}
