package lambdaweb

// metrics.go — CloudWatch metrics and bus events for the data plane (docs:
// web-functions-monitoring-metrics in the Lambda Developer Guide).
//
//	AWS/Lambda/Web         FunctionName+EndpointName: ExecutedRequests,
//	                       HTTPResponseStatusClass4XX/5XX (sparse)
//	                       FunctionName: EnvironmentActiveTime, EnvironmentCPUTime,
//	                       EnvironmentMaxMemoryUsage, EnvironmentConcurrentRequests,
//	                       ResponseBytes (once a minute per active environment)
//	AWS/Lambda/Web/Usage   FunctionName+EndpointName+ExecutionRegion:
//	                       ExecutedRequests, EndpointActiveEnvironments and the
//	                       sparse throttle metrics
//
// Request facts are aggregated in memory by the worker and handed to the
// recorder every MetricsInterval, as one observation per series, so a load test
// costs a few observations per interval and not one per request.
//
// Regions: AWS centralises AWS/Lambda/Web into the function's home Region and
// keeps Usage metrics in the serving Region. Overcast's metrics recorder
// (internal/metrics) is not regional — an Observation carries no Region and the
// recorder stores one series set — so every observation lands in that one set;
// the Usage metrics keep their ExecutionRegion dimension, which is what tells
// Regions apart.

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/metrics"
)

const (
	metricsNSWeb   = "AWS/Lambda/Web"
	metricsNSUsage = "AWS/Lambda/Web/Usage"
)

// throttle metric indices into agg.thr.
const (
	thrAccountRequest = iota
	thrEndpointRequest
	thrAccountVCPU
	thrEndpointEnv
	thrKinds
)

var throttleMetricNames = [thrKinds]string{
	"AccountRequestThrottles", "EndpointRequestThrottles",
	"AccountActiveVCPUArm64Throttles", "EndpointActiveEnvironmentThrottles",
}

// throttleKind maps a throttle error type to its one metric. Platform.
// EndpointConcurrentInitThrottle increments none (documented by AWS).
func throttleKind(errType string) (int, bool) {
	switch errType {
	case errTypeAccountRequestThrottle:
		return thrAccountRequest, true
	case errTypeEndpointRequestThrot:
		return thrEndpointRequest, true
	case errTypeAccountVCPUThrottle:
		return thrAccountVCPU, true
	case errTypeEndpointMaxEnvThrottle:
		return thrEndpointEnv, true
	}
	return 0, false
}

type aggKey struct{ region, function, endpoint string }

// agg is one (Region, function, endpoint)'s counts since the last flush.
type agg struct {
	executed int64
	c4, c5   int64
	thr      [thrKinds]int64
}

func (t *Telemetry) aggFor(region, function, endpoint string) *agg {
	k := aggKey{region, function, endpoint}
	a := t.aggs[k]
	if a == nil {
		a = &agg{}
		t.aggs[k] = a
	}
	return a
}

func countStatus(a *agg, status int) {
	switch {
	case status >= 400 && status < 500:
		a.c4++
	case status >= 500 && status < 600:
		a.c5++
	}
}

func (t *Telemetry) handleRequest(e requestEvent) {
	if e.Env.Function == "" {
		return
	}
	if e.Env.Endpoint != "" {
		a := t.aggFor(e.Env.Region, e.Env.Function, e.Env.Endpoint)
		countStatus(a, e.Status)
		if e.Env.Environment != "" {
			a.executed++
		}
	}
	if e.Env.Environment == "" {
		return // refused before an environment saw it
	}
	if st := t.envs[e.Env.Environment]; st != nil {
		st.winRequests++
		st.winBytes += e.ResponseBytes
		c := e.Concurrent
		if c < 1 {
			c = 1
		}
		if c > st.winConc {
			st.winConc = c
		}
		if c > st.allConc {
			st.allConc = c
		}
	}
	t.emit(e.Env, sysDebug, "platform.requestComplete", e.Start.Add(e.Duration), recRequestComplete{
		RequestID: e.RequestID, EndpointName: e.Env.Endpoint, Result: e.Status, ErrorType: e.ErrorType,
		Metrics: metricsRequest{DurationMs: ms(e.Duration), ResponseBytes: e.ResponseBytes},
	})
	t.coalesceRequest(e)
}

