package lambda

// runtime_pool.go — InstancePool manages the warm execution environments for
// every function.
//
// AWS Lambda reuses execution environments (containers) for sequential
// invocations and scales out to one environment per concurrent invocation.
// InstancePool models both: each function has a *warm set* of idle instances,
// and concurrent invocations each take their own.
//
// Capacity model:
//   - Concurrent invocations of one function each get their own instance;
//     whatever the warm set cannot supply is cold started.
//   - After a burst, surplus instances stay warm (up to maxWarm) and are
//     reaped by the sweeper once idle for poolIdleTTL, rather than being
//     destroyed the moment their invocation ends.
//   - Provisioned concurrency keeps a target number of instances alive: they
//     are created eagerly, exempt from the idle sweep, and replenished when
//     lost. See SetProvisionedConcurrency.
//   - Instances whose identity no longer matches the function (see
//     functionInstanceIdentity) are retired: immediately by InvalidateFunction
//     when idle, on Release when an invocation is in flight, and as a backstop
//     on the next Acquire.
//
// Retiring an on-demand instance starts no immediate replacement — the next
// invocation cold starts one, or, when proactive initialization is enabled,
// one is created in the background after the configuration settles (see
// ProactiveInit). Retiring a provisioned instance always starts one, because
// that is what provisioned concurrency means.
//
// Thread safety: all public methods are safe for concurrent use.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/logging"
)

const (
	// poolSweepInterval is how often the sweeper goroutine wakes up.
	poolSweepInterval = 30 * time.Second
	// poolIdleTTL is how long an instance may sit idle before being evicted.
	poolIdleTTL = 15 * time.Minute
	// defaultMaxWarmInstances bounds the warm set per function when no explicit
	// cap is configured. A burst of concurrent invocations would otherwise leave
	// one container per invocation holding memory on a dev machine until the
	// idle TTL expired.
	defaultMaxWarmInstances = 10
)

// InstanceObserver is told about execution environments the pool creates or
// destroys outside an invocation, so a tracker of them can stay in step.
// Implemented by instanceTracker.
type InstanceObserver interface {
	// InstanceWarmed reports an environment created without an invocation —
	// proactive initialization or provisioned concurrency pre-warming.
	// containerID identifies the backing Docker container ("" when not
	// container-backed) so the tracker can sample its resource usage. origin
	// is one of the instanceOrigin* constants and records why the environment
	// was created, for the instances panel's origin badge.
	InstanceWarmed(functionName, instanceID, containerID, origin string)
	// InstanceLost reports an environment that no longer exists, for any
	// reason: idle sweep, reclaimed for capacity, retired after an update,
	// died in Docker, or its function was deleted. reason is one of the
	// evictReason* constants and records why, for the instances panel's
	// removal-reason display.
	InstanceLost(functionName, instanceID, reason string)
}

// poolEntry holds one idle RuntimeInstance.
type poolEntry struct {
	inst     RuntimeInstance
	identity string    // functionInstanceIdentity when the instance was created
	lastUsed time.Time // updated on every Release
}

// provisionedState is the provisioned-concurrency target for one function.
type provisionedState struct {
	target int
	fn     *Function // snapshot used to cold start replacements
}

// InstancePool manages warm RuntimeInstances for every function.
type InstancePool struct {
	mu sync.Mutex
	// entries is the warm (idle) set per function name. Instances handed to a
	// caller are removed and only reappear on Release.
	entries map[string][]*poolEntry
	// checkedOut counts instances currently serving an invocation, per
	// function. entries + checkedOut is how many environments exist, which is
	// what provisioned-concurrency replenishment is measured against.
	checkedOut map[string]int
	// expected records the identity each function is currently expected to run
	// with, so Release can retire an instance that was checked out before a
	// code or configuration update landed.
	expected map[string]string
	// evicted names functions whose environments have been retired wholesale —
	// DeleteFunction, or a region reset. EvictFunction can only close the idle
	// instances it can see; an invocation still in flight releases afterwards,
	// and without this marker it takes the same "expected is empty" branch a
	// never-admitted function does and gets pooled for a function that no
	// longer exists, leaving a container nothing will ever reclaim. Cleared
	// when the function is admitted again, so a recreated function pools
	// normally.
	evicted map[string]bool
	// starting holds the cold starts in flight for each function — the one
	// window in which a container can exist while nothing else here can name
	// it. See coldStart. Never emptied by stop: a creation that outlives the
	// pool still has to deregister itself.
	starting map[string]map[*coldStart]struct{}
	// provisioned holds the provisioned-concurrency target per function.
	provisioned map[string]*provisionedState
	// provisionedInstances records which execution environments hold each
	// function's reservation, keyed by function name then instance ID. Tracking
	// the identities — rather than counting environments — is what keeps
	// provisioned concurrency a floor: on-demand instances created by a burst
	// never count towards the target, and the environments that are exempt from
	// the idle sweep are exactly the ones the UI shows as provisioned.
	provisionedInstances map[string]map[string]bool
	// provisionedPending counts environments a replenish goroutine is currently
	// starting, so concurrent calls do not each create the same shortfall.
	provisionedPending map[string]int

	maxWarm        int
	maxInstances   int
	maxPerFunction int
	// debugger, when set, is consulted by admission: a function with a live
	// debug target is pinned to one execution environment, because the debug
	// port belongs to one container. Nil means no function is ever pinned.
	debugger *debugger.Manager
	// maxMemoryMB bounds Σ MemorySize over live containers; 0 = unlimited.
	maxMemoryMB int
	// liveMemMB records the MemorySize (MB) each live container was created
	// with, keyed by instance ID, so closing it releases exactly what its
	// admission reserved even if the function's configuration changed since.
	liveMemMB map[string]int
	// pendingMemMB is memory reserved by admitContainer for containers whose
	// cold start has not yet produced an instance.
	pendingMemMB int
	// reservedMemMB is the running total: Σ liveMemMB + pendingMemMB.
	reservedMemMB int
	// memWarnOnce gates the one warning emitted when the memory budget first
	// forces an invocation to queue.
	memWarnOnce sync.Once
	// memPressure latches while releases are shedding finished containers for
	// memory (the high-water regime), so the regime is logged once at each
	// edge rather than once per destroyed container. memPressureShed counts
	// containers shed in the current episode; memPressureSince is when it
	// began. See Release for why the transitions live at its decision point.
	memPressure      bool
	memPressureShed  int
	memPressureSince time.Time
	// slotFreed is closed and replaced whenever capacity frees up, waking every
	// invocation queued behind an instance limit. See signalSlotFreed.
	slotFreed chan struct{}

	rt       Runtime // used for cold starts
	log      *zap.Logger
	clk      clock.Clock
	stopOnce sync.Once
	stopCh   chan struct{}
	// observer, when set, is told about environments the pool creates or
	// destroys outside an invocation, so the instance tracker never reports an
	// environment that no longer exists. Set once during wiring, before the
	// pool is used.
	observer InstanceObserver
	// onInvoked, when set, is told the function name of every admitted
	// invocation — the proactive initializer's evidence that a function is
	// actually in use. Set once during wiring, before the pool is used.
	onInvoked func(functionName string)
	// concurrencyObserver, when set, is told a function's current real
	// in-flight admission count immediately after admit's success path or
	// decCheckedOutLocked change it — the ConcurrentExecutions gauge sample
	// (service-metrics-platform.md: "Instance acquire/release already
	// provides a natural concurrency transition"). Deliberately NOT invoked
	// for provisioned-concurrency prewarm or ProactiveInit's own checkedOut
	// bookkeeping (see their call sites below): those reserve capacity ahead
	// of traffic rather than represent a real concurrent invocation, and
	// counting them would overstate what AWS's ConcurrentExecutions actually
	// measures. Called while p.mu is held: must stay fast (a single
	// sharded-lock metrics update) and must never call back into the pool.
	// Set once during wiring, before the pool is used.
	concurrencyObserver func(functionName string, current int)
	// warmWG tracks background provisioning goroutines so Stop can wait.
	warmWG sync.WaitGroup
}

