package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/hostbridge/trust"
	"github.com/overcast-sh/overcast/internal/hostnamecheck"
	"github.com/overcast-sh/overcast/internal/inithooks"
	"github.com/overcast-sh/overcast/internal/router"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const defaultUIPort = 4567

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the AWS service emulator daemon",
		Long: `Start the emulator daemon and serve AWS API requests on the configured port.

All configuration is via environment variables. See https://overcast.sh/docs/configuration/reference.

Examples:
  overcast serve
  OVERCAST_STATE=memory overcast serve
  OVERCAST_LISTEN=127.0.0.1 overcast serve`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			uiPort, _ := cmd.Flags().GetInt("ui-port")
			bridgeEnabled, _ := cmd.Flags().GetBool("bridge")
			bridgeBindIP, _ := cmd.Flags().GetString("bridge-bind-ip")
			return runServe(uiPort, bridgeEnabled, bridgeBindIP)
		},
	}
	cmd.Flags().Int("ui-port", defaultUIPort, "port for the web UI (0 = disable; env: OVERCAST_UI_PORT)")
	cmd.Flags().Bool("bridge", false, "also run the mDNS bridge and port-80 reverse proxy (see: overcast bridge --help)")
	cmd.Flags().String("bridge-bind-ip", "127.0.0.1", "IP to advertise in mDNS when --bridge is set")
	return cmd
}

