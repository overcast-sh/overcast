package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/overcast-sh/overcast/internal/services/eventbridge"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/alarmaction"
	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/domainregistry"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/hostbridge/trust"
	"github.com/overcast-sh/overcast/internal/inithooks"
	"github.com/overcast-sh/overcast/internal/listenstatus"
	"github.com/overcast-sh/overcast/internal/metrics"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/services/acm"
	"github.com/overcast-sh/overcast/internal/services/apigateway"
	"github.com/overcast-sh/overcast/internal/services/appconfig"
	"github.com/overcast-sh/overcast/internal/services/appconfigdata"
	"github.com/overcast-sh/overcast/internal/services/appregistry"
	"github.com/overcast-sh/overcast/internal/services/appsync"
	"github.com/overcast-sh/overcast/internal/services/athena"
	"github.com/overcast-sh/overcast/internal/services/autoscaling"
	"github.com/overcast-sh/overcast/internal/services/backup"
	"github.com/overcast-sh/overcast/internal/services/bedrock"
	"github.com/overcast-sh/overcast/internal/services/cloudformation"
	"github.com/overcast-sh/overcast/internal/services/cloudfront"
	"github.com/overcast-sh/overcast/internal/services/cloudtrail"
	"github.com/overcast-sh/overcast/internal/services/cloudwatch"
	"github.com/overcast-sh/overcast/internal/services/cloudwatch/logs"
	"github.com/overcast-sh/overcast/internal/services/cognito"
	"github.com/overcast-sh/overcast/internal/services/dynamodb"
	"github.com/overcast-sh/overcast/internal/services/dynamodbstreams"
	"github.com/overcast-sh/overcast/internal/services/ec2"
	"github.com/overcast-sh/overcast/internal/services/ecr"
	"github.com/overcast-sh/overcast/internal/services/ecs"
	"github.com/overcast-sh/overcast/internal/services/efs"
	"github.com/overcast-sh/overcast/internal/services/eks"
	"github.com/overcast-sh/overcast/internal/services/elasticache"
	elbv2svcpkg "github.com/overcast-sh/overcast/internal/services/elbv2"
	"github.com/overcast-sh/overcast/internal/services/firehose"
	"github.com/overcast-sh/overcast/internal/services/glue"
	"github.com/overcast-sh/overcast/internal/services/iam"
	"github.com/overcast-sh/overcast/internal/services/kinesis"
	"github.com/overcast-sh/overcast/internal/services/kms"
	"github.com/overcast-sh/overcast/internal/services/lambda"
	"github.com/overcast-sh/overcast/internal/services/msk"
	"github.com/overcast-sh/overcast/internal/services/opensearch"
	"github.com/overcast-sh/overcast/internal/services/organizations"
	"github.com/overcast-sh/overcast/internal/services/pipes"
	"github.com/overcast-sh/overcast/internal/services/rds"
	route53svcpkg "github.com/overcast-sh/overcast/internal/services/route53"
	"github.com/overcast-sh/overcast/internal/services/s3"
	"github.com/overcast-sh/overcast/internal/services/scheduler"
	"github.com/overcast-sh/overcast/internal/services/secretsmanager"
	"github.com/overcast-sh/overcast/internal/services/ses"
	"github.com/overcast-sh/overcast/internal/services/shield"
	"github.com/overcast-sh/overcast/internal/services/sns"
	"github.com/overcast-sh/overcast/internal/services/sqs"
	"github.com/overcast-sh/overcast/internal/services/ssm"
	"github.com/overcast-sh/overcast/internal/services/stepfunctions"
	"github.com/overcast-sh/overcast/internal/services/sts"
	"github.com/overcast-sh/overcast/internal/services/transfer"
	"github.com/overcast-sh/overcast/internal/services/waf"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/smtp"
	"github.com/overcast-sh/overcast/internal/state"
	"github.com/overcast-sh/overcast/internal/trace"
)

