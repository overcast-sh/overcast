package lambdaweb

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/events/eventstest"
	"github.com/overcast-sh/overcast/internal/metrics"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// ─── fakes ───────────────────────────────────────────────────────────────────

type fakeLogs struct {
	mu      sync.Mutex
	streams map[string][]string // "region|group|stream" -> messages
	ensured []string
	block   chan struct{} // when non-nil, WriteLogEvents waits for it to close
}

func newFakeLogs() *fakeLogs { return &fakeLogs{streams: map[string][]string{}} }

func (f *fakeLogs) EnsureLogGroup(context.Context, string) error { return nil }
func (f *fakeLogs) EnsureLogStream(ctx context.Context, g, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, middleware.RegionFromContext(ctx, "?")+"|"+g+"|"+s)
	return nil
}
func (f *fakeLogs) WriteLogEvents(ctx context.Context, g, s string, es []events.LogEntry) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := middleware.RegionFromContext(ctx, "?") + "|" + g + "|" + s
	for _, e := range es {
		f.streams[k] = append(f.streams[k], e.Message)
	}
	return nil
}
func (f *fakeLogs) lines(region, group, stream string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, m := range f.streams[region+"|"+group+"|"+stream] {
		var v map[string]any
		if err := json.Unmarshal([]byte(m), &v); err != nil {
			panic(err)
		}
		out = append(out, v)
	}
	return out
}
func types(ls []map[string]any) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l["type"].(string))
	}
	return out
}

type fakeMetrics struct {
	mu  sync.Mutex
	obs []metrics.Observation
}

func (f *fakeMetrics) Observe(_ context.Context, o metrics.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

// find returns the observations of a metric in a namespace.
func (f *fakeMetrics) find(ns, name string) []metrics.Observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []metrics.Observation
	for _, o := range f.obs {
		if o.Namespace == ns && o.Name == name {
			out = append(out, o)
		}
	}
	return out
}
func (f *fakeMetrics) sum(ns, name string) float64 {
	var s float64
	for _, o := range f.find(ns, name) {
		s += o.Value
	}
	return s
}
func dimStr(o metrics.Observation) string {
	var p []string
	for _, d := range o.Dimensions {
		p = append(p, d.Name+"="+d.Value)
	}
	sort.Strings(p)
	return strings.Join(p, ",")
}

type fakeEnv struct {
	stats envStats
}

func (e *fakeEnv) ID() string                                { return "env-1" }
func (e *fakeEnv) Addr() string                              { return "127.0.0.1:1" }
func (e *fakeEnv) Done() <-chan struct{}                     { return nil }
func (e *fakeEnv) ExitReason() exitReason                    { return exitReason{} }
func (e *fakeEnv) Stats(context.Context) envStats            { return e.stats }
func (e *fakeEnv) Stop(context.Context, time.Duration) error { return nil }

// ─── harness ─────────────────────────────────────────────────────────────────

type tHarness struct {
	t    *testing.T
	clk  *clock.Mock
	tel  *Telemetry
	logs *fakeLogs
	met  *fakeMetrics
	bus  *events.Bus
}

var t0 = time.Date(2026, 9, 25, 11, 12, 30, 893_000_000, time.UTC)

func newHarness(t *testing.T, mut func(*TelemetryOptions)) *tHarness {
	t.Helper()
	clk := clock.NewMock()
	clk.Set(t0)
	opts := TelemetryOptions{Clock: clk}
	if mut != nil {
		mut(&opts)
	}
	h := &tHarness{t: t, clk: clk, tel: NewTelemetry(opts), logs: newFakeLogs(), met: &fakeMetrics{}, bus: events.NewBusWithClock(clk)}
	h.tel.InitLogWriter(h.logs)
	h.tel.InitMetrics(h.met)
	h.tel.InitBus(h.bus)
	t.Cleanup(func() { h.tel.Stop(context.Background()); h.bus.Stop() })
	return h
}

