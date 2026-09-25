package lambda

// runtime_api.go — Lambda Runtime API server.
//
// AWS Lambda containers communicate with the execution environment via the
// Lambda Runtime Interface (RIC). The RIC inside the container:
//
//  1. GET  /2018-06-01/runtime/invocation/next     — long-poll for work
//  2. POST /2018-06-01/runtime/invocation/{id}/response — deliver success result
//  3. POST /2018-06-01/runtime/invocation/{id}/error    — deliver function error
//  4. POST /2018-06-01/runtime/init/error               — cold-start failure
//
// This server listens on ports reachable from Lambda containers (via the
// Docker network). The AWS_LAMBDA_RUNTIME_API env var in each container
// points here. One server answers every container; each pending invocation is
// keyed by its request ID.
//
// Identifying the *caller* is the subtle part, because the RIC builds its own
// requests: it sends no header, no token and no path prefix Overcast could
// choose, and AWS_LAMBDA_RUNTIME_API is parsed as a bare host:port, so there is
// nothing to put in the URL either. The one thing left that Overcast controls
// is which address the container was told to dial — so every execution
// environment gets a listener of its own (containerListener) and is told about
// only that port. See containerListener for why the source address, which is
// what this used to key on, is not a safe identity.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/services/lambda/initproto"
	"go.uber.org/zap"
)

// pendingInvocation represents a single in-flight Lambda invocation waiting
// for a container to pick it up and return a result.
type pendingInvocation struct {
	RequestID   string
	FunctionARN string
	Deadline    time.Time
	TraceID     string // X-Ray trace header (Root=1-...;Parent=...;Sampled=0)
	Event       []byte
	ResultCh    chan invokeResponse

	// cancelled is set by CancelInvocation once the caller has given up. The
	// invocation may still be sitting in a function queue or in a waiter's
	// buffer at that point, and handing it to a container would run the
	// handler for a request that has already been reported as failed.
	// Guarded by RuntimeAPIServer.mu.
	cancelled bool
}

// runtimeWaiter is one container parked on GET /next. Waiters are tracked per
// container rather than per function: a function may be served by several
// execution environments at once, and each of them polls independently.
type runtimeWaiter struct {
	ch          chan *pendingInvocation
	containerIP string
}

type runtimeContainerConfig struct {
	FunctionARN  string
	FunctionName string
	// ContainerID is the Docker container behind this environment. The Runtime
	// API itself addresses environments by their listener and IP; this is what
	// lets a callback name the container to whoever works in Docker's terms.
	ContainerID        string
	Handler            string
	ExpectedExtensions []string
	// LogSink is where this environment's in-container init ships its output.
	// It is created before the container exists, because the init starts
	// publishing INIT-phase frames the moment the container starts — long
	// before the acquire that created it returns. See container_logs.go.
	LogSink *logSink
	// LogFormat is the function's resolved LoggingConfig format. The Telemetry
	// API needs it at publish time: under a modern schemaVersion a JSON-format
	// function's log line is embedded in the record as the object it already
	// is, not as a string holding JSON — see publishTelemetryLine.
	LogFormat string
}

type extensionRegistrationRequest struct {
	Events []string `json:"events"`
}

type extensionState struct {
	ID          string
	Name        string
	ContainerIP string
	FunctionARN string
	Events      map[string]bool
	Queue       []extensionEvent
	Waiter      chan extensionEvent
	Logs        *extensionLogsSubscription
}

// subscriptionAPI names which of the two subscription surfaces created a
// subscription. AWS: "After subscribing using one of these APIs, any attempt
// to subscribe using the other API returns an error."
// (https://docs.aws.amazon.com/lambda/latest/dg/telemetry-api.html)
type subscriptionAPI string

const (
	subscriptionAPILogs      subscriptionAPI = "Logs API"
	subscriptionAPITelemetry subscriptionAPI = "Telemetry API"
)

type extensionLogsSubscription struct {
	Types     map[string]bool
	URI       string
	Buffering bufferingConfig
	// API is the surface the subscription came through, for the cross-API
	// exclusivity rule above.
	API subscriptionAPI
	// RecordObjects is whether this subscriber's schemaVersion (2022-12-13 or
	// later) embeds a JSON-format function's log line as an object rather
	// than as a string containing JSON.
	RecordObjects bool
}

type extensionLogsSubscribeRequest struct {
	// SchemaVersion is Telemetry API only; the Logs API predates it. Absent
	// means the oldest behaviour.
	SchemaVersion string   `json:"schemaVersion"`
	Types         []string `json:"types"`
	Buffering     *struct {
		TimeoutMs int64 `json:"timeoutMs"`
		MaxBytes  int64 `json:"maxBytes"`
		MaxItems  int64 `json:"maxItems"`
	} `json:"buffering"`
	Destination struct {
		Protocol string `json:"protocol"`
		URI      string `json:"URI"`
	} `json:"destination"`
}

// bufferingConfig is a subscription's resolved batching bounds. Values come
// from the request, defaulted and clamped to the table AWS documents
// (https://docs.aws.amazon.com/lambda/latest/dg/telemetry-api.html#telemetry-api-buffering);
// whether AWS rejects an out-of-range value is not documented, so clamping is
// the chosen behaviour and the docs say so.
type bufferingConfig struct {
	Timeout  time.Duration
	MaxBytes int64
	MaxItems int64
}

func resolveBuffering(in *extensionLogsSubscribeRequest) bufferingConfig {
	cfg := bufferingConfig{Timeout: time.Second, MaxBytes: 262144, MaxItems: 10000}
	if in.Buffering == nil {
		return cfg
	}
	clamp := func(v, min, max, def int64) int64 {
		switch {
		case v == 0:
			return def
		case v < min:
			return min
		case v > max:
			return max
		}
		return v
	}
	cfg.Timeout = time.Duration(clamp(in.Buffering.TimeoutMs, 25, 30000, 1000)) * time.Millisecond
	cfg.MaxBytes = clamp(in.Buffering.MaxBytes, 262144, 1048576, 262144)
	cfg.MaxItems = clamp(in.Buffering.MaxItems, 1000, 10000, 10000)
	return cfg
}

// telemetrySchemaVersions is every schemaVersion the Telemetry API documents
// (https://docs.aws.amazon.com/lambda/latest/dg/telemetry-api.html). The value
// is whether that version embeds a JSON-format function's log record as an
// object ("If the schema version you're using is older than the 2022-12-13
// version, then the record is always rendered as a string" —
// telemetry-schema-reference).
var telemetrySchemaVersions = map[string]bool{
	"2022-07-01": false,
	"2022-12-13": true,
	"2025-01-29": true,
}

type extensionEvent struct {
	ID   string
	Body []byte
}

type extensionLogDelivery struct {
	// ContainerIP is the execution environment whose subscriber this batch is
	// for. It is identity, never a destination: the POST is made by that
	// environment's init, from inside the sandbox, and nothing here dials a
	// container. It is also what keeps two environments' subscribers apart
	// when they subscribe the same loopback address, which — now that the
	// address is no longer rewritten per container — they routinely do.
	ContainerIP string
	URI         string
	Body        []byte
	// Events is how many records the body carries, for the drop accounting
	// when the delivery fails every attempt.
	Events int64
}

const (
	maxExtensionEventQueue = 100
	logsDeliveryWorkers    = 4
	logsDeliveryQueueSize  = 1024
	// extensionLogDeliveryAttempts is how many times the worker tries one
	// delivery before it is dropped. Telemetry delivery is at-least-once: a
	// transient transport failure — the extension's server briefly saturated
	// on a loaded host — must not lose a record, because for the three
	// init-phase platform records a single lost POST is observable as an
	// extension that was never told its own INIT phase ended (issue #1437).
	// Retrying inline keeps each worker's own deliveries in order; the cost
	// is head-of-line delay against an endpoint that is dead rather than
	// slow, bounded by attempts x (timeout + backoff) per delivery and by the
	// environment's lifetime overall.
	extensionLogDeliveryAttempts = 3
	// extensionLogDeliveryRetryDelay paces the attempts; the n-th retry waits
	// n of these.
	extensionLogDeliveryRetryDelay = 100 * time.Millisecond
)

// invokeResponse is sent back from the container via the Runtime API.
type invokeResponse struct {
	Payload       []byte
	FunctionError string // "" for success, "Unhandled" for a runtime-reported error
	IsInitError   bool   // true if POST /runtime/init/error was called
	ErrorPayload  []byte // error details JSON when FunctionError != ""
	// LogSeq is the highest log frame the in-container init had published when
	// it forwarded this response — initproto.HeaderLogSeq. Waiting for the
	// ingest to reach it is what makes the tail and the CloudWatch ordering
	// exact. Zero when the header is absent, which means the responder is not
	// behind our init (a direct Runtime API client, or a test) and there is
	// nothing to wait for.
	LogSeq uint64
}