// New builds and returns the root HTTP handler for the emulator, plus a
// cleanup function that should be called during graceful shutdown with the
// remaining shutdown context. It stops background service resources (e.g. the
// Lambda Runtime API server) and closes any other open handles (e.g. the SMTP
// listener).
//
// The returned preShutdown function must be called BEFORE http.Server.Shutdown
// to unblock long-lived handlers (e.g. the SSE /_overcast/events endpoint).
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock, hookRunner ...*inithooks.Runner) (handler http.Handler, preShutdown func(), cleanup func(context.Context), waitReady func()) {
	prof := newStartupProfiler()

	r := chi.NewRouter()
	var cleanups []func()

	// shutdownCh is closed during pre-shutdown to unblock long-lived handlers
	// (e.g. the SSE /_overcast/events endpoint) so http.Server.Shutdown can complete
	// without waiting for the full ShutdownTimeout.
	shutdownCh := make(chan struct{})
	preShutdown = sync.OnceFunc(func() { close(shutdownCh) })

	// smtpCtx is cancelled during shutdown so the SMTP server's Accept
	// loop exits promptly without waiting for context.Background().
	smtpCtx, smtpCancel := context.WithCancel(context.Background())
	cleanups = append(cleanups, smtpCancel)

	// ---- Container-facing DNS resolver -------------------------------------
	// The server is kept so the data-plane guard can be attached once Docker is
	// probed, further down. Binding has to happen here, before any service is
	// constructed, so cfg.DNSListening is settled before a container could be
	// pointed at the resolver.
	dnsServer, stopDNS := startContainerDNS(cfg, logger)
	if stopDNS != nil {
		cleanups = append(cleanups, stopDNS)
	}

	// ---- Event bus (declared early so middleware can reference it) ----------
	// The bus pointer is set below, after middleware registration.
	// middleware.RequestEvents dereferences it at request time (always after bus
	// creation since http.Server.Serve is not called until New returns).
	var bus *events.Bus

	// Trace buffer — created here so the middleware can capture it at
	// construction time. When cfg.Debug is false the middleware is identity
	// and the buffer is never accessed.
	//
	// The retention policy is passed explicitly rather than inferred from a
	// capacity, because it is more than a size. The floor is what a quiet
	// emulator keeps; the ceiling and window are what a burst needs; the pinned
	// cap is what a *failed* burst needs, since a deploy that fails does not
	// stop but rolls back, and the rollback would otherwise push the error out
	// of a purely recency-ordered buffer.
	traceBuf := trace.NewBufferWithPolicy(trace.RetentionPolicy{
		Floor:   cfg.DebugTraceBuffer,
		Ceiling: cfg.DebugTraceCeiling,
		Window:  cfg.DebugTraceWindow,
		Pinned:  cfg.DebugTracePinned,
		Bytes:   cfg.DebugTraceBytes,
	}, clk)

	// ---- Middleware chain --------------------------------------------------
	r.Use(middleware.RealIP)
	r.Use(middleware.CORS)
	r.Use(middleware.DrainBody)
	// HostAddressing owns the whole Host-header decision: S3 virtual-hosted
	// addressing AND host-routed services (execute-api / lambda-url /
	// appsync-api). They are one middleware, not two, because the two schemes
	// share a hostname space — when they were registered separately both
	// claimed the same request and each rewrote the path the other had already
	// rewritten. See docs/plans/host-routing-precedence.md.
	//
	// hostRoutes is populated further down, once the services it dispatches
	// to (API Gateway, Lambda, AppSync) are constructed — see "Host-based
	// routing" below. The pointer is read at request time (same pattern as
	// queryDispatchers just below), so it only needs to be fully populated
	// before Serve starts, not before this Use call.
	var hostRoutes []middleware.HostRouteRow
	r.Use(middleware.HostAddressing(cfg.Hostname, &hostRoutes, logger))
	r.Use(middleware.RequestID)
	r.Use(middleware.Recovery(logger))
	r.Use(middleware.DebugTrace(cfg, traceBuf, clk))
	r.Use(middleware.Logger(logger, clk))
	// NotReady short-circuits with a 503 while the storage backend is still
	// completing a one-time startup migration (storage-plan.md item — see
	// internal/middleware/notready.go) — placed after Logger so a rejected
	// request is still observable in logs, and before every other
	// middleware below so none of that work (event recording, SigV4, IAM,
	// region/protocol detection) runs for a request about to be rejected
	// anyway.
	r.Use(middleware.NotReady(store))
	r.Use(middleware.RequestEvents(&bus, clk))
	r.Use(middleware.SigV4(cfg.SigV4Validate, middleware.NewSecretResolver(store), logger, clk))
	r.Use(middleware.IAMEnforce(cfg.EnforceIAM, store, logger))
	r.Use(middleware.Region)
	// ClientEndpoint stamps the origin the caller dialled, so services that
	// hand back resource URLs (SQS queue URLs above all) mint them on an
	// origin that caller can reach. See internal/middleware/clientendpoint.go
	// for why a single server-wide hostname cannot serve host CLIs and sibling
	// containers at once.
	r.Use(middleware.ClientEndpoint)
	// Environment preflight (deploy-failure-diagnosis.md W4): "the client's
	// endpoint is pointed somewhere other than the developer thinks", the
	// one shape of it Overcast can see for itself — see
	// endpointpreflight.go's doc comment. Placed right after ClientEndpoint
	// since both inspect the same Host header.
	r.Use(middleware.WarnRealAWSHost(cfg, logger))
	// Protocol-detection middleware (Smithy alignment, see
	// docs/plans/smithy.md). Always-on as of Phase 6 completion.
	r.Use(middleware.Protocol(codec.DefaultIdentifiers()))
	// queryGetMiddleware must be registered here, before any route is added
	// (chi requirement). It intercepts GET /?Action=... requests (e.g. SNS
	// UnsubscribeURL) letting S3's GET / handle everything else. The pointer
	// is read at request time, so the slice is fully populated by then.
	var queryDispatchers []QueryDispatcher
	operationRegistry := awsapi.NewRegistry()
	r.Use(queryGetMiddleware(&queryDispatchers, operationRegistry))
	prof.mark("middleware chain")

	// ---- Internal endpoints (always available) ----------------------------
	// healthHandler is registered after the service loop so it can include
	// the list of enabled service names and their emulation tiers in the response.
	var enabledServiceNames []string
	enabledTiers := make(map[string]string)
	// Suppress noisy 404s when browsers auto-request /favicon.ico.
	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// ---- Init hook status (LocalStack-compatible, always available) --------
	var initRunner *inithooks.Runner
	if len(hookRunner) > 0 {
		initRunner = hookRunner[0]
	}
	// Held in variables so the /_localstack/init aliases registered further
	// down are the same function value, not a second construction of it —
	// which is what makes "alias" literal rather than approximate. See
	// localstack_compat.go.
	initStatus := initStatusHandler(initRunner)
	initStageStatus := initStageStatusHandler(initRunner)
	r.Get("/_overcast/init", initStatus)
	r.Get("/_overcast/init/{stage}", initStageStatus)

	// ---- Local CA certificate (always available) ---------------------------
	// GET /_overcast/ca.pem — the daemon's CA certificate, so a host-side
	// `overcast https enable --endpoint ...` can trust a containerized
	// daemon without a shared volume. 404 until a CA exists (OVERCAST_TLS=auto).
	r.Get(trust.CAPemPath, caCertHandler(cfg))

	// ---- TLS settings (always available) ------------------------------------
	// The web console's Settings → HTTPS page: status of the CA / trust store /
	// serving mode, and the in-daemon `overcast https enable` equivalent.
	// See tls_settings.go for the cross-origin guard on the mutating route.
	r.Get("/_overcast/tls/status", tlsStatusHandler(cfg, logger))
	r.Post("/_overcast/tls/setup", tlsSetupHandler(cfg, logger))

	// ---- Debug dependencies ---------------------------------------------------
	// ec2Svc is constructed before the rest of the services because the debug
	// namespace exposes EC2 internals (/_overcast/debug/ec2/vpcs). Its constructor has no
	// startup side-effects — see docs/dev/performance.md § Startup budget.
	ec2Svc := ec2.New(cfg, store, logger, clk)

	// ---- Event bus (create) ------------------------------------------------
	bus = events.NewBus()

	// ---- SSE event stream (always available) --------------------------------
	// GET /_overcast/events?source=s3 — streams all internal events to connected clients.
	r.Get("/_overcast/events", eventsHandler(bus, logger, clk, shutdownCh))
	r.Get("/_overcast/events/request/{requestId}", eventsByRequestHandler(bus))

	// ---- Custom domain registry (always available) -------------------------
	// Tracks API Gateway / CloudFront custom domain names and streams changes
	// to the host CLI (overcast dev) so it can drive mDNS publishing.
	domainReg := domainregistry.New()
	r.Get("/_overcast/domains/watch", domainsWatchHandler(domainReg, logger, shutdownCh))

	// ---- Runtime metrics (always available) ---------------------------------
	// GET /_overcast/metrics — returns a snapshot of Go runtime memory/GC/goroutine stats.
	r.Get("/_overcast/metrics", metricsHandler())

	// ---- Server info (always available) -------------------------------------
	// GET /_overcast/info — returns the server's configured region and account ID.
	// Used by the web UI to seed the region selector on first load.
	r.Get("/_overcast/info", newInfoHandler(cfg))
	prof.mark("bus + SSE + internal routes")

	// Smithy RPC v2 requests use /service/{Service}/operation/{Operation}.
	// The route is registered after the private S3 router is populated so a
	// headerless S3 request with the same legal path can delegate to S3.
	smithyDispatchers := make(map[string]*smithyRPCService)

	// MCP runtime routes are mounted through a build-tag-aware hook.
	// Slim builds intentionally do not expose MCP endpoints.
	registerMCPRoutes(r, cfg, store, bus, logger, shutdownCh)

	// ---- Service registry -------------------------------------------------
	// To add a new service: implement router.Service and append it here.
	prof.mark("MCP routes + internal endpoints")
	s3Svc := s3.New(cfg, store, logger, clk, bus)
	prof.mark("  new: s3")
	sqsSvc := sqs.New(cfg, store, logger, clk)
	prof.mark("  new: sqs")
	snsSvc := sns.New(cfg, store, logger, clk)
	prof.mark("  new: sns")
	sesSvc := ses.New(cfg, store, logger, clk)
	prof.mark("  new: ses")
	ddbSvc := dynamodb.New(cfg, store, logger, clk, bus)
	prof.mark("  new: dynamodb")
	// logsSvc is constructed here (ahead of its usual place in the service
	// registry order below) because the debug namespace below needs it as a
	// DebugStateProvider (its events live in a dedicated logs_events SQL
	// table — see storage-plan.md 2.3 — invisible to /_overcast/debug/state without
	// this). Its constructor has no startup side-effects, same as ec2Svc
	// above — see docs/dev/performance.md § Startup budget.
	logsSvc := logs.New(cfg, store, logger, clk)
	prof.mark("  new: logs")
	// lambdaSvc is constructed here (ahead of its usual place in the service
	// registry order below), same reason as logsSvc above: the debug
	// namespace's advisories need it as a debugLambdaProvider (see
	// checkLambdaInitVolumeOwnership in advisories.go) before the debug
	// routes are registered a few lines down. Its constructor has no startup
	// side-effects beyond spawning the background Docker probe it always
	// spawns regardless of where it's constructed — see docs/dev/performance.md
	// § Startup budget.
	// One debug-target manager for every compute service: it owns the debug
	// ports, so the registry that allocates them has to be shared. Closed with
	// the other handles at shutdown, after the services that register targets
	// have stopped.
	debuggerMgr := debugger.NewManager(clk, logger, cfg.DebuggerListen, cfg.DebuggerPorts, cfg.DebuggerTimeout)
	cleanups = append(cleanups, debuggerMgr.Close)
	lambdaSvc := lambda.New(cfg, store, logger, clk, debuggerMgr)
	prof.mark("  new: lambda")
	// debugProviders is built unconditionally: /_overcast/reset (always-on,
	// below) needs it regardless of cfg.Debug, and it's used again for the
	// debug-gated block right after.
	debugProviders := []DebugStateProvider{ddbSvc, logsSvc, sqsSvc}
	// dockerStatusFn is assigned further down, once the Docker supervisor and
	// its tracker exist. Handlers registered before then are handed a closure
	// over the variable rather than its current (nil) value, so the debug
	// namespace and the health endpoint read one snapshot source between them.
	var dockerStatusFn func() *docker.Status
	dockerStatusNow := func() *docker.Status {
		if dockerStatusFn == nil {
			return nil
		}
		return dockerStatusFn()
	}
	if cfg.Debug {
		r.Route("/_overcast/debug", debugHandlers(cfg, store, ec2Svc, lambdaSvc, debugProviders, traceBuf, dockerStatusNow))
	}
	// ---- Reset (always available) ------------------------------------------
	// Unlike the rest of the /_overcast/debug namespace above, reset is not
	// expensive or leaky instrumentation, and it grants no destructive power
	// beyond what the unauthenticated AWS surface already exposes — so it is
	// never gated on OVERCAST_DEBUG. Registered with absolute paths directly
	// on r, matching /_overcast/health, /_overcast/metrics, etc. below rather
	// than a chi sub-router — see the chi-subrouter-swallows-fallback gotcha
	// noted throughout this package. See reset.go.
	// Held for the same reason as initStatus above: /_localstack/state/reset
	// is registered onto this exact handler further down.
	resetAll := resetHandler(store, debugProviders)
	r.Post("/_overcast/reset", resetAll)
	r.Post("/_overcast/reset/{service}", resetServiceHandler(store, debugProviders))
	pipesSvc := pipes.New(cfg, store, logger, clk)
	prof.mark("  new: pipes")
	smSvc := secretsmanager.New(cfg, store, logger, clk)
	prof.mark("  new: secretsmanager")
	stsSvc := sts.New(cfg, store, logger, clk)
	prof.mark("  new: sts")
	ssmSvc := ssm.New(cfg, store, logger, clk)
	prof.mark("  new: ssm")
	kmsSvc := kms.New(cfg, store, logger, clk)
	prof.mark("  new: kms")
	iamSvc := iam.New(cfg, store, logger, clk)
	prof.mark("  new: iam")
	cfnSvc := cloudformation.New(cfg, store, logger, clk)
	prof.mark("  new: cloudformation")
	rdsSvc := rds.New(cfg, store, logger, clk)
	prof.mark("  new: rds")
	ecsSvc := ecs.New(cfg, store, logger, clk, debuggerMgr)
	prof.mark("  new: ecs")
	// The debugger's own endpoints, once both compute services exist: each
	// synthesises the entry for an untagged resource of its own under its
	// debugger.Service key. Registered on r directly, like the reset routes
	// above; chi does not care what order absolute routes are added in.
	registerDebuggerRoutes(r, debuggerMgr, debuggerDescribers{
		debugger.ServiceLambda: lambdaSvc,
		debugger.ServiceECS:    ecsSvc,
	})
	cognitoSvc := cognito.New(cfg, store, logger, clk)
	prof.mark("  new: cognito")
	sfnSvc := stepfunctions.New(cfg, store, logger, clk)
	prof.mark("  new: stepfunctions")
	wafSvc := waf.New(cfg, store, logger, clk)
	prof.mark("  new: waf")
	shieldSvc := shield.New(cfg, store, logger, clk)
	prof.mark("  new: shield")
	appsyncSvc := appsync.New(cfg, store, logger, clk)
	prof.mark("  new: appsync")
	apigwSvc := apigateway.New(cfg, store, logger, clk)
	prof.mark("  new: apigateway")
	cloudfrontSvc := cloudfront.New(cfg, store, logger, clk)
	prof.mark("  new: cloudfront")
	ebSvc := eventbridge.New(cfg, store, logger, clk)
	prof.mark("  new: eventbridge")
	kinesisSvc := kinesis.New(cfg, store, logger, clk)
	prof.mark("  new: kinesis")
	appregistrySvc := appregistry.New(cfg, store, logger, clk)
	prof.mark("  new: appregistry")
	cloudwatchSvc := cloudwatch.New(cfg, store, logger, clk)
	prof.mark("  new: cloudwatch")
	acmSvc := acm.New(cfg, store, logger, clk)
	prof.mark("  new: acm")
	opensearchSvc := opensearch.New(cfg, store, logger, clk)
	prof.mark("  new: opensearch")
	appconfigSvc := appconfig.New(cfg, store, logger, clk)
	prof.mark("  new: appconfig")
	appconfigdataSvc := appconfigdata.New(cfg, store, logger, clk, appconfigSvc)
	prof.mark("  new: appconfigdata")
	bedrockSvc := bedrock.New(cfg, store, logger, clk)
	prof.mark("  new: bedrock")
	glueSvc := glue.New(cfg, store, logger, clk)
	prof.mark("  new: glue")
	firehoseSvc := firehose.New(cfg, store, logger, clk)
	prof.mark("  new: firehose")
	athenaSvc := athena.New(cfg, store, logger, clk)
	prof.mark("  new: athena")
	elasticacheSvc := elasticache.New(cfg, store, logger, clk)
	prof.mark("  new: elasticache")
	mskSvc := msk.New(cfg, store, logger, clk)
	prof.mark("  new: msk")
	ecrSvc := ecr.New(cfg, store, logger, clk)
	prof.mark("  new: ecr")
	eksSvc := eks.New(cfg, store, logger, clk)
	prof.mark("  new: eks")

	schedulerSvc := scheduler.New(cfg, store, logger, clk)
	prof.mark("  new: scheduler")
	route53Svc := route53svcpkg.New(cfg, store, logger, clk)
	prof.mark("  new: route53")
	// Wires the resolver to Route 53's store the moment the service exists —
	// no need to wait for Docker like the data-plane guard below, since a
	// hosted zone's records are ordinary emulator state, not a container
	// endpoint. See internal/services/route53/dns_resolve.go.
	if dnsServer != nil {
		dnsServer.SetRoute53(route53Svc)
	}
	elbv2Svc := elbv2svcpkg.New(cfg, store, logger, clk)
	prof.mark("  new: elbv2")
	organizationsSvc := organizations.New(cfg, store, logger, clk)
	prof.mark("  new: organizations")
	autoScalingSvc := autoscaling.New(cfg, store, logger, clk)
	prof.mark("  new: autoscaling")
	cloudTrailSvc := cloudtrail.New(cfg, store, logger, clk)
	prof.mark("  new: cloudtrail")
	backupSvc := backup.New(cfg, store, logger, clk)
	prof.mark("  new: backup")
	transferSvc := transfer.New(cfg, store, logger)
	prof.mark("  new: transfer")
	efsSvc := efs.New(cfg, store, logger, clk)
	prof.mark("  new: efs")

	prof.mark("service constructors (47)")

	allServices := []Service{
		// S3 is listed last so its /{bucket}/* wildcard routes are registered
		// after all other services, preventing it from shadowing /_<service> paths.
		sqsSvc,
		ddbSvc,
		snsSvc,
		sesSvc,
		lambdaSvc,
		dynamodbstreams.New(ddbSvc, logger),
		pipesSvc,
		logsSvc,
		smSvc,
		stsSvc,
		ssmSvc,
		kmsSvc,
		iamSvc,
		cfnSvc,
		ec2Svc,
		rdsSvc,
		ecsSvc,
		cognitoSvc,
		sfnSvc,
		wafSvc,
		shieldSvc,
		appsyncSvc,
		apigwSvc,
		cloudfrontSvc,
		ebSvc,
		kinesisSvc,
		appregistrySvc,
		cloudwatchSvc,
		acmSvc,
		opensearchSvc,
		appconfigSvc,
		appconfigdataSvc,
		bedrockSvc,
		glueSvc,
		firehoseSvc,
		athenaSvc,
		elasticacheSvc,
		mskSvc,
		ecrSvc,
		eksSvc,
		schedulerSvc,
		route53Svc,
		elbv2Svc,
		organizationsSvc,
		autoScalingSvc,
		cloudTrailSvc,
		backupSvc,
		transferSvc,
		efsSvc,
		s3Svc, // must be last — registers /{bucket}/* wildcard
	}

	// Collect target dispatchers for services that share POST /.
	var dispatchers []TargetDispatcher
	// Query dispatchers are declared above, near middleware registration.
	type namedStopper struct {
		name string
		Stopper
	}
	var stoppers []namedStopper
	var readiers []Readier
	serviceByName := make(map[string]Service, len(allServices))
	// Keep S3's deliberately broad bucket/object routes private until every
	// non-S3 service has had an opportunity to own an explicit path. The final
	// fallback below asks the generated REST registry about otherwise-unmatched
	// paths before delegating to these routes.
	s3Router := chi.NewRouter()

	// Sub-routers the main router picks at request time. See dispatchMount for
	// why they have to be recorded rather than discovered by walking r.
	var dispatchMounts []dispatchMount
	dispatchMounts = recordDispatchMount(dispatchMounts, "", "s3", s3Router)

	// Attributes each pattern registered directly on r to the service whose
	// RegisterRoutes call produced it — the other half of dispatchMount's
	// attribution, for routes that never go through a dispatched sub-router.
	// See routeOwnerTracker (routeinventory_dev.go / routeinventory_prod.go).
	//
	// The empty-owner snapshot below has to run before the loop: r already
	// carries the router's own endpoints registered above (favicon, the
	// /_overcast/init, /_overcast/tls, /_overcast/events, /_overcast/domains,
	// /_overcast/metrics, /_overcast/info and /_overcast/debug families), none
	// of which any service registered. Without this call the first service's
	// attribute() would silently credit all of them to itself, because
	// routeOwnerTracker attributes every pattern it has not yet seen.
	routeOwners := newRouteOwnerTracker()
	routeOwners.attribute(r, "")

	for _, svc := range allServices {
		// Test-only isolation. nil in every production path — see
		// config.TestOnlyServiceSubset for why this exists and why it is not
		// reachable from the environment. Skipping before dispatcher
		// registration is what makes the service genuinely absent rather than
		// merely unrouted.
		if cfg.TestOnlyServiceSubset != nil && !cfg.TestOnlyServiceSubset[svc.Name()] {
			// An excluded service still claims its own path prefixes, with a
			// 501. Leaving them unclaimed would drop those paths into S3's
			// broad bucket/object fallback, so the very routes a subset test
			// is trying to isolate would answer as S3 and the test would
			// report a fallthrough that no real run can produce.
			if pp, ok := svc.(PathPrefixService); ok {
				for _, prefix := range pp.PathPrefixes() {
					r.HandleFunc(prefix, testOnlyAbsentHandler)
					r.HandleFunc(prefix+"/*", testOnlyAbsentHandler)
				}
			}
			continue
		}
		if td, ok := svc.(TargetDispatcher); ok {
			rpcService := newSmithyRPCService(td, svc)
			smithyDispatchers[strings.ToLower(svc.Name())] = rpcService
			if prefix := strings.TrimSuffix(td.TargetPrefix(), "."); prefix != "" {
				smithyDispatchers[strings.ToLower(prefix)] = rpcService
			}
		}
		serviceByName[svc.Name()] = svc
		if svc.Name() == "s3" {
			svc.RegisterRoutes(s3Router)
		} else {
			svc.RegisterRoutes(r)
			routeOwners.attribute(r, svc.Name())
		}
		prof.mark("  routes: " + svc.Name())
		if td, ok := svc.(TargetDispatcher); ok {
			dispatchers = append(dispatchers, td)
		}
		if qd, ok := svc.(QueryDispatcher); ok {
			queryDispatchers = append(queryDispatchers, qd)
		}
		if st, ok := svc.(Stopper); ok {
			stoppers = append(stoppers, namedStopper{name: svc.Name(), Stopper: st})
		}
		if rd, ok := svc.(Readier); ok {
			readiers = append(readiers, rd)
		}
		enabledServiceNames = append(enabledServiceNames, svc.Name())
		if tier, ok := ServiceTiers[svc.Name()]; ok {
			enabledTiers[svc.Name()] = tier
		} else {
			enabledTiers[svc.Name()] = TierStub
		}
		logger.Info("service enabled", zap.String("service", svc.Name()))
	}

	prof.mark("RegisterRoutes for enabled services")

	// ---- Service metrics substrate (docs/plans/service-metrics-platform.md) ----
	// Lambda pilot: emulated services automatically publish the AWS-native
	// CloudWatch metrics real AWS publishes, so alarms configured against
	// them can actually fire. No I/O in construction (mirrors cloudwatch.New's
	// own contract) — collection only touches the store from its background
	// flush/sweep loops. auto and enabled currently behave identically because
	// CloudWatch is always wired; see config.ServiceMetricsAuto.
	var svcMetrics *metrics.Service
	if cfg.MetricsCollectionEnabled() {
		svcMetrics = metrics.NewRecorder(store, clk, logger)
		cloudwatchSvc.InitMetrics(svcMetrics)
		lambdaSvc.InitMetrics(svcMetrics)
		sqsSvc.InitMetrics(svcMetrics)
		snsSvc.InitMetrics(svcMetrics)
		ddbSvc.InitMetrics(svcMetrics)
		apigwSvc.InitMetrics(svcMetrics)
		prof.mark("  new: metrics")
	}

	// ---- Event notification wiring ----------------------------------------
	// S3 notifications → SQS + SNS + Lambda + EventBridge: connect after all
	// services are constructed.
	s3Svc.InitNotifications(sqsSvc.Enqueuer(), snsSvc.TopicPublisher(), lambdaSvc.Invoker(), lambdaSvc, ebSvc.BusPublisher(), bus, logger)
	// Lambda → CloudWatch Logs: wire log writer so Lambda can write invocation logs.
	lambdaSvc.InitLogWriter(logsSvc.LogWriter())
	// Lambda bus: lifecycle events for topology / UI.
	lambdaSvc.InitBus(bus)
	// Lambda ← S3: reactive code sync — when a function's code object is
	// updated in S3, automatically refresh CodeZip and invalidate the warm pool
	// so the next invoke picks up the new code without a redeploy.
	lambdaSvc.InitS3Sync(func(ctx context.Context, bucket, key, versionID string) ([]byte, *protocol.AWSError) {
		return s3Svc.GetObjectBytes(ctx, bucket, key, versionID)
	})
	// Lambda → EC2: VPC resolver so Lambda can connect containers to VPC networks.
	lambdaSvc.SetVPCResolver(ec2Svc)
	// EFS → Lambda/ECS: FileSystemConfigs and efsVolumeConfiguration mount the
	// backing volume in live mode.
	lambdaSvc.SetEFSResolver(efsSvc)
	ecsSvc.SetEFSResolver(efsSvc)
	ecsSvc.InitLogWriter(logsSvc.LogWriter())
	ecsSvc.SetTargetRegistrar(elbv2Svc)
	ecsSvc.SetSecretResolvers(smSvc, ssmSvc)
	efsSvc.SetSubnetZoneResolver(ec2Svc)
	// ECS/Lambda → ECR: a task definition's image, or a PackageType=Image
	// function's ImageUri, may name this account's ECR registry — the shape CDK
	// synthesises for a container asset — which is a real AWS address the bytes
	// are not at. ECR maps it onto the registry it serves, and supplies that
	// registry's credentials. Wired before the Docker probe below, which is
	// where the pullers that use it are built.
	ecsSvc.SetImageResolver(ecrSvc)
	lambdaSvc.SetImageResolver(ecrSvc)
	// ECS/RDS → EC2: resolve subnet-backed launches against VPC network state.
	ecsSvc.SetVPCResolver(ec2Svc)
	rdsSvc.SetVPCResolver(ec2Svc)
	elasticacheSvc.SetVPCResolver(ec2Svc)
	// MSK/EKS → EC2: the same, for the subnets a cluster is created with. On
	// AWS there is no non-VPC MSK cluster, so a broker container that stayed on
	// the default plane could not be reached by a function in the VPC that
	// created it.
	mskSvc.SetVPCResolver(ec2Svc)
	eksSvc.SetVPCResolver(ec2Svc)
	// SNS: wire lifecycle/publish events for topology / UI.
	snsSvc.InitBus(bus)
	// SNS → SQS: wire enqueuer for Publish fan-out (and subscription DLQs).
	snsSvc.InitSQSDelivery(sqsSvc.Enqueuer())
	// SNS → Lambda: wire the invoker for lambda-protocol subscriptions.
	snsSvc.InitLambdaDelivery(lambdaSvc.Invoker(), lambdaSvc)
	// SNS/SES → email: wire SMTP mailer. The mock capture server (if enabled) is
	// started as a background goroutine; its lifecycle is tied to the process.
	mailStore := smtp.NewMailStore(cfg.SMTPInboxMax)

	// publishInbox fires the inbox SSE event for any captured message
	// (email or SMS). Defined once and shared by both transport callbacks.
	publishInbox := func(m *smtp.CapturedMessage) {
		bus.Publish(context.Background(), events.Event{
			Type:   events.InboxDelivered,
			Time:   m.ReceivedAt,
			Source: "inbox",
			Payload: events.InboxDeliveredPayload{
				ID:      m.ID,
				From:    m.From,
				To:      m.To,
				Subject: m.Subject,
			},
		})
	}

	// SMS capture is always available — it writes directly into the store.
	smsSender := smtp.NewMockSMSSender(mailStore)
	smsSender.OnMessage = publishInbox
	snsSvc.InitSMSDelivery(smsSender)
	cognitoSvc.InitSMSDelivery(smsSender)

	// Webhook + push capture for SNS http/https and application subscriptions.
	outbound := smtp.NewMockOutboundCapture(mailStore, publishInbox)
	snsSvc.InitOutboundCapture(outbound)

	// Expose the inbox capture API under /_overcast/ses/inbox/.
	r.Route("/_overcast/ses/inbox", inboxHandlers(mailStore))

	// listeners is what /_overcast/health reports for the auxiliary listeners
	// bound beside the AWS API. The Lambda Runtime API reports through the
	// Lambda service instead, because it binds only once Docker has answered.
	listeners := listenstatus.NewTracker()

	var mailerHost string
	var mailerPort int
	if cfg.SMTPMock {
		// Bound here rather than in the background: a loopback bind is instant,
		// and settling it now means the mailer, the cleanup and the health
		// endpoint all see the outcome instead of racing it.
		smtpSrv, boundAddr, fellBack, err := listenSMTPMock(mailStore, cfg.SMTPPort, config.DefaultSMTPPort, logger)
		var mockMailer smtp.Mailer
		if err != nil {
			logger.Error("smtp mock server failed to bind — Inbox capture unavailable: SES, SNS email and Cognito mail cannot be sent until it is fixed",
				zap.Int("port", cfg.SMTPPort),
				zap.Error(err),
				zap.String("fix", smtpListenFix))
			listeners.Set(listenstatus.SMTP, listenstatus.Status{State: listenstatus.Failed, Error: err.Error(), Fix: smtpListenFix})
			// A send fails with the reason rather than waiting for a server
			// that is never coming.
			mockMailer = smtp.Unavailable(fmt.Errorf("smtp: mock server is not listening: %w (%s)", err, smtpListenFix))
		} else {
			smtpSrv.OnMessage = publishInbox
			cleanups = append(cleanups, func() { smtpSrv.Close() })
			go smtpSrv.Serve(smtpCtx)

			host, portStr, _ := net.SplitHostPort(boundAddr)
			port, _ := strconv.Atoi(portStr)
			mockMailer = smtp.NewMailer(smtp.Config{
				Host: host,
				Port: port,
			})
			listeners.Set(listenstatus.SMTP, listenstatus.Status{State: listenstatus.Listening, Addr: boundAddr, FellBack: fellBack})
			logger.Info("smtp mock server started", zap.String("addr", boundAddr))
		}

		snsSvc.InitEmailDelivery(mockMailer)
		sesSvc.InitEmailDelivery(mockMailer)
		cognitoSvc.InitEmailDelivery(mockMailer)
	} else {
		mailerHost = cfg.SMTPHost
		mailerPort = cfg.SMTPPort
	}
	if mailerHost != "" {
		mailer := smtp.NewMailer(smtp.Config{
			Host:     mailerHost,
			Port:     mailerPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			TLS:      cfg.SMTPTLS,
		})
		snsSvc.InitEmailDelivery(mailer)
		sesSvc.InitEmailDelivery(mailer)
		cognitoSvc.InitEmailDelivery(mailer)
	}
	// SQS: wire bus for queue lifecycle events (topology map).
	sqsSvc.InitBus(bus)
	// CloudWatch Logs: wire bus for log group lifecycle events.
	logsSvc.InitBus(bus)
	// IAM: wire bus for role/user/policy lifecycle events.
	iamSvc.InitBus(bus)
	// STS: wire bus for assume-role events.
	stsSvc.InitBus(bus)
	// SSM: wire bus for parameter lifecycle events.
	ssmSvc.InitBus(bus)
	// KMS: wire bus for key lifecycle events.
	kmsSvc.InitBus(bus)
	// Secrets Manager: wire bus for secret lifecycle events, and the
	// synchronous Lambda invoker the four-step rotation protocol drives —
	// every step has to complete before the next begins, so the fire-and-forget
	// async path would not do.
	smSvc.InitBus(bus)
	smSvc.InitLambdaInvoker(lambdaSvc.SyncInvoker())
	// Cognito: wire the synchronous Lambda invoker LambdaConfig triggers
	// (PreSignUp, PostConfirmation, PreTokenGeneration, PostAuthentication,
	// CustomMessage) run through (issue #1171) — same seam as the two above.
	cognitoSvc.InitLambdaInvoker(lambdaSvc.SyncInvoker())
	// SES: wire bus for email/identity/template events.
	sesSvc.InitBus(bus)
	// Kinesis: wire bus for stream lifecycle events.
	kinesisSvc.InitBus(bus)
	// RDS: wire bus so Docker container events update instance status.
	rdsSvc.InitBus(bus)
	// ECS: wire bus so Docker container events update task status.
	ecsSvc.InitBus(bus)
	efsSvc.InitBus(bus)
	eksSvc.InitBus(bus)
	// ElastiCache: wire bus so Docker container events update cluster status.
	elasticacheSvc.InitBus(bus)
	// ECR: wire bus for repository lifecycle events.
	ecrSvc.InitBus(bus)
	// MSK: wire bus so Docker container events update cluster status.
	mskSvc.InitBus(bus)
	// EC2: wire bus for VPC/subnet/security group lifecycle events.
	ec2Svc.InitBus(bus)
	// EventBridge: wire bus for bus/rule lifecycle events.
	ebSvc.InitBus(bus)
	ebSvc.InitLambdaAuthorizer(lambdaSvc)
	ebSvc.InitRouter(r)
	// EC2 and ECS: wire the EventBridge bus publisher so instance/task state
	// transitions emit the same service-originated events real AWS does
	// (EC2 Instance State-change Notification, ECS Task State Change).
	ec2Svc.InitEventBridge(ebSvc.BusPublisher())
	ecsSvc.InitEventBridge(ebSvc.BusPublisher())
	// Step Functions: wire the EventBridge bus publisher so execution status
	// transitions emit a Step Functions Execution Status Change event, the
	// remainder of #758 (#1221).
	sfnSvc.InitEventBridge(ebSvc.BusPublisher())
	// Lambda: wire the root router so a dead-lettered async invocation reaches
	// its SQS queue or SNS topic over the same path an SDK client would take.
	lambdaSvc.InitRouter(r)
	// CloudWatch: wire alarm-action dispatch. Deliveries go back through the
	// root router, so an alarm's SNS action takes exactly the path an SDK
	// Publish would, and the state-change event goes onto the default event
	// bus through the real PutEvents. A service that owns an alarm action
	// ARN type — Auto Scaling scaling policies, issue #474 — claims it with
	// alarmActions.Register("autoscaling", …) here, rather than by adding a
	// case inside the CloudWatch package.
	alarmActions := alarmaction.NewDispatcher(r, cfg.Region, bus, logger)
	cloudwatchSvc.InitAlarmActions(alarmActions)
	// Auto Scaling: claim those scalingPolicy action ARNs, so an alarm naming
	// a scaling policy actually executes it (#474). The reconciler launches
	// and terminates EC2 instances through the root router for the same
	// reason alarm actions go through it — the call takes exactly the path an
	// SDK client's RunInstances would, so a missing AMI is EC2's own error
	// rather than a silent no-op.
	alarmActions.Register("autoscaling", autoScalingSvc.HandleAlarmAction)
	autoScalingSvc.InitRouter(r)
	// Step Functions: wire bus for state machine/execution lifecycle events,
	// plus the root router so Task states reach Lambda/SQS/SNS/DynamoDB
	// through the same handlers an SDK call would.
	sfnSvc.InitBus(bus)
	sfnSvc.InitRouter(r)
	// WAF: keep Web ACL lists and topology nodes fresh for SDK and UI mutations.
	wafSvc.InitBus(bus)
	// AppSync: wire bus for API lifecycle events.
	appsyncSvc.InitBus(bus)
	appsyncSvc.InitLambdaInvoker(lambdaSvc.SyncInvoker())
	appsyncSvc.InitDynamoDBInvoker(ddbSvc.DynamoDBInvoker())
	appsyncSvc.InitCognitoValidator(cognitoSvc)
	// CloudFront: wire bus for distribution lifecycle events.
	cloudfrontSvc.InitBus(bus)
	// CloudFormation: wire bus + router for async provisioning.
	cfnSvc.InitBus(bus)
	// API Gateway: wire bus + Lambda invoker for proxy execution.
	apigwSvc.InitBus(bus)
	apigwSvc.InitDomainRegistry(domainReg)
	apigwSvc.InitLambdaInvoker(lambdaSvc.SyncInvoker(), lambdaSvc)
	// Lambda ← API GW / AppSync: proactive-init trigger evidence — these
	// services can attest a function is wired to receive traffic. Queried
	// only when a function's configuration settles after a deploy.
	lambdaSvc.AddTriggerSource(apigwSvc)
	lambdaSvc.AddTriggerSource(appsyncSvc)
	apigwSvc.InitCognitoValidator(cognitoSvc)
	// EventBridge Pipes: subscribe to DynamoDB stream events, wire the polled
	// sources (SQS, Kinesis) and the Lambda enrichment, and hand the service the
	// root router so a target is reached the same way an SDK call would reach it.
	pipesSvc.InitDelivery(bus, pipes.Sources{
		Messages: sqsSvc.Receiver(),
		Streams:  kinesisSvc.StreamReceiver(),
		Enricher: lambdaSvc.SyncInvoker(),
	})
	pipesSvc.InitRouter(r)
	// EventBridge Scheduler: hand the service the root router and start its cron
	// engine. Firing goes through the same internal/eventtarget dispatcher an
	// EventBridge rule delivers through, so one target ARN behaves identically
	// on a schedule and on a rule (#734).
	schedulerSvc.InitRouter(r)
	// ---- Docker Supervisor ------------------------------------------------
	// A single Supervisor probes Docker once per unique socket, creates per-
	// service networks, runs one event watcher, and reconciles container state.
	// Services that need Docker (Lambda, RDS, ECS) are wired in a single
	// background goroutine so startup is not blocked if Docker is slow.
	// Lambda manages its own client/runtime but still benefits from the shared
	// watcher (container lifecycle events on the bus).
	dockerServices := map[string]docker.ServiceConfig{}
	dockerSetters := map[string]func(*docker.Client){} // name → SetDocker callback
	// Lambda probes Docker independently (it needs the Runtime API server);
	// we register its socket so the Supervisor starts a watcher.
	dockerServices["lambda"] = docker.ServiceConfig{Name: "lambda", Socket: cfg.LambdaDockerSocket}
	dockerServices["ecr"] = docker.ServiceConfig{Name: "ecr", Socket: cfg.LambdaDockerSocket}
	// Live is the default. Mock mode is not registered at all, so no probe runs
	// for containers that will never exist.
	if cfg.RDSMode == config.RDSModeLive {
		dockerServices["rds"] = docker.ServiceConfig{Name: "rds", Socket: cfg.RDSDockerSocket}
		dockerSetters["rds"] = rdsSvc.SetDocker
	}
	dockerServices["elasticache"] = docker.ServiceConfig{Name: "elasticache", Socket: cfg.ElastiCacheDockerSocket}
	dockerSetters["elasticache"] = elasticacheSvc.SetDocker
	dockerServices["msk"] = docker.ServiceConfig{Name: "msk", Socket: cfg.MSKDockerSocket}
	dockerSetters["msk"] = mskSvc.SetDocker
	dockerServices["ecs"] = docker.ServiceConfig{Name: "ecs", Socket: cfg.ECSDockerSocket}
	dockerSetters["ecs"] = ecsSvc.SetDocker
	// EC2 additionally manages one network per VPC, created on demand rather
	// than at probe time.
	dockerServices["ec2"] = docker.ServiceConfig{Name: "ec2", Socket: cfg.LambdaDockerSocket}
	dockerSetters["ec2"] = ec2Svc.SetDocker
	if cfg.EKSMode == config.EKSModeLive && cfg.EKSDockerSocket != "" {
		dockerServices["eks"] = docker.ServiceConfig{Name: "eks", Socket: cfg.EKSDockerSocket}
		dockerSetters["eks"] = eksSvc.SetDocker
	}
	// EFS live mode is the default, so this normally registers: it manages
	// named volumes, and without the opt-in NFS data plane it starts no
	// long-lived containers. A failed probe leaves the service metadata-only,
	// which is what mock mode is.
	if cfg.EFSMode == config.EFSModeLive && cfg.EFSDockerSocket != "" {
		dockerServices["efs"] = docker.ServiceConfig{Name: "efs", Socket: cfg.EFSDockerSocket}
		dockerSetters["efs"] = efsSvc.SetDocker
	}
	if len(dockerServices) > 0 {
		dockerTracker := docker.NewTracker()
		dockerStatusFn = func() *docker.Status {
			s := dockerTracker.Snapshot()
			return &s
		}
		dockerSup := docker.NewSupervisorWithTracker(bus, logger, dockerTracker)
		cleanups = append(cleanups, dockerSup.Close)
		// EC2's networks are created on demand, one per VPC, long after the
		// probe that ensures the two planes — and only EC2 can resolve the
		// instance identity that says whose they are. Both reach health
		// through the same tracker.
		ec2Svc.SetNetworkReporter(dockerTracker)

		go func() {
			// Collect configs in deterministic order.
			var configs []docker.ServiceConfig
			for _, name := range []string{"lambda", "ecr", "rds", "elasticache", "msk", "ecs", "ec2", "eks", "efs"} {
				if sc, ok := dockerServices[name]; ok {
					configs = append(configs, sc)
				}
			}

			results := dockerSup.Probe(context.Background(), configs, dataplane.PlaneSpecs(cfg))

			// Environment preflight (deploy-failure-diagnosis.md W4): one
			// summary Warn naming every service Docker was unreachable for,
			// rather than a wall of per-socket lines nobody reads — see
			// preflight_docker.go's doc comment for why this needs no second
			// probe of its own.
			if msg := dockerUnavailableWarning(configs, results); msg != "" {
				logger.Warn(msg)
			}

			// The resolver has been answering since before any of this ran, with
			// no way to tell a reachable endpoint from an unreachable one. Now
			// that a daemon is here, give it one — see internal/dataplane.Guard
			// for why this is affordable on the query path.
			if dnsServer != nil && len(results) > 0 {
				dnsServer.SetGuard(dataplane.NewGuard(results[0].Client, logger))
			}

			// Wire every successful service before reconciling. Services sharing
			// one daemon then consume one daemon-wide snapshot rather than each
			// issuing its own full list call.
			for _, res := range results {
				if setter, ok := dockerSetters[res.Name]; ok {
					setter(res.Client)
				}
			}
			targetsByClient := dockerReconcileTargetsByClient(results, serviceByName)

			// Snapshot only after the watcher has opened its stream. The initial
			// connected event covers startup; every reconnect closes a missed-event
			// gap through the same idempotent reconciliation path.
			bus.Subscribe(events.DockerDaemonConnected, dockerConnectedHandler(targetsByClient, store, logger))

			// Start one watcher per unique Docker daemon (blocks until shutdown).
			dockerSup.Run(context.Background())
		}()
	}
	// SQS/DynamoDB Streams → Lambda via event source mappings.
	var receiver events.MessageReceiver = sqsSvc.Receiver()
	lambdaSvc.InitESMDelivery(receiver, bus)

	// Build goal tier map for enabled services.
	enabledGoalTiers := make(map[string]string, len(enabledServiceNames))
	for _, name := range enabledServiceNames {
		if goal, ok := ServiceGoalTiers[name]; ok {
			enabledGoalTiers[name] = goal
		} else if current, ok := ServiceTiers[name]; ok {
			// No explicit goal: goal == current tier (already where we want to be)
			enabledGoalTiers[name] = current
		}
	}

	// ---- Host-based routing (execute-api / lambda-url / appsync-api) ------
	// Populates the hostRoutes slice declared with the middleware chain
	// above (middleware.HostDispatch reads it via pointer at request time).
	// Adding a new host-routed service costs exactly one row here plus one
	// label in internal/middleware/hostroute.go's hostRouteLabels — see that
	// file's doc comment for the full recipe.
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelExecuteAPI, Rewrite: apigwSvc.HostRouteRewrite})
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelCloudFront, Rewrite: cloudfrontSvc.HostRouteRewrite})
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelLambdaURL, Rewrite: lambdaSvc.HostRouteRewrite})
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelAppSyncAPI, Rewrite: appsyncSvc.HostRouteRewrite})
	// Real AWS serves subscriptions on a separate appsync-realtime-api
	// host, and Amplify derives it by substituting into the GraphQL URL.
	// Both labels reach the same service; the rewrite picks the endpoint.
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelAppSyncRealtimeAPI, Rewrite: appsyncSvc.HostRouteRewrite})
	hostRoutes = append(hostRoutes, middleware.HostRouteRow{Label: middleware.LabelELB, Rewrite: elbv2Svc.HostRouteRewrite})

	// ---- /v2/apis service dispatch ----------------------------------------
	// Both API Gateway v2 and AppSync Events API register routes under /v2/apis.
	// In real AWS these live on different hostnames; in this emulator they share
	// one listener. We disambiguate using the SigV4 credential scope service
	// name ("apigateway" vs "appsync"). If neither service is enabled, or if
	// the credential scope cannot be parsed, we fall back to API Gateway (the
	// more commonly used service at this path).
	{
		var apigwV2Router, appsyncEventsRouter chi.Router
		if registeredForTest(cfg, "apigateway") {
			apigwV2Router = apigwSvc.V2APIRouter()
		}
		if registeredForTest(cfg, "appsync") {
			appsyncEventsRouter = appsyncSvc.EventsAPIRouter()
		}
		dispatchMounts = recordDispatchMount(dispatchMounts, "/v2/apis", "apigateway", apigwV2Router)
		dispatchMounts = recordDispatchMount(dispatchMounts, "/v2/apis", "appsync", appsyncEventsRouter)
		if apigwV2Router != nil || appsyncEventsRouter != nil {
			r.Route("/v2/apis", func(sub chi.Router) {
				sub.HandleFunc("/*", v2APIsDispatch(apigwV2Router, appsyncEventsRouter))
				sub.HandleFunc("/", v2APIsDispatch(apigwV2Router, appsyncEventsRouter))
			})
		}
	}

	// ---- /applications service dispatch ------------------------------------
	// AppConfig and Service Catalog AppRegistry both model the /applications
	// tree, and the two overlap exactly where it hurts: POST /applications and
	// GET|PATCH|DELETE /applications/{id} are modeled by both. In real AWS they
	// are different hostnames; here they share one listener, so the main router
	// owns the path and picks the owner from the SigV4 credential scope, as it
	// already does for /v2/apis.
	//
	// AppRegistry is the fallback owner: it answered this path alone until #854,
	// and unsigned callers (the web UI among them) must keep reaching it.
	//
	// Both sub-routers hand a path they do not serve back to the REST fallback
	// rather than answering chi's bare 404. Without that, every modeled
	// operation beneath /applications that Overcast does not implement — the
	// deployment and experiment surfaces, among others — would 404 with no AWS
	// error body at all. A chi sub-router owns its whole subtree, and that is
	// the mechanism that made #854 silent in the first place.
	{
		applicationsFallback := restFallback(operationRegistry, s3Router)
		var appconfigApps, appregistryApps chi.Router
		if registeredForTest(cfg, "appconfig") {
			appconfigApps = appconfigSvc.ApplicationsRouter()
			delegateUnmatched(appconfigApps, applicationsFallback)
		}
		if registeredForTest(cfg, "appregistry") {
			appregistryApps = appregistrySvc.ApplicationsRouter()
			delegateUnmatched(appregistryApps, applicationsFallback)
		}
		dispatchMounts = recordDispatchMount(dispatchMounts, "/applications", "appconfig", appconfigApps)
		dispatchMounts = recordDispatchFallback(dispatchMounts, "/applications", "appregistry", appregistryApps)
		if appconfigApps != nil || appregistryApps != nil {
			r.Route("/applications", func(sub chi.Router) {
				sub.HandleFunc("/*", applicationsDispatch(appconfigApps, appregistryApps))
				sub.HandleFunc("/", applicationsDispatch(appconfigApps, appregistryApps))
			})
		}
	}

	// ---- /v1/tags service dispatch -----------------------------------------
	// AppSync and MSK both expose TagResource/UntagResource/ListTagsForResource
	// at /v1/tags/{resourceArn}. Unlike /v2/apis above, the path here carries
	// the resourceArn itself, which is self-describing — its
	// "arn:{partition}:{service}:..." shape names the owning service
	// directly — so we dispatch on that instead of the SigV4 credential
	// scope. This also means a malformed or foreign-service ARN gets a
	// clean 404 rather than being silently serviced by whichever service's
	// route happens to match, matching how real AWS would never route such
	// a request to either service in the first place.
	{
		tagRouters := map[string]http.Handler{}
		if registeredForTest(cfg, "appsync") {
			routes := appsyncSvc.TagsRouter()
			tagRouters["appsync"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/v1/tags", "appsync", routes)
		}
		if registeredForTest(cfg, "msk") {
			routes := mskSvc.TagsRouter()
			tagRouters["kafka"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/v1/tags", "msk", routes)
		}
		if len(tagRouters) > 0 {
			r.Route("/v1/tags", func(sub chi.Router) {
				sub.HandleFunc("/*", tagsDispatch(tagRouters, nil))
			})
		}
	}

	// ---- /tags service dispatch ---------------------------------------------
	// Pipes, EKS, Scheduler, AppConfig, API Gateway (whose endpoint
	// AppRegistry's SDK shares) and Backup (TagResource/ListTags only — see
	// below) all answer tag operations at /tags/{resourceArn}. Left to their
	// own RegisterRoutes they race for the same chi patterns and the last
	// registration silently wins, so the main router owns the path and
	// dispatches on the ARN's service prefix, exactly like /v1/tags above.
	// API Gateway's ARN-keyed tag store answers its own ARNs and AppRegistry's
	// "servicecatalog" ones — AppRegistry's SDK shares API Gateway's endpoint
	// and stores tags in the same ARN-keyed map, see
	// internal/services/appregistry/service.go. It is deliberately NOT the
	// owner of every other ARN: it used to be, and dozens of services'
	// unimplemented tag operations read as HTTP 200 {"tags":{}} — #976 found
	// `aws backup list-tags` succeeding against a service that, at the time,
	// implemented no tag operations at all, which is #963's failure mode.
	// Backup now has real tag operations of its own (#1195) and is dispatched
	// here like every other owner. An ARN no router claims now falls back to
	// restFallback, exactly as if no /tags route had matched: a signed caller
	// reaches the generated 501, and unsigned traffic gets S3's answer, the
	// router-wide design for unclaimed paths.
	{
		tagRouters := map[string]http.Handler{}
		if registeredForTest(cfg, "pipes") {
			routes := pipesSvc.TagsRouter()
			tagRouters["pipes"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "pipes", routes)
		}
		if registeredForTest(cfg, "eks") {
			routes := eksSvc.TagsRouter()
			tagRouters["eks"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "eks", routes)
		}
		if registeredForTest(cfg, "scheduler") {
			routes := schedulerSvc.TagsRouter()
			tagRouters["scheduler"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "scheduler", routes)
		}
		if registeredForTest(cfg, "appconfig") {
			routes := appconfigSvc.TagsRouter()
			tagRouters["appconfig"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "appconfig", routes)
		}
		if registeredForTest(cfg, "apigateway") {
			routes := apigwSvc.TagsRouter()
			tagRouters["apigateway"] = routes
			tagRouters["servicecatalog"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "apigateway", routes)
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "appregistry", routes)
		}
		if registeredForTest(cfg, "backup") {
			// Backup's TagResource and ListTags share this path space
			// (#1195); UntagResource does not — it lives at
			// /untag/{ResourceArn}, registered directly by Backup's own
			// RegisterRoutes, since no other service answers there.
			routes := backupSvc.TagsRouter()
			tagRouters["backup"] = routes
			dispatchMounts = recordDispatchMount(dispatchMounts, "/tags", "backup", routes)
		}
		if len(tagRouters) > 0 {
			r.Route("/tags", func(sub chi.Router) {
				sub.HandleFunc("/*", tagsDispatch(tagRouters, restFallback(operationRegistry, s3Router)))
			})
		}
	}

	healthHandler := newHealthHandler(cfg, store, enabledServiceNames, enabledTiers, enabledGoalTiers, dockerStatusFn, listenerStatusFn(listeners, lambdaSvc))
	r.Get("/_overcast/health", healthHandler)

	// The two health URLs Overcast answers that are not its own: /_health (its
	// own, before #927) and /_localstack/health (LocalStack's). Both are what
	// container healthchecks and Testcontainers wait strategies hard-code, and
	// a 404 on either reads as a dead container — see health_compat.go for why
	// that is worth serving rather than documenting.
	compatHinter := &aliasHinter{logger: logger}
	r.Get(middleware.LegacyHealthPath, newLegacyHealthHandler(healthHandler, compatHinter))
	r.Get(middleware.LocalStackHealthPath, newLocalStackHealthHandler(cfg, enabledServiceNames, compatHinter))

	// The rest of LocalStack's operational namespace that Overcast already has
	// an endpoint for, aliased onto that endpoint's own handler — see
	// localstack_compat.go for what is deliberately left to the 404 below.
	// Registered before the wildcard for readability only: chi's trie prefers
	// a static segment over a wildcard whatever the registration order.
	r.Get(localStackInitPath, newLocalStackInitHandler(initStatus, localStackInitPath, compatHinter))
	r.Get(localStackInitStagePath, newLocalStackInitHandler(initStageStatus, localStackInitStagePath, compatHinter))
	r.Post(localStackResetPath, newLocalStackResetHandler(resetAll, compatHinter))

	r.HandleFunc(middleware.LocalStackPrefix+"*", newLocalStackNotFoundHandler(compatHinter))

	// LocalStack's /_aws/ inspection namespace: the half a test suite hits,
	// to read back what SES "sent" and what is sitting in a queue. Each served
	// path is a translation of an Overcast store the /_overcast/ API already
	// exposes — the inbox behind /_overcast/ses/inbox, the SQS peek behind
	// GET /{account}/{queue} — into LocalStack's wire shape; see aws_compat.go
	// for the shapes, and for what is left to the 404.
	r.Get(awsCompatSESPath, newAWSCompatSESListHandler(mailStore, compatHinter))
	r.Delete(awsCompatSESPath, newAWSCompatSESDeleteHandler(mailStore, compatHinter))
	sqsPeek := newAWSCompatSQSPeekHandler(cfg, sqsSvc, compatHinter)
	r.Get(awsCompatSQSMessagesPath, sqsPeek)
	r.Get(awsCompatSQSMessagesPathForm, sqsPeek)
	r.HandleFunc(middleware.AWSCompatPrefix+"*", newAWSCompatNotFoundHandler(compatHinter))

	// GET /_overcast/topology — full cross-region resource graph for the system map.
	r.Get("/_overcast/topology", newTopologyHandler(cfg, store))

	// GET /_overcast/preflight/region — why a console list page came back
	// empty when the resources are in a region the reader is not looking at.
	r.Get("/_overcast/preflight/region", newPreflightRegionHandler(cfg, store))

	// Register POST / handler for AWS target and query-protocol dispatch. The
	// generated registry also owns modeled operations when no configured service
	// dispatcher does, so this route is present even in a minimal S3-only setup.
	r.Post("/", targetDispatch(dispatchers, queryDispatchers, operationRegistry))
	r.Post("/service/{service}/operation/{operation}", smithyRPCDispatch(smithyDispatchers, operationRegistry, s3Router))
	// SigV4's service scope disambiguates the small number of modeled REST root
	// bindings from S3 ListBuckets; unsigned and S3-signed root requests retain
	// the established S3 behavior.
	r.Get("/", restFallback(operationRegistry, s3Router))
	r.HandleFunc("/*", restFallback(operationRegistry, s3Router))

	r.NotFound(notFoundHandler)

	prof.mark("cross-service wiring + final routes")

	// Seal the startup phase timeline and record the ready timestamp atomically
	// so that startup_duration_ms and startup_phases share the same reference.
	prof.finalize()
	readyTime = time.Now()

	// Wire CloudFormation's internal dispatch router now that all routes are registered.
	cfnSvc.InitRouter(r)

	// Start goroutine leak monitor in debug mode.
	// Samples every 5 s; dumps all goroutine stacks when the count stays above
	// 150 for 30 s so leaks can be diagnosed without reaching for a profiler.
	if cfg.Debug {
		mon := newGoroutineMonitor(logger)
		mon.Start()
		cleanups = append(cleanups, mon.Stop)
	}

	return withDispatchMounts(r, dispatchMounts, routeOwners.ownersByKey()), preShutdown, func(ctx context.Context) {
			// Stop background service resources (e.g. Runtime API long-poll server).
			for _, st := range stoppers {
				t0 := time.Now()
				st.Stop(ctx)
				logger.Info("service stopped", zap.String("service", st.name), zap.Duration("elapsed", time.Since(t0)))
			}
			// Shut down the event bus worker pool after services have stopped
			// so in-flight events are drained before the workers exit.
			t0 := time.Now()
			bus.Stop()
			logger.Info("event bus stopped", zap.Duration("elapsed", time.Since(t0)))
			// Stop the service-metrics recorder after every service that might
			// still call Observe (Lambda in particular) has stopped, so its
			// final bounded Flush sees no more concurrent writers.
			if svcMetrics != nil {
				t0 := time.Now()
				svcMetrics.Stop(ctx)
				logger.Info("service metrics recorder stopped", zap.Duration("elapsed", time.Since(t0)))
			}
			// Close other handles (e.g. SMTP listener).
			for _, fn := range cleanups {
				fn()
			}
		}, func() {
			// Wait for all background-initialising services to become ready.
			for _, rd := range readiers {
				rd.WaitReady()
			}
		}
}

