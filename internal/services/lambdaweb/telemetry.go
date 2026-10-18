package lambdaweb

// telemetry.go — the data plane's telemetry sink: system logs (systemlog.go),
// CloudWatch metrics (metrics.go) and bus events, behind the `telemetry`
// interface of dataplane.go.
//
// Shape: the four interface methods only copy their argument into a bounded
// channel with a non-blocking send (no allocation for requests and throttles)
// and return. One worker goroutine owns every piece of mutable state — the
// per-environment records, the metric aggregates, the bus coalescer — so none
// of it needs a lock. A second goroutine delivers log lines to CloudWatch Logs
// so a slow store can only back up (and drop) log lines, never telemetry
// aggregation or the request path. Both goroutines start lazily, on the first
// event, never in NewTelemetry.
//
// Time: every timestamp and every cadence (metric flush, invokeSummary, bus
// coalescing) comes from the injected clock, so tests drive them with a mock.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/metrics"
)

// Telemetry cadences and queue sizes. Each is a TelemetryOptions field default.
const (
	defaultTickInterval        = time.Second
	defaultMetricsInterval     = 10 * time.Second
	defaultSummaryInterval     = 60 * time.Second
	defaultRequestEventSpacing = time.Second
	defaultEventQueue          = 8192
	defaultLogQueue            = 4096
	defaultStatsTimeout        = time.Second
)

// TelemetryOptions configures NewTelemetry. Zero values take the defaults.
type TelemetryOptions struct {
	Clock  clock.Clock
	Logger *zap.Logger
	// TickInterval is how often the worker checks its timers (default 1 s).
	TickInterval time.Duration
	// MetricsInterval is how often aggregated request metrics are handed to
	// the recorder (default 10 s). The recorder rolls them into one-minute
	// buckets, so this only bounds how stale a chart can be.
	MetricsInterval time.Duration
	// SummaryInterval is the platform.invokeSummary and environment-metric
	// cadence per active environment (default 60 s).
	SummaryInterval time.Duration
	// RequestEventSpacing is the minimum gap between lambda-web:RequestCompleted
	// bus events for one endpoint (default 1 s); requests in between are
	// coalesced into the next event's Count.
	RequestEventSpacing time.Duration
	// EventQueue and LogQueue size the bounded queues (defaults 8192, 4096).
	EventQueue int
	LogQueue   int
	// StatsTimeout bounds one environment.Stats sample (default 1 s).
	StatsTimeout time.Duration
}

// metricsObserver is the part of *metrics.Service Telemetry uses.
type metricsObserver interface {
	Observe(ctx context.Context, o metrics.Observation) error
}

type lifeItem struct {
	init *envInitEvent
	spin *spinDownEvent
}

// barrier asks the worker to drain its queues and run its timers as of "now",
// then reports back. Tests use it to make the asynchronous pipeline
// deterministic; Stop uses it to flush.
type barrier struct {
	done chan struct{}
}

// Telemetry implements the `telemetry` interface.
type Telemetry struct {
	opts TelemetryOptions
	clk  clock.Clock
	log  *zap.Logger

	cfgMu   sync.RWMutex
	lw      events.LogWriter
	metrics metricsObserver
	bus     *events.Bus

	reqCh   chan requestEvent
	thrCh   chan throttleEvent
	lifeCh  chan lifeItem
	ctrlCh  chan barrier
	logCh   chan logItem
	stopCh  chan struct{}
	started atomic.Bool
	startMu sync.Mutex
	stopped atomic.Bool
	stopWg  sync.WaitGroup

	// Dropped counts events refused because a queue was full.
	droppedEvents atomic.Int64

	// Worker-owned state: touched only by the worker goroutine.
	envs  map[string]*telEnv
	aggs  map[aggKey]*agg
	bcast map[aggKey]*coalesced
	drops map[logKey]*dropState

	lastMetricsFlush time.Time
}