func (t *Telemetry) handleThrottle(e throttleEvent) {
	if e.Function == "" || e.Endpoint == "" {
		return
	}
	a := t.aggFor(e.Region, e.Function, e.Endpoint)
	a.c4++ // a throttle is a 429 and counts toward HTTPResponseStatusClass4XX
	if k, ok := throttleKind(e.ErrorType); ok {
		a.thr[k]++
	}
}

func (t *Telemetry) observe(name, ns, unit string, dims []metrics.Dimension, at time.Time, v float64) {
	_, m, _ := t.cfg()
	if m == nil {
		return
	}
	if err := m.Observe(context.Background(), metrics.Observation{
		Namespace: ns, Name: name, Dimensions: dims, Timestamp: at, Unit: unit, Value: v,
	}); err != nil {
		t.log.Debug("lambdaweb: metrics observe failed", zap.String("metric", name), zap.Error(err))
	}
}

func fnDims(fn string) []metrics.Dimension {
	return []metrics.Dimension{{Name: "FunctionName", Value: fn}}
}

// flushRequestMetrics hands the interval's aggregates to the recorder. Only
// series with a non-zero value are observed: the sparse metrics never publish a
// zero data point.
func (t *Telemetry) flushRequestMetrics(now time.Time) {
	if len(t.aggs) == 0 {
		return
	}
	active := map[aggKey]int{}
	for _, st := range t.envs {
		active[aggKey{st.ref.Region, st.ref.Function, st.ref.Endpoint}]++
	}
	for k, a := range t.aggs {
		web := []metrics.Dimension{{Name: "FunctionName", Value: k.function}, {Name: "EndpointName", Value: k.endpoint}}
		usage := append(append([]metrics.Dimension(nil), web...),
			metrics.Dimension{Name: "ExecutionRegion", Value: k.region})
		if a.executed > 0 {
			t.observe("ExecutedRequests", metricsNSWeb, "Count", web, now, float64(a.executed))
			t.observe("ExecutedRequests", metricsNSUsage, "Count", usage, now, float64(a.executed))
		}
		if a.c4 > 0 {
			t.observe("HTTPResponseStatusClass4XX", metricsNSWeb, "Count", web, now, float64(a.c4))
		}
		if a.c5 > 0 {
			t.observe("HTTPResponseStatusClass5XX", metricsNSWeb, "Count", web, now, float64(a.c5))
		}
		throttled := false
		for i, n := range a.thr {
			if n > 0 {
				throttled = true
				t.observe(throttleMetricNames[i], metricsNSUsage, "Count", usage, now, float64(n))
			}
		}
		if a.executed > 0 || throttled {
			t.observe("EndpointActiveEnvironments", metricsNSUsage, "Count", usage, now, float64(active[k]))
		}
	}
	clear(t.aggs)
}

// observeEnvWindow publishes one environment's once-a-minute data points.
func (t *Telemetry) observeEnvWindow(st *envState, now time.Time, cpu time.Duration) {
	d := fnDims(st.ref.Function)
	t.observe("EnvironmentActiveTime", metricsNSWeb, "Milliseconds", d, now, ms(now.Sub(st.lastActive)))
	t.observe("EnvironmentCPUTime", metricsNSWeb, "Milliseconds", d, now, ms(cpu))
	t.observe("EnvironmentMaxMemoryUsage", metricsNSWeb, "Megabytes", d, now, float64(mb(st.maxMem)))
	t.observe("EnvironmentConcurrentRequests", metricsNSWeb, "Count", d, now, float64(st.winConc))
	t.observe("ResponseBytes", metricsNSWeb, "Bytes", d, now, float64(st.winBytes))
}