type dockerReconcileTarget struct {
	name    string
	service Service
}

type dockerReconcileGroup struct {
	client  *docker.Client
	targets []dockerReconcileTarget
	mu      sync.Mutex
}

func (g *dockerReconcileGroup) reconcile(ctx context.Context, logger *zap.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	reconcileDockerDaemon(ctx, g.client, g.targets, logger)
}

func dockerReconcileTargetsByClient(results []docker.ServiceResult, services map[string]Service) map[*docker.Client]*dockerReconcileGroup {
	groups := make(map[*docker.Client]*dockerReconcileGroup)
	for _, result := range results {
		if service := services[result.Name]; service != nil {
			group := groups[result.Client]
			if group == nil {
				group = &dockerReconcileGroup{client: result.Client}
				groups[result.Client] = group
			}
			group.targets = append(group.targets, dockerReconcileTarget{name: result.Name, service: service})
		}
	}
	return groups
}

// dockerReconcileStoreWait bounds how long a Docker reconcile waits for the
// state store's one-time startup migration. A migration still unfinished after
// this long is a problem of its own, and the reconcile stops waiting on it —
// each reconciler is then responsible for what it does with a store that still
// reads back empty (EC2 skips its pass rather than sweeping; see
// reconcileNetworks).
const dockerReconcileStoreWait = 90 * time.Second