var _ telemetry = (*Telemetry)(nil)

// NewTelemetry builds a Telemetry. It starts nothing: the worker goroutines
// start on the first event.
func NewTelemetry(opts TelemetryOptions) *Telemetry {
	if opts.Clock == nil {
		opts.Clock = clock.New()
	}
	if opts.Logger == nil {
		opts.Logger = zap.NewNop()
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = defaultTickInterval
	}
	if opts.MetricsInterval <= 0 {
		opts.MetricsInterval = defaultMetricsInterval
	}
	if opts.SummaryInterval <= 0 {
		opts.SummaryInterval = defaultSummaryInterval
	}
	if opts.RequestEventSpacing <= 0 {
		opts.RequestEventSpacing = defaultRequestEventSpacing
	}
	if opts.EventQueue <= 0 {
		opts.EventQueue = defaultEventQueue
	}
	if opts.LogQueue <= 0 {
		opts.LogQueue = defaultLogQueue
	}
	if opts.StatsTimeout <= 0 {
		opts.StatsTimeout = defaultStatsTimeout
	}
	return &Telemetry{
		opts: opts, clk: opts.Clock, log: opts.Logger,
		reqCh:  make(chan requestEvent, opts.EventQueue),
		thrCh:  make(chan throttleEvent, opts.EventQueue),
		lifeCh: make(chan lifeItem, 1024),
		ctrlCh: make(chan barrier),
		logCh:  make(chan logItem, opts.LogQueue),
		stopCh: make(chan struct{}),
		envs:   map[string]*telEnv{},
		aggs:   map[aggKey]*agg{},
		bcast:  map[aggKey]*coalesced{},
		drops:  map[logKey]*dropState{},
	}
}

// InitLogWriter wires CloudWatch Logs. Without it system logs are discarded.
func (t *Telemetry) InitLogWriter(w events.LogWriter) {
	t.cfgMu.Lock()
	t.lw = w
	t.cfgMu.Unlock()
}

// InitMetrics wires the service-metrics recorder (*metrics.Service). Without it
// metrics are discarded.
func (t *Telemetry) InitMetrics(m metricsObserver) {
	t.cfgMu.Lock()
	t.metrics = m
	t.cfgMu.Unlock()
}

// InitBus wires the event bus. Without it no bus events are published.
func (t *Telemetry) InitBus(b *events.Bus) {
	t.cfgMu.Lock()
	t.bus = b
	t.cfgMu.Unlock()
}

// Dropped reports how many request, throttle or lifecycle events were refused
// because the event queue was full.
func (t *Telemetry) Dropped() int64 { return t.droppedEvents.Load() }

// ─── Hot path ────────────────────────────────────────────────────────────────

// EnvironmentInit implements telemetry.
func (t *Telemetry) EnvironmentInit(e envInitEvent) {
	t.enqueueLife(lifeItem{init: &e})
}

// EnvironmentSpinDown implements telemetry.
func (t *Telemetry) EnvironmentSpinDown(e spinDownEvent) {
	t.enqueueLife(lifeItem{spin: &e})
}

// RequestDone implements telemetry. It is a channel send: no lock, no
// allocation, never blocks.
func (t *Telemetry) RequestDone(e requestEvent) {
	if t.stopped.Load() {
		return
	}
	t.start()
	select {
	case t.reqCh <- e:
	default:
		t.droppedEvents.Add(1)
	}
}

// Throttled implements telemetry. Same cost as RequestDone.
func (t *Telemetry) Throttled(e throttleEvent) {
	if t.stopped.Load() {
		return
	}
	t.start()
	select {
	case t.thrCh <- e:
	default:
		t.droppedEvents.Add(1)
	}
}