// RuntimeAPIServer serves the Lambda Runtime API to containers.
type RuntimeAPIServer struct {
	mu               sync.Mutex
	pending          map[string]*pendingInvocation   // keyed by request ID
	funcQueues       map[string][]*pendingInvocation // keyed by function ARN — FIFO
	waiting          map[string][]*runtimeWaiter     // keyed by function ARN — FIFO of parked containers
	containers       map[string]string               // container ID → function ARN (registered on Acquire)
	containerConfigs map[string]runtimeContainerConfig
	containerExts    map[string]map[string]bool
	containerErrors  map[string]string
	extensions       map[string]*extensionState
	seenNext         map[string]bool          // container IP → true after first GET /next
	firstNextAt      map[string]time.Time     // container IP → time of first GET /next
	ready            map[string]chan struct{} // container IP → closed after first GET /next
	containerPorts   map[int]string           // per-environment listener port → container IP
	// logSinks is the ingest's own index, keyed by the per-environment
	// listener port rather than by the container's address. It exists because
	// the init opens its log stream the moment the container starts, which is
	// before Docker will tell us the address — so an ingest that could only be
	// resolved by address would spend the whole INIT phase waiting for a
	// registration, and the INIT output would land after the first START.
	// The port is the same authority containerForRequestLocked prefers anyway.
	logSinks map[int]*logSink
	// initRecords is the init-phase platform events a container has published,
	// keyed by container IP, held so a subscription that does not exist yet
	// still receives them.
	//
	// It exists because of when those three events happen. platform.initStart
	// is emitted before the extensions are even started, and initRuntimeDone /
	// initReport can beat an extension that is still registering — yet AWS
	// delivers all three to a Telemetry subscriber, because an extension's
	// whole job is to be told about the phase it was started in. Without this
	// they would be published to nobody and lost. The buffer holds only those
	// three: every later record is emitted while subscriptions are live.
	initRecords map[string][]json.RawMessage
	// server fronts every listener the caller bound, plus every per-environment
	// listener added since; http.Server tracks them itself, so Stop's Shutdown
	// closes them all and nothing here needs to hold them.
	server *http.Server
	logger *zap.Logger
	addr   string // host:port as seen by containers
	// containerHost and bindHosts are addr's host and the local addresses it
	// resolved to, kept so a per-environment listener can be bound on the same
	// set at a fresh port. See AddContainerListener.
	containerHost string
	bindHosts     []string
	// registrationWait bounds how long a Runtime API call waits for its
	// container's registration to land. See registrationWaitFor.
	registrationWait time.Duration
	stopOnce         sync.Once
	done             chan struct{} // closed on Stop to unblock long-polling handlers
	clk              clock.Clock
	logsDeliveries   chan extensionLogDelivery
	buffersMu        sync.Mutex
	telemetryBuffers map[string]*telemetryBuffer
	// telemetryRelayPorts and telemetryRelays are the same relays under the two
	// identities a Runtime API call is attributed by: the per-environment
	// listener it arrives on, and the container address a publisher knows the
	// environment as. The port index is populated with the listener, before the
	// container exists, so the init's first poll is answered; the address index
	// follows when Docker reports it, which is before any extension can have
	// registered. See telemetry_relay.go.
	telemetryRelayPorts map[int]*telemetryRelay
	telemetryRelays     map[string]*telemetryRelay

	// OnFirstNext is called (in a goroutine) the first time a container's RIC
	// issues GET /next — the moment that container's INIT phase ends. It is
	// told which environment reported it, not only which function: a function
	// can have several environments in INIT at once, and anything acting on
	// this (the INIT-burst throttle-down) acts on one container.
	OnFirstNext func(functionARN, containerID string)
}

// NewRuntimeAPIServer creates and starts the Runtime API server.
// listenAddr is the address to bind to (e.g. "0.0.0.0:9001").
// containerAddr is the host:port that containers use to reach this server
// (may differ from listenAddr when Overcast runs inside Docker).
func NewRuntimeAPIServer(listenAddr string, containerAddr string, logger *zap.Logger, clk clock.Clock) (*RuntimeAPIServer, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("runtime api: listen %s: %w", listenAddr, err)
	}
	return NewRuntimeAPIServerFromListener(ln, containerAddr, logger, clk)
}

// NewRuntimeAPIServerFromListener is like NewRuntimeAPIServer but accepts a
// pre-created listener. This allows the caller to bind first (e.g. to resolve
// port 0) and then derive containerAddr from the actual port.
func NewRuntimeAPIServerFromListener(ln net.Listener, containerAddr string, logger *zap.Logger, clk clock.Clock) (*RuntimeAPIServer, error) {
	return NewRuntimeAPIServerFromListeners([]net.Listener{ln}, containerAddr, defaultLambdaInitTimeout, logger, clk)
}

// NewRuntimeAPIServerFromListeners fronts every listener with the one server,
// so a RIC is answered identically whichever address its request arrived on.
//
// Several listeners rather than one wildcard is how the Runtime API stays off
// networks this machine merely happens to be attached to while remaining
// reachable from the containers that have to connect back to it: the address
// they dial, plus loopback. See containerendpoint.ResolveListen, which decides
// the set.
//
// initTimeout is the budget an invocation gives a cold start (see
// lambdaInitTimeout); it bounds how long a Runtime API call waits for its
// container's registration, which must not outlive it.
func NewRuntimeAPIServerFromListeners(lns []net.Listener, containerAddr string, initTimeout time.Duration, logger *zap.Logger, clk clock.Clock) (*RuntimeAPIServer, error) {
	if len(lns) == 0 {
		return nil, fmt.Errorf("runtime api: no listeners")
	}
	containerHost, _, err := net.SplitHostPort(containerAddr)
	if err != nil {
		containerHost = containerAddr
	}
	s := &RuntimeAPIServer{
		pending:             make(map[string]*pendingInvocation),
		funcQueues:          make(map[string][]*pendingInvocation),
		waiting:             make(map[string][]*runtimeWaiter),
		containers:          make(map[string]string),
		containerConfigs:    make(map[string]runtimeContainerConfig),
		containerExts:       make(map[string]map[string]bool),
		containerErrors:     make(map[string]string),
		extensions:          make(map[string]*extensionState),
		seenNext:            make(map[string]bool),
		firstNextAt:         make(map[string]time.Time),
		ready:               make(map[string]chan struct{}),
		containerPorts:      make(map[int]string),
		logSinks:            make(map[int]*logSink),
		initRecords:         make(map[string][]json.RawMessage),
		logger:              logger,
		addr:                containerAddr,
		containerHost:       containerHost,
		bindHosts:           bindHostsOf(lns),
		registrationWait:    registrationWaitFor(initTimeout),
		done:                make(chan struct{}),
		clk:                 clk,
		logsDeliveries:      make(chan extensionLogDelivery, logsDeliveryQueueSize),
		telemetryBuffers:    make(map[string]*telemetryBuffer),
		telemetryRelayPorts: make(map[int]*telemetryRelay),
		telemetryRelays:     make(map[string]*telemetryRelay),
	}
	for i := 0; i < logsDeliveryWorkers; i++ {
		go s.logsDeliveryWorker()
	}

	mux := http.NewServeMux()
	// Lambda Runtime API routes (2018-06-01 version).
	mux.HandleFunc("/2018-06-01/runtime/invocation/next", s.handleNext)
	mux.HandleFunc("/2018-06-01/runtime/invocation/", s.handleInvocationAction)
	mux.HandleFunc("/2018-06-01/runtime/init/error", s.handleInitError)
	mux.HandleFunc("/2020-01-01/extension/register", s.handleExtensionRegister)
	mux.HandleFunc("/2020-01-01/extension/event/next", s.handleExtensionNext)
	mux.HandleFunc("/2020-01-01/extension/init/error", s.handleExtensionError)
	mux.HandleFunc("/2020-01-01/extension/exit/error", s.handleExtensionError)
	mux.HandleFunc("/2020-08-15/logs", s.handleExtensionLogsSubscribe)
	mux.HandleFunc("/2022-07-01/telemetry", s.handleTelemetrySubscribe)
	// AWS's own reference appends the version and "telemetry/", so the
	// trailing-slash spelling is a request real extensions make.
	mux.HandleFunc("/2022-07-01/telemetry/", s.handleTelemetrySubscribe)
	// Not an AWS API: the emulator-internal log channel from the in-container
	// init. It is on this mux because this is the listener a Lambda container
	// can reach and the one that identifies it. See log_ingest.go.
	mux.HandleFunc(initproto.LogsPath, s.handleContainerLogs)
	// Likewise emulator-internal, and the same listener for the same reason:
	// the init's poll for the Telemetry API batches it carries into the
	// sandbox. See telemetry_relay.go.
	mux.HandleFunc(initproto.TelemetryPath, s.handleTelemetryRelay)

	s.server = &http.Server{Handler: mux}

	listenAddrs := make([]string, 0, len(lns))
	for _, ln := range lns {
		listenAddrs = append(listenAddrs, ln.Addr().String())
		go s.serve(ln)
	}

	logger.Info("lambda runtime API started",
		zap.Strings("listen", listenAddrs),
		zap.String("container_addr", containerAddr))

	return s, nil
}

// runtimeAPIListenFix is what to change when the shared Runtime API listener
// cannot bind; it travels with the startup warning and /_overcast/health.
const runtimeAPIListenFix = "set LAMBDA_RUNTIME_API_PORT to a free port, or 0 for an ephemeral one"

// listenRuntimeAPI binds the shared Runtime API listener set on hosts.
//
// The default port falls back to an ephemeral one when it is busy — a second
// Overcast on the same host is the usual reason — because nothing depends on
// the number: no container is ever told the shared port, each execution
// environment dials its own per-container listener (AddContainerListener), and
// the shared one only settles which local addresses those join. A port set to
// anything else is pinned and fails, so a deliberate choice is never silently
// replaced. fellBack reports whether the fallback was taken.
func listenRuntimeAPI(hosts []string, port, defaultPort int, logger *zap.Logger) (lns []net.Listener, fellBack bool, err error) {
	lns, err = containerendpoint.ListenOn(hosts, port, logger)
	if err == nil || port != defaultPort || port == 0 {
		return lns, false, err
	}
	logger.Warn("runtime api: default port busy; selecting an ephemeral port",
		zap.Int("requested_port", port),
		zap.Error(err),
		zap.String("hint", "set LAMBDA_RUNTIME_API_PORT to pin a different port"))
	lns, err = containerendpoint.ListenOn(hosts, 0, logger)
	return lns, err == nil, err
}

// serve runs one listener under the shared handler. A per-environment listener
// is closed on its own when the execution environment goes away, which is not
// an error — only Shutdown reports ErrServerClosed, so net.ErrClosed has to be
// tolerated here as well.
func (s *RuntimeAPIServer) serve(ln net.Listener) {
	addr := ln.Addr().String()
	if err := s.server.Serve(ln); err != nil &&
		!errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		s.logger.Error("runtime api: serve error", zap.String("listen", addr), zap.Error(err))
	}
}