// observeEnvFinal publishes the last data points when an environment stops.
func (t *Telemetry) observeEnvFinal(st *envState, e spinDownEvent, end time.Time) {
	cpu := e.Stats.CPUTime - st.cpuSent
	if cpu < 0 {
		cpu = 0
	}
	if e.Stats.MaxMemoryMB > st.maxMem {
		st.maxMem = e.Stats.MaxMemoryMB
	}
	t.observeEnvWindow(st, end, cpu)
}

// observeEnvActive publishes the active time of an environment that never
// finished initialising.
func (t *Telemetry) observeEnvActive(ref envRef, d time.Duration, at time.Time) {
	t.observe("EnvironmentActiveTime", metricsNSWeb, "Milliseconds", fnDims(ref.Function), at, ms(d))
}

// ─── Bus events ──────────────────────────────────────────────────────────────

// coalesced is the pending RequestCompleted event for one endpoint. The rule:
// the first request after a quiet spell of at least RequestEventSpacing
// publishes immediately; requests within the spacing are folded into one event
// (Count = how many, last request's status, duration and environment, ColdStart
// if any was) that the next tick publishes once the spacing has elapsed. A load
// test therefore produces at most one event per endpoint per spacing (1 s by
// default), whatever its request rate.
type coalesced struct {
	last    time.Time
	pending int
	cold    bool
	e       requestEvent
}

func (t *Telemetry) busOrNil() *events.Bus {
	_, _, b := t.cfg()
	return b
}

func (t *Telemetry) publishEnv(typ events.Type, ref envRef, reason string, at time.Time) {
	b := t.busOrNil()
	if b == nil {
		return
	}
	b.Publish(context.Background(), events.Event{
		Type: typ, Time: at, Source: serviceName,
		Payload: events.LambdaWebEnvironmentPayload{
			Function: ref.Function, Region: ref.Region, Revision: ref.RevisionID,
			Environment: ref.Environment, Endpoint: ref.Endpoint, Reason: reason,
		},
	})
}

func (t *Telemetry) coalesceRequest(e requestEvent) {
	if t.busOrNil() == nil {
		return
	}
	now := t.clk.Now()
	k := aggKey{e.Env.Region, e.Env.Function, e.Env.Endpoint}
	c := t.bcast[k]
	if c == nil {
		c = &coalesced{}
		t.bcast[k] = c
	}
	c.pending++
	c.cold = c.cold || e.ColdStart
	c.e = e
	if c.last.IsZero() || now.Sub(c.last) >= t.opts.RequestEventSpacing {
		t.publishRequest(c, now)
	}
}

func (t *Telemetry) publishRequest(c *coalesced, now time.Time) {
	b := t.busOrNil()
	if b != nil && c.pending > 0 {
		e := c.e
		b.Publish(context.Background(), events.Event{
			Type: events.LambdaWebRequestCompleted, Time: now, Source: serviceName,
			Payload: events.LambdaWebRequestPayload{
				Function: e.Env.Function, Region: e.Env.Region, Endpoint: e.Env.Endpoint,
				Revision: e.Env.RevisionID, Environment: e.Env.Environment, Status: e.Status,
				DurationMs: ms(e.Duration), ColdStart: c.cold, Count: c.pending,
			},
		})
	}
	c.pending, c.cold, c.last = 0, false, now
}

// flushRequestEvents publishes coalesced events whose spacing has elapsed (all
// of them when force) and forgets endpoints that have gone quiet.
func (t *Telemetry) flushRequestEvents(now time.Time, force bool) {
	for k, c := range t.bcast {
		switch {
		case c.pending > 0 && (force || now.Sub(c.last) >= t.opts.RequestEventSpacing):
			t.publishRequest(c, now)
		case c.pending == 0 && now.Sub(c.last) > time.Minute:
			delete(t.bcast, k)
		}
	}
}
