package lambda

// runtime_pool_admission.go — deciding whether an invocation may run now.
//
// Two independent gates sit in front of every Acquire:
//
//  1. Reserved concurrency — the AWS-visible limit a user sets with
//     PutFunctionConcurrency. Exceeding it throttles immediately with a 429,
//     because that is the behaviour applications are written against (retry
//     policies, DLQs, ESM back-off). Overcast does not emulate the account-wide
//     unreserved pool; only the per-function reservation is enforced.
//
//  2. Instance limits — how many containers Overcast is willing to run, in
//     total and per function. These protect the host, not AWS fidelity, so
//     they try hard not to change what the application observes: an invocation
//     that cannot get a slot first reclaims the least-recently-used idle
//     container, then waits for one to free, and only throttles if it is still
//     waiting when the function's timeout expires.
//
// Provisioned environments are never reclaimed — reserving them is the whole
// point.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

const (
	// defaultMaxInstances bounds the total number of Lambda containers across
	// all functions.
	defaultMaxInstances = 25
	// defaultMaxInstancesPerFunction bounds concurrent containers for a single
	// function.
	defaultMaxInstancesPerFunction = 10
	// defaultFunctionTimeout matches AWS's default function timeout, used when
	// a function does not set one.
	defaultFunctionTimeout = 3 * time.Second
	// defaultFunctionMemoryMB is AWS's default MemorySize, applied by
	// CreateFunction; the budget uses it for any Function value that predates
	// that defaulting so nothing is ever admitted as costing nothing.
	defaultFunctionMemoryMB = 128
	// memoryHighWaterPercent is the share of the memory budget above which
	// Release prefers destroying a finished container over keeping it warm.
	memoryHighWaterPercent = 90
)

// Memory-pressure regime edge log lines. The per-container destruction stays
// at Debug; these mark the episode itself so the default log level shows why
// every invocation suddenly cold starts.
const (
	memPressureEnterMsg = "lambda pool: memory budget above high water — finished containers will be destroyed instead of kept warm (expect cold starts)"
	memPressureExitMsg  = "lambda pool: memory budget back under high water — resuming warm container pooling"
)

// Throttle reasons. These are the values real Lambda puts in the Reason field
// of a TooManyRequestsException — see the Invoke API reference, which documents
// exceeding a concurrency limit "at either the account level
// (ConcurrentInvocationLimitExceeded) or function level
// (ReservedFunctionConcurrentInvocationLimitExceeded)". Overcast reuses them
// rather than inventing its own so that application retry logic written against
// AWS matches unchanged.
const (
	// reasonReservedConcurrency: the function exceeded its own reservation.
	reasonReservedConcurrency = "ReservedFunctionConcurrentInvocationLimitExceeded"
	// reasonConcurrencyLimit: no concurrency was available anywhere. On AWS
	// that is the account limit; in Overcast it is LAMBDA_MAX_INSTANCES, which
	// occupies the same position in the model — the shared pool everything
	// draws from.
	reasonConcurrencyLimit = "ConcurrentInvocationLimitExceeded"
)

// throttleRetryAfterSeconds is the retry delay advertised to clients. AWS
// models this as TooManyRequestsException.retryAfterSeconds, bound to the
// Retry-After header in the REST protocol.
const throttleRetryAfterSeconds = 1

// throttleError is a concurrency rejection. It carries the Reason field that
// Lambda's TooManyRequestsException body includes, which protocol.AWSError has
// no room for.
type throttleError struct {
	Reason string
}

func (e *throttleError) Error() string {
	return "TooManyRequestsException: Rate Exceeded. (" + e.Reason + ")"
}

func errThrottled(reason string) error { return &throttleError{Reason: reason} }

// asThrottle reports whether err is a concurrency throttle.
func asThrottle(err error) (*throttleError, bool) {
	var t *throttleError
	if errors.As(err, &t) {
		return t, true
	}
	return nil, false
}

// writeThrottleError writes the AWS-shaped 429 for a throttled invocation.
// Real Lambda answers with Type/message/Reason in the body plus the
// X-Amzn-Errortype and Retry-After headers, which SDK retry policies key on.
func writeThrottleError(w http.ResponseWriter, r *http.Request, t *throttleError) {
	body, _ := json.Marshal(map[string]any{
		"Type":              "User",
		"message":           "Rate Exceeded.",
		"Reason":            t.Reason,
		"retryAfterSeconds": throttleRetryAfterSeconds,
	})
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Amzn-Errortype", "TooManyRequestsException")
	w.Header().Set("Retry-After", strconv.Itoa(throttleRetryAfterSeconds))
	w.Header().Set("x-amzn-requestid", protocol.RequestIDFromContext(r.Context()))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write(body)
}