// awaitStoreReady blocks until the state store has finished the one-time
// schema migration it may still be running at startup.
//
// Every reconciler reads the store to decide what the daemon's containers and
// networks *mean*: which of them a record still names, and which are litter.
// A pass that runs while the store is still migrating reads no records at all,
// so everything on the daemon looks like litter — and EC2's network pass acts
// on that, removing the networks its own VPC records name. The seed then loads
// those records pointing at networks deleted seconds earlier, and every ECS
// task and Lambda placed in the VPC fails with "network … not found" for the
// life of the process, because a completed pass vouches for every region and
// nothing revisits the question.
//
// A store with no such startup phase (memory, WAL) implements neither
// interface, and the absence means "already ready" — the same convention
// state.ReadyAwaiter and middleware.NotReady use.
func awaitStoreReady(ctx context.Context, store state.Store, logger *zap.Logger) {
	awaiter, ok := store.(state.ReadyAwaiter)
	if !ok {
		return
	}
	if r, ok := store.(state.NotReadyReporter); ok && !r.NotReady() {
		return
	}
	logger.Info("docker reconcile: waiting for the state store to finish its startup migration",
		zap.Duration("timeout", dockerReconcileStoreWait))
	waitCtx, cancel := context.WithTimeout(ctx, dockerReconcileStoreWait)
	defer cancel()
	t0 := time.Now()
	if err := awaiter.WaitReady(waitCtx); err != nil {
		logger.Warn("docker reconcile: the state store did not become ready; reconciling against it as it stands",
			zap.Duration("waited", time.Since(t0)), zap.Error(err))
		return
	}
	logger.Info("docker reconcile: state store ready", zap.Duration("waited", time.Since(t0)))
}