// bindHostsOf recovers the local addresses a set of listeners is bound to, in
// order, so a later per-environment listener can join the same set at a fresh
// port. Order matters: listenAllOn settles the port on the first host, and the
// first is the one containers dial.
func bindHostsOf(lns []net.Listener) []string {
	hosts := make([]string, 0, len(lns))
	for _, ln := range lns {
		tcp, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			continue
		}
		host := tcp.IP.String()
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// defaultLambdaInitTimeout is the cold-start budget an invocation gives an
// execution environment when LAMBDA_INIT_TIMEOUT_SECONDS says nothing.
const defaultLambdaInitTimeout = 10 * time.Second

// registrationWaitMargin is how far ahead of the init timeout the registration
// wait gives up, leaving room for the 403 and its log line to be written before
// the invocation abandons the environment.
const registrationWaitMargin = time.Second

// registrationWaitFor returns how long a Runtime API call may block waiting for
// its container's registration to arrive.
//
// The wait covers a real race: the RIC starts polling /next the instant the
// container boots, while Overcast cannot register the environment until Docker
// reports its address. But it has to expire *before* awaitRuntimeReady gives up
// at the init timeout, and for a long time it did not — the wait was a hardcoded
// 15 s against a 10 s init timeout, so the environment was always torn down
// first and the 403 was never written. An unattributable container therefore
// produced no diagnostic at all, and surfaced to the caller as the function's
// own Runtime.InitError. Shorter than the init timeout for every positive value
// of it, and as close to it as the margin allows.
func registrationWaitFor(initTimeout time.Duration) time.Duration {
	if initTimeout <= 0 {
		initTimeout = defaultLambdaInitTimeout
	}
	wait := initTimeout - registrationWaitMargin
	if quarter := initTimeout / 4; wait < quarter {
		wait = quarter
	}
	return wait
}

// containerListener is one execution environment's own Runtime API endpoint.
//
// Overcast used to identify a calling container by the source address of its
// Runtime API requests, matched against the bridge IP InspectContainer
// reported. That identity holds only while the container's packets arrive
// on-link. Docker Desktop's userspace host proxy re-originates the connection
// from the host, so a natively-built Overcast on Windows or macOS saw 127.0.0.1
// or the host's LAN address for every container and could match none of them —
// two of the three published binaries could not invoke Lambda at all.
//
// The listener a request arrives on is not something a proxy can rewrite, so
// each environment is given a port of its own and told about only that one.
// The count is bounded by LAMBDA_MAX_INSTANCES.
type containerListener struct {
	srv  *RuntimeAPIServer
	lns  []net.Listener
	port int
	addr string
	once sync.Once

	// relay is this environment's telemetry delivery channel, created with the
	// listener because the init opens its poll as the container starts — long
	// before Docker will say what address it has. See telemetry_relay.go.
	relay *telemetryRelay

	// accepted counts the connections this endpoint has taken, over every
	// address it is bound on. See countingListener.
	accepted atomic.Int64
}

// countingListener tallies the connections accepted on one execution
// environment's Runtime API endpoint.
//
// The count is the one thing that tells "the RIC never reached us" apart from
// "it reached us and we turned it away", and those have nothing in common: the
// first is the container's route back to this host, the second is identity. In
// Overcast's logs both used to look the same — silence — and an INIT timeout
// blaming the function was the only symptom either produced. See
// containerInstance.logInitTimeout, which reports this on the failure path.
type countingListener struct {
	net.Listener
	accepted *atomic.Int64
}

func (l countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

// AddContainerListener binds a fresh port across the same local addresses the
// server already answers on, fronted by the same handler, for one execution
// environment's exclusive use.
func (s *RuntimeAPIServer) AddContainerListener() (*containerListener, error) {
	if len(s.bindHosts) == 0 {
		return nil, fmt.Errorf("runtime api: no bind address for a per-container listener")
	}
	lns, err := containerendpoint.ListenOn(s.bindHosts, 0, s.logger)
	if err != nil {
		return nil, err
	}
	bound, ok := lns[0].Addr().(*net.TCPAddr)
	if !ok {
		for _, ln := range lns {
			_ = ln.Close()
		}
		return nil, fmt.Errorf("runtime api: per-container listen: not a TCP address")
	}
	cl := &containerListener{
		srv:   s,
		lns:   lns,
		port:  bound.Port,
		addr:  net.JoinHostPort(s.containerHost, strconv.Itoa(bound.Port)),
		relay: newTelemetryRelay(),
	}
	s.mu.Lock()
	s.telemetryRelayPorts[cl.port] = cl.relay
	s.mu.Unlock()
	for _, ln := range lns {
		go s.serve(countingListener{Listener: ln, accepted: &cl.accepted})
	}
	return cl, nil
}

// Accepted reports how many connections this environment's endpoint has taken.
func (l *containerListener) Accepted() int64 {
	if l == nil {
		return 0
	}
	return l.accepted.Load()
}

// Addr is what this environment's AWS_LAMBDA_RUNTIME_API must be set to.
func (l *containerListener) Addr() string {
	if l == nil {
		return ""
	}
	return l.addr
}

// Attach records which container reaches the Runtime API on this listener.
// Called once the environment's address is known and registered, because every
// other piece of per-container state — readiness, extensions, log delivery —
// is still keyed by that address.
// RegisterLogSink indexes an execution environment's log sink against its own
// listener, before the container exists. See RuntimeAPIServer.logSinks.
func (l *containerListener) RegisterLogSink(sink *logSink) {
	if l == nil || sink == nil {
		return
	}
	l.srv.mu.Lock()
	l.srv.logSinks[l.port] = sink
	l.srv.mu.Unlock()
}

// logSinkForPort returns the sink of the environment that owns a listener.
func (s *RuntimeAPIServer) logSinkForPort(port int) *logSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logSinks[port]
}

func (l *containerListener) Attach(containerIP string) {
	if l == nil || containerIP == "" {
		return
	}
	l.srv.mu.Lock()
	l.srv.containerPorts[l.port] = containerIP
	// The publishers know an environment by its address, so the relay the init
	// is already polling on gets its second name here.
	l.srv.telemetryRelays[containerIP] = l.relay
	l.srv.mu.Unlock()
}

// telemetryRelayForPort returns the relay of the environment that owns a
// listener. See telemetry_relay.go.
func (s *RuntimeAPIServer) telemetryRelayForPort(port int) *telemetryRelay {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.telemetryRelayPorts[port]
}

// Close retires the environment's endpoint. Idempotent: Close runs on every
// failure path in acquireContainer as well as on containerInstance.Close.
func (l *containerListener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.srv.mu.Lock()
		delete(l.srv.containerPorts, l.port)
		delete(l.srv.logSinks, l.port)
		delete(l.srv.telemetryRelayPorts, l.port)
		for ip, relay := range l.srv.telemetryRelays {
			if relay == l.relay {
				delete(l.srv.telemetryRelays, ip)
			}
		}
		l.srv.mu.Unlock()
		for _, ln := range l.lns {
			_ = ln.Close()
		}
	})
	return nil
}

// Addr returns the host:port that containers should use to reach this server.
func (s *RuntimeAPIServer) Addr() string { return s.addr }

// RegisterContainer maps the container's IP address to a function ARN so that
// incoming GET /next requests from that container can be routed to the correct
// invocation queue. Call this as soon as Docker has assigned the container IP.
func (s *RuntimeAPIServer) RegisterContainer(containerIP, functionARN string) {
	s.RegisterContainerConfig(containerIP, runtimeContainerConfig{FunctionARN: functionARN, FunctionName: functionNameFromARN(functionARN)})
}

func (s *RuntimeAPIServer) RegisterContainerConfig(containerIP string, cfg runtimeContainerConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers[containerIP] = cfg.FunctionARN
	s.containerConfigs[containerIP] = cfg
	if _, ok := s.containerExts[containerIP]; !ok {
		s.containerExts[containerIP] = make(map[string]bool, len(cfg.ExpectedExtensions))
	}
	if _, ok := s.ready[containerIP]; !ok {
		s.ready[containerIP] = make(chan struct{})
	}
	s.maybeMarkReadyLocked(containerIP)
}

func (s *RuntimeAPIServer) ReadyChan(containerIP string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isReadyLocked(containerIP) {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	ch, ok := s.ready[containerIP]
	if !ok {
		ch = make(chan struct{})
		s.ready[containerIP] = ch
	}
	return ch
}

// FirstNextAt returns when the container's RIC issued its first GET /next —
// the moment the execution environment finished initialising. ok is false
// until that first poll arrives (or after the container is unregistered).
func (s *RuntimeAPIServer) FirstNextAt(containerIP string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.firstNextAt[containerIP]
	return t, ok
}

// UnregisterContainer removes the container IP from the registry.
func (s *RuntimeAPIServer) UnregisterContainer(containerIP string) {
	// Before the lock, and outside it: the buffers have their own. A buffer is
	// per (environment, destination), so an environment that goes away takes
	// its buffers with it rather than leaving one behind per subscriber for the
	// life of the process.
	s.dropTelemetryBuffers(containerIP)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.telemetryRelays, containerIP)
	delete(s.containers, containerIP)
	delete(s.containerConfigs, containerIP)
	delete(s.containerExts, containerIP)
	delete(s.containerErrors, containerIP)
	delete(s.seenNext, containerIP)
	delete(s.firstNextAt, containerIP)
	delete(s.ready, containerIP)
	delete(s.initRecords, containerIP)
	// Any per-environment listener still pointing here goes with it. The
	// listener is normally closed by containerInstance.Close, which drops its
	// own entry; this covers an environment retired without one and keeps a
	// recycled bridge address from resolving through a dead port.
	for port, ip := range s.containerPorts {
		if ip == containerIP {
			delete(s.containerPorts, port)
		}
	}
	for id, ext := range s.extensions {
		if ext.ContainerIP == containerIP {
			delete(s.extensions, id)
		}
	}
	// Drop this container's parked polls. Its own handlers unwind when their
	// request contexts end, but until they do the container looks available and
	// SubmitInvocation would hand it work it can no longer run.
	for arn, waiters := range s.waiting {
		kept := waiters[:0]
		for _, waiter := range waiters {
			if waiter.containerIP == containerIP {
				continue
			}
			kept = append(kept, waiter)
		}
		if len(kept) == 0 {
			delete(s.waiting, arn)
			continue
		}
		s.waiting[arn] = kept
	}
}