// queueBudget is how long an invocation of fn may wait for capacity before it
// is throttled: its own timeout, which is the longest the caller was ever
// willing to wait for a result anyway.
func queueBudget(fn *Function) time.Duration { return functionTimeout(fn) }

// admit reserves a concurrency slot for one invocation of fn, blocking until
// capacity is available or ctx expires. The returned function releases the
// slot; it is called by Release, or by Acquire itself when the cold start
// fails.
//
// The slot is counted the moment it is granted, so it covers both the warm
// instance the caller is about to take and the container it is about to
// create.
func (p *InstancePool) admit(ctx context.Context, fn *Function) error {
	perFunction := p.maxPerFunction
	if p.debugPinned(fn) {
		// The debug port belongs to one container, so a function an editor
		// can attach to runs one execution environment; further invocations
		// queue exactly as they do at the emulator cap. The target exists
		// from the first cold start on, so a burst of first invocations is
		// admitted at the ordinary cap and only the container that binds
		// last answers on the port until the others retire.
		perFunction = 1
	}
	if reserved := reservedConcurrencyOf(fn); reserved >= 0 && reserved < perFunction {
		// A reservation below the emulator's own cap makes the emulator cap
		// irrelevant — the AWS limit binds first and throttles rather than
		// queues.
		perFunction = reserved
	}

	for {
		p.mu.Lock()
		if p.checkedOut == nil { // pool stopped
			p.mu.Unlock()
			return errThrottled(reasonConcurrencyLimit)
		}
		inFlight := p.checkedOut[fn.Name]

		// Gate 1: reserved concurrency throttles immediately, no queueing.
		if reserved := reservedConcurrencyOf(fn); reserved >= 0 && inFlight >= reserved {
			p.mu.Unlock()
			p.log.Debug("lambda pool: reserved concurrency exceeded",
				zap.String("function", fn.Name),
				zap.Int("reserved", reserved),
				zap.Int("in_flight", inFlight),
			)
			return errThrottled(reasonReservedConcurrency)
		}

		// Gate 2: emulator capacity. Wait rather than fail — this limit is
		// about the host, not the application.
		if inFlight < perFunction {
			p.checkedOut[fn.Name] = inFlight + 1
			if p.concurrencyObserver != nil {
				p.concurrencyObserver(fn.Name, inFlight+1)
			}
			p.mu.Unlock()
			return nil
		}
		waiter := p.slotFreed
		p.mu.Unlock()

		p.log.Debug("lambda pool: waiting for a per-function instance slot",
			zap.String("function", fn.Name),
			zap.Int("limit", perFunction),
		)
		if err := waitForSlot(ctx, waiter); err != nil {
			return err
		}
	}
}

// debugPinned reports whether fn has a live debug target and is therefore
// held to one execution environment. One nil check for a pool without a
// manager, which is every pool the debugger is not enabled for.
func (p *InstancePool) debugPinned(fn *Function) bool {
	return p.debugger != nil && liveDebugTarget(p.debugger, fn.Name) != nil
}