func dockerConnectedHandler(groups map[*docker.Client]*dockerReconcileGroup, store state.Store, logger *zap.Logger) events.HandlerFunc {
	return func(ctx context.Context, e events.Event) {
		p, ok := e.Payload.(docker.DaemonConnectedPayload)
		if !ok || p.Client == nil {
			return
		}
		group := groups[p.Client]
		if group == nil {
			return
		}
		awaitStoreReady(ctx, store, logger)
		group.reconcile(ctx, logger)
	}
}

// reconcileDockerDaemon takes one snapshot of each Docker object kind and
// fans the service-labelled subsets out to every reconciler on that daemon.
// It runs at startup and after watcher reconnects, so services get one
// idempotent lifecycle contract without N repeated full-daemon list calls.
func reconcileDockerDaemon(ctx context.Context, dc *docker.Client, targets []dockerReconcileTarget, logger *zap.Logger) {
	containerReconcilers := make([]dockerReconcileTarget, 0, len(targets))
	networkReconcilers := make([]dockerReconcileTarget, 0, len(targets))
	for _, target := range targets {
		if _, ok := target.service.(ContainerReconciler); ok {
			containerReconcilers = append(containerReconcilers, target)
		}
		if _, ok := target.service.(NetworkReconciler); ok {
			networkReconcilers = append(networkReconcilers, target)
		}
	}

	if len(containerReconcilers) > 0 {
		containers, err := dc.ListContainers(ctx, "")
		if err != nil {
			logger.Warn("docker reconcile: list containers failed", zap.Error(err))
		} else {
			serviceNames := make([]string, 0, len(containerReconcilers))
			for _, target := range containerReconcilers {
				serviceNames = append(serviceNames, target.name)
			}
			byService := docker.ContainersByService(containers, serviceNames...)
			for _, target := range containerReconcilers {
				snapshot := byService[target.name]
				logger.Info("docker reconcile: syncing container state",
					zap.String("service", target.name), zap.Int("containers", len(snapshot)))
				target.service.(ContainerReconciler).ReconcileContainers(ctx, snapshot)
			}
		}
	}

	if len(networkReconcilers) > 0 {
		networks, err := dc.ListNetworks(ctx, "")
		if err != nil {
			logger.Warn("docker reconcile: list networks failed", zap.Error(err))
		} else {
			serviceNames := make([]string, 0, len(networkReconcilers))
			for _, target := range networkReconcilers {
				serviceNames = append(serviceNames, target.name)
			}
			byService := docker.NetworksByService(networks, serviceNames...)
			for _, target := range networkReconcilers {
				snapshot := byService[target.name]
				logger.Info("docker reconcile: syncing network state",
					zap.String("service", target.name), zap.Int("networks", len(snapshot)))
				target.service.(NetworkReconciler).ReconcileNetworks(ctx, snapshot)
			}
		}
	}
}