func (s *RuntimeAPIServer) ContainerError(containerIP string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason, ok := s.containerErrors[containerIP]
	return reason, ok
}

func (s *RuntimeAPIServer) lookupContainerConfigWait(ctx context.Context, r *http.Request) (string, runtimeContainerConfig, bool) {
	ip, functionARN, ok := s.lookupContainerWait(ctx, r)
	if !ok {
		return "", runtimeContainerConfig{}, false
	}
	s.mu.Lock()
	cfg, ok := s.containerConfigs[ip]
	s.mu.Unlock()
	if ok {
		return ip, cfg, true
	}
	return ip, runtimeContainerConfig{FunctionARN: functionARN, FunctionName: functionNameFromARN(functionARN)}, true
}

// lookupContainerWait resolves a Runtime API request to the execution
// environment that made it, returning the container's address and its function
// ARN. It blocks up to the registration wait if the registration has not landed
// yet, and returns ok=false if that expires or the request is cancelled first.
func (s *RuntimeAPIServer) lookupContainerWait(ctx context.Context, r *http.Request) (string, string, bool) {
	localPort := localPortOf(r)
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	s.mu.Lock()
	ip, arn, ok := s.containerForRequestLocked(localPort, remoteIP)
	s.mu.Unlock()
	if ok {
		return ip, arn, true
	}
	deadline := s.clk.Now().Add(s.registrationWait)
	ticker := s.clk.Ticker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", "", false
		case <-s.done:
			return "", "", false
		case <-ticker.C:
			s.mu.Lock()
			ip, arn, ok = s.containerForRequestLocked(localPort, remoteIP)
			s.mu.Unlock()
			if ok {
				return ip, arn, true
			}
			if s.clk.Now().After(deadline) {
				return "", "", false
			}
		}
	}
}

// containerForRequestLocked names the execution environment a Runtime API call
// came from. Caller must hold s.mu.
//
// The listener it arrived on is tried first: that is assigned per environment
// and survives anything in the path rewriting the source address. The source
// address is the fallback, and stays the whole answer for a containerised
// Overcast, where the Lambda containers are siblings on the same Docker network
// and their packets arrive on-link exactly as registered.
func (s *RuntimeAPIServer) containerForRequestLocked(localPort int, remoteIP string) (string, string, bool) {
	if ip, mapped := s.containerPorts[localPort]; mapped {
		if arn, known := s.containers[ip]; known {
			return ip, arn, true
		}
	}
	if arn, known := s.containers[remoteIP]; known {
		return remoteIP, arn, true
	}
	return "", "", false
}

// localPortOf reports the port the request was accepted on, which is how a
// per-environment listener identifies its container. Zero when the server did
// not record a local address (a handler driven directly by a test).
func localPortOf(r *http.Request) int {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return 0
	}
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

// logUnknownContainer reports a Runtime API call Overcast could not attribute
// to any execution environment.
//
// This used to be answered 403 in silence, which is most of the reason a
// container Overcast could not identify took a day to diagnose: the operator
// saw a Runtime.InitError blaming their function and nothing at all in
// Overcast's own logs. The source address and the registered set are what
// distinguish "registration has not landed yet" from "this caller's address is
// not the one Docker reported".
func (s *RuntimeAPIServer) logUnknownContainer(r *http.Request) {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	s.mu.Lock()
	registeredIPs := make([]string, 0, len(s.containers))
	for ip := range s.containers {
		registeredIPs = append(registeredIPs, ip)
	}
	registeredPorts := make([]int, 0, len(s.containerPorts))
	for port := range s.containerPorts {
		registeredPorts = append(registeredPorts, port)
	}
	s.mu.Unlock()

	slices.Sort(registeredIPs)
	slices.Sort(registeredPorts)
	s.logger.Debug("runtime api: call from an unidentified container — answering 403",
		zap.String("path", r.URL.Path),
		zap.String("source_ip", remoteIP),
		zap.Int("arrived_on_port", localPortOf(r)),
		zap.Duration("waited", s.registrationWait),
		zap.Strings("registered_ips", registeredIPs),
		zap.Ints("registered_ports", registeredPorts))
}

// CancelInvocation removes a pending invocation from the map and closes its
// ResultCh so that any goroutine blocked on <-resultCh is unblocked. This
// must be called when the container crashes or the invoke times out to prevent
// goroutine leaks from drain goroutines that would otherwise block forever.
//
// The invocation is also dropped from its function queue and marked cancelled.
// Without that it stayed queued after the caller gave up, and the next
// container to poll ran the handler for a request that had already been
// reported as timed out — real side effects, under a dead request ID, with the
// response then discarded because nothing was left in s.pending to route it to.
func (s *RuntimeAPIServer) CancelInvocation(reqID string) {
	s.mu.Lock()
	inv, ok := s.pending[reqID]
	if ok {
		delete(s.pending, reqID)
		inv.cancelled = true
		s.dropFromQueueLocked(inv)
	}
	s.mu.Unlock()
	if ok {
		close(inv.ResultCh)
	}
}

// dropFromQueueLocked removes inv from its function's pending queue. Caller
// must hold s.mu. An invocation already handed to a waiter is not in the queue;
// the cancelled flag covers that case.
func (s *RuntimeAPIServer) dropFromQueueLocked(inv *pendingInvocation) {
	queue := s.funcQueues[inv.FunctionARN]
	for i, queued := range queue {
		if queued != inv {
			continue
		}
		s.funcQueues[inv.FunctionARN] = append(queue[:i:i], queue[i+1:]...)
		return
	}
}

// PrepareInvocation registers an invocation — its ID, payload, deadline and
// result channel — without offering it to any execution environment. Until
// ReleaseInvocation, no GET /next can return it, so no log frame can carry its
// request ID yet. That gap is the point: the invoke path writes the
// invocation's START record between the two calls, which is what makes "the
// tail opens with START" a structural property rather than a race the invoke
// goroutine has to win — the handler cannot have printed a line for a request
// the runtime has not been handed.
func (s *RuntimeAPIServer) PrepareInvocation(functionARN string, event []byte, deadline time.Time) (string, <-chan invokeResponse) {
	reqID := uuid.New().String()
	ch := make(chan invokeResponse, 1)

	inv := &pendingInvocation{
		RequestID:   reqID,
		FunctionARN: functionARN,
		Deadline:    deadline,
		TraceID:     newXRayTraceID(s.clk.Now()),
		Event:       event,
		ResultCh:    ch,
	}

	s.mu.Lock()
	s.pending[reqID] = inv
	s.mu.Unlock()
	return reqID, ch
}

// ReleaseInvocation offers a prepared invocation to the function's execution
// environments: straight to a parked runtime when one is waiting, queued for
// the next GET /next otherwise. An invocation cancelled between the two calls
// is already gone from pending, so releasing it is a no-op.
func (s *RuntimeAPIServer) ReleaseInvocation(reqID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.pending[reqID]
	if !ok || inv.cancelled {
		return
	}

	// If a container for this function is already waiting (long-polling /next),
	// hand it straight over. The waiter is removed from the list as it is
	// claimed, so two invocations arriving together go to two containers rather
	// than both targeting the same one.
	if waiters := s.waiting[inv.FunctionARN]; len(waiters) > 0 {
		waiter := waiters[0]
		s.waiting[inv.FunctionARN] = waiters[1:]
		waiter.ch <- inv // buffered, and this waiter is now claimed by us alone
		return
	}

	// No waiter — enqueue for later pickup.
	s.funcQueues[inv.FunctionARN] = append(s.funcQueues[inv.FunctionARN], inv)
}

// SubmitInvocation prepares an invocation and releases it in one call. It is
// the form for a caller with nothing to order in front of the hand-off; the
// invoke path uses the Prepare/Release pair so the START record is in the tail
// buffer before the runtime can start the handler — see PrepareInvocation.
func (s *RuntimeAPIServer) SubmitInvocation(functionARN string, event []byte, deadline time.Time) (string, <-chan invokeResponse) {
	reqID, ch := s.PrepareInvocation(functionARN, event, deadline)
	s.ReleaseInvocation(reqID)
	return reqID, ch
}

func (s *RuntimeAPIServer) enqueueExtensionInvokeLocked(containerIP string, inv *pendingInvocation) {
	for _, ext := range s.extensions {
		if ext.ContainerIP != containerIP || ext.FunctionARN != inv.FunctionARN || !ext.Events["INVOKE"] {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"eventType":          "INVOKE",
			"deadlineMs":         inv.Deadline.UnixMilli(),
			"requestId":          inv.RequestID,
			"invokedFunctionArn": inv.FunctionARN,
			"tracing": map[string]string{
				"type":  "X-Amzn-Trace-Id",
				"value": inv.TraceID,
			},
		})
		s.enqueueExtensionEventLocked(ext, extensionEvent{ID: uuid.New().String(), Body: body})
	}
}

func (s *RuntimeAPIServer) EnqueueExtensionShutdown(containerIP, reason string, deadline time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := 0
	for _, ext := range s.extensions {
		if ext.ContainerIP != containerIP || !ext.Events["SHUTDOWN"] {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"eventType":      "SHUTDOWN",
			"shutdownReason": reason,
			"deadlineMs":     deadline.UnixMilli(),
		})
		s.enqueueExtensionEventLocked(ext, extensionEvent{ID: uuid.New().String(), Body: body})
		queued++
	}
	return queued
}

func (s *RuntimeAPIServer) enqueueExtensionEventLocked(ext *extensionState, event extensionEvent) {
	if ext.Waiter != nil {
		select {
		case ext.Waiter <- event:
			ext.Waiter = nil
		default:
			ext.Queue = append(ext.Queue, event)
		}
		return
	}
	if len(ext.Queue) >= maxExtensionEventQueue {
		copy(ext.Queue, ext.Queue[1:])
		ext.Queue[len(ext.Queue)-1] = event
		return
	}
	ext.Queue = append(ext.Queue, event)
}