func ref(level string) envRef {
	return envRef{
		Region: "us-east-1", Function: "my-web-app", Endpoint: "default", RevisionID: "rev-fs0fq",
		Environment: "52754708-44f1-407a-a1fd-6d2a98175a45", LogGroup: "/aws/lambda/web/my-web-app",
		LogStream: "2026/09/25/[rev-fs0fq]abc", SysLogLevel: level,
	}
}

func (h *tHarness) start(r envRef) {
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: h.clk.Now(), Duration: 250 * time.Millisecond})
	h.clk.Add(250 * time.Millisecond)
	h.tel.sync()
}

func (h *tHarness) lines(r envRef) []map[string]any {
	return h.logs.lines(r.Region, r.LogGroup, r.LogStream)
}

// ─── system logs ─────────────────────────────────────────────────────────────

func TestSystemLogInitSuccessShapesAndLocalCompanion(t *testing.T) {
	// Given a telemetry sink with INFO system logs
	h := newHarness(t, nil)
	r := ref("INFO")

	// When an environment initialises successfully
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: t0, Duration: 250 * time.Millisecond})
	h.tel.sync()

	// Then initStart, initReport and invokeStart are written with the schema's shape
	got := h.lines(r)
	require.Equal(t, []string{"platform.initStart", "platform.initReport", "platform.invokeStart"}, types(got))
	want := `{"timestamp":"2026-09-25T11:12:30.893Z","level":"INFO","type":"platform.initStart","region":"us-east-1",
	 "record":{"functionName":"my-web-app","revisionId":"rev-fs0fq","instanceId":"52754708-44f1-407a-a1fd-6d2a98175a45","initializationType":"native-http"}}`
	assertJSONEq(t, want, got[0])
	want = `{"timestamp":"2026-09-25T11:12:31.143Z","level":"INFO","type":"platform.initReport","region":"us-east-1",
	 "record":{"status":"success","initializationType":"native-http","metrics":{"totalDurationMs":250,"totalCpuTimeMs":0,"maxMemoryUsageMb":0}}}`
	assertJSONEq(t, want, got[1])

	// And the same lines are in the .local companion group, same Region and stream
	local := h.logs.lines(r.Region, r.LogGroup+".local", r.LogStream)
	assert.Equal(t, got, local)
}

func assertJSONEq(t *testing.T, want string, got any) {
	t.Helper()
	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(b))
}

func TestSystemLogInitFailureIsWarnWithErrorType(t *testing.T) {
	// Given a WARN-level sink
	h := newHarness(t, nil)
	r := ref("WARN")

	// When initialisation fails
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: t0, Duration: 320 * time.Millisecond,
		Failure: &initFailure{Type: errTypeImportCode, Detail: "boom"}})
	h.tel.sync()

	// Then only the failing initReport is delivered, WARN, with errorType, and no invokeStart
	got := h.lines(r)
	require.Equal(t, []string{"platform.initReport"}, types(got))
	assertJSONEq(t, `{"timestamp":"2026-09-25T11:12:31.213Z","level":"WARN","type":"platform.initReport","region":"us-east-1",
	 "record":{"status":"failure","errorType":"Runtime.ImportCodeError","initializationType":"native-http","metrics":{"totalDurationMs":320,"totalCpuTimeMs":0,"maxMemoryUsageMb":0}}}`, got[0])
	assert.Empty(t, eventstest.Published(h.bus, events.LambdaWebEnvironmentStarted))
}

func TestSystemLogLevelFilteringForRequestComplete(t *testing.T) {
	for _, tc := range []struct {
		level string
		want  int
	}{{"DEBUG", 1}, {"INFO", 0}, {"WARN", 0}, {"", 0}} {
		t.Run("level_"+tc.level, func(t *testing.T) {
			// Given a sink at this systemLogLevel and a running environment
			h := newHarness(t, nil)
			r := ref(tc.level)
			h.start(r)

			// When a request completes (a timeout, so errorType is present)
			h.tel.RequestDone(requestEvent{Env: r, RequestID: "REQ1", Status: 504, ErrorType: errTypeRequestTimeout,
				Start: h.clk.Now(), Duration: 3001229 * time.Microsecond, ResponseBytes: 0, Concurrent: 1})
			h.tel.sync()

			// Then platform.requestComplete is delivered only at DEBUG
			n := 0
			for _, l := range h.lines(r) {
				if l["type"] == "platform.requestComplete" {
					n++
					assert.Equal(t, "DEBUG", l["level"])
					assert.Equal(t, map[string]any{"requestId": "REQ1", "endpointName": "default", "result": float64(504),
						"errorType": "Runtime.RequestTimeout", "metrics": map[string]any{"durationMs": 3001.229, "responseBytes": float64(0)}}, l["record"])
				}
			}
			assert.Equal(t, tc.want, n)
		})
	}
}

