package lambdaweb

// systemlog.go — platform.* system log events (schema:
// web-functions-logging-system-log-schema in the Lambda Developer Guide) and the
// goroutine that delivers them to CloudWatch Logs.
//
// Every event goes to the revision's log group and to its ".local" companion,
// both in the Region that served the request, in the environment's log stream.
// The Docker provider writes the application's own lines into the same stream.

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// System log levels, ordered. An event is delivered when its level is at least
// the revision's systemLogLevel.
const (
	sysDebug = iota
	sysInfo
	sysWarn
)

var sysLevelNames = [...]string{"DEBUG", "INFO", "WARN"}

func sysLevelRank(name string) int {
	switch name {
	case "DEBUG":
		return sysDebug
	case "WARN":
		return sysWarn
	default:
		return sysInfo // INFO and the unset default
	}
}

const (
	logsDroppedReason = "Some logs were dropped because the downstream consumer is slower than the logs production rate"
	initTypeNative    = "native-http"
	errTypeSpinDownTO = "Runtime.SpinDownTimeout"
)

// sysEvent is the envelope every system event shares.
type sysEvent struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Type      string `json:"type"`
	Region    string `json:"region"`
	Record    any    `json:"record"`
}

type recInitStart struct {
	RuntimeVersion    string `json:"runtimeVersion,omitempty"`
	RuntimeVersionARN string `json:"runtimeVersionArn,omitempty"`
	FunctionName      string `json:"functionName"`
	RevisionID        string `json:"revisionId"`
	InstanceID        string `json:"instanceId"`
	InitializationTyp string `json:"initializationType"`
}

type metricsInit struct {
	TotalDurationMs float64 `json:"totalDurationMs"`
	TotalCPUTimeMs  float64 `json:"totalCpuTimeMs"`
	MaxMemoryMB     int     `json:"maxMemoryUsageMb"`
}

type recInitReport struct {
	Status            string      `json:"status"`
	ErrorType         string      `json:"errorType,omitempty"`
	InitializationTyp string      `json:"initializationType"`
	Metrics           metricsInit `json:"metrics"`
}

type recIdentity struct {
	FunctionName string `json:"functionName"`
	RevisionID   string `json:"revisionId"`
	InstanceID   string `json:"instanceId"`
}

type metricsRequest struct {
	DurationMs    float64 `json:"durationMs"`
	ResponseBytes int64   `json:"responseBytes"`
}

type recRequestComplete struct {
	RequestID    string         `json:"requestId"`
	EndpointName string         `json:"endpointName"`
	Result       int            `json:"result"`
	ErrorType    string         `json:"errorType,omitempty"`
	Metrics      metricsRequest `json:"metrics"`
}

type metricsSummary struct {
	Requests           int64   `json:"requests"`
	ConcurrentRequests int     `json:"concurrentRequests"`
	CPUTimeMs          float64 `json:"cpuTimeMs"`
	MaxMemoryMB        int     `json:"maxMemoryUsageMb"`
}

type recInvokeSummary struct {
	StartTime  string         `json:"startTime"`
	InstanceID string         `json:"instanceId"`
	Metrics    metricsSummary `json:"metrics"`
}

type metricsInvokeReport struct {
	TotalCPUTimeMs     float64 `json:"totalCpuTimeMs"`
	TotalDurationMs    float64 `json:"totalDurationMs"`
	TotalRequests      int64   `json:"totalRequests"`
	ConcurrentRequests int     `json:"concurrentRequests"`
	MaxMemoryMB        int     `json:"maxMemoryUsageMb"`
}

type recInvokeReport struct {
	Status     string              `json:"status"`
	ErrorType  string              `json:"errorType,omitempty"`
	InstanceID string              `json:"instanceId"`
	Metrics    metricsInvokeReport `json:"metrics"`
}

type recSpinDownReport struct {
	Status    string      `json:"status"`
	ErrorType string      `json:"errorType,omitempty"`
	Metrics   metricsInit `json:"metrics"` // same three fields as SpinDownReportMetrics
}

type recLogsDropped struct {
	Reason         string `json:"reason"`
	DroppedRecords int64  `json:"droppedRecords"`
	DroppedBytes   int64  `json:"droppedBytes"`
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*1000) / 1000 }
func mb(v float64) int           { return int(math.Round(v)) }

// telEnv is the worker's record of one live environment.
type telEnv struct {
	ref        envRef
	handle     environment // optional, for Stats
	servingAt  time.Time   // invokeStart
	lastTick   time.Time   // start of the current summary window
	lastActive time.Time   // end of the previous EnvironmentActiveTime datapoint
	cpuSent    time.Duration
	maxMem     float64

	winRequests int64
	winConc     int
	winBytes    int64
	allConc     int
}