func (s *RuntimeAPIServer) handleExtensionRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.Header.Get("Lambda-Extension-Name")
	if strings.TrimSpace(name) == "" {
		http.Error(w, "missing Lambda-Extension-Name", http.StatusForbidden)
		return
	}
	var in extensionRegistrationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&in); err != nil {
		http.Error(w, "invalid register request", http.StatusBadRequest)
		return
	}
	events := make(map[string]bool, len(in.Events))
	for _, event := range in.Events {
		switch event {
		case "INVOKE", "SHUTDOWN":
			events[event] = true
		default:
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
	}
	containerIP, cfg, known := s.lookupContainerConfigWait(r.Context(), r)
	if !known {
		s.logUnknownContainer(r)
		http.Error(w, "unknown container", http.StatusForbidden)
		return
	}
	id := uuid.New().String()
	s.mu.Lock()
	s.extensions[id] = &extensionState{ID: id, Name: name, ContainerIP: containerIP, FunctionARN: cfg.FunctionARN, Events: events}
	if _, ok := s.containerExts[containerIP]; !ok {
		s.containerExts[containerIP] = make(map[string]bool)
	}
	s.containerExts[containerIP][name] = true
	s.maybeMarkReadyLocked(containerIP)
	s.mu.Unlock()

	// The registration is itself a platform event — {events, name, state},
	// per AWS's platform.extension example. Retained with the init-phase
	// records rather than only published: registration happens during INIT,
	// before anything can have subscribed, and the record is part of the
	// phase's story a later subscription is replayed. (Whether AWS replays
	// it is not documented; delivering it is the reading under which an
	// extension can actually see it.)
	if event, err := json.Marshal(map[string]any{
		"time": s.clk.Now().UTC().Format(time.RFC3339Nano),
		"type": "platform.extension",
		"record": map[string]any{
			"events": in.Events,
			"name":   name,
			"state":  "Ready",
		},
	}); err == nil {
		s.PublishInitPhaseRecord(containerIP, string(event))
	}

	w.Header().Set("Lambda-Extension-Identifier", id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"functionName":    cfg.FunctionName,
		"functionVersion": "$LATEST",
		"handler":         cfg.Handler,
	})
}