func TestSystemLogWarnFilterDropsInfoButKeepsFailures(t *testing.T) {
	// Given a WARN-level sink and an environment that crashes
	h := newHarness(t, nil)
	r := ref("WARN")
	h.start(r)

	// When it exits on its own with a non-zero status
	h.tel.EnvironmentSpinDown(spinDownEvent{Env: r, Started: h.clk.Now(), Duration: 10 * time.Millisecond,
		Reason: exitReason{Code: 1}, Requests: 3})
	h.tel.sync()

	// Then only the WARN reports remain
	got := h.lines(r)
	assert.Equal(t, []string{"platform.invokeReport", "platform.spinDownReport"}, types(got))
	assert.Equal(t, "Runtime.ExitError", got[0]["record"].(map[string]any)["errorType"])
	assert.Equal(t, "Runtime.ExitError", got[1]["record"].(map[string]any)["errorType"])
}

func TestSpinDownReports(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sd        spinDownEvent
		invoke    string // invokeReport errorType
		spin      string // spinDownReport errorType
		busReason string
	}{
		{"clean stop", spinDownEvent{Reason: exitReason{Requested: true}}, "", "", "requested"},
		{"sigkill after grace", spinDownEvent{Reason: exitReason{Requested: true, Code: 137}, Timeout: true}, "", "Runtime.SpinDownTimeout", "requested"},
		{"non-zero exit on stop", spinDownEvent{Reason: exitReason{Requested: true, Code: 2}}, "", "Runtime.ExitError", "requested"},
		{"oom", spinDownEvent{Reason: exitReason{OOM: true, Code: 137}}, "Runtime.OutOfMemory", "Runtime.ExitError", "oom"},
		{"exit zero unrequested", spinDownEvent{}, "Runtime.Exit", "", "exited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a running environment at the default level
			h := newHarness(t, nil)
			r := ref("INFO")
			h.start(r)

			// When it spins down
			tc.sd.Env, tc.sd.Started, tc.sd.Duration = r, h.clk.Now(), 21*time.Millisecond
			tc.sd.Stats = envStats{MaxMemoryMB: 157, CPUTime: 973 * time.Millisecond}
			tc.sd.Requests = 4
			h.tel.EnvironmentSpinDown(tc.sd)
			h.tel.sync()

			// Then the reports carry the expected error types and totals
			byType := map[string]map[string]any{}
			for _, l := range h.lines(r) {
				byType[l["type"].(string)] = l
			}
			inv := byType["platform.invokeReport"]["record"].(map[string]any)
			sd := byType["platform.spinDownReport"]["record"].(map[string]any)
			assert.Equal(t, tc.invoke, str(inv["errorType"]))
			assert.Equal(t, tc.spin, str(sd["errorType"]))
			assert.Equal(t, "INFO", byType["platform.spinDownStart"]["level"])
			m := inv["metrics"].(map[string]any)
			assert.EqualValues(t, 4, m["totalRequests"])
			assert.EqualValues(t, 973, m["totalCpuTimeMs"])
			assert.EqualValues(t, 157, m["maxMemoryUsageMb"])

			// And EnvironmentStopped is published with the reason
			stopped := eventstest.Published(h.bus, events.LambdaWebEnvironmentStopped)
			require.Len(t, stopped, 1)
			assert.Equal(t, tc.busReason, stopped[0].Payload.(events.LambdaWebEnvironmentPayload).Reason)
		})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func TestInvokeSummaryCadence(t *testing.T) {
	// Given a running environment whose Stats grow
	h := newHarness(t, nil)
	r := ref("INFO")
	env := &fakeEnv{}
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: h.clk.Now(), Duration: 0, Handle: env})
	h.tel.sync()
	count := func() int {
		n := 0
		for _, l := range h.lines(r) {
			if l["type"] == "platform.invokeSummary" {
				n++
			}
		}
		return n
	}

	// When 59 s pass
	h.clk.Add(59 * time.Second)
	h.tel.sync()
	// Then nothing is summarised yet
	assert.Equal(t, 0, count())

	// When the 60 s mark passes, with traffic in the window
	env.stats = envStats{MaxMemoryMB: 154, CPUTime: 420 * time.Millisecond}
	for i := 0; i < 4; i++ {
		h.tel.RequestDone(requestEvent{Env: r, Status: 200, Concurrent: 1, ResponseBytes: 10, Start: h.clk.Now()})
	}
	h.tel.sync() // settle the requests before the clock crosses the window edge
	h.clk.Add(time.Second)
	h.tel.sync()

	// Then exactly one invokeSummary with the window's numbers
	require.Equal(t, 1, count())
	var sum map[string]any
	for _, l := range h.lines(r) {
		if l["type"] == "platform.invokeSummary" {
			sum = l
		}
	}
	rec := sum["record"].(map[string]any)
	assert.Equal(t, "2026-09-25T11:12:30.893Z", rec["startTime"])
	assert.Equal(t, map[string]any{"requests": float64(4), "concurrentRequests": float64(1), "cpuTimeMs": float64(420), "maxMemoryUsageMb": float64(154)}, rec["metrics"])

	// And an idle window follows 60 s later with requests 0
	h.clk.Add(60 * time.Second)
	h.tel.sync()
	require.Equal(t, 2, count())
	// And the per-environment metrics were published once per window
	assert.Len(t, h.met.find(metricsNSWeb, "EnvironmentCPUTime"), 2)
	assert.EqualValues(t, 420, h.met.find(metricsNSWeb, "EnvironmentCPUTime")[0].Value)
	assert.EqualValues(t, 0, h.met.find(metricsNSWeb, "EnvironmentCPUTime")[1].Value)
	assert.EqualValues(t, 154, h.met.find(metricsNSWeb, "EnvironmentMaxMemoryUsage")[0].Value)
	assert.EqualValues(t, 40, h.met.find(metricsNSWeb, "ResponseBytes")[0].Value)
	assert.EqualValues(t, 60000, h.met.find(metricsNSWeb, "EnvironmentActiveTime")[0].Value)
	assert.Equal(t, "FunctionName=my-web-app", dimStr(h.met.find(metricsNSWeb, "ResponseBytes")[0]))
}