// PoolLimits bounds how many containers the pool will keep and run. Zero
// fields take their defaults.
type PoolLimits struct {
	// MaxWarmPerFunction bounds the idle set for one function.
	MaxWarmPerFunction int
	// MaxInstances bounds containers across all functions.
	MaxInstances int
	// MaxInstancesPerFunction bounds concurrent containers for one function.
	MaxInstancesPerFunction int
	// MaxMemoryMB bounds Σ MemorySize over all live containers, in MB. Each
	// container is hard-capped at its function's MemorySize with swap disabled,
	// so this sum is a real bound on host memory, not an estimate. Zero means
	// unlimited.
	MaxMemoryMB int
}

func (l PoolLimits) withDefaults() PoolLimits {
	if l.MaxWarmPerFunction < 1 {
		l.MaxWarmPerFunction = defaultMaxWarmInstances
	}
	if l.MaxInstances < 1 {
		l.MaxInstances = defaultMaxInstances
	}
	if l.MaxInstancesPerFunction < 1 {
		l.MaxInstancesPerFunction = defaultMaxInstancesPerFunction
	}
	// A per-function limit above the global one can never be reached; clamping
	// keeps the two consistent so error messages name the limit that binds.
	if l.MaxInstancesPerFunction > l.MaxInstances {
		l.MaxInstancesPerFunction = l.MaxInstances
	}
	return l
}

// NewInstancePool creates an InstancePool backed by rt and starts the
// background sweeper. Call Stop() to shut it down.
func NewInstancePool(rt Runtime, log *zap.Logger, clk clock.Clock, limits PoolLimits) *InstancePool {
	limits = limits.withDefaults()
	p := &InstancePool{
		entries:              make(map[string][]*poolEntry),
		checkedOut:           make(map[string]int),
		expected:             make(map[string]string),
		evicted:              make(map[string]bool),
		starting:             make(map[string]map[*coldStart]struct{}),
		provisioned:          make(map[string]*provisionedState),
		provisionedInstances: make(map[string]map[string]bool),
		provisionedPending:   make(map[string]int),
		maxWarm:              limits.MaxWarmPerFunction,
		maxInstances:         limits.MaxInstances,
		maxPerFunction:       limits.MaxInstancesPerFunction,
		maxMemoryMB:          limits.MaxMemoryMB,
		liveMemMB:            make(map[string]int),
		slotFreed:            make(chan struct{}),
		rt:                   rt,
		log:                  log,
		clk:                  clk,
		stopCh:               make(chan struct{}),
	}
	go p.sweepLoop()
	return p
}