// targetDispatch returns a handler that inspects the X-Amz-Target header and
// delegates to the correct service dispatcher. SQS/DynamoDB use X-Amz-Target;
// SNS uses the Query protocol (form-encoded body with Action field + XML).
func targetDispatch(dispatchers []TargetDispatcher, queryDispatchers []QueryDispatcher, operationRegistry *awsapi.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		for _, td := range dispatchers {
			if strings.HasPrefix(target, td.TargetPrefix()) {
				td.Dispatch(w, r)
				return
			}
		}
		if target != "" {
			if claim, ok := operationRegistry.ClaimTarget(target); ok {
				writeNotImplemented(w, r, claim)
				return
			}
		}
		// No X-Amz-Target match — try AWS Query protocol services (SNS, SES v1).
		if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			// This is the parse that decides the operation for every Query
			// service; it caches its results, so it is safe to call before
			// dispatching. protocol.ParseFormPreservingBody may already have
			// declined the body as too large to buffer speculatively, which is
			// not a rejection — it leaves the body readable precisely so this
			// parse can make the real decision on it.
			//
			// The cap is stated rather than inherited: ParseForm has its own
			// undocumented ceiling, and MaxQueryRequestBody matches it so the
			// set of accepted requests is unchanged. What changes is the
			// failure: a body this parse refuses used to fall past every branch
			// below to the NotImplementedQueryXML at the end of this handler,
			// so an oversized CreateStack came back 501 with
			// x-emulator-unsupported — an answer that names the wrong problem
			// and sends the caller looking for a feature gap.
			//
			// The refusal is written in the generic Query envelope even for a
			// service that uses EC2's. Nothing better is available: the failure
			// is that the body could not be parsed, so the Action naming the
			// service was never read. The 501 this replaced had the same
			// constraint.
			r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxQueryRequestBody)
			if err := r.ParseForm(); err != nil {
				protocol.WriteQueryXMLError(w, r, protocol.QueryFormParseError(err))
				return
			}
			action := r.FormValue("Action")
			version := r.FormValue("Version")
			// Version is a stricter discriminator than action name — it avoids
			// action name collisions between services (e.g. both SES and
			// CloudFormation implement "GetTemplate").
			if qd, ok := queryOwner(operationRegistry, queryDispatchers, version, action); ok {
				qd.DispatchQuery(w, r)
				return
			}
			if claim, ok := operationRegistry.ClaimQuery(version, action); ok {
				writeNotImplemented(w, r, claim)
				return
			}
			// Final fallback: first dispatcher with no ownership declaration.
			for _, qd := range queryDispatchers {
				if _, isActionOwner := qd.(QueryActionOwner); isActionOwner {
					continue
				}
				if _, isVersionOwner := qd.(QueryVersionOwner); isVersionOwner {
					continue
				}
				qd.DispatchQuery(w, r)
				return
			}
		}
		// No match — return an error in the appropriate format.
		// Query-protocol requests (IAM, STS, etc.) expect XML; JSON-target
		// requests expect JSON. Sending JSON to an XML-expecting SDK causes
		// a parse error ("char '{' is not expected").
		if r.Body != nil {
			io.Copy(io.Discard, r.Body) //nolint:errcheck
			r.Body.Close()              //nolint:errcheck
		}
		if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			protocol.NotImplementedQueryXML(w, r)
			return
		}
		body, _ := json.Marshal(struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}{Type: "UnknownOperationException", Message: "Unknown target: " + target})
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusBadRequest)
		w.Write(body)
	}
}