func TestSystemLogGroupDefaultsAndRegion(t *testing.T) {
	// Given an env ref with no explicit log group, served from another Region
	h := newHarness(t, nil)
	r := ref("INFO")
	r.LogGroup, r.Region = "", "eu-west-1"

	// When it initialises
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: t0})
	h.tel.sync()

	// Then lines land in /aws/lambda/web/{function} and its .local companion in that Region
	assert.NotEmpty(t, h.logs.lines("eu-west-1", "/aws/lambda/web/my-web-app", r.LogStream))
	assert.NotEmpty(t, h.logs.lines("eu-west-1", "/aws/lambda/web/my-web-app.local", r.LogStream))
	assert.Empty(t, h.logs.lines("us-east-1", "/aws/lambda/web/my-web-app", r.LogStream))
}

func TestLogOverflowReportsLogsDropped(t *testing.T) {
	// Given a log store that stalls and a tiny log queue
	h := newHarness(t, func(o *TelemetryOptions) { o.LogQueue = 2 })
	h.logs.block = make(chan struct{})
	r := ref("INFO")

	// When far more lines are produced than the queue holds
	const inits = 100
	for i := 0; i < inits; i++ {
		h.tel.EnvironmentInit(envInitEvent{Env: r, Started: t0, Duration: time.Millisecond, Failure: &initFailure{Type: errTypeInitCode}})
	}
	h.tel.barrierOnly()
	// and the store recovers (the queue drains) and one more event arrives
	close(h.logs.block)
	h.tel.sync()
	h.tel.EnvironmentInit(envInitEvent{Env: r, Started: t0, Duration: time.Millisecond, Failure: &initFailure{Type: errTypeInitCode}})
	h.tel.sync()

	// Then a WARN platform.logsDropped precedes the next delivered line, and
	// delivered + dropped accounts for every line produced (2 per init)
	var dropped, delivered int
	var sawDrop bool
	for _, l := range h.lines(r) {
		if l["type"] == "platform.logsDropped" {
			sawDrop = true
			assert.Equal(t, "WARN", l["level"])
			rec := l["record"].(map[string]any)
			assert.Equal(t, logsDroppedReason, rec["reason"])
			assert.Greater(t, rec["droppedBytes"], float64(0))
			dropped += int(rec["droppedRecords"].(float64))
		} else {
			delivered++
		}
	}
	require.True(t, sawDrop)
	assert.Equal(t, (inits+1)*2, delivered+dropped)
}