func (s *RuntimeAPIServer) handleExtensionNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.Header.Get("Lambda-Extension-Identifier")
	if id == "" {
		http.Error(w, "missing Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	ext, ok := s.extensions[id]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "invalid Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	if len(ext.Queue) > 0 {
		event := ext.Queue[0]
		ext.Queue = ext.Queue[1:]
		s.mu.Unlock()
		s.writeExtensionEvent(w, event)
		return
	}
	waiter := make(chan extensionEvent, 1)
	ext.Waiter = waiter
	s.mu.Unlock()

	select {
	case event := <-waiter:
		s.writeExtensionEvent(w, event)
	case <-r.Context().Done():
		s.releaseExtensionWaiter(ext, waiter)
	case <-s.done:
		s.releaseExtensionWaiter(ext, waiter)
	}
}

// releaseExtensionWaiter clears an extension's parked poll, putting back any
// event that was handed to it in the same moment. The invocation queue had the
// same hazard: a buffered send always succeeds, so an event delivered just as
// the poll unwound was lost rather than redelivered — and a lost SHUTDOWN is an
// extension that never gets told to flush.
func (s *RuntimeAPIServer) releaseExtensionWaiter(ext *extensionState, waiter chan extensionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ext.Waiter == waiter {
		ext.Waiter = nil
		return
	}
	select {
	case event := <-waiter:
		ext.Queue = append([]extensionEvent{event}, ext.Queue...)
	default:
	}
}

func (s *RuntimeAPIServer) handleExtensionError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.Header.Get("Lambda-Extension-Identifier")
	if id == "" {
		http.Error(w, "missing Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		http.Error(w, "read body failed", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	ext, ok := s.extensions[id]
	if ok {
		s.containerErrors[ext.ContainerIP] = r.Header.Get("Lambda-Extension-Function-Error-Type")
		if s.containerErrors[ext.ContainerIP] == "" {
			s.containerErrors[ext.ContainerIP] = "Extension.Error"
		}
	}
	if ok && strings.HasSuffix(r.URL.Path, "/exit/error") {
		delete(s.extensions, id)
		if registered := s.containerExts[ext.ContainerIP]; registered != nil {
			delete(registered, ext.Name)
		}
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	s.logger.Debug("runtime api: extension reported error",
		zap.String("extension", ext.Name),
		zap.String("path", r.URL.Path),
		zap.String("error_type", r.Header.Get("Lambda-Extension-Function-Error-Type")),
		zap.Int("body_bytes", len(body)))
	w.WriteHeader(http.StatusAccepted)
}

// handleExtensionLogsSubscribe serves the Logs API's PUT /2020-08-15/logs.
func (s *RuntimeAPIServer) handleExtensionLogsSubscribe(w http.ResponseWriter, r *http.Request) {
	s.handleSubscribe(w, r, subscriptionAPILogs)
}

// handleTelemetrySubscribe serves the Telemetry API's PUT /2022-07-01/telemetry
// — the surface that superseded the Logs API and the one current observability
// extensions call. Same subscription machinery; what differs is the
// schemaVersion in the request, the "OK" body AWS documents for the response,
// and how a modern schema renders a JSON-format function's log records.
// (https://docs.aws.amazon.com/lambda/latest/dg/telemetry-api.html)
func (s *RuntimeAPIServer) handleTelemetrySubscribe(w http.ResponseWriter, r *http.Request) {
	s.handleSubscribe(w, r, subscriptionAPITelemetry)
}

func (s *RuntimeAPIServer) handleSubscribe(w http.ResponseWriter, r *http.Request, api subscriptionAPI) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.Header.Get("Lambda-Extension-Identifier")
	if id == "" {
		http.Error(w, "missing Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	var in extensionLogsSubscribeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&in); err != nil {
		http.Error(w, "invalid subscribe request", http.StatusBadRequest)
		return
	}
	recordObjects := false
	if api == subscriptionAPITelemetry && in.SchemaVersion != "" {
		// A version this build does not know is refused rather than
		// half-honoured: an extension pinned to a future schema would
		// otherwise get records in a shape it was written not to expect.
		objects, known := telemetrySchemaVersions[in.SchemaVersion]
		if !known {
			http.Error(w, "unsupported schemaVersion", http.StatusBadRequest)
			return
		}
		recordObjects = objects
	}
	if !strings.EqualFold(in.Destination.Protocol, "HTTP") || strings.TrimSpace(in.Destination.URI) == "" {
		http.Error(w, "invalid destination", http.StatusBadRequest)
		return
	}
	if _, err := url.ParseRequestURI(in.Destination.URI); err != nil {
		http.Error(w, "invalid destination URI", http.StatusBadRequest)
		return
	}
	types := make(map[string]bool, len(in.Types))
	for _, typ := range in.Types {
		switch typ {
		case "platform", "function", "extension":
			types[typ] = true
		default:
			http.Error(w, "invalid log type", http.StatusBadRequest)
			return
		}
	}
	if len(types) == 0 {
		http.Error(w, "missing log types", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ext, ok := s.extensions[id]
	var containerIP, deliveryURI string
	var replayTarget telemetryTarget
	var existingAPI subscriptionAPI
	if ok {
		if ext.Logs != nil && ext.Logs.API != api {
			existingAPI = ext.Logs.API
		} else {
			// The destination is stored exactly as the extension subscribed
			// it. It used to be rewritten from loopback to the container's
			// bridge IP, because the POST was made from this process; it is
			// made by the environment's own init now, from inside the sandbox
			// where that loopback address is the listener the extension
			// actually stood up. See telemetry_relay.go.
			deliveryURI = in.Destination.URI
			ext.Logs = &extensionLogsSubscription{
				Types:         types,
				URI:           deliveryURI,
				Buffering:     resolveBuffering(&in),
				API:           api,
				RecordObjects: recordObjects,
			}
			replayTarget = telemetryTarget{containerIP: ext.ContainerIP, uri: deliveryURI, recordObjects: recordObjects, buffering: ext.Logs.Buffering}
			containerIP = ext.ContainerIP
		}
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid Lambda-Extension-Identifier", http.StatusForbidden)
		return
	}
	if existingAPI != "" {
		// AWS documents the refusal but not its shape; 400 with the other
		// API's name is an inference, chosen to fail loudly and locally.
		http.Error(w, "already subscribed via the "+string(existingAPI), http.StatusBadRequest)
		return
	}
	if types["platform"] {
		// The INIT phase this extension was started in has already reported
		// itself, possibly before the extension's process existed. Those
		// records are its business, so it gets them now.
		s.deliverRetainedInitRecords(containerIP, replayTarget)
	}
	// The subscription is itself a platform event, named for the surface it
	// came through: the Logs API documents platform.logsSubscription, the
	// Telemetry API platform.telemetrySubscription — same {name, state,
	// types} record either way. Published after the subscription is stored,
	// so the subscriber that asked is among the recipients, which is the only
	// way its own example record can ever be seen.
	subscriptionEventType := "platform.telemetrySubscription"
	if api == subscriptionAPILogs {
		subscriptionEventType = "platform.logsSubscription"
	}
	if event, err := json.Marshal(map[string]any{
		"time": s.clk.Now().UTC().Format(time.RFC3339Nano),
		"type": subscriptionEventType,
		"record": map[string]any{
			"name":  ext.Name,
			"state": "Subscribed",
			"types": in.Types,
		},
	}); err == nil {
		s.PublishPlatformRecord(containerIP, string(event))
	}
	if api == subscriptionAPITelemetry {
		// The documented success body is the JSON string "OK".
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"OK"`))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// PublishExtensionLog publishes one log line — a "function" or "extension"
// record — to the subscribers of that type on this container.
//
// What the record *is* depends on the subscriber. The Logs API and the oldest
// Telemetry schemaVersion embed the line as a string; from schemaVersion
// 2022-12-13 a JSON-format function's line is embedded as the JSON object it
// already is ("If the schema version you're using is older than the 2022-12-13
// version, then the record is always rendered as a string even when your
// function's logging format is configured as JSON" —
// https://docs.aws.amazon.com/lambda/latest/dg/telemetry-schema-reference.html).
// A Text-format function's line stays a string for everyone, as does a line
// that is not itself a JSON object. Each shape is marshalled at most once,
// however many subscribers want it.
func (s *RuntimeAPIServer) PublishExtensionLog(containerIP, typ, record string) {
	if record == "" {
		return
	}
	targets, jsonFormat := s.telemetryTargets(containerIP, typ)
	if len(targets) == 0 {
		return
	}
	now := s.clk.Now().UTC().Format(time.RFC3339Nano)
	asObject := jsonFormat && isJSONObjectLine(record)
	var stringEvent, objectEvent []byte
	for _, target := range targets {
		var event []byte
		var err error
		if target.recordObjects && asObject {
			if objectEvent == nil {
				objectEvent, err = json.Marshal(map[string]any{
					"time": now, "type": typ, "record": json.RawMessage(record),
				})
			}
			event = objectEvent
		} else {
			if stringEvent == nil {
				stringEvent, err = json.Marshal(map[string]any{
					"time": now, "type": typ, "record": record,
				})
			}
			event = stringEvent
		}
		if err != nil {
			return
		}
		s.bufferTelemetryEvent(target, event)
	}
}

// isJSONObjectLine reports whether a log line is one complete JSON object —
// the only shape a modern-schema subscriber is handed as an object.
func isJSONObjectLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed))
}

// telemetryTarget is one subscriber's delivery destination, record shape and
// batching bounds.
type telemetryTarget struct {
	// containerIP is the execution environment the subscriber belongs to. It
	// is part of a destination's identity, not a place anything is sent: two
	// environments' extensions routinely subscribe the same loopback address,
	// and before the delivery moved into the sandbox they were told apart only
	// because the host rewrote each to its own container IP.
	containerIP   string
	uri           string
	recordObjects bool
	buffering     bufferingConfig
}

// key names one destination's buffer: a destination is a subscriber's URI
// *within* an execution environment.
func (t telemetryTarget) key() string { return telemetryBufferKey(t.containerIP, t.uri) }

// telemetryBufferKey names a destination. The separator cannot appear in the
// address half, so no pair of (environment, URI) can collide with another.
func telemetryBufferKey(containerIP, uri string) string { return containerIP + "|" + uri }

// telemetryTargets returns every subscriber to typ on this container, and
// whether the container's function logs in JSON format — the two facts a
// publisher needs to choose each subscriber's record shape.
func (s *RuntimeAPIServer) telemetryTargets(containerIP, typ string) ([]telemetryTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var targets []telemetryTarget
	for _, ext := range s.extensions {
		if ext.ContainerIP != containerIP || ext.Logs == nil || !ext.Logs.Types[typ] {
			continue
		}
		targets = append(targets, telemetryTarget{containerIP: containerIP, uri: ext.Logs.URI, recordObjects: ext.Logs.RecordObjects, buffering: ext.Logs.Buffering})
	}
	return targets, s.containerConfigs[containerIP].LogFormat == logFormatJSON
}

// PublishPlatformRecord publishes one platform event to the subscribers of the
// "platform" type. event is the complete Telemetry API event, already
// marshalled by the caller that owns the schema (see logging_json.go): the
// {"time","type","record"} envelope, whose type is the event's own —
// "platform.initStart", "platform.start", … — and whose record is an object.
// That is what AWS documents, and it is why this does not go through
// PublishExtensionLog, whose record is a string.
func (s *RuntimeAPIServer) PublishPlatformRecord(containerIP, event string) {
	if event == "" {
		return
	}
	s.publishTelemetryEvent(containerIP, "platform", json.RawMessage(event))
}

// initPhaseRecordsRetained bounds the per-container replay buffer: the three
// INIT-phase records plus one platform.extension per registered extension.
// Environments run a handful of extensions at most; one that somehow exceeds
// the bound stops retaining rather than growing without limit, which also
// covers a container nothing ever subscribes to.
const initPhaseRecordsRetained = 16

// PublishInitPhaseRecord publishes one of the three init-phase platform events
// and remembers it for a subscription that has not been made yet — see
// RuntimeAPIServer.initRecords for why those three need that and no others.
func (s *RuntimeAPIServer) PublishInitPhaseRecord(containerIP, event string) {
	if event == "" || containerIP == "" {
		return
	}
	s.mu.Lock()
	held := s.initRecords[containerIP]
	if len(held) < initPhaseRecordsRetained {
		s.initRecords[containerIP] = append(held, json.RawMessage(event))
	}
	s.mu.Unlock()
	s.publishTelemetryEvent(containerIP, "platform", json.RawMessage(event))
}

// deliverRetainedInitRecords sends a fresh platform subscription the init-phase
// events its container published before it existed, in the order they
// happened. Called once, as the subscription is created.
func (s *RuntimeAPIServer) deliverRetainedInitRecords(containerIP string, target telemetryTarget) {
	s.mu.Lock()
	held := append([]json.RawMessage(nil), s.initRecords[containerIP]...)
	s.mu.Unlock()
	// Through the subscription's own buffer, like every other record: they
	// arrive as one batch (or open the first mixed one), in order, and a
	// full delivery queue is absorbed by the batching rather than answered
	// with a drop — if a cut batch is shed anyway, the drop accounting says
	// so on the next one, which is the guarantee these records exist for.
	for _, event := range held {
		s.bufferTelemetryEvent(target, event)
	}
}

// hasTelemetrySubscribers reports whether anything on this container is
// subscribed to typ. It exists so a caller on the invoke path can skip building
// a record nobody will read: in Text mode the platform records are marshalled
// for subscribers alone, and the overwhelmingly common case is that there are
// none.
func (s *RuntimeAPIServer) hasTelemetrySubscribers(containerIP, typ string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ext := range s.extensions {
		if ext.ContainerIP == containerIP && ext.Logs != nil && ext.Logs.Types[typ] {
			return true
		}
	}
	return false
}

// publishTelemetryEvent queues one event for delivery to every extension
// subscribed to typ on this container. Delivery is asynchronous and
// best-effort: a full queue drops the event rather than holding up whatever
// produced it, which is on the invoke path.
func (s *RuntimeAPIServer) publishTelemetryEvent(containerIP, typ string, event any) {
	targets, _ := s.telemetryTargets(containerIP, typ)
	if len(targets) == 0 {
		return
	}
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	for _, target := range targets {
		s.bufferTelemetryEvent(target, body)
	}
}

// telemetryBuffer batches one destination's events between deliveries, per
// the subscription's buffering configuration: a batch is cut when it reaches
// maxItems or maxBytes, or when the oldest event in it has waited timeoutMs —
// the same three bounds AWS documents. Cut batches ride the existing delivery
// queue and its retry, so batching changes when a POST happens, never whether
// a record survives a hiccup.
//
// The buffer also owns the destination's drop accounting: a batch the queue
// sheds or the workers exhaust is counted here, and the next batch cut for
// this destination opens with one platform.logsDropped event carrying the
// accumulated counts. Emitting at the cut is what makes the report unable to
// amplify itself: a dropped batch containing a logsDropped event simply folds
// back into the counters, and a destination that never recovers accumulates
// numbers rather than events.
type telemetryBuffer struct {
	mu          sync.Mutex
	containerIP string
	uri         string
	cfg         bufferingConfig
	events      []json.RawMessage
	bytes       int64
	timer       *clock.Timer

	droppedRecords int64
	droppedBytes   int64
}

// bufferFor returns the destination's buffer, creating or reconfiguring it.
func (s *RuntimeAPIServer) telemetryBufferFor(target telemetryTarget) *telemetryBuffer {
	s.buffersMu.Lock()
	defer s.buffersMu.Unlock()
	key := target.key()
	buf, ok := s.telemetryBuffers[key]
	if !ok {
		buf = &telemetryBuffer{containerIP: target.containerIP, uri: target.uri, cfg: target.buffering}
		s.telemetryBuffers[key] = buf
		return buf
	}
	buf.mu.Lock()
	buf.cfg = target.buffering
	buf.mu.Unlock()
	return buf
}

// bufferTelemetryEvent adds one marshalled event to a destination's batch,
// cutting it if a size bound is reached and arming the timeout for the first
// event of a fresh batch.
func (s *RuntimeAPIServer) bufferTelemetryEvent(target telemetryTarget, event []byte) {
	buf := s.telemetryBufferFor(target)
	buf.mu.Lock()
	buf.events = append(buf.events, json.RawMessage(event))
	buf.bytes += int64(len(event))
	if int64(len(buf.events)) >= buf.cfg.MaxItems || buf.bytes >= buf.cfg.MaxBytes {
		s.cutBatchLocked(buf)
		buf.mu.Unlock()
		return
	}
	if buf.timer == nil {
		buf.timer = s.clk.AfterFunc(buf.cfg.Timeout, func() { s.flushTelemetryBuffer(buf) })
	}
	buf.mu.Unlock()
}

// flushTelemetryBuffer cuts whatever the destination's batch holds. The
// timeout path, and the shutdown sweep.
func (s *RuntimeAPIServer) flushTelemetryBuffer(buf *telemetryBuffer) {
	buf.mu.Lock()
	s.cutBatchLocked(buf)
	buf.mu.Unlock()
}

// cutBatchLocked turns the buffered events into one delivery. Caller holds
// buf.mu.
func (s *RuntimeAPIServer) cutBatchLocked(buf *telemetryBuffer) {
	if buf.timer != nil {
		buf.timer.Stop()
		buf.timer = nil
	}
	if len(buf.events) == 0 && buf.droppedRecords == 0 {
		return
	}
	events := buf.events
	if buf.droppedRecords > 0 {
		// The destination is told what it missed before it gets what it
		// didn't. Shape per AWS's own example (runtimes-logs-api.html,
		// platform.logsDropped): reason, droppedRecords, droppedBytes.
		dropped, err := json.Marshal(map[string]any{
			"time": s.clk.Now().UTC().Format(time.RFC3339Nano),
			"type": "platform.logsDropped",
			"record": map[string]any{
				"reason":         "Consumer seems to have fallen behind as it has not acknowledged receipt of logs.",
				"droppedRecords": buf.droppedRecords,
				"droppedBytes":   buf.droppedBytes,
			},
		})
		if err == nil {
			events = append([]json.RawMessage{json.RawMessage(dropped)}, events...)
		}
		buf.droppedRecords, buf.droppedBytes = 0, 0
	}
	buf.events = nil
	buf.bytes = 0
	body, err := json.Marshal(events)
	if err != nil {
		return
	}
	select {
	case s.logsDeliveries <- extensionLogDelivery{ContainerIP: buf.containerIP, URI: buf.uri, Body: body, Events: int64(len(events))}:
	default:
		// The queue itself is the backpressure of last resort — shed the
		// batch and let the counters carry the news on the next cut.
		s.noteDroppedLocked(buf, int64(len(events)), int64(len(body)))
		s.logger.Debug("runtime api: shedding a telemetry batch because the delivery queue is full", zap.String("uri", buf.uri))
	}
}

// noteDroppedLocked folds a lost delivery into the destination's counters.
// Caller holds buf.mu.
func (s *RuntimeAPIServer) noteDroppedLocked(buf *telemetryBuffer, records, bytes int64) {
	buf.droppedRecords += records
	buf.droppedBytes += bytes
}

// noteDeliveryDropped is the worker-side report: a batch failed every attempt.
func (s *RuntimeAPIServer) noteDeliveryDropped(containerIP, uri string, records, bytes int64) {
	s.buffersMu.Lock()
	buf, ok := s.telemetryBuffers[telemetryBufferKey(containerIP, uri)]
	s.buffersMu.Unlock()
	if !ok {
		return
	}
	buf.mu.Lock()
	s.noteDroppedLocked(buf, records, bytes)
	buf.mu.Unlock()
}

// dropTelemetryBuffers forgets an environment's destinations. Called as the
// environment is unregistered: its subscribers went with it, and a buffer kept
// past that point would hold drop counters nothing will ever report.
func (s *RuntimeAPIServer) dropTelemetryBuffers(containerIP string) {
	if containerIP == "" {
		return
	}
	s.buffersMu.Lock()
	defer s.buffersMu.Unlock()
	for key, buf := range s.telemetryBuffers {
		if buf.containerIP == containerIP {
			delete(s.telemetryBuffers, key)
		}
	}
}

// flushTelemetryBuffers cuts every destination's pending batch — the shutdown
// sweep, mirroring AWS's flush when an input stream closes.
func (s *RuntimeAPIServer) flushTelemetryBuffers() {
	s.buffersMu.Lock()
	buffers := make([]*telemetryBuffer, 0, len(s.telemetryBuffers))
	for _, buf := range s.telemetryBuffers {
		buffers = append(buffers, buf)
	}
	s.buffersMu.Unlock()
	for _, buf := range buffers {
		s.flushTelemetryBuffer(buf)
	}
}

func (s *RuntimeAPIServer) logsDeliveryWorker() {
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case delivery := <-s.logsDeliveries:
			s.deliverExtensionLog(client, delivery)
		case <-s.done:
			return
		}
	}
}

// deliverExtensionLog posts one delivery, retrying a transport failure so a
// hiccup does not lose the record. Only the send is retried — a response, of
// any status, means the destination received the bytes and the delivery is
// done. An endpoint that stays dead costs its attempts and is then dropped at
// Warn: that is the one remaining way to lose a record, and it is no longer
// silent.
func (s *RuntimeAPIServer) deliverExtensionLog(client *http.Client, delivery extensionLogDelivery) {
	for attempt := 1; ; attempt++ {
		err := s.postExtensionLog(client, delivery)
		if err == nil {
			return
		}
		if attempt == extensionLogDeliveryAttempts {
			s.logger.Warn("runtime api: dropping a telemetry delivery — its destination failed every attempt",
				zap.String("uri", delivery.URI),
				zap.Int("attempts", attempt),
				zap.Error(err),
			)
			// The destination's own stream carries the news: the counts fold
			// into its buffer and the next cut batch opens with a
			// platform.logsDropped event.
			s.noteDeliveryDropped(delivery.ContainerIP, delivery.URI, delivery.Events, int64(len(delivery.Body)))
			return
		}
		s.logger.Debug("runtime api: telemetry delivery failed, retrying",
			zap.String("uri", delivery.URI), zap.Int("attempt", attempt), zap.Error(err))
		select {
		case <-s.clk.After(time.Duration(attempt) * extensionLogDeliveryRetryDelay):
		case <-s.done:
			return
		}
	}
}

// postExtensionLog is one delivery attempt.
//
// The batch reaches an extension's listener the way AWS's platform delivers it:
// from inside the execution environment, over the channel the environment's own
// init opened. Nothing here connects to a container — which is what makes
// telemetry work on Docker Desktop, where the engine is in a VM the host has no
// route into (#1799).
//
// The direct POST below is what remains for a Runtime API server with no
// per-environment listeners, which is no container at all: it is the surface
// the unit tests drive, where the subscribed destination is a server on this
// host and is reached exactly as subscribed. A real environment always has a
// listener, so it always has a relay, and an init too old to poll it costs the
// batch its attempts and is reported as a drop — never a POST at a loopback
// address that means something else entirely on this side of the sandbox.
func (s *RuntimeAPIServer) postExtensionLog(client *http.Client, delivery extensionLogDelivery) error {
	if relay := s.telemetryRelayFor(delivery.ContainerIP); relay != nil {
		ctx, cancel := context.WithTimeout(context.Background(), telemetryRelayAttempt)
		defer cancel()
		return relay.deliver(ctx, s.done, delivery)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URI, bytes.NewReader(delivery.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}

func (s *RuntimeAPIServer) writeExtensionEvent(w http.ResponseWriter, event extensionEvent) {
	w.Header().Set("Lambda-Extension-Event-Identifier", event.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(event.Body)
}

// handleNext serves GET /2018-06-01/runtime/invocation/next.
// This is a long-poll: the container blocks here until an invocation arrives.
func (s *RuntimeAPIServer) handleNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Identify the calling execution environment — by the listener the request
	// arrived on, falling back to its source address.
	//
	// Race window: the container's runtime starts polling /next the instant
	// it boots, but overcast can't register it until InspectContainer reports
	// the IP (up to several seconds later on slow Docker hosts). Early polls
	// would 403 and the RIC would give up with an init error. Wait a bounded
	// window for registration to catch up — the registration is almost always
	// in flight when we land here.
	containerIP, functionARN, known := s.lookupContainerWait(r.Context(), r)
	if !known {
		s.logUnknownContainer(r)
		http.Error(w, "unknown container", http.StatusForbidden)
		return
	}

	// Detect the first GET /next from this container — signals that the RIC
	// (and the language runtime + handler code) has finished initialising.
	//
	// Say so in the log. It is one line per execution environment, and it is
	// the only positive record that a container ever reached the Runtime API:
	// without it, a successful cold start and one that never made contact leave
	// Overcast's log looking exactly the same, and #800 spent its diagnosis
	// reading that silence as proof of the second.
	// What the init had published when the runtime went idle. Everything at or
	// below it belongs before the invocation this poll is about to be given,
	// and Invoke waits for it before writing that invocation's START.
	if seq, err := strconv.ParseUint(r.Header.Get(initproto.HeaderLogSeq), 10, 64); err == nil && seq > 0 {
		if sink := s.logSinkForPort(localPortOf(r)); sink != nil {
			sink.noteIdleSeq(seq)
		}
	}

	if s.noteFirstNext(containerIP) {
		s.logger.Debug("runtime api: execution environment finished INIT",
			zap.String("function", functionNameFromARN(functionARN)),
			zap.String("container_ip", containerIP),
			zap.Int("arrived_on_port", localPortOf(r)))
	}

	s.mu.Lock()
	// Check the function's invocation queue first.
	if inv := s.popQueuedLocked(functionARN); inv != nil {
		// Do NOT delete from s.pending here — handleInvocationAction needs it
		// to route the container's response POST back to the caller's ResultCh.
		s.enqueueExtensionInvokeLocked(containerIP, inv)
		s.mu.Unlock()
		s.writeNextResponse(w, inv)
		return
	}

	// No pending invocation — park this container and long-poll. Each container
	// gets its own waiter: a function may be served by several execution
	// environments at once, and a shared slot meant the last one to poll
	// displaced the rest, leaving them blocked on a channel nothing would send
	// to while queued work went undelivered until its deadline expired.
	waiter := &runtimeWaiter{ch: make(chan *pendingInvocation, 1), containerIP: containerIP}
	s.waiting[functionARN] = append(s.waiting[functionARN], waiter)
	s.mu.Unlock()

	ctx := r.Context()
	select {
	case inv := <-waiter.ch:
		s.mu.Lock()
		if inv.cancelled {
			// Cancelled between hand-off and pickup — take the next queued
			// invocation instead of running work the caller has abandoned.
			inv = s.popQueuedLocked(functionARN)
		}
		if inv == nil {
			s.waiting[functionARN] = append(s.waiting[functionARN], waiter)
			s.mu.Unlock()
			s.awaitWaiter(w, r, functionARN, waiter, containerIP)
			return
		}
		s.enqueueExtensionInvokeLocked(containerIP, inv)
		s.mu.Unlock()
		s.writeNextResponse(w, inv)
	case <-ctx.Done():
		s.releaseWaiter(functionARN, waiter)
	case <-s.done:
		s.releaseWaiter(functionARN, waiter)
	}
}

// noteFirstNext records an execution environment's first GET /next — the point
// at which its RIC, language runtime and handler code have all initialised —
// and reports whether this call was it.
//
// It takes the lock itself rather than sharing handleNext's, so the log line
// that follows is written outside it. Nothing is lost by not holding the two
// together: readiness is per environment and this is the only writer of it,
// while the queue handleNext goes on to check is per function and already
// contended by every other environment serving that function.
func (s *RuntimeAPIServer) noteFirstNext(containerIP string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenNext[containerIP] {
		return false
	}
	s.seenNext[containerIP] = true
	s.firstNextAt[containerIP] = s.clk.Now()
	s.maybeMarkReadyLocked(containerIP)
	return true
}

// awaitWaiter re-enters the long poll after a claimed invocation turned out to
// be cancelled. Split out so handleNext stays a single level deep.
func (s *RuntimeAPIServer) awaitWaiter(w http.ResponseWriter, r *http.Request, functionARN string, waiter *runtimeWaiter, containerIP string) {
	select {
	case inv := <-waiter.ch:
		s.mu.Lock()
		if inv.cancelled {
			s.mu.Unlock()
			s.releaseWaiter(functionARN, waiter)
			return
		}
		s.enqueueExtensionInvokeLocked(containerIP, inv)
		s.mu.Unlock()
		s.writeNextResponse(w, inv)
	case <-r.Context().Done():
		s.releaseWaiter(functionARN, waiter)
	case <-s.done:
		s.releaseWaiter(functionARN, waiter)
	}
}

// popQueuedLocked removes and returns the next live invocation for functionARN,
// discarding any that were cancelled while queued. Caller must hold s.mu.
func (s *RuntimeAPIServer) popQueuedLocked(functionARN string) *pendingInvocation {
	queue := s.funcQueues[functionARN]
	for len(queue) > 0 {
		inv := queue[0]
		queue = queue[1:]
		if inv.cancelled {
			continue
		}
		s.funcQueues[functionARN] = queue
		return inv
	}
	s.funcQueues[functionARN] = queue
	return nil
}

// releaseWaiter removes a container's waiter when its poll ends. An invocation
// handed over in the same moment is put back on the queue rather than lost with
// the waiter — that is the difference between another container picking it up
// immediately and the caller waiting out the full function timeout.
func (s *RuntimeAPIServer) releaseWaiter(functionARN string, waiter *runtimeWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.waiting[functionARN]
	for i, w := range waiters {
		if w != waiter {
			continue
		}
		s.waiting[functionARN] = append(waiters[:i:i], waiters[i+1:]...)
		return
	}
	// Not in the list: SubmitInvocation already claimed it. Recover whatever it
	// was handed so the work is not stranded in a channel nobody will read.
	select {
	case inv := <-waiter.ch:
		if inv.cancelled {
			return
		}
		s.redeliverLocked(functionARN, inv)
	default:
	}
}

// redeliverLocked hands inv to another parked container, or puts it back at the
// head of the queue if none is available. Caller must hold s.mu.
func (s *RuntimeAPIServer) redeliverLocked(functionARN string, inv *pendingInvocation) {
	if waiters := s.waiting[functionARN]; len(waiters) > 0 {
		next := waiters[0]
		s.waiting[functionARN] = waiters[1:]
		next.ch <- inv
		return
	}
	s.funcQueues[functionARN] = append([]*pendingInvocation{inv}, s.funcQueues[functionARN]...)
}

func (s *RuntimeAPIServer) maybeMarkReadyLocked(containerIP string) {
	if !s.isReadyLocked(containerIP) {
		return
	}
	ready, ok := s.ready[containerIP]
	if !ok {
		return
	}
	cfg := s.containerConfigs[containerIP]
	close(ready)
	delete(s.ready, containerIP)
	if cb := s.OnFirstNext; cb != nil {
		go cb(cfg.FunctionARN, cfg.ContainerID)
	}
}

func (s *RuntimeAPIServer) isReadyLocked(containerIP string) bool {
	if !s.seenNext[containerIP] {
		return false
	}
	cfg, ok := s.containerConfigs[containerIP]
	if !ok {
		return false
	}
	registered := s.containerExts[containerIP]
	for _, name := range cfg.ExpectedExtensions {
		if !registered[name] {
			return false
		}
	}
	return true
}

// writeNextResponse sends the invocation event to the container with
// the required Runtime API headers.
func (s *RuntimeAPIServer) writeNextResponse(w http.ResponseWriter, inv *pendingInvocation) {
	w.Header().Set("Lambda-Runtime-Aws-Request-Id", inv.RequestID)
	w.Header().Set("Lambda-Runtime-Deadline-Ms", fmt.Sprintf("%d", inv.Deadline.UnixMilli()))
	w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", inv.FunctionARN)
	w.Header().Set("Lambda-Runtime-Trace-Id", inv.TraceID)
	// Lambda-Runtime-Client-Context and Lambda-Runtime-Cognito-Identity are
	// only set by real Lambda for mobile-SDK invocations. Omitting them when
	// absent matches AWS behaviour and keeps RICs that check for header
	// presence happy.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(inv.Event)
}

// newXRayTraceID generates a syntactically-valid X-Ray trace header with
// Sampled=0 so SDKs don't attempt to ship segments to a daemon that isn't
// running.
func newXRayTraceID(now time.Time) string {
	var buf [20]byte
	_, _ = rand.Read(buf[:])
	rootRand := hex.EncodeToString(buf[0:12])
	parent := hex.EncodeToString(buf[12:20])
	return fmt.Sprintf("Root=1-%08x-%s;Parent=%s;Sampled=0", uint32(now.Unix()), rootRand, parent)
}

// InvocationTraceID reports the X-Amzn-Trace-Id header a pending invocation's
// runtime receives, or "" once the invocation is no longer pending.
func (s *RuntimeAPIServer) InvocationTraceID(reqID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv, ok := s.pending[reqID]; ok {
		return inv.TraceID
	}
	return ""
}

// handleInvocationAction routes POST .../invocation/{id}/response and .../invocation/{id}/error.
func (s *RuntimeAPIServer) handleInvocationAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request ID and action from the path.
	// Path format: /2018-06-01/runtime/invocation/{id}/response
	//           or /2018-06-01/runtime/invocation/{id}/error
	path := strings.TrimPrefix(r.URL.Path, "/2018-06-01/runtime/invocation/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	reqID := parts[0]
	action := parts[1]

	// The in-container init adds this on the way through: every log frame at or
	// below it was published on the log stream before this request was
	// forwarded. The invoke path waits for the ingest to catch up to it before
	// it writes END/REPORT and snapshots the tail. Absent — a Runtime API
	// client that is not behind our init — reads as zero, which is "nothing to
	// wait for"; so does a value we cannot parse, because refusing the response
	// over a log header would be a far worse failure than a tail that is one
	// line short.
	logSeq, seqErr := strconv.ParseUint(r.Header.Get(initproto.HeaderLogSeq), 10, 64)
	if seqErr != nil {
		// Including overflow, where ParseUint hands back MaxUint64 rather than
		// zero: waiting for a sequence no init will ever reach would cost every
		// such invocation the whole bound.
		logSeq = 0
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 6*1024*1024+1024)) // 6MB + buffer
	if err != nil {
		http.Error(w, "read body failed", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	inv, ok := s.pending[reqID]
	if ok {
		delete(s.pending, reqID)
	}
	s.mu.Unlock()

	if !ok {
		// The invocation was already delivered or timed out.
		// This can happen if the caller's context was cancelled.
		s.logger.Debug("runtime api: invocation not found (already completed or timed out)",
			zap.String("request_id", reqID), zap.String("action", action))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch action {
	case "response":
		inv.ResultCh <- invokeResponse{Payload: body, LogSeq: logSeq}
	case "error":
		// Every error a runtime reports here — a thrown exception, a
		// Runtime.ExitError, anything — reaches the invoker as
		// X-Amz-Function-Error: Unhandled, exactly as AWS reports it; the
		// error class itself travels in the payload's errorType. "Handled" is
		// a value no current AWS runtime produces for an exception (it dates
		// from the callback-era Node.js runtimes), and defaulting to it here
		// was a fidelity gap the compat suites' InvokeWithError exposed.
		inv.ResultCh <- invokeResponse{
			FunctionError: "Unhandled",
			ErrorPayload:  body,
			LogSeq:        logSeq,
		}
	default:
		http.Error(w, "unknown action: "+action, http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleInitError serves POST /2018-06-01/runtime/init/error.
// Called when the container fails during cold start (e.g. import error).
func (s *RuntimeAPIServer) handleInitError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	errorType := r.Header.Get("Lambda-Runtime-Function-Error-Type")

	s.logger.Error("lambda container init error",
		zap.String("error_type", errorType),
		zap.String("body", string(body)))

	// Find the pending invocation for this container. The container sends the
	// request ID in the Lambda-Runtime-Aws-Request-Id header (set by the RIC
	// from the /next response). If not present, we try to match any pending
	// invocation — in practice there's usually exactly one.
	reqID := r.Header.Get("Lambda-Runtime-Aws-Request-Id")

	s.mu.Lock()
	var inv *pendingInvocation
	if reqID != "" {
		inv = s.pending[reqID]
		delete(s.pending, reqID)
	} else {
		// Grab any pending invocation (best effort).
		for id, p := range s.pending {
			inv = p
			delete(s.pending, id)
			break
		}
	}
	s.mu.Unlock()

	if inv != nil {
		// Build an error payload matching AWS format.
		errResp := map[string]string{
			"errorMessage": "Runtime initialization failed: " + errorType,
			"errorType":    errorType,
		}
		payload, _ := json.Marshal(errResp)
		inv.ResultCh <- invokeResponse{
			FunctionError: "Unhandled",
			IsInitError:   true,
			ErrorPayload:  payload,
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

// Stop gracefully shuts down the Runtime API server. Idempotent.
func (s *RuntimeAPIServer) Stop(ctx context.Context) error {
	// Close the done channel first so long-polling handleNext requests
	// unblock and complete. Without this, Shutdown blocks waiting for
	// in-flight requests that will never finish on their own. Once-guarded
	// because Service.Stop, which drives this, is reachable more than once;
	// http.Server.Shutdown is already idempotent.
	// Pending batches are cut before the workers' stop signal, mirroring
	// AWS's flush when an input stream closes; whatever the workers can
	// still deliver, they do, and the rest is bounded by ctx.
	s.flushTelemetryBuffers()
	s.stopOnce.Do(func() { close(s.done) })
	return s.server.Shutdown(ctx)
}