func (t *Telemetry) enqueueLife(it lifeItem) {
	if t.stopped.Load() {
		return
	}
	t.start()
	select {
	case t.lifeCh <- it:
	default:
		t.droppedEvents.Add(1)
	}
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

// start launches the goroutines once.
func (t *Telemetry) start() {
	if t.started.Load() {
		return
	}
	t.startMu.Lock()
	defer t.startMu.Unlock()
	if t.started.Load() || t.stopped.Load() {
		return
	}
	// The ticker is created here, on the caller's goroutine, so a mock clock
	// that is advanced right after the first event still reaches it.
	tk := t.clk.Ticker(t.opts.TickInterval)
	t.lastMetricsFlush = t.clk.Now()
	t.stopWg.Add(2)
	go t.runWorker(tk)
	go t.runLogWriter()
	t.started.Store(true)
}

// Stop flushes everything queued — pending metrics, log lines, coalesced bus
// events — and ends the goroutines. It is a no-op when nothing ever started.
func (t *Telemetry) Stop(ctx context.Context) {
	t.startMu.Lock()
	if t.stopped.Swap(true) {
		t.startMu.Unlock()
		return
	}
	started := t.started.Load()
	t.startMu.Unlock()
	if !started {
		return
	}
	close(t.stopCh)
	done := make(chan struct{})
	go func() { t.stopWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.log.Warn("lambdaweb: telemetry stop timed out; some telemetry was not flushed")
	}
}

func (t *Telemetry) runWorker(tk *clock.Ticker) {
	defer t.stopWg.Done()
	defer tk.Stop()
	for {
		select {
		case e := <-t.reqCh:
			t.handleRequest(e)
		case e := <-t.thrCh:
			t.handleThrottle(e)
		case it := <-t.lifeCh:
			t.handleLife(it)
		case <-tk.C:
			t.periodic(t.clk.Now(), false, false)
		case b := <-t.ctrlCh:
			t.drain()
			t.periodic(t.clk.Now(), true, false)
			close(b.done)
		case <-t.stopCh:
			t.drain()
			t.periodic(t.clk.Now(), true, true)
			close(t.logCh) // the worker is the only producer
			return
		}
	}
}

// drain processes everything currently queued. Lifecycle items go first so a
// request's environment exists before the request is counted against it.
func (t *Telemetry) drain() {
	for {
		select {
		case it := <-t.lifeCh:
			t.handleLife(it)
			continue
		default:
		}
		select {
		case e := <-t.reqCh:
			t.handleRequest(e)
			continue
		default:
		}
		select {
		case e := <-t.thrCh:
			t.handleThrottle(e)
			continue
		default:
		}
		return
	}
}

func (t *Telemetry) handleLife(it lifeItem) {
	switch {
	case it.init != nil:
		t.handleInit(*it.init)
	case it.spin != nil:
		t.handleSpinDown(*it.spin)
	}
}

// periodic runs the timers: metric flush, per-environment summaries and the
// coalesced bus events. flushNow sends the request metrics regardless of the
// interval (barriers and Stop); final also publishes every coalesced bus event.
func (t *Telemetry) periodic(now time.Time, flushNow, final bool) {
	if flushNow || now.Sub(t.lastMetricsFlush) >= t.opts.MetricsInterval {
		t.flushRequestMetrics(now)
		t.lastMetricsFlush = now
	}
	t.summarize(now)
	t.flushRequestEvents(now, final)
}

// configuration snapshots, read by the worker only.

func (t *Telemetry) cfg() (events.LogWriter, metricsObserver, *events.Bus) {
	t.cfgMu.RLock()
	defer t.cfgMu.RUnlock()
	return t.lw, t.metrics, t.bus
}

// NewLogStreamName returns a log stream name for a new execution environment of
// the revision: "YYYY/MM/DD/[<revisionID>]<32 hex>". The format is inferred
// from the Lambda Developer Guide's description of a web function's log stream;
// AWS does not publish it for web functions.
func NewLogStreamName(clk clock.Clock, revisionID string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return clk.Now().UTC().Format("2006/01/02") + "/[" + revisionID + "]" + hex.EncodeToString(b[:])
}