func TestLogWriterGetsEnsuredOnceAndToleratesNoWriter(t *testing.T) {
	// Given two events on one stream
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)
	h.tel.RequestDone(requestEvent{Env: r, Status: 200})
	h.tel.EnvironmentSpinDown(spinDownEvent{Env: r, Started: h.clk.Now(), Reason: exitReason{Requested: true}})
	h.tel.sync()

	// Then each of the two groups' stream is ensured exactly once, in the request Region
	h.logs.mu.Lock()
	assert.Equal(t, []string{"us-east-1|/aws/lambda/web/my-web-app|" + r.LogStream, "us-east-1|/aws/lambda/web/my-web-app.local|" + r.LogStream}, h.logs.ensured)
	h.logs.mu.Unlock()

	// And a sink with nothing wired does not panic
	bare := NewTelemetry(TelemetryOptions{Clock: clock.NewMock()})
	bare.EnvironmentInit(envInitEvent{Env: r})
	bare.RequestDone(requestEvent{Env: r})
	bare.Throttled(throttleEvent{Function: "f", Endpoint: "e"})
	bare.sync()
	bare.Stop(context.Background())
}

// ─── metrics ─────────────────────────────────────────────────────────────────

func TestRequestMetricsAreSparseAndBatched(t *testing.T) {
	// Given a running environment
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)

	// When only 2xx and 3xx responses are served
	for i := 0; i < 5; i++ {
		h.tel.RequestDone(requestEvent{Env: r, Status: 200 + i%2*100, Concurrent: 1})
	}
	h.tel.sync()

	// Then ExecutedRequests is one batched observation in both namespaces and no error-class data point exists
	web := h.met.find(metricsNSWeb, "ExecutedRequests")
	require.Len(t, web, 1)
	assert.EqualValues(t, 5, web[0].Value)
	assert.Equal(t, "EndpointName=default,FunctionName=my-web-app", dimStr(web[0]))
	assert.Equal(t, "Count", web[0].Unit)
	use := h.met.find(metricsNSUsage, "ExecutedRequests")
	require.Len(t, use, 1)
	assert.Equal(t, "EndpointName=default,ExecutionRegion=us-east-1,FunctionName=my-web-app", dimStr(use[0]))
	assert.Empty(t, h.met.find(metricsNSWeb, "HTTPResponseStatusClass4XX"))
	assert.Empty(t, h.met.find(metricsNSWeb, "HTTPResponseStatusClass5XX"))
	assert.Empty(t, h.met.find(metricsNSUsage, "AccountRequestThrottles"))
	// and EndpointActiveEnvironments reports the live count
	act := h.met.find(metricsNSUsage, "EndpointActiveEnvironments")
	require.Len(t, act, 1)
	assert.EqualValues(t, 1, act[0].Value)

	// When 4xx and 5xx responses follow
	h.tel.RequestDone(requestEvent{Env: r, Status: 404})
	h.tel.RequestDone(requestEvent{Env: r, Status: 503})
	h.tel.RequestDone(requestEvent{Env: r, Status: 503})
	h.tel.sync()

	// Then the status classes appear with their counts
	assert.EqualValues(t, 1, h.met.sum(metricsNSWeb, "HTTPResponseStatusClass4XX"))
	assert.EqualValues(t, 2, h.met.sum(metricsNSWeb, "HTTPResponseStatusClass5XX"))
	// and an idle flush adds nothing
	n := len(h.met.obs)
	h.tel.sync()
	assert.Len(t, h.met.obs, n)
}