type smithyRPCService struct {
	dispatcher      TargetDispatcher
	protocolService ProtocolService
	operations      map[string]struct{}
	protocols       map[string]struct{}
	indexOnce       sync.Once
}

func newSmithyRPCService(dispatcher TargetDispatcher, service Service) *smithyRPCService {
	entry := &smithyRPCService{
		dispatcher: dispatcher,
	}
	if protocolService, ok := service.(ProtocolService); ok {
		entry.protocolService = protocolService
	}
	return entry
}

func (s *smithyRPCService) supports(operation, protocolName string) bool {
	if s.protocolService == nil {
		return false
	}
	s.indexOnce.Do(func() {
		operations := s.protocolService.Operations()
		protocols := s.protocolService.SupportedProtocols()
		s.operations = make(map[string]struct{}, len(operations))
		s.protocols = make(map[string]struct{}, len(protocols))
		for _, implemented := range operations {
			s.operations[implemented.Name()] = struct{}{}
		}
		for _, protocolCodec := range protocols {
			s.protocols[protocolCodec.Name()] = struct{}{}
		}
	})
	_, operationImplemented := s.operations[operation]
	_, protocolImplemented := s.protocols[protocolName]
	return operationImplemented && protocolImplemented
}

// smithyRPCServiceFor picks the implementation that answers a Smithy RPC v2
// request, from the two signals available: the `{service}` path segment and
// the registry's attribution of the modeled operation.
//
// The path segment leads, because it is the only one always present. A claim
// names an owner only when the pinned models can attribute the binding to one:
// where several services declare the same protocol, service shape and
// operation, awsmodelgen marks the entry Ambiguous and blanks its service
// rather than guess. `dispatchers[claim.Service]` then reads
// `dispatchers[""]`, which is not a lookup that fails cleanly — it is one that
// attributes an explicitly unattributable request to whatever is registered
// under the empty key. So the claim is consulted only when it names a service.
//
// The pinned models contain no ambiguous RPC binding today, and the service
// shape of the one implemented service they do bind (CloudWatch's
// GraniteServiceVersion20100801) is also its target prefix, so the claim
// branch is a bridge for the future rather than a live path. That is exactly
// why it needs a guard: a model refresh that collided an implemented service's
// RPC key would silently turn it into a 501, with nothing failing to say so.
// TestSmithyRPCServiceFor_* synthesises the ambiguous claim the models cannot
// yet supply.
//
// The segment is matched raw first so no dispatcher key can be shadowed by
// normalisation, then as its bare shape name, because Smithy's `{service}`
// label may be written as an absolute shape ID and ClaimRPC already accepts
// that spelling. Without the second attempt a caller using the qualified form
// gets a claim but no dispatcher — an implemented service answering 501.
//
// Returning nil is still possible, and is honest when it happens: both
// spellings of the shape have been tried against every registered service, so
// nothing here implements the request.
func smithyRPCServiceFor(dispatchers map[string]*smithyRPCService, serviceShape string, claim awsapi.Claim, modeled bool) *smithyRPCService {
	if service := dispatchers[strings.ToLower(serviceShape)]; service != nil {
		return service
	}
	if shape := awsapi.ServiceShapeName(serviceShape); shape != serviceShape {
		if service := dispatchers[strings.ToLower(shape)]; service != nil {
			return service
		}
	}
	if modeled && claim.Service != "" {
		return dispatchers[claim.Service]
	}
	return nil
}

func smithyRPCDispatch(dispatchers map[string]*smithyRPCService, operationRegistry *awsapi.Registry, s3Router http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		protocolHeader := strings.TrimSpace(r.Header.Get("Smithy-Protocol"))
		if protocolHeader == "" {
			s3Router.ServeHTTP(w, r)
			return
		}
		var wireProtocol awsapi.Protocol
		var wireCodec codec.Codec
		switch {
		case strings.EqualFold(protocolHeader, "rpc-v2-cbor"):
			wireProtocol, wireCodec = awsapi.ProtocolRPCV2CBOR, codec.RPCv2CBOR
		case strings.EqualFold(protocolHeader, "rpc-v2-json"):
			wireProtocol, wireCodec = awsapi.ProtocolRPCV2JSON, codec.RPCv2JSON
		default:
			notFoundHandler(w, r)
			return
		}
		serviceShape := chi.URLParam(r, "service")
		operation := chi.URLParam(r, "operation")
		claim, modeled := operationRegistry.ClaimRPC(wireProtocol, serviceShape, operation)
		service := smithyRPCServiceFor(dispatchers, serviceShape, claim, modeled)
		if service != nil && modeled {
			if service.supports(operation, wireCodec.Name()) {
				service.dispatcher.Dispatch(w, r)
				return
			}
			writeNotImplemented(w, r, claim)
			return
		}
		if service != nil {
			service.dispatcher.Dispatch(w, r)
			return
		}
		if modeled {
			writeNotImplemented(w, r, claim)
			return
		}
		w.Header().Set("x-emulator-unsupported-protocol", wireCodec.Name())
		wireCodec.WriteError(w, r, &protocol.AWSError{
			Code:       "UnsupportedProtocol",
			Message:    "This service does not support wire protocol " + wireCodec.Name() + ".",
			HTTPStatus: http.StatusUnsupportedMediaType,
		})
	}
}

// queryGetMiddleware returns middleware that intercepts GET / requests carrying
// an AWS Query-protocol Action param (e.g. SNS UnsubscribeURL).
// It uses a pointer to the dispatchers slice so it reads the fully-populated
// slice at request time — the slice is filled in by the service registration
// loop after middleware registration.
func queryGetMiddleware(queryDispatchers *[]QueryDispatcher, operationRegistry *awsapi.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/" || r.URL.Query().Get("Action") == "" {
				next.ServeHTTP(w, r)
				return
			}
			if err := r.ParseForm(); err != nil {
				next.ServeHTTP(w, r)
				return
			}
			action := r.FormValue("Action")
			version := r.FormValue("Version")
			if qd, ok := queryOwner(operationRegistry, *queryDispatchers, version, action); ok {
				qd.DispatchQuery(w, r)
				return
			}
			if claim, ok := operationRegistry.ClaimQuery(version, action); ok {
				writeNotImplemented(w, r, claim)
				return
			}
			// A root GET carrying Action is AWS Query traffic, not S3. Returning
			// Query XML keeps an unimplemented AWS command from becoming S3's
			// ListBuckets response.
			protocol.NotImplementedQueryXML(w, r)
		})
	}
}

// writeNotImplemented centralizes the modeled AWS error-envelope choice for
// registry-owned operations. Service handlers remain responsible for their
// own implemented and explicitly stubbed operations.
// registeredForTest reports whether a service is part of the test-only subset.
// Always true in production, where config.TestOnlyServiceSubset is nil.
//
// Only the two shared-path dispatchers below need this: /v2/apis and /v1/tags
// are registered outside the service loop because more than one service answers
// on them, so the loop's own subset check cannot reach them.
func registeredForTest(cfg *config.Config, service string) bool {
	return cfg.TestOnlyServiceSubset == nil || cfg.TestOnlyServiceSubset[service]
}

// testOnlyAbsentHandler answers for a service excluded by
// config.TestOnlyServiceSubset. It is unreachable in any production
// configuration, where the subset is always nil.
func testOnlyAbsentHandler(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		protocol.WriteQueryXMLError(w, r, protocol.ErrNotImplemented)
		return
	}
	protocol.WriteJSONError(w, r, protocol.ErrNotImplemented)
}

// writeNotImplemented answers a modeled operation no service handler claimed.
// The envelope choice lives in serviceutil so a service dispatching its own
// operations refuses an unimplemented one identically — see
// serviceutil.NotImplementedTarget.
func writeNotImplemented(w http.ResponseWriter, r *http.Request, claim awsapi.Claim) {
	serviceutil.WriteNotImplemented(w, r, claim)
}

// restFallback owns no AWS operation itself. It is deliberately registered
// after every explicit service route, so a modeled binding reaches it only
// when no implementation or disabled-service route claimed the request. S3 is
// then the sole remaining legitimate catch-all.
// s3ControlAccountHeader is sent by S3 Control on every operation and never by
// S3 itself. It is what separates the two APIs, which share a signing name.
const s3ControlAccountHeader = "X-Amz-Account-Id"

// isS3APISigningName reports whether a SigV4 credential scope belongs to a
// service that speaks the S3 object API itself, and whose traffic must
// therefore reach S3's routes. Object Lambda and S3 Express are S3 under
// another signing name.
//
// "s3-outposts" is deliberately absent. It signs the separate s3outposts
// control API, whose modeled paths cannot be mistaken for an object path.
func isS3APISigningName(credentialService string) bool {
	switch strings.ToLower(credentialService) {
	case "s3", "s3-object-lambda", "s3express":
		return true
	}
	return false
}

// hasBearerAuthorization reports whether the caller authenticated with a bearer
// token rather than SigV4. S3 has no bearer-token mode, so for an API modeled
// without an aws.auth#sigv4 name this is the only evidence that separates it
// from an S3 request.
func hasBearerAuthorization(r *http.Request) bool {
	const scheme = "Bearer "
	authorization := r.Header.Get("Authorization")
	return len(authorization) > len(scheme) && strings.EqualFold(authorization[:len(scheme)], scheme)
}

// addressesNonS3 reports whether a request carries positive evidence that it is
// not addressing S3. It reads only headers, and restClaimFor consults it before
// the generated trie, so ordinary S3 traffic never walks the trie.
func addressesNonS3(r *http.Request, credentialService string) bool {
	switch {
	case credentialService == "":
		// Unsigned traffic is S3's, unless it presents a bearer token — an auth
		// scheme S3 has no mode for.
		return hasBearerAuthorization(r)
	case isS3APISigningName(credentialService):
		// An S3-family scope is S3's, unless the caller also names the account
		// that only S3 Control addresses.
		return r.Header.Get(s3ControlAccountHeader) != ""
	default:
		// Every other AWS signing name: no SDK signs an S3 request as another
		// service, so the scope alone settles it.
		return true
	}
}

// claimAnswersCaller reports whether a modeled binding may answer this caller.
// Ambiguity decides which service to *name*, not whether S3 owns the path, so
// an ambiguous binding always qualifies once addressesNonS3 has ruled S3 out.
func claimAnswersCaller(claim awsapi.Claim, credentialService string) bool {
	switch {
	case claim.Ambiguous:
		return true
	case claim.SigningName == "":
		// Modeled with no aws.auth#sigv4 name, so only the bearer-token branch
		// of addressesNonS3 can have established that this is not S3.
		return credentialService == ""
	default:
		return strings.EqualFold(credentialService, claim.SigningName)
	}
}

// claimScopeMismatchesCaller reports whether a modeled binding that did not
// answer this caller (claimAnswersCaller already said no) failed because the
// caller positively signed for a *different, real* AWS service — the one
// case AWS itself answers with a scoped-credential error rather than routing
// the request anywhere else. See #887.
//
// Two narrower cases deliberately still fall through to S3, unchanged:
//   - claim.SigningName == "": the binding declares no expected signing name
//     to name in the error (Overcast has nothing to tell the caller it wants
//     instead), so there is nothing to answer with.
//   - !awsapi.IsSigningName(credentialService): the caller's scope is not a
//     signing name any pinned model actually declares — a typo, a
//     placeholder, or a non-AWS SigV4 client's own convention rather than
//     evidence a real AWS SDK produced this request. Overcast does not
//     validate signatures, so an unrecognised scope on its own does not
//     support a confident claim about what the caller meant; today's
//     behaviour (S3's wildcard) is the safer default. This mirrors
//     addressesNonS3's absent-scope branch, which the same rationale governs.
//
// Ambiguous claims never reach here: claimAnswersCaller already answers true
// for them regardless of scope, so no ambiguous binding can be "mismatched".
func claimScopeMismatchesCaller(claim awsapi.Claim, credentialService string) bool {
	return claim.SigningName != "" && awsapi.IsSigningName(credentialService)
}