func runServe(uiPortFlag int, bridgeEnabled bool, bridgeBindIPStr string) error {
	profileStartup := os.Getenv("OVERCAST_PROFILE_STARTUP") == "1"
	prof := newPhaseTimer(profileStartup)
	prof.mark("Go runtime + package init")

	// ---- Config ------------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.Version = version
	prof.mark("config.Load")

	// ---- Logger ------------------------------------------------------------
	// Use the human-friendly development encoder when stdout is a terminal so
	// that coloured [SERVICE] tags and readable timestamps are shown. In
	// non-interactive environments (CI, Docker, pipe) use the JSON production
	// encoder so structured fields are machine-parseable. Either way, the
	// zap.Config's Level is set explicitly from cfg.LogLevel (via
	// serviceutil.ParseLevel, which understands Overcast's "trace" level in
	// addition to zap's built-ins) rather than relying on the dev/prod
	// config's own default level — NewDevelopmentConfig defaults to DEBUG and
	// NewProductionConfig to INFO, neither of which honours e.g.
	// OVERCAST_LOG_LEVEL=warn or =trace.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	// An invalid OVERCAST_LOG_LEVEL must not prevent the emulator from
	// starting (an observability typo shouldn't kill a CI pipeline) — fall
	// back to the default and say so, naming the level actually in effect,
	// once the logger exists below.
	logLevel, logLevelOK := serviceutil.ParseLevelOrDefault(cfg.LogLevel)
	var zcfg zap.Config
	if isTTY || cfg.LogLevel == "debug" || cfg.LogLevel == "trace" {
		zcfg = zap.NewDevelopmentConfig()
		zcfg.EncoderConfig.EncodeLevel = serviceutil.WrapLevelEncoder(zcfg.EncoderConfig.EncodeLevel, "TRACE")
	} else {
		zcfg = zap.NewProductionConfig()
		zcfg.EncoderConfig.EncodeLevel = serviceutil.WrapLevelEncoder(zcfg.EncoderConfig.EncodeLevel, "trace")
	}
	zcfg.Level = zap.NewAtomicLevelAt(logLevel)
	logger, err := zcfg.Build()
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync()
	if !logLevelOK {
		logger.Warn("invalid OVERCAST_LOG_LEVEL — falling back to default",
			zap.String("configured", cfg.LogLevel),
			zap.String("effective", "info"),
			zap.String("valid_values", "trace, debug, info, warn, error"))
	}
	prof.mark("logger init")

	if cfg.SigV4Validate {
		logger.Info("SigV4 signature validation enabled — requests with invalid signatures are rejected with 403 InvalidSignatureException; secrets resolve through IAM user access keys and STS session credentials, falling back to \"test\"")
	}
	if cfg.Debug {
		logger.Warn("debug endpoints enabled (/_overcast/debug/*) — do not expose this port publicly")
	}
	if cfg.StateSource == config.StateSourceAuto {
		logger.Info(
			fmt.Sprintf("storage mode auto-detected: %s (%s) — set OVERCAST_STATE to override", cfg.State, cfg.StateAutoReason),
			zap.String("state", string(cfg.State)),
			zap.String("stateAutoSignal", cfg.StateAutoSignal),
		)
	}
	// Environment preflight: memory mode is fine on its own (the line above
	// already says so when auto-detected) — it only becomes a trap when there
	// is a database already sitting in the data directory that this run is
	// about to silently ignore. See preflight_ephemeral.go.
	warnIfExistingDatabaseIgnored(cfg, logger)
	logListenResolution(logger, cfg)
	logHostnameAlias(logger, cfg)
	logLocalStackAliases(logger, cfg)

	// Whether OVERCAST_HOSTNAME's subdomains resolve on this host is a
	// property of the host's resolver, not of Overcast, and it breaks
	// virtual-hosted-style S3 and `cdk deploy` asset publishing in a way that
	// surfaces far from the cause (a bare ENOTFOUND on a bucket-shaped
	// hostname). Probed here, off the critical path: it is two DNS lookups,
	// bounded but still I/O, and must not add to boot latency (see the #252
	// startup-metrics work) or block startup on a wedged resolver — the
	// goroutine logs its own Warn once it has an answer, whenever that is.
	go hostnamecheck.Run(context.Background(), cfg.ExternalHostname(), cfg.SplitHorizonHosts, logger)

	// ---- Init hooks -----------------------------------------------------------
	var hookRunner *inithooks.Runner
	if cfg.InitEnabled {
		hookRunner = inithooks.NewRunner(cfg.InitDirs, buildHookEnv(cfg), cfg.InitTimeout, logger)
		hookRunner.Discover()
		prof.mark("hookRunner.Discover")

		// START hooks run before store/router init — useful for pre-flight setup.
		hookRunner.Run(context.Background(), inithooks.StageStart)
		prof.mark("hookRunner.Run(START)")
	}

	// ---- State backend -----------------------------------------------------
	store, err := buildStore(cfg, cfg.State, cfg.DataDir, logger)
	if err != nil {
		return fmt.Errorf("init state backend: %w", err)
	}
	// closeStore performs a bounded, logged Close() on the final value of
	// `store` (which may be replaced by a NamespacedStore below — the closure
	// reads the variable, not a snapshot, so it always sees the final value).
	//
	// It is invoked explicitly at the end of the graceful shutdown sequence,
	// after cleanup() has run, so that both signal-triggered and error-triggered
	// shutdowns close the store the same bounded way. The deferred call here is
	// only a safety net for early-return paths above that point in the function
	// (e.g. a per-service buildStore failure, or the listener failing to bind) —
	// sync.Once makes the deferred call a no-op once the explicit call has
	// already run.
	var closeStoreOnce sync.Once
	closeStore := func() {
		closeStoreOnce.Do(func() {
			closeStoreBounded(store, cfg.ShutdownTimeout, logger)
		})
	}
	defer closeStore()

	logStoreMode(logger, cfg)
	logSweepDomain(logger, cfg)
	prof.mark("buildStore")

	// Per-service overrides: build a NamespacedStore when any service requests
	// a different mode from the global default.
	if len(cfg.ServiceStates) > 0 {
		newStore, err := buildServiceRoutes(cfg, store, logger)
		if err != nil {
			return err
		}
		// Replace store with the (possibly wrapping) result; NamespacedStore.Close()
		// handles closing all underlying stores, so we swap the deferred Close target.
		store = newStore
		prof.mark("buildStore: per-svc overrides")
	}

	// Recover the host port our API is published on before building the router:
	// both the web UI and the container-endpoint mapper need it, and a container
	// cannot see its own port mapping from the inside.
	resolvePublishedPort(cfg, logger)
	prof.mark("resolvePublishedPort")

	// ---- TLS material --------------------------------------------------------
	// Resolved before the servers are built because both the API and the web
	// UI listeners share it — serving the UI over plain HTTP while the API is
	// TLS would leave the SPA unable to call the API (mixed content). With TLS
	// on, browsers negotiate HTTP/2 via ALPN and multiplex the UI's SSE
	// stream, invoke progress streams, and polling over one connection instead
	// of fighting over the 6-socket HTTP/1.1 limit.
	tlsServerConf, tlsTrustPEM, err := serverTLSConfig(cfg, logger)
	if err != nil {
		return err
	}
	// Containerized daemons mint their CA where the host cannot read it —
	// point the operator at the one command that trusts it from outside
	// (fetches the CA certificate via /_overcast/ca.pem, no shared volume
	// needed). Once, at startup; browserAPIPort is the port a host-side
	// caller actually dials when the API port is remapped.
	if cfg.TLSAuto() && containerendpoint.RunningInContainer() {
		// https, not http: this branch only runs with TLS already on, and a
		// plain-HTTP dial at the TLS port is answered with a handshake error
		// ("client sent an HTTP request to an HTTPS server") that the operator
		// then has to reason about. The fetch survives the wrong spelling —
		// trust.FetchRemoteCA retries https — but the log line is the thing
		// people copy, so it has to name the scheme the listener speaks.
		logger.Info("running in a container with TLS — trust this daemon's CA from the host with one command",
			zap.String("command", fmt.Sprintf("overcast https enable --endpoint https://localhost:%d", browserAPIPort(cfg))),
		)
	}
	prof.mark("serverTLSConfig")

	// ---- HTTP server -------------------------------------------------------
	handler, preShutdown, cleanup, _ := router.New(cfg, store, logger, clock.New(), hookRunner)
	prof.mark("router.New (full)")

	srv := &http.Server{
		Handler:   handler,
		TLSConfig: tlsServerConf,
		// IdleTimeout governs how long a keep-alive connection may sit idle
		// between requests. Without this, every AWS SDK connection (which uses
		// HTTP keep-alive by default) holds a goroutine open indefinitely —
		// after a compat/integration test run this can accumulate hundreds of
		// goroutines. 60 s matches the AWS service default and is safe for all
		// SDK clients, which retry on connection reset.
		IdleTimeout: 60 * time.Second,
	}
	// When TLS is enabled the standard library negotiates HTTP/2 via ALPN and
	// the default protocol set is the right one. A plain-text listener has to
	// opt in to cleartext HTTP/2 (h2c), which clients use with prior
	// knowledge — the AWS SDKs and curl --http2-prior-knowledge. This is the
	// standard library's replacement for the x/net/http2/h2c wrapper that used
	// to sit here; that package is deprecated in favour of this field. The one
	// thing it does not carry over is the `Upgrade: h2c` handshake, which a
	// client that tries it simply does not get — it continues on HTTP/1.1.
	if !cfg.TLSEnabled() {
		srv.Protocols = new(http.Protocols)
		srv.Protocols.SetHTTP1(true)
		srv.Protocols.SetUnencryptedHTTP2(true)
	}

	// Bind the listeners explicitly so we know the ports are ready before
	// running READY hooks and before entering the select loop. OVERCAST_LISTEN
	// usually names one address; when it names several they all front the same
	// http.Server, so a request is answered identically whichever it arrives
	// on. logListenResolution above names which addresses these actually bound
	// and, when defaulted, why (#761: containerised binds every interface —
	// required for Docker's -p publishing — a native run narrows to loopback).
	lns, err := listenAll(cfg.Addrs())
	if err != nil {
		return err
	}
	ln := lns[0]

	// ---- Web UI server -----------------------------------------------------
	// Resolve the UI port: env var overrides flag; 0 disables the UI server.
	// When the default port (4567) is taken we fall back to an ephemeral port
	// so the emulator still starts. An explicit non-zero port fails hard.
	uiPort := uiPortFlag
	if ep := os.Getenv("OVERCAST_UI_PORT"); ep != "" {
		fmt.Sscanf(ep, "%d", &uiPort)
	}
	var uiLn net.Listener
	if uiPort != 0 {
		uiHandler, err := newUIHandler(cfg.Port, browserAPIPort(cfg), cfg.Region, cfg.Debug, cfg.TLSEnabled(), tlsTrustPEM)
		if err != nil {
			logger.Warn("web UI unavailable", zap.Error(err))
		} else {
			// Bound on cfg.Host, not on the wildcard: OVERCAST_LISTEN=127.0.0.1
			// is how an unauthenticated emulator is kept off the network the
			// machine is attached to, and a UI that ignored it left the state
			// browser — and its write actions — reachable from that network
			// anyway. Only the first address: the extra ones exist so
			// container-side clients can reach the API, and the UI is a
			// browser surface on this machine.
			uiAddr := net.JoinHostPort(cfg.Host, strconv.Itoa(uiPort))
			uiLn, err = net.Listen("tcp", uiAddr)
			if err != nil {
				if uiPort == defaultUIPort {
					logger.Warn("web UI default port busy; selecting an ephemeral UI port",
						zap.Int("requested_port", defaultUIPort),
						zap.Error(err),
					)
					// Default port busy — pick any free port.
					uiLn, err = net.Listen("tcp", net.JoinHostPort(cfg.Host, "0"))
					if err != nil {
						logger.Warn("web UI listener failed", zap.Error(err))
					} else {
						logger.Info("web UI fallback port selected",
							zap.String("addr", uiLn.Addr().String()),
						)
					}
				} else {
					return fmt.Errorf("listen (ui) %s: %w", uiAddr, err)
				}
			}
			if uiLn != nil {
				// The UI listener shares the API's TLS material: one mode
				// governs both, so the SPA's https origin can call the https
				// API, and browsers get HTTP/2 (via ALPN) for the UI's SSE and
				// progress streams instead of the 6-connection HTTP/1.1 cap.
				//
				// Shared material, not a shared *tls.Config. ServeTLS calls
				// http2ConfigureServer, which appends "h2"/"http/1.1" to
				// srv.TLSConfig.NextProtos in place — so handing the same
				// pointer to two http.Servers that each ServeTLS from their own
				// goroutine is a data race on the ALPN list both listeners
				// advertise (`go test -race` reports it on this exact shape).
				// A clone per server costs nothing and removes it.
				uiSrv := &http.Server{Handler: uiHandler, TLSConfig: cloneServerTLSConfig(tlsServerConf), IdleTimeout: 60 * time.Second}
				go func() {
					logger.Info("web UI listening",
						zap.String("addr", uiLn.Addr().String()),
						zap.Bool("tls", tlsServerConf != nil),
					)
					var uiErr error
					if tlsServerConf != nil {
						uiErr = uiSrv.ServeTLS(uiLn, "", "")
					} else {
						uiErr = uiSrv.Serve(uiLn)
					}
					if uiErr != nil && uiErr != http.ErrServerClosed {
						logger.Warn("web UI server error", zap.Error(uiErr))
					}
				}()
				defer uiSrv.Shutdown(context.Background()) //nolint:errcheck
			}
		}
	}

	// ---- Bridge (optional) ------------------------------------------------
	if bridgeEnabled && cfg.TLSEnabled() {
		// The bridge's port-80 reverse proxy speaks plain HTTP to its
		// backends; pointing it at TLS listeners would fail every request.
		logger.Warn("--bridge is not supported while TLS is enabled — skipping the mDNS bridge and port-80 proxy")
		bridgeEnabled = false
	}
	if bridgeEnabled {
		bindIP := net.ParseIP(bridgeBindIPStr)
		if bindIP == nil {
			return fmt.Errorf("invalid --bridge-bind-ip %q", bridgeBindIPStr)
		}
		apiAddr := fmt.Sprintf("http://%s", ln.Addr())
		uiAddr := fmt.Sprintf("http://localhost:%d", defaultUIPort)
		if uiLn != nil {
			uiAddr = fmt.Sprintf("http://%s", uiLn.Addr())
		}
		bridgeCtx, cancelBridge := context.WithCancel(context.Background())
		stopBridge, err := startBridge(bridgeCtx, apiAddr, bindIP, apiAddr, uiAddr, 80, logger)
		if err != nil {
			cancelBridge()
			logger.Warn("bridge unavailable", zap.Error(err))
		} else {
			defer func() {
				stopBridge()
				cancelBridge()
			}()
		}
	}

	// Signals for graceful shutdown (Ctrl+C or SIGTERM from Docker/Kubernetes).
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	proto := "h2c"
	if cfg.TLSEnabled() {
		proto = "https" // HTTP/2 over TLS via ALPN
	}
	// One send per listener: the select below takes the first error, and a
	// buffer this size means the others never block on a channel nobody is
	// reading once shutdown has started.
	serverErr := make(chan error, len(lns))

	// Announced before a single goroutine starts serving, and that ordering is
	// the contract rather than a detail.
	//
	// The LocalStack Testcontainers modules block on the "Ready." line and then
	// connect, and other callers poll /_overcast/health instead — so the two
	// have to agree, and the only way they can is for the marker to be out
	// before anything can answer a request. Emitting it after the serve
	// goroutines start makes it a race that the marker loses whenever startup
	// logging is busy: health returns ok, a caller greps the logs, and the line
	// arrives immediately afterwards. Every listener is already bound by
	// listenAll, so the marker is true here — a client that connects on the
	// strength of it is accepted into the socket's backlog and served the
	// moment Serve picks it up, which is the next scheduling slot.
	//
	// The per-listener lines go first for the same reason they always did:
	// written on this goroutine, so they cannot land after the marker.
	for _, listener := range lns {
		logger.Info("overcast listening",
			zap.String("addr", listener.Addr().String()),
			zap.String("protocol", proto),
			zap.String("state", string(cfg.State)),
			zap.Bool("debug", cfg.Debug),
		)
	}
	emitReady(logger, os.Stderr, lns)

	for _, listener := range lns {
		go func() {
			// Kept local rather than assigned to the enclosing err, which the
			// pre-list single-listener version did: with the main goroutine
			// still reading err below that was a race even with one listener,
			// and it would be one per listener now.
			var serveErr error
			if tlsServerConf != nil {
				// Certificates already live in srv.TLSConfig — for both the
				// explicit cert/key pair and the auto-minted leaf.
				serveErr = srv.ServeTLS(listener, "", "")
			} else {
				serveErr = srv.Serve(listener)
			}
			if serveErr != nil && serveErr != http.ErrServerClosed {
				serverErr <- serveErr
			}
		}()
	}

	// READY hooks run asynchronously after the port is bound so the server
	// can accept requests while init scripts execute (matches LocalStack).
	if hookRunner != nil {
		go hookRunner.Run(context.Background(), inithooks.StageReady)
	}

	// Both branches below converge on the same shutdown tail: preShutdown,
	// SHUTDOWN hooks, HTTP drain, cleanup, and the bounded store close. A
	// server-serve error must still surface as a non-nil return, but it must
	// not skip cleanup — cleanup() flushes service-level buffered state (e.g.
	// CloudWatch Logs' debounced write cache via Stop) that would otherwise be
	// lost if the daemon failed shortly after a partial start.
	var serveErr error
	select {
	case err := <-serverErr:
		serveErr = err
		logger.Error("server error; running shutdown sequence before exiting", zap.Error(err))
	case sig := <-quit:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	shutdownStart := time.Now()

	// Unblock long-lived handlers (SSE) before asking the server to drain.
	preShutdown()
	logger.Info("pre-shutdown complete", zap.Duration("elapsed", time.Since(shutdownStart)))

	// SHUTDOWN hooks run after SSE is unblocked but before HTTP drain.
	if hookRunner != nil {
		shutdownHookCtx, shutdownHookCancel := context.WithTimeout(context.Background(), cfg.InitTimeout*10)
		hookRunner.Run(shutdownHookCtx, inithooks.StageShutdown)
		shutdownHookCancel()
	}

	// Give in-flight HTTP requests a short grace period to finish. After
	// preShutdown() all long-lived handlers (SSE) have already returned, so
	// only short-lived requests remain. 2 s is generous for a local dev tool.
	const drainGrace = 2 * time.Second
	drainCtx, drainCancel := context.WithTimeout(context.Background(), drainGrace)
	defer drainCancel()

	if err := srv.Shutdown(drainCtx); err != nil {
		logger.Warn("http drain grace exceeded, force-closing connections", zap.Error(err))
		srv.Close()
	}
	logger.Info("http server drained", zap.Duration("elapsed", time.Since(shutdownStart)))

	// Always run cleanup — even when srv.Shutdown times out — so background
	// resources (SMTP listener, Lambda Runtime API) are released promptly.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cleanupCancel()
	cleanup(cleanupCtx)
	logger.Info("cleanup complete", zap.Duration("elapsed", time.Since(shutdownStart)))

	// Explicit, bounded close on the normal shutdown path — see closeStore's
	// definition above, near where `store` is created. The deferred call
	// registered there is a sync.Once no-op by the time it runs, since it
	// already ran here.
	closeStore()
	logger.Info("store close sequence complete", zap.Duration("elapsed", time.Since(shutdownStart)))

	if serveErr != nil {
		return fmt.Errorf("server error: %w", serveErr)
	}
	logger.Info("server stopped cleanly")
	return nil
}

// listenAll binds every address in order and returns the listeners, first
// first. A failure closes whatever it already opened before returning, so a
// partial bind never leaves the daemon half-listening on an address it is
// about to abandon — the caller returns the error and exits, and a port left
// held would fail the next start for a reason that no longer exists.
//
// The error names the address that failed. With one address that reads the
// same as it always did; with several it is the only way to tell which of them
// is the problem.
func listenAll(addrs []string) ([]net.Listener, error) {
	lns := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, open := range lns {
				_ = open.Close()
			}
			return nil, fmt.Errorf("listen %s: %w", addr, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

// closeStoreBounded closes store with a time budget so shutdown can never
// hang indefinitely on it. HybridStore.Close (and similarly-shaped persistent
// backends) perform a full synchronous flush of all dirty entries to SQLite
// before returning; on a slow disk — notably a bind-mounted /data volume
// under Docker Desktop — that flush can run long enough to exceed `docker
// stop`'s default ~10s grace period, which SIGKILLs the process mid-flush.
// That's survivable: SQLite writes are transactional and the hybrid store's
// pending log replays anything that didn't make it into SQLite on the next
// start. But an unbounded wait here would defeat the point of a "graceful"
// shutdown, so the wait is capped: on timeout we log loudly and let the
// process exit anyway rather than block indefinitely (or get SIGKILLed with
// no record of why).
func closeStoreBounded(store state.Store, timeout time.Duration, logger *zap.Logger) {
	pendingBefore := -1
	if health, ok := state.PersistentHealthSnapshot(store); ok {
		pendingBefore = health.PendingWrites
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- store.Close() }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err != nil {
			logger.Warn("store close returned error", zap.Error(err), zap.Duration("elapsed", elapsed))
			return
		}
		fields := []zap.Field{zap.Duration("elapsed", elapsed)}
		if pendingBefore >= 0 {
			fields = append(fields, zap.Int("pending_writes_at_close", pendingBefore))
		}
		logger.Info("store closed cleanly", fields...)
	case <-time.After(timeout):
		logger.Warn("store close exceeded shutdown timeout; process exiting with the close still in flight — pending writes will replay from the pending log on next start",
			zap.Duration("timeout", timeout),
			zap.Int("pending_writes_at_timeout", pendingBefore),
		)
	}
}

// buildServiceRoutes builds the per-service storage overrides described by
// cfg.ServiceStates and wraps defaultStore in a state.NamespacedStore when at
// least one service ends up needing a storage mode different from the global
// default (cfg.State). Services whose override mode matches cfg.State are
// skipped — there's no need for a separate store when it would behave
// identically to the default.
//
// When there are no overrides left after that skip (including the
// len(cfg.ServiceStates) == 0 case), defaultStore is returned unchanged so
// callers never need to special-case "no overrides configured".
//
// Namespaces route on storage-namespace prefix (state.NamespacedStore.storeFor
// matches the segment before ":", e.g. "cfn" in "cfn:stacks"), which is NOT
// always the same as the config service name used in cfg.ServiceStates — see
// config.ServiceNamespacePrefix for the handful of services (cloudformation,
// apigateway, eventbridge) where they differ. The route map returned here is
// therefore keyed by the resolved prefix, not by the raw config service name;
// keying by the config name would silently make the override for those three
// services a no-op, since NamespacedStore would never match their real
// namespace prefix. The on-disk sub-directory for each override's data
// (svcDir) is deliberately kept keyed by the config service name instead,
// since that's the human-readable name operators expect to find on disk.
//
// On a per-service buildStore failure, every store already built earlier in
// this call is closed before the error is returned, so a partial failure
// never leaks stores that were successfully constructed for other services.
//
// A handful of services accept an OVERCAST_STATE_<SERVICE> override that can
// never take effect regardless of prefix routing — see
// config.ServiceOverrideIneffective for the full list and why (e.g.
// dynamodbstreams is a store-less facade over dynamodb; sts writes under the
// "iam:sessions" namespace). The override store is still built and routed
// for these services (harmless, and keeps behavior uniform), but a Warn is
// logged so the no-op isn't silently mistaken for a working override.
func buildServiceRoutes(cfg *config.Config, defaultStore state.Store, logger *zap.Logger) (state.Store, error) {
	if len(cfg.ServiceStates) == 0 {
		return defaultStore, nil
	}

	routes := make(map[string]state.Store, len(cfg.ServiceStates))
	var perSvcStores []state.Store // tracked for cleanup on error

	for svc, mode := range cfg.ServiceStates {
		if mode == cfg.State {
			continue // same mode as global default — no need to create a separate store
		}
		// Each service gets its own sub-directory, keyed by the human-readable
		// config service name, so db files don't collide and stay easy to
		// find on disk.
		svcDir := filepath.Join(cfg.DataDir, svc)
		svcStore, err := buildStore(cfg, mode, svcDir, logger.With(zap.String("service_state", svc)))
		if err != nil {
			for _, s := range perSvcStores {
				s.Close()
			}
			return nil, fmt.Errorf("init state backend for service %s: %w", svc, err)
		}

		prefix := config.ServiceNamespacePrefix(svc)
		routes[prefix] = svcStore
		perSvcStores = append(perSvcStores, svcStore)

		logFields := []zap.Field{
			zap.String("service", svc),
			zap.String("mode", string(mode)),
		}
		if prefix != svc {
			logFields = append(logFields, zap.String("namespace_prefix", prefix))
		}
		logger.Info("service state override", logFields...)

		if reason, ineffective := config.ServiceOverrideIneffective(svc); ineffective {
			logger.Warn("service state override has no effect",
				zap.String("service", svc),
				zap.String("reason", reason),
			)
		}
	}

	if len(routes) == 0 {
		return defaultStore, nil
	}
	// NamespacedStore.Close() handles closing all underlying stores
	// (including defaultStore), so the caller only needs to track this
	// single returned value going forward.
	return state.NewNamespacedStore(defaultStore, routes), nil
}

// buildStore creates a Store for the given mode and dataDir.
// The caller is responsible for calling Close() on the returned store.
func buildStore(cfg *config.Config, mode config.StateBackend, dataDir string, logger *zap.Logger) (state.Store, error) {
	switch mode {
	case config.StateBackendPersistent:
		s, err := state.NewSQLiteStoreWithLogger(dataDir, logger)
		if err != nil {
			return nil, fmt.Errorf("persistent store: %w", err)
		}
		return s, nil
	case config.StateBackendWAL:
		s, err := state.NewWALStore(dataDir, state.WALOptions{
			SyncMode:     state.WALSyncMode(cfg.WALFsyncMode),
			SyncInterval: cfg.WALFsyncInterval,
			MaxLogBytes:  cfg.WALMaxLogBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("wal store: %w", err)
		}
		return s, nil
	case config.StateBackendHybrid:
		s, err := state.NewHybridStoreWithOptions(dataDir, state.HybridOptions{
			FlushInterval:       cfg.HybridFlushInterval,
			SyncMode:            state.WALSyncMode(cfg.HybridSyncMode),
			SyncInterval:        cfg.HybridSyncInterval,
			DirtyEntryThreshold: cfg.HybridDirtyEntryThreshold,
			DirtyByteThreshold:  cfg.HybridDirtyByteThreshold,
			MaintenanceInterval: cfg.HybridMaintenanceInterval,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("hybrid store: %w", err)
		}
		return s, nil
	default: // memory
		return state.NewMemoryStore(), nil
	}
}

// logSweepDomain logs what this instance's sweep-domain identity is anchored
// to: the data directory's volume, bind or host path, from which every Docker
// resource's overcast.instance label is derived (serviceutil.DataDirAnchor).
// One line at startup, because it is the first thing to check when an
// instance fails to reclaim its own networks after a wipe, or two instances
// turn out to be reclaiming each other's — and nothing else says it.
func logSweepDomain(logger *zap.Logger, cfg *config.Config) {
	anchor := serviceutil.DataDirAnchor(cfg.DataDir)
	if anchor.ID == "" {
		logger.Warn("sweep domain: no durable identity can be derived for the data directory — "+
			"Docker resources will be labelled with an identity minted per state store, "+
			"which a wiped or memory-backed store does not carry forward",
			zap.String("data_dir", cfg.DataDir))
		return
	}
	logger.Info("sweep domain: Docker resources are labelled for this data directory",
		zap.String("instance", anchor.ID), zap.String("anchor", anchor.Source))
}

// logStoreMode logs a structured INFO event describing the active storage backend.
func logStoreMode(logger *zap.Logger, cfg *config.Config) {
	switch cfg.State {
	case config.StateBackendPersistent:
		logger.Info("state backend: persistent (SQLite, synchronous writes)",
			zap.String("path", cfg.DataDir))
	case config.StateBackendWAL:
		logger.Info("state backend: wal (memory reads, append-log durability)",
			zap.String("path", cfg.DataDir),
			zap.String("wal_fsync", cfg.WALFsyncMode),
			zap.Duration("wal_fsync_interval", cfg.WALFsyncInterval),
			zap.Int64("wal_max_log_bytes", cfg.WALMaxLogBytes))
	case config.StateBackendHybrid:
		logger.Info("state backend: hybrid (memory reads, async SQLite flush)",
			zap.String("path", cfg.DataDir),
			zap.Duration("flush_interval", cfg.HybridFlushInterval),
			zap.String("hybrid_sync", cfg.HybridSyncMode),
			zap.Duration("hybrid_sync_interval", cfg.HybridSyncInterval),
			zap.Int("hybrid_dirty_entry_threshold", cfg.HybridDirtyEntryThreshold),
			zap.Int64("hybrid_dirty_byte_threshold", cfg.HybridDirtyByteThreshold),
			zap.Duration("hybrid_maintenance_interval", cfg.HybridMaintenanceInterval))
	default:
		logger.Info("state backend: memory (data will not persist across restarts)")
	}
}

// logListenResolution names the resolved bind address(es) at startup and
// that OVERCAST_LISTEN changes it — mirroring how logStoreMode above reports
// the storage mode it resolved to. Unlike logStoreMode this always logs,
// whether or not the value was defaulted: unlike storage mode, there is no
// "the interesting case is auto-detection" — an operator who set the bind
// address explicitly still benefits from a single line confirming what
// actually got bound.
//
// When the value was defaulted (cfg.ListenSource == ListenSourceAuto), the
// line also says *why* — containerised or native (#761) — since that default
// is now environment-dependent rather than a single constant, so "what did it
// bind, and why" stops being answerable by reading the env alone.
func logListenResolution(logger *zap.Logger, cfg *config.Config) {
	addrs := strings.Join(cfg.Addrs(), ", ")
	if cfg.ListenSource == config.ListenSourceAuto {
		logger.Info(
			fmt.Sprintf("listening on %s (default: %s) — set OVERCAST_LISTEN to change this", addrs, cfg.ListenAutoReason),
			zap.Strings("addrs", cfg.Hosts),
			zap.String("listenAutoSignal", cfg.ListenAutoSignal),
		)
		return
	}
	logger.Info(
		fmt.Sprintf("listening on %s — set OVERCAST_LISTEN to change this", addrs),
		zap.Strings("addrs", cfg.Hosts),
	)
}

// logHostnameAlias names, once at startup, which LocalStack-compatibility
// environment variable (currently only LOCALSTACK_HOST, #1190) supplied or
// confirmed OVERCAST_HOSTNAME, so an operator migrating a LocalStack compose
// file line-by-line can see their setting was recognised rather than
// silently ignored. Logs nothing when cfg.HostnameAliasSource is empty —
// which covers both "no alias variable was set" and "OVERCAST_HOSTNAME alone
// was set" — mirroring logListenResolution's "only say something when there
// is something to say" shape for the auto-detected case.
func logHostnameAlias(logger *zap.Logger, cfg *config.Config) {
	if cfg.HostnameAliasSource == "" {
		return
	}
	logger.Info(
		fmt.Sprintf(
			"%s recognised as a LocalStack-compatibility alias for OVERCAST_HOSTNAME=%s",
			cfg.HostnameAliasSource, cfg.Hostname,
		),
		zap.String("hostname", cfg.Hostname),
		zap.String("hostnameAliasSource", cfg.HostnameAliasSource),
	)
}

// logLocalStackAliases names, once at startup per variable, every other
// LocalStack-compatibility alias (#1190, config.Config.LocalStackAliasesUsed)
// that supplied or confirmed an Overcast setting, and every
// LocalStack-documented variable Overcast recognises but ignores
// (config.Config.IgnoredLocalStackVars) — so an operator migrating a
// LocalStack compose file sees every one of their settings was seen, whether
// it took effect or was deliberately inert. Hostname's own alias
// (LOCALSTACK_HOST/HOSTNAME_EXTERNAL) is reported separately by
// logHostnameAlias, which predates this and already has its own line.
//
// Keys are sorted before logging so the line order is deterministic across
// runs (map iteration order is not) — this also keeps test assertions
// stable.
func logLocalStackAliases(logger *zap.Logger, cfg *config.Config) {
	overcastVars := make([]string, 0, len(cfg.LocalStackAliasesUsed))
	for overcastVar := range cfg.LocalStackAliasesUsed {
		overcastVars = append(overcastVars, overcastVar)
	}
	sort.Strings(overcastVars)
	for _, overcastVar := range overcastVars {
		source := cfg.LocalStackAliasesUsed[overcastVar]
		logger.Info(
			fmt.Sprintf("%s recognised as a LocalStack-compatibility alias for %s", source, overcastVar),
			zap.String("overcastVar", overcastVar),
			zap.String("localStackAliasSource", source),
		)
	}

	if cfg.LocalStackVolumeDataDir != "" {
		logger.Info(
			fmt.Sprintf("state directory taken from the volume mounted at %s — LocalStack's volume path, "+
				"and the only one mounted; set OVERCAST_DATA_DIR (or mount at /data) to choose another",
				cfg.LocalStackVolumeDataDir),
			zap.String("dataDir", cfg.LocalStackVolumeDataDir),
		)
	}

	if cfg.DockerSocketSource != "" {
		logger.Info(
			fmt.Sprintf("Docker endpoint taken from %s: %s — set LAMBDA_DOCKER_SOCKET to override",
				cfg.DockerSocketSource, cfg.LambdaDockerSocket),
			zap.String("dockerSocket", cfg.LambdaDockerSocket),
			zap.String("dockerSocketSource", cfg.DockerSocketSource),
		)
	}
	if cfg.DockerHostUnsupported != "" {
		logger.Warn(
			fmt.Sprintf("DOCKER_HOST=%q names a transport Overcast cannot dial (unix://, tcp://, npipe:// "+
				"and http:// are the supported forms) — falling back to %s, which is a different daemon "+
				"than the one you configured, or none at all; set LAMBDA_DOCKER_SOCKET to a reachable endpoint",
				cfg.DockerHostUnsupported, cfg.LambdaDockerSocket),
			zap.String("dockerHost", cfg.DockerHostUnsupported),
			zap.String("dockerSocket", cfg.LambdaDockerSocket),
		)
	}

	ignored := append([]string(nil), cfg.IgnoredLocalStackVars...)
	sort.Strings(ignored)
	for _, name := range ignored {
		logger.Info(
			fmt.Sprintf("%s is a recognised LocalStack setting with no Overcast effect: %s", name, config.IgnoredLocalStackReason(name)),
			zap.String("ignoredLocalStackVar", name),
		)
	}
}

// buildHookEnv returns the environment variables passed to init hook scripts.
func buildHookEnv(cfg *config.Config) []string {
	scheme := "http"
	if cfg.TLSEnabled() {
		scheme = "https"
	}
	env := []string{
		fmt.Sprintf("AWS_ENDPOINT_URL=%s://localhost:%d", scheme, cfg.Port),
		"AWS_DEFAULT_REGION=" + cfg.Region,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"LOCALSTACK_HOSTNAME=localhost",
		fmt.Sprintf("EDGE_PORT=%d", cfg.Port),
	}
	// With TLS on, point the AWS CLI / boto3 in hook scripts at the trust
	// root so their https calls verify: the local CA in auto mode, the
	// operator's own certificate chain in explicit mode.
	if cfg.TLSAuto() {
		env = append(env, "AWS_CA_BUNDLE="+filepath.Join(cfg.CACertDir(), trust.CACertFile))
	} else if cfg.TLSEnabled() {
		env = append(env, "AWS_CA_BUNDLE="+cfg.TLSCertFile)
	}
	return env
}

// serverTLSConfig resolves the TLS material both listeners (API and web UI)
// serve with, plus the PEM roots the BFF must trust to dial the API. Returns
// (nil, nil, nil) when TLS is off.
//
// Auto mode (OVERCAST_TLS=auto) mints — or reuses, see
// trust.ServerCertificate — a leaf signed by the local overcast CA under
// <data dir>/ca, covering every name Overcast advertises that can be
// enumerated (cfg.TLSAutoSANs), and keeps the CA open to mint for the ones
// that cannot: a host-routed name has a variable middle
// ("{id}.execute-api.{region}.<base>") that no single-label wildcard covers,
// so trust.CertSource mints per SNI under cfg.TLSWildcardBases.
//
// Explicit mode loads the configured cert/key pair and hands the BFF the
// certificate file's PEM (self-signed certs and bundled chains both verify
// that way; a chain-less private-CA leaf will not — ship the chain in the
// cert file).
func serverTLSConfig(cfg *config.Config, logger *zap.Logger) (*tls.Config, []byte, error) {
	switch {
	case cfg.TLSAuto():
		caDir := cfg.CACertDir()
		certs, _, err := trust.NewCertSource(caDir, cfg.TLSAutoSANs(), cfg.TLSWildcardBases())
		if err != nil {
			return nil, nil, fmt.Errorf("mint TLS certificate: %w", err)
		}
		caPEM, err := os.ReadFile(filepath.Join(caDir, trust.CACertFile))
		if err != nil {
			return nil, nil, fmt.Errorf("read CA certificate: %w", err)
		}
		logger.Info("TLS auto mode: serving HTTPS with a certificate minted from the local overcast CA",
			zap.String("ca_dir", caDir),
			zap.String("hint", "run `overcast trust install` once per machine so browsers and SDKs trust it"),
		)
		return certs.TLSConfig(), caPEM, nil
	case cfg.TLSEnabled():
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("load TLS cert/key: %w", err)
		}
		certPEM, err := os.ReadFile(cfg.TLSCertFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read TLS certificate: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, certPEM, nil
	default:
		return nil, nil, nil
	}
}

// cloneServerTLSConfig returns a copy each http.Server can own. ServeTLS
// mutates the config it is handed (http2ConfigureServer appends to NextProtos),
// so the API and UI servers must not be given the same pointer. nil in, nil out
// — the TLS-off path relies on that.
func cloneServerTLSConfig(c *tls.Config) *tls.Config {
	if c == nil {
		return nil
	}
	return c.Clone()
}

// browserAPIPort returns the API port a browser on the host must dial, which
// is what the web UI bakes into the SPA it serves.
//
// It is cfg.Port everywhere except inside a container published on a different
// host port: `-p 4580:4566` leaves the process listening on 4566 while host
// callers arrive on 4580, and nothing inside the container records that. The
// SPA served on the remapped UI port would then be told the API is on 4566,
// which is closed, and every request from it fails. Ask Docker for our own
// container's bindings instead; when that cannot be answered — native binary,
// no Docker socket, port not published — keep cfg.Port, so the non-container
// path is unchanged.
func resolvePublishedPort(cfg *config.Config, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dc := docker.NewClient(cfg.LambdaDockerSocket, logger)
	p := containerendpoint.PublishedPort(ctx, dc, cfg.Port, logger)
	if p == 0 || p == cfg.Port {
		return
	}
	cfg.PublishedPort = p
	logger.Warn(publishedPortMismatchWarning(cfg.Port, p),
		zap.Int("listen_port", cfg.Port), zap.Int("published_port", p))
}

// publishedPortMismatchWarning is the message resolvePublishedPort logs when
// Overcast's container remaps its own API port (`-p 4580:4566` — Overcast
// listens on cfg.Port inside the container, but a host caller reaches it on
// publishedPort instead).
//
// This is docs/plans/deploy-failure-diagnosis.md's W4 "ports" instance, and
// it is already mostly handled rather than newly broken: containerendpoint's
// URL-rewriting (see WithPublishedPort) already fixes the common case —
// queue URLs and split-horizon hostnames a host-side deploy bakes into
// container environment dial back correctly from both sides. What rewriting
// cannot fix is a value baked in for comparison rather than for dialing: a
// Cognito token minted from the host carries the published port in its
// `iss`, and a validator inside a container comparing that literally against
// cfg.Port reports a mismatch no URL rewrite can paper over (see
// docs/networking/urls.md's "no single port can be dialable from both sides
// of a remap" for the full story). So this is raised as an actionable Warn, not
// mere Info: most setups need do nothing (the rewrite already covers them),
// but a setup that hits the one case it cannot has a one-line fix.
func publishedPortMismatchWarning(listenPort, publishedPort int) string {
	return fmt.Sprintf(
		"the API is published on a different host port (%d) than it listens on (%d) — Overcast already "+
			"rewrites queue URLs and split-horizon hostnames baked into container environment so both "+
			"sides of the remap can dial back correctly. The one thing that rewrite cannot fix: a value "+
			"compared rather than dialed, e.g. a Cognito token's `iss` minted on the published port, "+
			"checked inside a container against the listen port — no single port is dialable from both "+
			"sides of a remap. If you hit that, publish 1:1 instead (e.g. -p %d:%d) rather than remapping.",
		publishedPort, listenPort, listenPort, listenPort,
	)
}

// browserAPIPort is the port the web UI hands the browser.
func browserAPIPort(cfg *config.Config) int {
	if cfg.PublishedPort > 0 {
		return cfg.PublishedPort
	}
	return cfg.Port
}