func TestRequestRefusedBeforeEnvironmentIsNotExecuted(t *testing.T) {
	// Given a request refused before an environment saw it (no environment id)
	h := newHarness(t, nil)
	r := ref("INFO")
	r.Environment, r.LogStream = "", ""

	// When it completes with a 409
	h.tel.RequestDone(requestEvent{Env: r, Status: 409, ErrorType: errTypeRevisionNotActive})
	h.tel.sync()

	// Then it counts as 4XX but not as an executed request, and logs nothing
	assert.Empty(t, h.met.find(metricsNSWeb, "ExecutedRequests"))
	assert.Empty(t, h.met.find(metricsNSUsage, "ExecutedRequests"))
	assert.EqualValues(t, 1, h.met.sum(metricsNSWeb, "HTTPResponseStatusClass4XX"))
	assert.Empty(t, h.logs.streams)
}

func TestThrottleMapsToExactlyOneMetric(t *testing.T) {
	for _, tc := range []struct{ errType, metric string }{
		{errTypeAccountRequestThrottle, "AccountRequestThrottles"},
		{errTypeEndpointRequestThrot, "EndpointRequestThrottles"},
		{errTypeAccountVCPUThrottle, "AccountActiveVCPUArm64Throttles"},
		{errTypeEndpointMaxEnvThrottle, "EndpointActiveEnvironmentThrottles"},
		{errTypeConcurrentInitThrottle, ""},
	} {
		t.Run(tc.errType, func(t *testing.T) {
			// Given a sink
			h := newHarness(t, nil)

			// When a request is throttled for this reason
			h.tel.Throttled(throttleEvent{Region: "us-east-1", Function: "my-web-app", Endpoint: "default", ErrorType: tc.errType, At: t0})
			h.tel.sync()

			// Then exactly the mapped Usage metric is incremented (none for concurrent init)
			var usage []metrics.Observation
			for _, o := range h.met.obs {
				if o.Namespace == metricsNSUsage && o.Name != "EndpointActiveEnvironments" {
					usage = append(usage, o)
				}
			}
			if tc.metric == "" {
				assert.Empty(t, usage)
			} else {
				require.Len(t, usage, 1)
				assert.Equal(t, tc.metric, usage[0].Name)
				assert.EqualValues(t, 1, usage[0].Value)
				assert.Equal(t, "EndpointName=default,ExecutionRegion=us-east-1,FunctionName=my-web-app", dimStr(usage[0]))
			}
			// And it is a 429: counted in 4XX, never in ExecutedRequests
			assert.EqualValues(t, 1, h.met.sum(metricsNSWeb, "HTTPResponseStatusClass4XX"))
			assert.Empty(t, h.met.find(metricsNSWeb, "ExecutedRequests"))
		})
	}
}

func TestEnvironmentFinalMetricsOnSpinDown(t *testing.T) {
	// Given a running environment that served a request
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)
	h.tel.RequestDone(requestEvent{Env: r, Status: 200, Concurrent: 3, ResponseBytes: 40})

	// When it spins down 30 s later
	h.clk.Add(30 * time.Second)
	h.tel.EnvironmentSpinDown(spinDownEvent{Env: r, Started: h.clk.Now(), Duration: 20 * time.Millisecond,
		Reason: exitReason{Requested: true}, Stats: envStats{MaxMemoryMB: 99.6, CPUTime: 80 * time.Millisecond}, Requests: 1})
	h.tel.sync()

	// Then the final data points are published
	assert.EqualValues(t, 3, h.met.sum(metricsNSWeb, "EnvironmentConcurrentRequests"))
	assert.EqualValues(t, 100, h.met.sum(metricsNSWeb, "EnvironmentMaxMemoryUsage"))
	assert.EqualValues(t, 80, h.met.sum(metricsNSWeb, "EnvironmentCPUTime"))
	assert.EqualValues(t, 40, h.met.sum(metricsNSWeb, "ResponseBytes"))
	assert.EqualValues(t, 30270, h.met.sum(metricsNSWeb, "EnvironmentActiveTime")) // 250 ms init + the serving time + 20 ms shutdown, from init start
}