// admitContainer is the global counterpart to admit, called only when the warm
// set could not satisfy the invocation and a new container must be created.
// The caller's slot is already counted, so capacity is checked against what
// creating this container would bring the total to. Two budgets gate creation:
// the instance count and the aggregate memory budget (Σ MemorySize of live
// containers). On success the function's MemorySize is reserved as pending
// memory; the caller must commit it to the created instance
// (commitPendingMem) or give it back (unreservePendingMem).
//
// Order of preference: reclaim an idle container, then wait for one to free,
// then throttle.
func (p *InstancePool) admitContainer(ctx context.Context, fn *Function) error {
	memMB := functionMemoryMB(fn)
	for {
		p.mu.Lock()
		if p.entries == nil { // pool stopped
			p.mu.Unlock()
			return errThrottled(reasonConcurrencyLimit)
		}
		if p.totalLiveLocked() <= p.maxInstances && p.memoryFitsLocked(memMB) {
			p.pendingMemMB += memMB
			p.reservedMemMB += memMB
			p.mu.Unlock()
			return nil
		}
		overMemory := !p.memoryFitsLocked(memMB)
		// Over capacity: reclaim the least-recently-used idle container. An
		// idle container is doing nothing, so taking it costs the next
		// invocation of that function a cold start and nothing else.
		reclaimed, victim := p.reclaimIdleLocked(fn.Name)
		waiter := p.slotFreed
		p.mu.Unlock()

		if reclaimed != nil {
			// A count-cap reclaim is routine pool behaviour (Info); a
			// memory-bound one means the host budget is the constraint and the
			// operator should act — raise LAMBDA_MAX_MEMORY_MB or lower the
			// functions' MemorySize — so it surfaces at Warn. Same pattern as
			// the sweep's leveled p.log.Log call.
			msg, level := "lambda pool: instance limit reached — reclaiming an idle instance", zap.InfoLevel
			if overMemory {
				msg, level = "lambda pool: memory budget reached — reclaiming an idle instance", zap.WarnLevel
			}
			p.log.Log(level, msg,
				zap.String("for_function", fn.Name),
				zap.String("reclaimed_from", victim),
				zap.Bool("memory_bound", overMemory),
				zap.Int("max_instances", p.maxInstances),
			)
			// Mirrors the message/level split above: a reclaim forced by the
			// memory budget reads to the UI as memory pressure, one forced by
			// the instance-count cap as ordinary surplus reclamation.
			reason := evictReasonSurplus
			if overMemory {
				reason = evictReasonMemoryPressure
			}
			p.closeInstance(victim, reclaimed, "close reclaimed instance failed", reason)
			p.signalSlotFreed()
			continue
		}

		// Everything is busy. Queue until a slot frees or the caller's deadline
		// (the function timeout) expires, then throttle like AWS.
		if overMemory {
			// Once per process, not per invocation: tell the operator which
			// knob raises the budget.
			p.memWarnOnce.Do(func() {
				p.log.Warn("lambda pool: aggregate memory budget exhausted — invocations queue until containers free; raise the budget to allow more",
					zap.String("env_var", "LAMBDA_MAX_MEMORY_MB"),
					zap.Int("budget_mb", p.maxMemoryMB),
					zap.Int("reserved_mb", p.memReservedMB()),
					zap.Int("requested_mb", memMB),
				)
			})
		}
		p.log.Debug("lambda pool: instance limit reached — queueing invocation",
			zap.String("function", fn.Name),
			zap.Bool("memory_bound", overMemory),
			zap.Int("max_instances", p.maxInstances),
		)
		if err := waitForSlot(ctx, waiter); err != nil {
			return err
		}
	}
}

// ─── Memory budget bookkeeping ───────────────────────────────────────────────
//
// Memory is attributed to containers, not invocations: a container holds its
// function's MemorySize from the moment admitContainer reserves it (pending),
// through its life warm or executing (live, keyed by instance ID), until
// closeInstance releases it. Warm reuse moves nothing — the container never
// stopped existing.

// functionMemoryMB is the memory a container for fn is created with, in MB.
func functionMemoryMB(fn *Function) int {
	if fn == nil || fn.MemorySize < 1 {
		return defaultFunctionMemoryMB
	}
	return fn.MemorySize
}

// memoryFitsLocked reports whether a container of memMB fits the budget.
// Caller must hold p.mu.
func (p *InstancePool) memoryFitsLocked(memMB int) bool {
	return p.maxMemoryMB < 1 || p.reservedMemMB+memMB <= p.maxMemoryMB
}

// overMemoryHighWaterLocked reports whether reserved memory has crossed the
// high-water mark of the budget. Caller must hold p.mu.
func (p *InstancePool) overMemoryHighWaterLocked() bool {
	return p.maxMemoryMB > 0 && p.reservedMemMB*100 >= p.maxMemoryMB*memoryHighWaterPercent
}

// commitPendingMem converts a pending reservation into a live one owned by
// instanceID, after a cold start produced a container.
func (p *InstancePool) commitPendingMem(instanceID string, memMB int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.liveMemMB == nil { // pool stopped; Release will close the instance
		return
	}
	p.pendingMemMB -= memMB
	p.liveMemMB[instanceID] = memMB
}