func (t *Telemetry) handleInit(e envInitEvent) {
	end := e.Started.Add(e.Duration)
	t.emit(e.Env, sysInfo, "platform.initStart", e.Started, recInitStart{
		FunctionName: e.Env.Function, RevisionID: e.Env.RevisionID, InstanceID: e.Env.Environment,
		InitializationTyp: initTypeNative,
	})
	rep := recInitReport{Status: "success", InitializationTyp: initTypeNative,
		Metrics: metricsInit{TotalDurationMs: ms(e.Duration)}}
	level := sysInfo
	if e.Failure != nil {
		rep.Status, rep.ErrorType, level = "failure", e.Failure.Type, sysWarn
	}
	t.emit(e.Env, level, "platform.initReport", end, rep)

	if e.Failure != nil {
		t.observeEnvActive(e.Env, e.Duration, end)
		return
	}
	st := &telEnv{ref: e.Env, handle: e.Handle, servingAt: end, lastTick: end, lastActive: e.Started}
	t.envs[e.Env.Environment] = st
	t.emit(e.Env, sysInfo, "platform.invokeStart", end, recIdentity{
		FunctionName: e.Env.Function, RevisionID: e.Env.RevisionID, InstanceID: e.Env.Environment,
	})
	t.publishEnv(events.LambdaWebEnvironmentStarted, e.Env, "initialized", end)
}

func (t *Telemetry) handleSpinDown(e spinDownEvent) {
	st := t.envs[e.Env.Environment]
	if st == nil {
		st = &telEnv{ref: e.Env, servingAt: e.Started, lastTick: e.Started, lastActive: e.Started}
	}
	delete(t.envs, e.Env.Environment)
	end := e.Started.Add(e.Duration)

	rep := recInvokeReport{Status: "success", InstanceID: e.Env.Environment, Metrics: metricsInvokeReport{
		TotalCPUTimeMs: ms(e.Stats.CPUTime), TotalDurationMs: ms(e.Started.Sub(st.servingAt)),
		TotalRequests: e.Requests, ConcurrentRequests: st.allConc, MaxMemoryMB: mb(e.Stats.MaxMemoryMB),
	}}
	level, reason := sysInfo, "requested"
	switch {
	case e.Reason.OOM:
		rep.Status, rep.ErrorType, level, reason = "failure", errTypeOutOfMemory, sysWarn, "oom"
	case !e.Reason.Requested && e.Reason.Code != 0:
		rep.Status, rep.ErrorType, level, reason = "failure", errTypeExitError, sysWarn, "exited"
	case !e.Reason.Requested:
		rep.Status, rep.ErrorType, level, reason = "failure", errTypeExit, sysWarn, "exited"
	}
	t.emit(e.Env, level, "platform.invokeReport", e.Started, rep)
	t.emit(e.Env, sysInfo, "platform.spinDownStart", e.Started, recIdentity{
		FunctionName: e.Env.Function, RevisionID: e.Env.RevisionID, InstanceID: e.Env.Environment,
	})
	sd := recSpinDownReport{Status: "success", Metrics: metricsInit{
		TotalDurationMs: ms(e.Duration), TotalCPUTimeMs: 0, MaxMemoryMB: mb(e.Stats.MaxMemoryMB),
	}}
	level = sysInfo
	switch {
	case e.Timeout:
		sd.Status, sd.ErrorType, level = "failure", errTypeSpinDownTO, sysWarn
	case e.Reason.Code != 0:
		sd.Status, sd.ErrorType, level = "failure", errTypeExitError, sysWarn
	}
	t.emit(e.Env, level, "platform.spinDownReport", end, sd)

	t.observeEnvFinal(st, e, end)
	t.publishEnv(events.LambdaWebEnvironmentStopped, e.Env, reason, end)
}

// summarize emits platform.invokeSummary and the per-environment metrics for
// every environment whose summary window has elapsed.
func (t *Telemetry) summarize(now time.Time) {
	for _, st := range t.envs {
		if now.Sub(st.lastTick) < t.opts.SummaryInterval {
			continue
		}
		stats := t.sample(st)
		cpu := stats.CPUTime - st.cpuSent
		if cpu < 0 {
			cpu = 0
		}
		if stats.MaxMemoryMB > st.maxMem {
			st.maxMem = stats.MaxMemoryMB
		}
		t.emit(st.ref, sysInfo, "platform.invokeSummary", now, recInvokeSummary{
			StartTime: st.lastTick.UTC().Format(time.RFC3339Nano), InstanceID: st.ref.Environment,
			Metrics: metricsSummary{Requests: st.winRequests, ConcurrentRequests: st.winConc,
				CPUTimeMs: ms(cpu), MaxMemoryMB: mb(st.maxMem)},
		})
		t.observeEnvWindow(st, now, cpu)
		st.cpuSent += cpu
		st.lastTick, st.lastActive = now, now
		st.winRequests, st.winConc, st.winBytes = 0, 0, 0
	}
}