// ─── bus events ──────────────────────────────────────────────────────────────

func TestBusEnvironmentAndRequestEvents(t *testing.T) {
	// Given a running environment
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)

	// Then EnvironmentStarted carried the environment's identity
	started := eventstest.Published(h.bus, events.LambdaWebEnvironmentStarted)
	require.Len(t, started, 1)
	assert.Equal(t, events.LambdaWebEnvironmentPayload{Function: "my-web-app", Region: "us-east-1", Revision: "rev-fs0fq",
		Environment: r.Environment, Endpoint: "default", Reason: "initialized"}, started[0].Payload)

	// When a cold request completes
	h.tel.RequestDone(requestEvent{Env: r, Status: 200, Duration: 12 * time.Millisecond, ColdStart: true})
	h.tel.sync()

	// Then RequestCompleted is published right away with the documented payload
	got := eventstest.Published(h.bus, events.LambdaWebRequestCompleted)
	require.Len(t, got, 1)
	assert.Equal(t, events.LambdaWebRequestPayload{Function: "my-web-app", Region: "us-east-1", Endpoint: "default",
		Revision: "rev-fs0fq", Environment: r.Environment, Status: 200, DurationMs: 12, ColdStart: true, Count: 1}, got[0].Payload)
}

func TestBusRequestCompletedIsCoalesced(t *testing.T) {
	// Given a running environment and a 1 s coalescing spacing
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)
	published := func() []events.LambdaWebRequestPayload {
		var out []events.LambdaWebRequestPayload
		for _, e := range eventstest.Published(h.bus, events.LambdaWebRequestCompleted) {
			out = append(out, e.Payload.(events.LambdaWebRequestPayload))
		}
		return out
	}

	// When a burst of 1000 requests arrives within one spacing window
	for i := 0; i < 1000; i++ {
		st := 200
		if i == 999 {
			st = 500
		}
		h.tel.RequestDone(requestEvent{Env: r, Status: st, ColdStart: i == 3})
	}
	h.tel.sync()

	// Then only the leading event is out; the rest wait for the spacing
	require.Len(t, published(), 1)
	assert.Equal(t, 1, published()[0].Count)

	// When the spacing elapses
	h.clk.Add(1100 * time.Millisecond)
	h.tel.sync()

	// Then one coalesced event stands for the other 999, last status, cold start preserved
	p := published()
	require.Len(t, p, 2)
	assert.Equal(t, 999, p[1].Count)
	assert.Equal(t, 500, p[1].Status)
	assert.True(t, p[1].ColdStart)

	// And a quiet endpoint publishes nothing more
	h.clk.Add(10 * time.Second)
	h.tel.sync()
	assert.Len(t, published(), 2)
}

func TestRequestCompletedIsNoise(t *testing.T) {
	// Given the classification table
	// Then per-request web telemetry is noise and lifecycle events are not
	assert.True(t, events.IsNoise(events.LambdaWebRequestCompleted))
	assert.False(t, events.IsNoise(events.LambdaWebEnvironmentStarted))
	assert.False(t, events.IsNoise(events.LambdaWebEnvironmentStopped))
}

// ─── lifecycle ───────────────────────────────────────────────────────────────