// unreservePendingMem gives back a pending reservation whose cold start
// produced nothing, and wakes anything queued on memory.
func (p *InstancePool) unreservePendingMem(memMB int) {
	p.mu.Lock()
	if p.liveMemMB != nil {
		p.pendingMemMB -= memMB
		p.reservedMemMB -= memMB
		p.signalSlotFreedLocked()
	}
	p.mu.Unlock()
}

// registerLiveMem counts an instance created outside admission (provisioned
// concurrency) against the budget.
func (p *InstancePool) registerLiveMem(instanceID string, memMB int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.liveMemMB == nil || instanceID == "" {
		return
	}
	p.liveMemMB[instanceID] = memMB
	p.reservedMemMB += memMB
}

// releaseLiveMemLocked returns instanceID's memory to the budget and wakes
// anything queued on it. Instances the pool never admitted have no entry and
// free nothing. Caller must hold p.mu.
func (p *InstancePool) releaseLiveMemLocked(instanceID string) {
	memMB, ok := p.liveMemMB[instanceID]
	if !ok {
		return
	}
	delete(p.liveMemMB, instanceID)
	p.reservedMemMB -= memMB
	p.signalSlotFreedLocked()
}

// memReservedMB reports the memory currently counted against the budget.
func (p *InstancePool) memReservedMB() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reservedMemMB
}

// waitForSlot blocks until waiter is signalled or ctx ends. A ctx that ends
// first is reported as a throttle, matching how AWS answers an invocation that
// could not be placed.
func waitForSlot(ctx context.Context, waiter <-chan struct{}) error {
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		return errThrottled(reasonConcurrencyLimit)
	}
}

// signalSlotFreed wakes everything waiting on capacity. The waiter channel is
// closed and replaced, which broadcasts to all current waiters without
// tracking them individually.
func (p *InstancePool) signalSlotFreed() {
	p.mu.Lock()
	p.signalSlotFreedLocked()
	p.mu.Unlock()
}

// signalSlotFreedLocked is signalSlotFreed for callers already holding p.mu.
func (p *InstancePool) signalSlotFreedLocked() {
	if p.slotFreed == nil {
		return
	}
	close(p.slotFreed)
	p.slotFreed = make(chan struct{})
}

// totalLiveLocked counts every container that exists or is being created,
// across all functions. Caller must hold p.mu.
func (p *InstancePool) totalLiveLocked() int {
	total := 0
	for _, warm := range p.entries {
		total += len(warm)
	}
	for _, n := range p.checkedOut {
		total += n
	}
	return total
}

// reclaimIdleLocked removes the least-recently-used idle instance so its slot
// can be reused, and returns it (with its function name) for the caller to
// close outside the lock. Provisioned environments are never reclaimed.
// Instances belonging to preferOther are considered last, so a function under
// load does not cannibalise its own warm set while other functions sit idle.
// Caller must hold p.mu.
func (p *InstancePool) reclaimIdleLocked(preferOther string) (RuntimeInstance, string) {
	type candidate struct {
		name     string
		index    int
		lastUsed time.Time
		own      bool // belongs to preferOther
	}
	var best *candidate

	// Better means: another function's instance beats our own (a function under
	// load should not eat its own warm set while others sit idle), and within
	// the same category the one idle longest goes first.
	better := func(c candidate) bool {
		switch {
		case best == nil:
			return true
		case best.own != c.own:
			return best.own // c wins only if best is ours and c is not
		default:
			return c.lastUsed.Before(best.lastUsed)
		}
	}

	for name, warm := range p.entries {
		own := name == preferOther
		for i, entry := range warm {
			if p.isProvisionedLocked(name, entry.inst) {
				continue // reserved capacity is never reclaimed
			}
			c := candidate{name: name, index: i, lastUsed: entry.lastUsed, own: own}
			if better(c) {
				chosen := c
				best = &chosen
			}
		}
	}
	if best == nil {
		return nil, ""
	}
	warm := p.entries[best.name]
	victim := warm[best.index].inst
	p.setWarmLocked(best.name, append(warm[:best.index:best.index], warm[best.index+1:]...))
	return victim, best.name
}

// reservedConcurrencyOf returns fn's reserved concurrency, or -1 when the
// function has no reservation.
func reservedConcurrencyOf(fn *Function) int {
	if fn == nil || fn.ReservedConcurrency == nil {
		return -1
	}
	return *fn.ReservedConcurrency
}