// restClaimOutcome classifies what a request positively identified against a
// modeled REST binding: no claim at all, a claim that answers the caller, or
// a claim the caller's own credential scope rules out for a different real
// service.
type restClaimOutcome uint8

const (
	// restClaimNone: no modeled binding answers this request; S3 keeps the path.
	restClaimNone restClaimOutcome = iota
	// restClaimAnswers: the modeled binding answers this caller.
	restClaimAnswers
	// restClaimScopeMismatch: the binding is real, but the caller's credential
	// scope names a different real AWS service. AWS answers this with a
	// scoped-credential error rather than routing to any service's handler.
	restClaimScopeMismatch
)

// restClaimFor classifies a request against the modeled REST bindings and the
// caller's SigV4 credential scope. See restClaimOutcome for what each result
// means; restFallback decides what to write for each.
func restClaimFor(operationRegistry *awsapi.Registry, r *http.Request) (awsapi.Claim, restClaimOutcome) {
	credentialService := middleware.ServiceFromCredential(r)
	if !addressesNonS3(r, credentialService) {
		return awsapi.Claim{}, restClaimNone
	}
	claim, ok := claimModeledPath(operationRegistry, r)
	if !ok {
		return awsapi.Claim{}, restClaimNone
	}
	if claimAnswersCaller(claim, credentialService) {
		return claim, restClaimAnswers
	}
	if claimScopeMismatchesCaller(claim, credentialService) {
		return claim, restClaimScopeMismatch
	}
	return awsapi.Claim{}, restClaimNone
}

// claimModeledPath walks the generated trie for a request, trying the escaped
// path before the decoded one.
//
// The escaped form is the one the model describes. A Smithy non-greedy
// httpLabel binds a single URI segment, so an SDK percent-encodes a value that
// contains a slash — every service that puts an ARN in the path does this, and
// smithy-go, botocore and the JS SDK all agree. url.URL.Path is that value
// *decoded*, so an MSK cluster ARN's "cluster/name/uuid" reappears as three
// path segments and the request no longer has the shape of any binding.
// Matching on the decoded path alone therefore made every ARN-labelled binding
// unclaimable, and the request fell past the 501 into S3's bucket/object
// wildcard: "aws kafka list-cluster-operations-v2", correctly signed, came back
// <Error><Code>NoSuchKey</Code><Message>The specified key does not exist:
// v2/clusters/arn:aws:kafka:…/operations</Message></Error>. That is #963's
// mechanism reached by a second route, and it is not MSK-specific — it holds
// for any modeled binding whose label carries an ARN.
//
// The decoded form is still tried, second, and not merely for safety: a
// hand-written caller may put the raw ARN in the path with its slashes intact,
// and chi matches the routes that serve those callers the same two ways. Trying
// escaped-then-decoded can only add claims, never lose one, so no path S3
// answers today stops being S3's on this account.
//
// EscapedPath returns Path re-encoded when the request carried no RawPath, so
// the comparison — not a nil check — is what keeps an ordinary path to a single
// trie walk.
func claimModeledPath(operationRegistry *awsapi.Registry, r *http.Request) (awsapi.Claim, bool) {
	if escaped := r.URL.EscapedPath(); escaped != r.URL.Path {
		if claim, ok := operationRegistry.ClaimRESTQuery(r.Method, escaped, r.URL.RawQuery); ok {
			return claim, true
		}
	}
	return operationRegistry.ClaimRESTQuery(r.Method, r.URL.Path, r.URL.RawQuery)
}

func restFallback(operationRegistry *awsapi.Registry, s3Router http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claim, outcome := restClaimFor(operationRegistry, r)
		switch outcome {
		case restClaimAnswers:
			writeNotImplemented(w, r, claim)
		case restClaimScopeMismatch:
			writeScopeMismatch(w, r, claim)
		case restClaimNone:
			s3Router.ServeHTTP(w, r)
		}
	}
}

// scopeMismatchError builds the InvalidSignatureException AWS itself answers
// when a SigV4 credential scope names a service other than the one the
// endpoint expects. AWS validates the scope's service component against the
// endpoint before it ever computes a signature, so this fires independently
// of whether the signature itself would have verified — matching Overcast's
// stance that reading the scope is free while validating it is out of scope
// (#887). Message wording verified against real AWS's InvalidSignatureException.
func scopeMismatchError(expectedSigningName string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidSignatureException",
		Message:    fmt.Sprintf("Credential should be scoped to correct service: '%s'.", expectedSigningName),
		HTTPStatus: http.StatusForbidden,
	}
}

// writeScopeMismatch answers a scope-mismatched modeled binding in the error
// envelope that operation's wire protocol expects — the same per-profile
// dispatch writeNotImplemented uses, so a caller sees one consistent shape
// from a service regardless of which of its errors it receives.
func writeScopeMismatch(w http.ResponseWriter, r *http.Request, claim awsapi.Claim) {
	aerr := scopeMismatchError(claim.SigningName)
	switch claim.ErrorProfile {
	case awsapi.ErrorProfileJSON:
		protocol.WriteJSONError(w, r, aerr)
	case awsapi.ErrorProfileEC2QueryXML:
		protocol.WriteEC2QueryXMLError(w, r, aerr)
	case awsapi.ErrorProfileQueryXML:
		protocol.WriteQueryXMLError(w, r, aerr)
	case awsapi.ErrorProfileXML:
		protocol.WriteXMLError(w, r, aerr)
	case awsapi.ErrorProfileRPCV2CBOR:
		codec.RPCv2CBOR.WriteError(w, r, aerr)
	case awsapi.ErrorProfileRPCV2JSON:
		codec.RPCv2JSON.WriteError(w, r, aerr)
	default:
		protocol.WriteJSONError(w, r, aerr)
	}
}

// queryOwner returns the dispatcher that explicitly owns an AWS Query request.
// API version wins over action name because actions can be shared by services.
//
// The second pass matches on the action name alone, and AWS reuses names freely
// across services. Elastic Load Balancing Classic (2012-06-01) and ELBv2
// (2015-12-01) share the whole vocabulary a load balancer needs —
// DescribeTags, DescribeLoadBalancers, CreateLoadBalancer,
// DescribeLoadBalancerAttributes — and Overcast implements only v2, so every
// Classic call fell into the action pass and was answered by ELBv2's handler:
// a 200 in the 2015-12-01 namespace, or a 400 naming an ELBv2 member the
// Classic caller never sent (#1884). Neither is an answer to the question that
// was asked, and both hide the 501 an unimplemented service owes it.
//
// So the models decide first. queryVersionService resolves (Version, Action) to
// exactly one service, which is the same fact each QueryVersionOwner states
// about itself, read from the one place both can share.
//
// The guard only ever withdraws a claim, never grants one. Where the models can
// attribute nothing — no Version at all, a version they do not carry, or a pair
// several modeled services declare — it names no service and the action pass
// decides exactly as it did before.
func queryOwner(operationRegistry *awsapi.Registry, dispatchers []QueryDispatcher, version, action string) (QueryDispatcher, bool) {
	for _, qd := range dispatchers {
		if owner, ok := qd.(QueryVersionOwner); ok && owner.OwnsVersion(version) {
			return qd, true
		}
	}
	modeled := queryVersionService(operationRegistry, version, action)
	for _, qd := range dispatchers {
		owner, ok := qd.(QueryActionOwner)
		if !ok || !owner.OwnsAction(action) {
			continue
		}
		if modeled != "" && !dispatcherIsService(qd, modeled) {
			continue
		}
		return qd, true
	}
	return nil, false
}

// queryVersionService names the Overcast service the pinned models attribute an
// AWS Query (Version, Action) pair to, or "" when they cannot attribute it —
// which Claim.Service already spells as the empty string for a pair that
// several modeled services declare.
func queryVersionService(operationRegistry *awsapi.Registry, version, action string) string {
	claim, ok := operationRegistry.ClaimQuery(version, action)
	if !ok {
		return ""
	}
	return claim.Service
}

// dispatcherIsService reports whether a Query dispatcher is the named service.
// A dispatcher that does not name itself gets the benefit of the doubt, because
// this answer is only ever used to take a claim away.
func dispatcherIsService(qd QueryDispatcher, service string) bool {
	named, ok := qd.(Service)
	if !ok {
		return true
	}
	return named.Name() == service
}

// v2APIsDispatch returns a handler that dispatches /v2/apis requests to either
// API Gateway v2 or AppSync Events API based on the SigV4 credential scope
// service name. If the credential scope indicates "appsync", the request is
// routed to the AppSync Events API handler; otherwise it falls back to the
// API Gateway v2 handler (the more commonly used service at this path).
func v2APIsDispatch(apigwRouter, appsyncRouter http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := middleware.ServiceFromCredential(r)
		if svc == "appsync" && appsyncRouter != nil {
			appsyncRouter.ServeHTTP(w, r)
			return
		}
		if apigwRouter != nil {
			apigwRouter.ServeHTTP(w, r)
			return
		}
		// Neither service enabled — 404.
		http.NotFound(w, r)
	}
}

// applicationsDispatch returns a handler that dispatches /applications
// requests to either AppConfig or Service Catalog AppRegistry, based on the
// SigV4 credential scope service name.
//
// Only an "appconfig" scope reaches AppConfig. Everything else — AppRegistry's
// own "servicecatalog" scope, an unparseable scope, and unsigned traffic such
// as the web UI's — goes to AppRegistry, which owned this path outright before
// #854 and must keep answering the callers it already had.
func applicationsDispatch(appconfigRouter, appregistryRouter http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if middleware.ServiceFromCredential(r) == "appconfig" && appconfigRouter != nil {
			appconfigRouter.ServeHTTP(w, r)
			return
		}
		if appregistryRouter != nil {
			appregistryRouter.ServeHTTP(w, r)
			return
		}
		// Neither service enabled — 404.
		http.NotFound(w, r)
	}
}

// delegateUnmatched makes a dispatched sub-router hand requests it does not
// serve back to the main router's REST fallback, instead of answering chi's
// own bare 404 or 405.
//
// A chi sub-router owns its whole subtree: a path it does not match hits *its*
// NotFound and never reaches the parent's "/*". That is why the modeled
// AppConfig operations Overcast does not implement answered a bodiless 404
// under AppRegistry's /applications rather than the generated registry's
// protocol-correct 501 (docs/plans/manifest-enforcement.md records the fault).
// MethodNotAllowed matters as much as NotFound: the model binds several
// unimplemented operations to a method on a path that *is* registered, and chi
// answers those 405.
func delegateUnmatched(sub chi.Router, fallback http.HandlerFunc) {
	sub.NotFound(fallback)
	sub.MethodNotAllowed(fallback)
}

// tagsDispatch returns a handler that dispatches tag-route requests
// (/v1/tags/{resourceArn}, /tags/{resourceArn}) to whichever service's tag
// router owns the resourceArn, as identified by protocol.ServiceFromARN.
// A resourceArn that doesn't parse, or whose service isn't one of the given
// routers, goes to fallback when one is provided — restFallback plays that
// role on /tags, so a signed caller addressing a service whose tag operations
// Overcast does not implement gets the generated 501 rather than a plausible
// answer from a service it never addressed (#976) — and otherwise gets a 404,
// so no service silently claims a request it doesn't recognize.
func tagsDispatch(routers map[string]http.Handler, fallback http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resourceArn := chi.URLParam(r, "*")
		// AWS SDKs URL-encode the ARN in the path (e.g. ":" as "%3A").
		if decoded, err := url.PathUnescape(resourceArn); err == nil {
			resourceArn = decoded
		}
		if router, ok := routers[protocol.ServiceFromARN(resourceArn)]; ok {
			router.ServeHTTP(w, r)
			return
		}
		if fallback != nil {
			fallback.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}
}