func TestStopFlushesPendingTelemetry(t *testing.T) {
	// Given requests that have not reached a metrics flush yet
	h := newHarness(t, nil)
	r := ref("INFO")
	h.start(r)
	h.tel.RequestDone(requestEvent{Env: r, Status: 200, Concurrent: 1})
	h.tel.RequestDone(requestEvent{Env: r, Status: 200, Concurrent: 1})

	// When Stop runs
	h.tel.Stop(context.Background())

	// Then the metrics, the coalesced bus event and the log lines are out
	assert.EqualValues(t, 2, h.met.sum(metricsNSWeb, "ExecutedRequests"))
	assert.Len(t, eventstest.Published(h.bus, events.LambdaWebRequestCompleted), 2)
	assert.NotEmpty(t, h.lines(r))
	// and later events are ignored without blocking
	h.tel.RequestDone(requestEvent{Env: r, Status: 200})
	h.tel.Stop(context.Background())
}

func TestNoGoroutinesBeforeFirstEvent(t *testing.T) {
	// Given a freshly built Telemetry
	tel := NewTelemetry(TelemetryOptions{Clock: clock.NewMock()})
	// Then nothing has started, and Stop is a no-op
	assert.False(t, tel.started.Load())
	tel.Stop(context.Background())
}

func TestNewLogStreamName(t *testing.T) {
	// Given a fixed clock
	clk := clock.NewMock()
	clk.Set(t0)

	// When names are generated
	a, b := NewLogStreamName(clk, "rev-fs0fq"), NewLogStreamName(clk, "rev-fs0fq")

	// Then they follow YYYY/MM/DD/[revision]<32 hex> and are unique
	re := regexp.MustCompile(`^2026/09/25/\[rev-fs0fq\][0-9a-f]{32}$`)
	assert.Regexp(t, re, a)
	assert.Regexp(t, re, b)
	assert.NotEqual(t, a, b)
}

func TestConcurrentProducers(t *testing.T) {
	// Given many goroutines reporting at once (run with -race)
	h := newHarness(t, nil)
	r := ref("DEBUG")
	h.start(r)

	// When they report requests, throttles and lifecycle events concurrently
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				h.tel.RequestDone(requestEvent{Env: r, Status: 200, Concurrent: g})
				h.tel.Throttled(throttleEvent{Region: "us-east-1", Function: "my-web-app", Endpoint: "default", ErrorType: errTypeAccountRequestThrottle})
			}
		}(g)
	}
	wg.Wait()
	h.clk.Add(time.Minute)
	h.tel.sync()

	// Then nothing is lost while the queues have room
	assert.Zero(t, h.tel.Dropped())
	assert.EqualValues(t, 1600, h.met.sum(metricsNSWeb, "ExecutedRequests"))
	assert.EqualValues(t, 1600, h.met.sum(metricsNSUsage, "AccountRequestThrottles"))
}

// ─── benchmarks ──────────────────────────────────────────────────────────────

func BenchmarkRequestDone(b *testing.B) {
	tel := NewTelemetry(TelemetryOptions{EventQueue: 1 << 20})
	defer tel.Stop(context.Background())
	e := requestEvent{Env: ref("INFO"), RequestID: "r", Status: 200, Duration: time.Millisecond, Concurrent: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tel.RequestDone(e)
	}
}

func BenchmarkThrottled(b *testing.B) {
	tel := NewTelemetry(TelemetryOptions{EventQueue: 1 << 20})
	defer tel.Stop(context.Background())
	e := throttleEvent{Region: "us-east-1", Function: "f", Endpoint: "e", ErrorType: errTypeEndpointRequestThrot}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tel.Throttled(e)
	}
}

// sync drains the queues, runs the timers as of the clock's current time and
// waits until every resulting log line has been handed to the log writer. It
// is test-only (a send racing Stop would hit the closed log queue).
func (t *Telemetry) sync() {
	if !t.started.Load() {
		return
	}
	if !t.barrierOnly() {
		return
	}
	// The worker has queued its log lines; push a marker through the writer.
	m := logItem{done: make(chan struct{})}
	select {
	case t.logCh <- m:
		<-m.done
	case <-t.stopCh:
	}
}

// barrierOnly runs one worker barrier without waiting for log delivery.
func (t *Telemetry) barrierOnly() bool {
	if !t.started.Load() {
		return false
	}
	b := barrier{done: make(chan struct{})}
	select {
	case t.ctrlCh <- b:
		<-b.done
		return true
	case <-t.stopCh:
		return false
	}
}