// functionInstanceIdentity fingerprints everything that is baked into an
// execution environment when its container is created: the deployment package
// (or image), plus the configuration that becomes container environment
// variables, resource limits, entrypoint, injected layers and attached
// networks.
//
// A container started with the old values can never observe the new ones — the
// process was exec'd with the old environment — so the pool compares this, not
// the code hash alone, when deciding whether a warm instance is still usable.
//
// Fields that never reach the container (Description, Role, RevisionId, tags
// other than the hot-reload path and the debug tags) are deliberately
// excluded: including them would force a pointless cold start on a cosmetic
// edit.
func functionInstanceIdentity(fn *Function) string {
	if fn == nil {
		return ""
	}
	h := sha256.New()
	// Length-prefix every field so no value can forge the encoding of another
	// (e.g. an env value containing "\nenv:OTHER=").
	add := func(field, value string) {
		_, _ = fmt.Fprintf(h, "%s=%d:%s\n", field, len(value), value)
	}

	add("code", functionCodeIdentity(fn))
	add("runtime", fn.Runtime)
	add("handler", fn.Handler)
	add("timeout", strconv.Itoa(fn.Timeout))
	add("memory", strconv.Itoa(fn.MemorySize))
	add("arch", strings.Join(fn.Architectures, ","))
	add("loggroup", fn.logGroupName())
	// The logging configuration reaches the container as environment variables
	// (AWS_LAMBDA_LOG_FORMAT / AWS_LAMBDA_LOG_LEVEL), so changing it has to
	// retire the environments baked with the old values.
	add("logformat", resolveLogFormat(fn))
	applicationLevel, systemLevel := resolveLogLevels(fn)
	add("applicationloglevel", applicationLevel.String())
	add("systemloglevel", systemLevel.String())

	keys := make([]string, 0, len(fn.Environment))
	for k := range fn.Environment {
		keys = append(keys, k)
	}
	slices.Sort(keys) // map iteration order must not leak into the fingerprint
	for _, k := range keys {
		add("env:"+k, fn.Environment[k])
	}

	for _, layer := range fn.Layers {
		add("layer", layer.ARN)
	}
	if fn.VpcConfig != nil {
		add("vpc", fn.VpcConfig.VpcId)
		add("subnets", strings.Join(fn.VpcConfig.SubnetIds, ","))
		add("security-groups", strings.Join(fn.VpcConfig.SecurityGroupIds, ","))
	}
	if fn.ImageConfig != nil {
		add("entrypoint", strings.Join(fn.ImageConfig.EntryPoint, "\x00"))
		add("command", strings.Join(fn.ImageConfig.Command, "\x00"))
		add("workdir", fn.ImageConfig.WorkingDirectory)
	}
	// The debug tags are the exception to "tags never reach the container":
	// they become an injected flag, OVERCAST_DEBUG_PORT and a port binding, so
	// a tag change has to retire the environment built without them.
	for _, key := range debugTagKeys {
		if value, ok := fn.Tags[key]; ok {
			add("tag:"+key, value)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ─── Acquire / Release ───────────────────────────────────────────────────────

// noteFunctionPresentLocked records the identity name is expected to run with,
// and lifts any wholesale-eviction marker EvictFunction left behind.
//
// The two belong together. The marker means "this name was deleted, so an
// environment released for it is a leftover" — true until the name is in use
// again, at which point leaving it set destroys environments built for the live
// function. Clearing it without recording an identity is the opposite mistake:
// Release would have nothing to compare a late environment against, and would
// pool a container built for the deleted incarnation.
//
// Every path that learns the function exists calls this: an invocation
// (takeWarm), an update (InvalidateFunction), a reservation
// (SetProvisionedConcurrency) and proactive initialization (ProactiveInit).
// Caller must hold p.mu.
func (p *InstancePool) noteFunctionPresentLocked(fn *Function) {
	p.expected[fn.Name] = functionInstanceIdentity(fn)
	delete(p.evicted, fn.Name)
}

// takeWarm removes and returns an idle instance for fn, or nil when the caller
// must cold start. Pooled instances whose identity no longer matches fn (its
// code or configuration changed) are closed here.
//
// It also records fn's current identity as the one the pool expects, so that
// Release can retire an instance checked out under an older configuration.
// The caller must already hold a concurrency slot from admit.
func (p *InstancePool) takeWarm(fn *Function) RuntimeInstance {
	identity := functionInstanceIdentity(fn)

	p.mu.Lock()
	if p.entries == nil { // pool stopped
		p.mu.Unlock()
		return nil
	}
	p.noteFunctionPresentLocked(fn)

	var stale []RuntimeInstance
	var chosen RuntimeInstance
	warm := p.entries[fn.Name]
	for i := 0; i < len(warm); i++ {
		if warm[i].identity != identity {
			// Backstop for updates that did not go through InvalidateFunction.
			stale = append(stale, warm[i].inst)
			continue
		}
		if chosen == nil {
			chosen = warm[i].inst
		}
	}
	if len(stale) > 0 || chosen != nil {
		kept := warm[:0]
		for _, e := range warm {
			if e.identity != identity || e.inst == chosen {
				continue
			}
			kept = append(kept, e)
		}
		p.setWarmLocked(fn.Name, kept)
	}
	p.mu.Unlock()

	for _, inst := range stale {
		p.log.Debug("lambda pool: evicting stale instance", zap.String("function", fn.Name))
		p.closeInstance(fn.Name, inst, "close stale instance failed", evictReasonConfigChange)
	}
	if chosen != nil {
		p.log.Debug("lambda pool: warm start", zap.String("function", fn.Name))
	}
	return chosen
}

// setWarmLocked stores the warm set for name, deleting the key when empty so
// the map does not grow one entry per function that has ever run.
// Caller must hold p.mu.
func (p *InstancePool) setWarmLocked(name string, warm []*poolEntry) {
	if len(warm) == 0 {
		delete(p.entries, name)
		return
	}
	p.entries[name] = warm
}

// Acquire returns an instance for fn — reused from the warm set when one
// matches, cold started otherwise. Concurrent callers never share an instance.
//
// It may block while the invocation queues behind an instance limit, and
// returns a TooManyRequestsException (see ThrottleError) when a concurrency
// limit refuses the invocation outright or ctx expires while queued.
func (p *InstancePool) Acquire(ctx context.Context, fn *Function) (RuntimeInstance, error) {
	return p.acquire(ctx, fn, nil)
}

// AcquireWithProgress is like Acquire but reports lifecycle steps via progress.
func (p *InstancePool) AcquireWithProgress(ctx context.Context, fn *Function, progress ProgressFunc) (RuntimeInstance, error) {
	return p.acquire(ctx, fn, progress)
}

func (p *InstancePool) acquire(ctx context.Context, fn *Function, progress ProgressFunc) (RuntimeInstance, error) {
	// Bound how long an invocation may queue for capacity by the function's own
	// timeout. Without this the async and event-source paths — which hand us a
	// cancellable context with no deadline — would block indefinitely instead
	// of eventually throttling.
	admitCtx, cancelAdmit := context.WithTimeout(ctx, queueBudget(fn))
	defer cancelAdmit()

	if err := p.admit(admitCtx, fn); err != nil {
		return nil, err
	}
	if p.onInvoked != nil {
		p.onInvoked(fn.Name)
	}
	if inst := p.takeWarm(fn); inst != nil {
		return inst, nil
	}
	// admitContainer reserved memory for the container about to be created;
	// every exit below must either commit that reservation to the instance or
	// give it back.
	memMB := functionMemoryMB(fn)
	if err := p.admitContainer(admitCtx, fn); err != nil {
		p.releaseSlot(fn.Name)
		return nil, err
	}

	p.log.Debug("lambda pool: cold start", zap.String("function", fn.Name))
	// Registered for the whole creation: until it returns, the container it is
	// making answers to nothing else in the pool, and DeleteFunction has to be
	// able to reach it.
	startCtx, cs := p.beginColdStart(ctx, fn.Name)
	var inst RuntimeInstance
	var err error
	if cr, ok := p.rt.(*ContainerRuntime); ok && progress != nil {
		inst, err = cr.AcquireWithProgress(startCtx, fn, progress)
	} else {
		inst, err = p.rt.Acquire(startCtx, fn)
	}
	if p.finishColdStart(fn.Name, cs) {
		// The function was deleted while this environment was being created.
		// If the abort landed too late to stop it, the container is here and
		// this is the only place that will ever know about it.
		p.abandonColdStart(fn.Name, inst, memMB)
		return nil, errFunctionDeletedDuringColdStart(fn.Name)
	}
	if err != nil || inst == nil {
		// A runtime that returns no instance and no error would otherwise leak
		// the admitted slot, permanently shrinking the function's concurrency.
		p.unreservePendingMem(memMB)
		p.releaseSlot(fn.Name)
		if err == nil {
			err = fmt.Errorf("lambda runtime returned no instance for %q", fn.Name)
		}
		return nil, err
	}
	p.commitPendingMem(inst.InstanceID(), memMB)
	return inst, nil
}

// releaseSlot gives back a concurrency slot taken by admit without an instance
// to show for it (the cold start failed, or capacity ran out).
func (p *InstancePool) releaseSlot(name string) {
	p.mu.Lock()
	p.decCheckedOutLocked(name)
	p.signalSlotFreedLocked()
	p.mu.Unlock()
}

// decCheckedOutLocked drops one in-flight slot for name. Caller must hold p.mu.
func (p *InstancePool) decCheckedOutLocked(name string) {
	if p.checkedOut == nil {
		return
	}
	p.checkedOut[name]--
	current := p.checkedOut[name]
	if current <= 0 {
		delete(p.checkedOut, name)
		current = 0
	}
	if p.concurrencyObserver != nil {
		p.concurrencyObserver(name, current)
	}
}

// releaseDisposition is what Release decided to do with an instance.
type releaseDisposition int

const (
	pooled releaseDisposition = iota
	closeUnhealthy
	closeAfterStop
	closeRetired
	closeSurplus
	closeMemoryPressure
)

// releaseLog maps a disposition to its log line.
var releaseLog = map[releaseDisposition]string{
	closeUnhealthy:      "lambda pool: discarding unhealthy instance",
	closeAfterStop:      "lambda pool: closing instance released after stop",
	closeRetired:        "lambda pool: retiring instance after configuration change",
	closeSurplus:        "lambda pool: warm set full — closing surplus instance",
	closeMemoryPressure: "lambda pool: memory budget near exhaustion — closing instead of keeping warm",
}

// releaseReason maps a disposition to the evictedReason the observer sees.
// closeRetired reports "config-change" for both switch cases that produce it
// below — a stale identity and a function deleted mid-invocation — since
// either way the instance is being destroyed because what Release compared it
// against moved out from under it, which reads to the UI as a configuration
// change either way.
var releaseReason = map[releaseDisposition]string{
	closeUnhealthy:      evictReasonUnhealthy,
	closeAfterStop:      evictReasonShutdown,
	closeRetired:        evictReasonConfigChange,
	closeSurplus:        evictReasonSurplus,
	closeMemoryPressure: evictReasonMemoryPressure,
}

// Release returns inst to the warm set after an invocation. The instance is
// destroyed instead when it is unhealthy, when the function was updated while
// the invocation was in flight, or when the warm set is already full.
// Implements the Runtime interface — inst.FunctionName() is the pool key.
func (p *InstancePool) Release(_ context.Context, inst RuntimeInstance, healthy bool) {
	name := inst.FunctionName()
	usable := healthy && inst.Healthy()

	// The decision and the append happen under one lock hold: splitting them
	// would let two concurrent releases both observe room for one more and
	// push the warm set past its cap.
	p.mu.Lock()
	p.decCheckedOutLocked(name)
	// Whatever is decided below, this invocation's slot is now free: either the
	// instance goes back to the warm set for the next caller, or it is closed
	// and the next caller cold starts into the space it leaves.
	p.signalSlotFreedLocked()

	disposition := pooled
	// expected is empty for a function the pool has never admitted, in which
	// case there is nothing to compare against; an identity is always a hash.
	switch want := p.expected[name]; {
	case !usable:
		disposition = closeUnhealthy
	case p.entries == nil:
		disposition = closeAfterStop
	// The function was deleted while this invocation was in flight. Its
	// environments were retired; this one is simply late, so destroy it rather
	// than pooling a container for a function that no longer exists.
	case p.evicted[name]:
		disposition = closeRetired
	// The function's code or configuration changed while this invocation was in
	// flight. AWS lets the in-flight invocation finish in the old environment
	// and then retires it, so destroy the instance now rather than pooling one
	// that can never see the new configuration.
	case want != "" && want != inst.ConfigIdentity():
		disposition = closeRetired
	// Above the memory budget's high-water mark, surplus warmth is the first
	// thing to give: the finished invocation's container is destroyed rather
	// than pooled, so queued work regains budget without waiting for the idle
	// sweep. A running invocation is never touched — this only decides what
	// happens to a container whose work is already done — and a provisioned
	// environment is always kept, that being what the reservation buys.
	case p.overMemoryHighWaterLocked() && !p.isProvisionedLocked(name, inst):
		disposition = closeMemoryPressure
	case len(p.entries[name]) >= p.warmCapacityLocked(name):
		disposition = closeSurplus
	default:
		p.entries[name] = append(p.entries[name], &poolEntry{
			inst:     inst,
			identity: inst.ConfigIdentity(),
			lastUsed: p.clk.Now(),
		})
	}

	// Memory-pressure regime edges are detected here — at the release decision
	// point — and nowhere else, so entry and exit always pair up. Detecting
	// exit where memory is freed (releaseLiveMemLocked) would un-latch the
	// regime on the shed's own close whenever one container spans the gap
	// between reserved memory and the high-water mark, and every following
	// release would then log a fresh entry: one line per release, exactly the
	// noise the latch exists to avoid. The cost is that a pool which goes
	// quiet mid-episode logs its recovery on the next release rather than on
	// the sweep that freed the memory.
	pressureEntered, pressureExited := false, false
	shedCount, reservedNow := 0, p.reservedMemMB
	var pressureFor time.Duration
	switch {
	case disposition == closeMemoryPressure:
		if !p.memPressure {
			p.memPressure = true
			p.memPressureShed = 0
			p.memPressureSince = p.clk.Now()
			pressureEntered = true
		}
		p.memPressureShed++
	case p.memPressure && !p.overMemoryHighWaterLocked():
		p.memPressure = false
		pressureExited = true
		shedCount = p.memPressureShed
		pressureFor = p.clk.Now().Sub(p.memPressureSince)
		p.memPressureShed = 0
	}
	p.mu.Unlock()

	if pressureEntered {
		p.log.Warn(memPressureEnterMsg,
			zap.Int("budget_mb", p.maxMemoryMB),
			zap.Int("high_water_mb", p.maxMemoryMB*memoryHighWaterPercent/100),
			zap.Int("reserved_mb", reservedNow),
			zap.String("env_var", "LAMBDA_MAX_MEMORY_MB"),
		)
	}
	if pressureExited {
		p.log.Info(memPressureExitMsg,
			zap.Int("containers_shed", shedCount),
			zap.Duration("pressure_for", pressureFor),
			zap.Int("reserved_mb", reservedNow),
		)
	}

	if disposition == pooled {
		p.log.Debug("lambda pool: instance returned to pool", zap.String("function", name))
		return
	}
	p.log.Debug(releaseLog[disposition], zap.String("function", name))
	p.closeInstance(name, inst, "close on release failed", releaseReason[disposition])
	// An environment lost while the reservation still stands must be rebuilt;
	// one closed because the pool is stopping or already full must not be.
	if disposition == closeUnhealthy || disposition == closeRetired {
		p.replenishProvisioned(name)
	}
}

// warmCapacityLocked is how many idle instances name may hold. Provisioned
// concurrency raises the cap when its target exceeds maxWarm, so a configured
// allocation is never fought by the warm-set limit.
// Caller must hold p.mu.
func (p *InstancePool) warmCapacityLocked(name string) int {
	capacity := p.maxWarm
	if st, ok := p.provisioned[name]; ok && st.target > capacity {
		capacity = st.target
	}
	return capacity
}

// closeInstance destroys inst and tells the observer the environment is gone.
// Every path that gets rid of a container goes through here so the tracker and
// the pool can never disagree about what exists. reason is one of the
// evictReason* constants, forwarded to the observer verbatim so the instances
// panel can explain why the environment disappeared.
func (p *InstancePool) closeInstance(name string, inst RuntimeInstance, msg, reason string) {
	p.mu.Lock()
	if ids := p.provisionedInstances[name]; ids != nil {
		delete(ids, inst.InstanceID())
		if len(ids) == 0 {
			delete(p.provisionedInstances, name)
		}
	}
	// The container's memory leaves the budget with it; wake anything queued
	// on memory. (Instances the pool never admitted — released to it directly —
	// have no entry and free nothing.)
	p.releaseLiveMemLocked(inst.InstanceID())
	p.mu.Unlock()
	if err := inst.Close(); err != nil {
		p.log.Warn("lambda pool: "+msg,
			zap.String("function", name),
			zap.Error(err),
		)
	}
	if p.observer != nil {
		p.observer.InstanceLost(name, inst.InstanceID(), reason)
	}
}

// CanHandle delegates to the underlying runtime.
func (p *InstancePool) CanHandle(runtimeID string) bool {
	return p.rt.CanHandle(runtimeID)
}

// ─── Cold starts in flight ───────────────────────────────────────────────────

// coldStart is one execution environment being created. Between the moment the
// runtime is asked for it and the moment the pool takes ownership of the
// result, its container can already exist on the Docker daemon while no map
// here can name it: not the warm set, not the provisioned reservation, not the
// instance tracker. Every creation registers one of these for exactly that
// window, so EvictFunction has something to reach — otherwise a function
// deleted mid cold start leaves a container running that nothing will ever
// reclaim (#1336).
type coldStart struct {
	// cancel stops the creation at the next step that can be stopped. The
	// runtime removes whatever it has already made on the way out.
	cancel context.CancelFunc
	// evicted records that the function was deleted while this creation was in
	// flight, so an environment that lands anyway is destroyed rather than
	// used. Guarded by InstancePool.mu.
	evicted bool
}

// beginColdStart registers a creation of one environment for name and returns
// the context it must run under. Every caller must pair it with
// finishColdStart.
func (p *InstancePool) beginColdStart(ctx context.Context, name string) (context.Context, *coldStart) {
	ctx, cancel := context.WithCancel(ctx)
	cs := &coldStart{cancel: cancel}

	p.mu.Lock()
	if p.starting[name] == nil {
		p.starting[name] = make(map[*coldStart]struct{})
	}
	p.starting[name][cs] = struct{}{}
	p.mu.Unlock()

	return ctx, cs
}

// finishColdStart deregisters cs and reports whether the function was deleted
// while it was in flight — in which case the caller owns whatever the creation
// produced and must destroy it, because nothing else can see it.
func (p *InstancePool) finishColdStart(name string, cs *coldStart) bool {
	p.mu.Lock()
	if starts := p.starting[name]; starts != nil {
		delete(starts, cs)
		if len(starts) == 0 {
			delete(p.starting, name)
		}
	}
	evicted := cs.evicted
	p.mu.Unlock()

	// Always: the context wraps the caller's, and leaving it uncancelled leaks
	// the parent's child list for as long as that parent lives.
	cs.cancel()
	return evicted
}

// errFunctionDeletedDuringColdStart is what an invocation gets when its
// environment was abandoned because the function went away underneath it.
// Named for what happened rather than surfacing the bare "context canceled"
// the abort produces.
func errFunctionDeletedDuringColdStart(name string) error {
	return fmt.Errorf("function %q was deleted while its execution environment was starting", name)
}

// abandonColdStart gives back everything a creation reserved and destroys the
// environment it produced, if it produced one. Used by every path that learns
// from finishColdStart that its function was deleted.
func (p *InstancePool) abandonColdStart(name string, inst RuntimeInstance, memMB int) {
	p.unreservePendingMem(memMB)
	p.releaseSlot(name)
	if inst != nil {
		p.closeInstance(name, inst, "close environment created for a deleted function failed", evictReasonFunctionDeleted)
	}
}

// ─── Eviction and retirement ─────────────────────────────────────────────────

// EvictFunction closes and removes every warm instance for the named function,
// aborts the environments still being created for it, and forgets its
// provisioned-concurrency target. Called by DeleteFunction so containers do not
// linger after deletion.
//
// An invocation still in flight is not interrupted — AWS does not either — and
// is destroyed on release instead, via the evicted marker.
func (p *InstancePool) EvictFunction(name string) {
	p.mu.Lock()
	warm := p.entries[name]
	delete(p.entries, name)
	delete(p.expected, name)
	delete(p.provisioned, name)
	delete(p.provisionedInstances, name)
	if p.evicted != nil {
		p.evicted[name] = true
	}
	// Environments still being created hold no entry above, and their
	// containers may already be running. Mark each one so whatever it produces
	// is destroyed, and cancel it so it stops making more.
	aborting := make([]*coldStart, 0, len(p.starting[name]))
	for cs := range p.starting[name] {
		cs.evicted = true
		aborting = append(aborting, cs)
	}
	p.mu.Unlock()

	for _, cs := range aborting {
		cs.cancel()
	}
	for _, entry := range warm {
		p.closeInstance(name, entry.inst, "evict close failed", evictReasonFunctionDeleted)
	}
}

// InvalidateFunction retires the execution environments for fn after its code
// or configuration changed.
//
//   - Idle instances: closed immediately, so stale containers do not linger
//     until the 15-minute idle sweep.
//   - Instances serving an invocation: left running. AWS never interrupts an
//     in-flight invocation to apply an update, so each finishes in the old
//     environment and Release then destroys it instead of pooling it.
//   - Identity unchanged (e.g. only the description was edited): the warm set
//     is kept.
//
// No on-demand replacement is started — the next invocation cold starts one.
// A function with provisioned concurrency is re-provisioned in the background
// against the new configuration, which is the point of having reserved it.
//
// Returns the number of idle instances that were closed.
func (p *InstancePool) InvalidateFunction(fn *Function) int {
	if fn == nil {
		return 0
	}
	name := fn.Name
	identity := functionInstanceIdentity(fn)

	p.mu.Lock()
	if p.entries == nil { // pool stopped
		p.mu.Unlock()
		return 0
	}
	p.noteFunctionPresentLocked(fn)
	warm := p.entries[name]
	var retire []RuntimeInstance
	kept := warm[:0]
	for _, entry := range warm {
		if entry.identity == identity {
			kept = append(kept, entry)
			continue
		}
		retire = append(retire, entry.inst)
	}
	p.setWarmLocked(name, kept)
	// Rebuild the reservation from the function as it is now, not the snapshot
	// taken when provisioned concurrency was first configured.
	reprovision := false
	if st, ok := p.provisioned[name]; ok {
		snapshot := *fn
		st.fn = &snapshot
		reprovision = true
	}
	p.mu.Unlock()

	for _, inst := range retire {
		p.log.Debug("lambda pool: retiring idle instance after configuration change",
			zap.String("function", name))
		p.closeInstance(name, inst, "close retired instance failed", evictReasonConfigChange)
	}
	if reprovision {
		p.replenishProvisioned(name)
	}
	return len(retire)
}

// handleContainerDied drops warm instances whose Docker container has died —
// removed by hand (`docker rm -f`), OOM-killed, or lost to a Docker restart.
//
// Without this, the pool keeps treating the dead container as warm: an
// invocation is handed the corpse, submits the event to a Runtime API that no
// container is polling, and blocks until the function's configured timeout
// expires before failing. Dropping the entry here makes that a cold start.
//
// The exitNotifier only routes die events to invocations that are in flight, so
// this handler is what covers a container that dies while idle in the pool.
// Subscribe it to events.DockerContainerDied.
func (p *InstancePool) handleContainerDied(_ context.Context, e events.Event) {
	payload, ok := e.Payload.(events.DockerContainerPayload)
	if !ok || payload.Service != "lambda" || payload.ContainerID == "" {
		return
	}

	p.mu.Lock()
	if p.entries == nil { // pool stopped
		p.mu.Unlock()
		return
	}
	var name string
	var dead RuntimeInstance
	for fnName, warm := range p.entries {
		for i, entry := range warm {
			if entry.inst.ContainerID() != payload.ContainerID {
				continue
			}
			name, dead = fnName, entry.inst
			p.setWarmLocked(fnName, append(warm[:i:i], warm[i+1:]...))
			break
		}
		if dead != nil {
			break
		}
	}
	p.mu.Unlock()

	if dead == nil {
		return // not warm: in flight (the exitNotifier has it) or already gone
	}
	p.log.Info("lambda pool: warm instance container died — next invoke will cold start",
		zap.String("function", name),
		zap.String("container", shortContainerID(payload.ContainerID)),
	)
	p.closeInstance(name, dead, "close dead instance failed", evictReasonContainerDied)
	p.replenishProvisioned(name)
}

// ─── Provisioned concurrency ─────────────────────────────────────────────────

// SetProvisionedConcurrency reserves target execution environments for fn.
// They are created in the background, exempt from the idle sweep, and
// replenished whenever one is lost. target <= 0 clears the reservation,
// after which the environments become ordinary warm instances subject to the
// idle TTL — matching AWS, which does not tear them down instantly.
func (p *InstancePool) SetProvisionedConcurrency(fn *Function, target int) {
	if fn == nil {
		return
	}
	if target <= 0 {
		p.ClearProvisionedConcurrency(fn.Name)
		return
	}
	snapshot := *fn
	p.mu.Lock()
	if p.entries == nil {
		p.mu.Unlock()
		return
	}
	// Reserving capacity for a name is proof it is in use — a function deleted
	// and created again under the same name would otherwise have every
	// environment the reservation builds destroyed on release as a leftover of
	// the deleted one, leaving the reservation unfilled until something
	// invoked the function.
	p.noteFunctionPresentLocked(fn)
	p.provisioned[fn.Name] = &provisionedState{target: target, fn: &snapshot}
	p.mu.Unlock()

	p.replenishProvisioned(fn.Name)
}

// ClearProvisionedConcurrency removes the reservation for name. The
// environments it was holding open are not torn down — they lose their
// exemption from the idle sweep and age out like any other warm instance,
// matching AWS, which does not kill them the moment the config is deleted.
func (p *InstancePool) ClearProvisionedConcurrency(name string) {
	p.mu.Lock()
	delete(p.provisioned, name)
	delete(p.provisionedInstances, name)
	p.mu.Unlock()
}

// ProvisionedStatus reports the reservation for name: how many environments are
// requested, how many exist (warm or serving an invocation), and how many are
// idle and immediately available. ok is false when name has no reservation.
func (p *InstancePool) ProvisionedStatus(name string) (requested, allocated, available int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.provisioned[name]
	if !ok {
		return 0, 0, 0, false
	}
	reserved := p.provisionedInstances[name]
	allocated = len(reserved)
	for _, entry := range p.entries[name] {
		if reserved[entry.inst.InstanceID()] {
			available++
		}
	}
	return st.target, allocated, available, true
}

// markProvisioned records that instanceID holds part of name's reservation.
func (p *InstancePool) markProvisioned(name, instanceID string) {
	if instanceID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provisionedInstances == nil {
		return
	}
	if p.provisionedInstances[name] == nil {
		p.provisionedInstances[name] = make(map[string]bool)
	}
	p.provisionedInstances[name][instanceID] = true
}

// isProvisionedLocked reports whether inst holds part of name's reservation.
// Caller must hold p.mu.
func (p *InstancePool) isProvisionedLocked(name string, inst RuntimeInstance) bool {
	return p.provisionedInstances[name][inst.InstanceID()]
}

// clearProvisionedPending releases the in-flight reservation starts a
// replenish goroutine claimed.
func (p *InstancePool) clearProvisionedPending(name string, n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provisionedPending == nil {
		return
	}
	p.provisionedPending[name] -= n
	if p.provisionedPending[name] <= 0 {
		delete(p.provisionedPending, name)
	}
}

// provisionedRuntime is implemented by runtimes that can mark an environment
// as pre-warmed for provisioned concurrency, so the container reports the AWS
// initialization type applications actually see.
type provisionedRuntime interface {
	AcquireProvisioned(ctx context.Context, fn *Function) (RuntimeInstance, error)
}

func (p *InstancePool) acquireProvisioned(ctx context.Context, fn *Function) (RuntimeInstance, error) {
	if pr, ok := p.rt.(provisionedRuntime); ok {
		return pr.AcquireProvisioned(ctx, fn)
	}
	return p.rt.Acquire(ctx, fn)
}

// replenishProvisioned tops the function back up to its provisioned target,
// cold starting the shortfall on a background goroutine so callers (Release, a
// died-container event, a configuration update) are never blocked on Docker.
func (p *InstancePool) replenishProvisioned(name string) {
	p.mu.Lock()
	st, ok := p.provisioned[name]
	if !ok || p.entries == nil || st.fn == nil {
		p.mu.Unlock()
		return
	}
	// Only environments that hold the reservation count. Provisioned
	// concurrency is a floor, not a ceiling: a burst of on-demand instances
	// must not make the pool think the reservation is already satisfied.
	shortfall := st.target - len(p.provisionedInstances[name]) - p.provisionedPending[name]
	if p.debugPinned(st.fn) {
		// One environment in total while a debugger can attach, counting
		// whatever exists or is starting, however the reservation is sized.
		if room := 1 - len(p.entries[name]) - p.checkedOut[name] - p.provisionedPending[name]; shortfall > room {
			shortfall = room
		}
	}
	if shortfall <= 0 {
		p.mu.Unlock()
		return
	}
	// Record the pending starts so concurrent replenish calls do not each
	// decide to create the same shortfall.
	p.provisionedPending[name] += shortfall
	fn := *st.fn
	p.mu.Unlock()

	p.warmWG.Add(1)
	go func() {
		defer p.warmWG.Done()
		defer p.clearProvisionedPending(fn.Name, shortfall)
		for range shortfall {
			select {
			case <-p.stopCh:
				continue
			default:
			}
			// Provisioned capacity is reserved, so it does not queue behind
			// the emulator's instance limits the way an on-demand invocation
			// does — the reservation was accepted up front.
			startCtx, cs := p.beginColdStart(context.Background(), fn.Name)
			inst, err := p.acquireProvisioned(startCtx, &fn)
			if p.finishColdStart(fn.Name, cs) {
				// The function was deleted while this reservation was being
				// filled. Destroy whatever came up and stop replenishing a
				// target that no longer exists.
				if inst != nil {
					p.closeInstance(fn.Name, inst, "close environment created for a deleted function failed", evictReasonFunctionDeleted)
				}
				break
			}
			if err != nil {
				p.log.Warn("lambda pool: provisioned concurrency cold start failed",
					zap.String("function", fn.Name),
					zap.Error(err),
				)
				continue
			}
			// Mark it as holding the reservation before pooling it, so the
			// sweeper and the capacity reclaimer see it as protected the
			// instant it becomes visible.
			p.markProvisioned(fn.Name, inst.InstanceID())
			// Provisioned capacity skips admission but not the books: its
			// memory counts against the budget so on-demand admission sees
			// the host as full as it really is.
			p.registerLiveMem(inst.InstanceID(), functionMemoryMB(&fn))
			if p.observer != nil {
				// Announce before pooling: if Release decides to close this
				// instance instead, closeInstance's InstanceLost retracts it.
				p.observer.InstanceWarmed(fn.Name, inst.InstanceID(), inst.ContainerID(), instanceOriginProvisioned)
			}
			// Release decrements the in-flight count, so borrow a slot first to
			// keep the books level — this environment was never checked out.
			p.mu.Lock()
			if p.checkedOut != nil {
				p.checkedOut[fn.Name]++
			}
			p.mu.Unlock()
			p.Release(context.Background(), inst, true)
			p.log.Debug("lambda pool: provisioned environment ready",
				zap.String("function", fn.Name))
		}
	}()
}

// ─── Proactive initialization ────────────────────────────────────────────────

// proactiveOutcome is what ProactiveInit decided.
type proactiveOutcome int

const (
	// proactiveStarted: an environment is being created in the background.
	proactiveStarted proactiveOutcome = iota
	// proactiveAlreadyWarm: the function already has an environment (warm,
	// in flight, or covered by provisioned concurrency) — nothing to do.
	proactiveAlreadyWarm
	// proactiveBusy: capacity is contended right now; the caller may retry
	// later. Proactive work never queues for capacity.
	proactiveBusy
	// proactiveUnavailable: the pool is stopped, the runtime cannot create
	// proactive environments, or the function is throttled to zero.
	proactiveUnavailable
)

// proactiveAcquirer is implemented by runtimes that can create an environment
// ahead of an invocation without blocking on cold-start capacity, and without
// the environment reporting an invoke-triggered Init Duration.
type proactiveAcquirer interface {
	AcquireProactive(ctx context.Context, fn *Function) (RuntimeInstance, error)
}

// ProactiveInit creates one execution environment for fn ahead of traffic,
// mirroring AWS's documented proactive initialization. It is strictly
// best-effort: every budget a real invocation would consume (per-function
// slot, instance count, memory — including the high-water margin) is checked
// non-blockingly, and contention returns proactiveBusy instead of queueing,
// so proactive work can never delay a real invocation. The environment lands
// in the warm set as an ordinary on-demand instance: it is swept on idleness,
// retired on updates, and never counted toward provisioned reservations.
func (p *InstancePool) ProactiveInit(fn *Function) proactiveOutcome {
	pr, ok := p.rt.(proactiveAcquirer)
	if !ok || fn == nil {
		return proactiveUnavailable
	}
	if reservedConcurrencyOf(fn) == 0 {
		return proactiveUnavailable // throttled to zero — AWS runs nothing either
	}
	name := fn.Name
	memMB := functionMemoryMB(fn)

	p.mu.Lock()
	switch {
	case p.entries == nil:
		p.mu.Unlock()
		return proactiveUnavailable
	case p.provisioned[name] != nil:
		// Provisioned concurrency already keeps environments ready.
		p.mu.Unlock()
		return proactiveAlreadyWarm
	case len(p.entries[name]) > 0 || p.checkedOut[name] > 0:
		p.mu.Unlock()
		return proactiveAlreadyWarm
	case p.totalLiveLocked()+1 > p.maxInstances,
		!p.memoryFitsLocked(memMB),
		p.overMemoryHighWaterLocked():
		p.mu.Unlock()
		return proactiveBusy
	}
	// Initializing ahead of traffic is proof the function exists, for the same
	// reason a reservation is (see noteFunctionPresentLocked).
	p.noteFunctionPresentLocked(fn)
	// Reserve the slot and memory exactly as admit/admitContainer would, so
	// concurrent real invocations see the capacity as taken.
	p.checkedOut[name]++
	p.pendingMemMB += memMB
	p.reservedMemMB += memMB
	p.mu.Unlock()

	snapshot := *fn
	p.warmWG.Add(1)
	go func() {
		defer p.warmWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		startCtx, cs := p.beginColdStart(ctx, name)
		inst, err := pr.AcquireProactive(startCtx, &snapshot)
		if p.finishColdStart(name, cs) {
			// Deleted mid-creation: give back the slot and memory reserved
			// above and destroy the environment if one landed anyway. Nothing
			// else has heard of it — the observer is told below, on the path
			// where it survives.
			p.abandonColdStart(name, inst, memMB)
			return
		}
		if err != nil || inst == nil {
			p.unreservePendingMem(memMB)
			p.releaseSlot(name)
			if err != nil && !errors.Is(err, errColdStartBusy) {
				p.log.Debug("lambda pool: proactive init failed",
					zap.String("function", name), zap.Error(err))
			}
			return
		}
		p.commitPendingMem(inst.InstanceID(), memMB)
		if p.observer != nil {
			p.observer.InstanceWarmed(name, inst.InstanceID(), inst.ContainerID(), instanceOriginProactive)
		}
		// Release decrements the slot reserved above and pools the instance.
		p.Release(context.Background(), inst, true)
		p.log.Debug("lambda pool: proactive environment ready", zap.String("function", name))
	}()
	return proactiveStarted
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

// Stop shuts down the background sweeper and closes every warm instance.
// Idempotent for the same reason instanceTracker.Stop is: Service.Stop drives
// both, and is itself reachable more than once.
func (p *InstancePool) Stop() {
	p.stopOnce.Do(p.stop)
}

func (p *InstancePool) stop() {
	close(p.stopCh)
	p.warmWG.Wait()

	// Cancel log contexts on all remaining instances so log streaming
	// goroutines exit cleanly and don't fight with GC during shutdown.
	p.mu.Lock()
	entries := p.entries
	p.entries = nil
	p.expected = nil
	p.provisioned = nil
	p.provisionedInstances = nil
	p.provisionedPending = nil
	p.checkedOut = nil
	p.liveMemMB = nil
	p.pendingMemMB = 0
	p.reservedMemMB = 0
	p.memPressure = false
	p.memPressureShed = 0
	// Wake anything queued behind an instance limit; it will see the stopped
	// pool and give up rather than wait out its whole timeout.
	p.signalSlotFreedLocked()
	p.mu.Unlock()
	for name, warm := range entries {
		for _, entry := range warm {
			p.closeInstance(name, entry.inst, "stop close failed", evictReasonShutdown)
		}
	}
}

// sweepLoop runs until Stop() is called, evicting instances idle longer than
// poolIdleTTL.
func (p *InstancePool) sweepLoop() {
	ticker := p.clk.Ticker(poolSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.sweep()
		case <-p.stopCh:
			return
		}
	}
}

func (p *InstancePool) sweep() {
	cutoff := p.clk.Now().Add(-poolIdleTTL)

	type staleInstance struct {
		name string
		inst RuntimeInstance
	}
	var stale []staleInstance

	p.mu.Lock()
	for name, warm := range p.entries {
		kept := warm[:0]
		for _, entry := range warm {
			// Environments holding a provisioned reservation are exempt from
			// the idle sweep regardless of how long they have been idle — that
			// is what the reservation buys. Spillover instances created by a
			// burst age out normally.
			if p.isProvisionedLocked(name, entry.inst) || !entry.lastUsed.Before(cutoff) {
				kept = append(kept, entry)
				continue
			}
			stale = append(stale, staleInstance{name, entry.inst})
		}
		p.setWarmLocked(name, kept)
	}
	p.mu.Unlock()

	for _, s := range stale {
		// A per-tick sweep-cycle outcome (fires on the sweeper's own
		// schedule, not because of a specific invoke) — TRACE per the
		// trace-vs-debug policy (CONTRIBUTING.md § Log levels).
		p.log.Log(logging.TraceLevel, "lambda pool: evicting idle instance", zap.String("function", s.name))
		p.closeInstance(s.name, s.inst, "sweep close failed", evictReasonIdleTTL)
	}
}