func (t *Telemetry) sample(st *telEnv) envStats {
	if st.handle == nil {
		return envStats{MaxMemoryMB: st.maxMem, CPUTime: st.cpuSent}
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.opts.StatsTimeout)
	defer cancel()
	return st.handle.Stats(ctx)
}

// ─── Queueing and delivery ───────────────────────────────────────────────────

type logKey struct{ region, group, stream string }

type dropState struct{ records, bytes int64 }

type logItem struct {
	key  logKey
	ts   int64
	msg  string
	done chan struct{} // set on a flush marker
}

// emit queues one system event, honouring the revision's systemLogLevel. It
// runs on the worker only.
func (t *Telemetry) emit(ref envRef, level int, typ string, at time.Time, record any) {
	if ref.LogStream == "" || level < sysLevelRank(ref.SysLogLevel) {
		return
	}
	lw, _, _ := t.cfg()
	if lw == nil {
		return
	}
	group := ref.LogGroup
	if group == "" {
		group = "/aws/lambda/web/" + ref.Function
	}
	line, err := json.Marshal(sysEvent{
		Timestamp: at.UTC().Format("2006-01-02T15:04:05.000Z"), Level: sysLevelNames[level],
		Type: typ, Region: ref.Region, Record: record,
	})
	if err != nil {
		t.log.Debug("lambdaweb: marshal system log event", zap.String("type", typ), zap.Error(err))
		return
	}
	key := logKey{region: ref.Region, group: group, stream: ref.LogStream}
	ds := t.drops[key]
	if ds != nil && ds.records > 0 {
		// Report what was lost before the next record that gets through.
		drop, _ := json.Marshal(sysEvent{
			Timestamp: at.UTC().Format("2006-01-02T15:04:05.000Z"), Level: "WARN",
			Type: "platform.logsDropped", Region: ref.Region,
			Record: recLogsDropped{Reason: logsDroppedReason, DroppedRecords: ds.records, DroppedBytes: ds.bytes},
		})
		if t.trySend(logItem{key: key, ts: at.UnixMilli(), msg: string(drop)}) {
			ds.records, ds.bytes = 0, 0
		}
	}
	if !t.trySend(logItem{key: key, ts: at.UnixMilli(), msg: string(line)}) {
		if ds == nil {
			ds = &dropState{}
			t.drops[key] = ds
		}
		ds.records++
		ds.bytes += int64(len(line))
	}
}

func (t *Telemetry) trySend(it logItem) bool {
	select {
	case t.logCh <- it:
		return true
	default:
		return false
	}
}

// runLogWriter delivers queued lines in batches, one write per stream and
// target group.
func (t *Telemetry) runLogWriter() {
	defer t.stopWg.Done()
	ensured := map[logKey]bool{}
	for first := range t.logCh {
		batch := []logItem{first}
	fill:
		for len(batch) < 256 {
			select {
			case it, ok := <-t.logCh:
				if !ok {
					break fill
				}
				batch = append(batch, it)
			default:
				break fill
			}
		}
		t.writeBatch(batch, ensured)
		for _, it := range batch {
			if it.done != nil {
				close(it.done)
			}
		}
	}
}

func (t *Telemetry) writeBatch(batch []logItem, ensured map[logKey]bool) {
	lw, _, _ := t.cfg()
	if lw == nil {
		return
	}
	if len(ensured) > 4096 {
		clear(ensured) // streams are per environment; do not remember them forever
	}
	byKey := map[logKey][]events.LogEntry{}
	var order []logKey
	for _, it := range batch {
		if it.done != nil {
			continue
		}
		if _, ok := byKey[it.key]; !ok {
			order = append(order, it.key)
		}
		byKey[it.key] = append(byKey[it.key], events.LogEntry{Timestamp: it.ts, Message: it.msg})
	}
	for _, k := range order {
		ctx, cancel := context.WithTimeout(middleware.ContextWithRegion(context.Background(), k.region), 5*time.Second)
		for _, group := range [...]string{k.group, k.group + ".local"} {
			gk := logKey{region: k.region, group: group, stream: k.stream}
			if !ensured[gk] {
				// EnsureLogStream is idempotent: an existing group or stream
				// (the Docker provider may have created it) is fine.
				if err := lw.EnsureLogStream(ctx, group, k.stream); err != nil {
					t.log.Debug("lambdaweb: ensure log stream", zap.String("group", group), zap.Error(err))
					continue
				}
				ensured[gk] = true
			}
			if err := lw.WriteLogEvents(ctx, group, k.stream, byKey[k]); err != nil {
				t.log.Debug("lambdaweb: write system logs", zap.String("group", group), zap.Error(err))
			}
		}
		cancel()
	}
}
