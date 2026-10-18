package lambdaweb

// engine.go — execution environments and how requests get one.
//
// An environment is one container (see environmentProvider). Environments
// belong to an endpoint's revision: pools are keyed (endpoint, revision), so
// two endpoints serving the same revision scale independently, as AWS says
// ("each endpoint scales independently, even endpoints belonging to the same
// Web Function"). The hot path is lock-free: a request walks the pool's
// immutable environment slice and claims a slot with one compare-and-swap.
// Everything that starts or stops a container takes the endpoint's mutex.

import (
	"context"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	// defaultEnvConcurrency is maxConcurrencyPerEnvironment's default.
	defaultEnvConcurrency = 64
	// initTimeout is how long a server may take to start listening.
	initTimeout = 10 * time.Second
	// maxConcurrentInits caps environments initialising at once per endpoint
	// (Platform.EndpointConcurrentInitThrottle).
	maxConcurrentInits = 16
	// startBackoff is how long a failed start is remembered, so a crash-looping
	// revision does not start a container per request.
	startBackoff = time.Second
	// headroomPercent is the load at which the next environment is started in
	// the background, so a small burst does not meet a cold start.
	headroomPercent = 80
	// drainWait bounds how long in-flight requests may finish after an
	// environment is drained (AWS: "on a best-effort basis").
	drainWait = 30 * time.Second
	// stopGrace is the SIGTERM grace period: AWS gives the process 1 s.
	stopGrace = time.Second
	// sweepInterval is how often idle environments are looked for.
	sweepInterval = 10 * time.Second
	// perEndpointRateLimit and accountRateLimit are the documented defaults when
	// an endpoint sets no rateLimit: 10,000 RPS per endpoint, 100,000 per account.
	perEndpointRateLimit = 10000
	accountRateLimit     = 100000
)

// dataPlane is the Service's runtime state. The zero value is ready to use, so
// New stays pure assignment.
type dataPlane struct {
	table     atomic.Pointer[routeTable]
	routeGen  atomic.Uint64
	rebuildMu sync.Mutex

	rtMu sync.Mutex
	rts  map[string]*endpointRT

	// envs registers every live environment, so the sweeper, the host-cap
	// reclaim and shutdown can find them without walking endpoint locks. Lock
	// order is endpointRT.mu, then pool.mu, then envMu.
	envMu sync.Mutex
	envs  map[*envState]struct{}

	// hostEnvs counts environments alive (starting or ready) across every
	// endpoint, against LAMBDA_WEB_MAX_ENVIRONMENTS.
	hostEnvs atomic.Int32
	acct     atomic.Pointer[limiter]

	// codeGone is told when an environment could not read its revision's
	// package (region, function, revision id, detail); the lifecycle marks the
	// revision Failed. Nil until wired.
	codeGone func(region, function, revisionID, detail string)

	// kick asks the rebuild loop to refresh the routing table after a control
	// plane change, so a deleted endpoint's environments drain without waiting
	// for the next request. started is set once kick exists.
	kick    chan struct{}
	started atomic.Bool

	startOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
	// ctx is cancelled at Stop; environment starts derive from it.
	ctx    context.Context
	cancel context.CancelFunc
}

// startEngine lazily starts the background sweeper and the engine context.
func (s *Service) startEngine() {
	s.dp.startOnce.Do(func() {
		s.dp.ctx, s.dp.cancel = context.WithCancel(context.Background())
		s.dp.stop = make(chan struct{})
		s.dp.acct.Store(newLimiter(accountRateLimit))
		s.dp.codeGone = func(region, function, revisionID, detail string) {
			s.markRevisionFailed(context.Background(), region, function, revisionID, "CodeUnavailable", detail)
		}
		s.dp.kick = make(chan struct{}, 1)
		s.dp.started.Store(true)
		s.dp.wg.Add(2)
		go s.sweepLoop()
		go s.rebuildLoop()
	})
}

// RoutesChanged is the control plane's change hook (InitChangeHook): the routing
// table is stale, and once the data plane is running it is refreshed at once.
func (s *Service) RoutesChanged(_, _ string) {
	s.invalidateRoutes()
	if s.dp.started.Load() {
		select {
		case s.dp.kick <- struct{}{}:
		default:
		}
	}
}

func (s *Service) rebuildLoop() {
	defer s.dp.wg.Done()
	for {
		select {
		case <-s.dp.kick:
			_, _ = s.rebuildRoutes(s.dp.ctx)
		case <-s.dp.stop:
			return
		}
	}
}

// ─── Endpoint runtime ────────────────────────────────────────────────────────

// endpointRT is the runtime state of one routed endpoint domain; it outlives
// routing-table snapshots.
type endpointRT struct {
	key                        string
	region, function, endpoint string

	limiter  atomic.Pointer[limiter]
	rate     atomic.Int32 // the configured rateLimit, -1 when unset
	disabled atomic.Bool  // rateLimit 0
	maxEnv   atomic.Int32 // 0 = unlimited
	// envs counts environments (starting or ready) across the endpoint's pools.
	envs  atomic.Int32
	inits atomic.Int32

	mu    sync.Mutex
	pools map[string]*pool
}

// endpointRuntime returns the runtime state for a routed domain, creating it.
func (s *Service) endpointRuntime(region, function, endpoint, id string) *endpointRT {
	key := region + "|" + function + "|" + endpoint + "|" + id
	s.dp.rtMu.Lock()
	defer s.dp.rtMu.Unlock()
	if rt, ok := s.dp.rts[key]; ok {
		return rt
	}
	if s.dp.rts == nil {
		s.dp.rts = make(map[string]*endpointRT)
	}
	rt := &endpointRT{key: key, region: region, function: function, endpoint: endpoint, pools: make(map[string]*pool)}
	rt.rate.Store(-2) // unconfigured; configure sets it
	s.dp.rts[key] = rt
	return rt
}

// configure applies an endpoint's scaling and throttle settings.
func (rt *endpointRT) configure(ep *endpoint) {
	rate := int32(-1)
	if ep.Throttle != nil && ep.Throttle.RateLimit != nil {
		rate = int32(*ep.Throttle.RateLimit)
	}
	if rt.rate.Swap(rate) != rate {
		switch {
		case rate == 0:
			rt.limiter.Store(nil)
		case rate < 0:
			rt.limiter.Store(newLimiter(perEndpointRateLimit))
		default:
			rt.limiter.Store(newLimiter(int(rate)))
		}
		rt.disabled.Store(rate == 0)
	}
	max := int32(0)
	if ep.Scaling != nil && ep.Scaling.MaxEnvironments != nil {
		max = int32(*ep.Scaling.MaxEnvironments)
	}
	rt.maxEnv.Store(max)
}

// retireEndpointRuntimes drains every endpoint runtime not in live (an
// endpoint, or a domain of one, that no longer exists).
func (s *Service) retireEndpointRuntimes(live map[string]struct{}) {
	s.dp.rtMu.Lock()
	var gone []*endpointRT
	for key, rt := range s.dp.rts {
		if _, ok := live[key]; !ok {
			gone = append(gone, rt)
			delete(s.dp.rts, key)
		}
	}
	s.dp.rtMu.Unlock()
	for _, rt := range gone {
		rt.drainAll(s)
	}
}

// retirePools drains the pools of revisions rt no longer routes to, once their
// requests finish.
func (rt *endpointRT) retirePools(s *Service, serving map[string]struct{}) {
	rt.mu.Lock()
	var stale []*pool
	for id, p := range rt.pools {
		if _, ok := serving[id]; !ok {
			stale = append(stale, p)
			delete(rt.pools, id)
		}
	}
	rt.mu.Unlock()
	for _, p := range stale {
		p.drainAll(s)
	}
}

func (rt *endpointRT) drainAll(s *Service) {
	rt.mu.Lock()
	pools := make([]*pool, 0, len(rt.pools))
	for _, p := range rt.pools {
		pools = append(pools, p)
	}
	rt.pools = make(map[string]*pool)
	rt.mu.Unlock()
	for _, p := range pools {
		p.drainAll(s)
	}
}

// ─── Pools and environments ──────────────────────────────────────────────────

// pool is the environments of one (endpoint, revision).
type pool struct {
	rt    *endpointRT
	revID string
	// envs is an immutable slice, replaced under mu; readers load it without
	// locking.
	envs atomic.Pointer[[]*envState]
	mu   sync.Mutex
	// lastFail remembers a failed start for startBackoff.
	lastFail atomic.Pointer[startFailure]
}

type startFailure struct {
	at   time.Time
	fail *initFailure
}

func (p *pool) snapshot() []*envState {
	if s := p.envs.Load(); s != nil {
		return *s
	}
	return nil
}

// add and remove replace the environment slice. Callers hold p.mu.
func (p *pool) add(es *envState) {
	cur := p.snapshot()
	next := make([]*envState, len(cur), len(cur)+1)
	copy(next, cur)
	next = append(next, es)
	p.envs.Store(&next)
}

func (p *pool) remove(es *envState) bool {
	cur := p.snapshot()
	for i, e := range cur {
		if e == es {
			next := make([]*envState, 0, len(cur)-1)
			next = append(next, cur[:i]...)
			next = append(next, cur[i+1:]...)
			p.envs.Store(&next)
			return true
		}
	}
	return false
}

func (p *pool) drainAll(s *Service) {
	p.mu.Lock()
	cur := p.snapshot()
	p.envs.Store(nil)
	p.mu.Unlock()
	for _, es := range cur {
		s.drainEnv(es, false)
	}
}

// envState is one environment and its request accounting.
type envState struct {
	pool  *pool
	limit int32
	ref   envRef

	// ready is closed when the start finished; failure is set before then.
	ready   chan struct{}
	failure *initFailure
	env     environment
	proxy   *httputil.ReverseProxy
	tr      *http.Transport

	inflight atomic.Int32
	draining atomic.Bool
	removed  atomic.Bool
	lastUsed atomic.Int64
	served   atomic.Int64
	started  time.Time

	drained     chan struct{}
	drainedOnce sync.Once
}

// tryAcquire claims one of the environment's concurrency slots. It reserves the
// slot while the environment is still starting, so waiters count against the
// environment's capacity.
func (es *envState) tryAcquire() bool {
	for {
		n := es.inflight.Load()
		if n >= es.limit {
			return false
		}
		if es.inflight.CompareAndSwap(n, n+1) {
			break
		}
	}
	if es.draining.Load() {
		es.release(0)
		return false
	}
	return true
}

// release returns a slot and records the time of last use (Unix nanoseconds;
// zero leaves it unchanged).
func (es *envState) release(now int64) {
	if now != 0 {
		es.lastUsed.Store(now)
	}
	if es.inflight.Add(-1) == 0 && es.draining.Load() {
		es.signalDrained()
	}
}

func (es *envState) signalDrained() { es.drainedOnce.Do(func() { close(es.drained) }) }

// isReady reports whether the start has finished.
func (es *envState) isReady() bool {
	select {
	case <-es.ready:
		return true
	default:
		return false
	}
}

// ─── Acquire ─────────────────────────────────────────────────────────────────

// acquisition is the outcome of getting an environment for a request.
type acquisition struct {
	es   *envState
	cold bool
}

// acquire returns an environment with a free slot for rev, starting one when
// none has room. The returned environment may still be starting: the caller
// waits for es.ready.
func (s *Service) acquire(rr *routeRev) (acquisition, *platformError) {
	p, rev := rr.pool, rr.rev
	rt := p.rt
	// Fast path: first-fit over the live environments packs requests onto the
	// oldest ones, so the rest can idle out.
	for _, es := range p.snapshot() {
		if es.tryAcquire() {
			cold := !es.isReady()
			s.maybeAddHeadroom(rt, p, es, rev)
			return acquisition{es: es, cold: cold}, nil
		}
	}
	return s.acquireSlow(rt, p, rev)
}

// pool returns the pool for a revision, creating it. The routing table resolves
// it once per snapshot, so the request path never calls this.
func (rt *endpointRT) pool(revID string) *pool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p, ok := rt.pools[revID]
	if !ok {
		p = &pool{rt: rt, revID: revID}
		rt.pools[revID] = p
	}
	return p
}

func (s *Service) acquireSlow(rt *endpointRT, p *pool, rev *revision) (acquisition, *platformError) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Another request may have started an environment while we waited.
	for _, es := range p.snapshot() {
		if es.tryAcquire() {
			return acquisition{es: es, cold: !es.isReady()}, nil
		}
	}
	if lf := p.lastFail.Load(); lf != nil && s.clk.Since(lf.at) < startBackoff {
		return acquisition{}, initFailureError(lf.fail)
	}
	es, perr := s.newEnv(rt, p, rev, true)
	if perr != nil {
		return acquisition{}, perr
	}
	return acquisition{es: es, cold: true}, nil
}

// newEnv starts a new environment in p, applying the endpoint, concurrent-init
// and host caps. The caller holds rt.mu and p.mu.
// newEnv starts an environment. With reserve it is created holding one slot for
// the caller, set before the environment is published so a concurrent fast-path
// acquire cannot be overwritten.
func (s *Service) newEnv(rt *endpointRT, p *pool, rev *revision, reserve bool) (*envState, *platformError) {
	if max := rt.maxEnv.Load(); max > 0 && rt.envs.Load() >= max {
		return nil, errThrottle(errTypeEndpointMaxEnvThrottle, "The endpoint reached its maximum number of environments.")
	}
	if rt.inits.Load() >= maxConcurrentInits {
		return nil, errThrottle(errTypeConcurrentInitThrottle, "Too many execution environments are initializing for this endpoint.")
	}
	if !s.reserveHostSlot() {
		return nil, errThrottle(errTypeAccountVCPUThrottle, "The host reached its maximum number of web function environments.")
	}
	limit := int32(defaultEnvConcurrency)
	if v := rev.Service.MaxConcurrencyPerEnvironment; v != nil && *v > 0 {
		limit = int32(*v)
	}
	es := &envState{
		pool: p, limit: limit, ready: make(chan struct{}), drained: make(chan struct{}),
		started: s.clk.Now(),
		ref: envRef{
			Region: rt.region, Function: rt.function, Endpoint: rt.endpoint, RevisionID: rev.ID,
			LogGroup: logGroupFor(rt.function, rev), LogStream: NewLogStreamName(s.clk, rev.ID),
			SysLogLevel: sysLogLevelFor(rev),
		},
	}
	es.lastUsed.Store(s.clk.Now().UnixNano())
	if reserve {
		es.inflight.Store(1)
	}
	p.add(es)
	s.registerEnv(es)
	rt.envs.Add(1)
	rt.inits.Add(1)
	spec := s.envSpecFor(rt, rev, es)
	s.startEngine()
	s.dp.wg.Add(1)
	go s.runEnv(es, spec)
	return es, nil
}

// maybeAddHeadroom starts the next environment in the background when es is
// past headroomPercent of its limit and there is room, so a growing load meets
// a ready environment instead of a cold start.
func (s *Service) maybeAddHeadroom(rt *endpointRT, p *pool, es *envState, rev *revision) {
	if es.inflight.Load()*100 < es.limit*headroomPercent || !es.isReady() {
		return
	}
	for _, e := range p.snapshot() {
		if e != es && e.inflight.Load()*100 < e.limit*headroomPercent {
			return // another environment has room
		}
	}
	if !rt.mu.TryLock() {
		return
	}
	defer rt.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.snapshot() {
		if e != es && e.inflight.Load()*100 < e.limit*headroomPercent {
			return
		}
	}
	// A refused headroom start is not an error: the request is already served.
	_, _ = s.newEnv(rt, p, rev, false)
}

// reserveHostSlot takes one of the host's environment slots, reclaiming an idle
// environment elsewhere when none is free.
func (s *Service) reserveHostSlot() bool {
	max := int32(s.cfg.LambdaWebMaxEnvironments)
	if max <= 0 {
		max = 8
	}
	for {
		n := s.dp.hostEnvs.Load()
		if n >= max {
			if s.reclaimIdle() {
				continue
			}
			return false
		}
		if s.dp.hostEnvs.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// reclaimIdle drains the least recently used idle environment on the host and
// reports whether it freed a slot. Callers may hold endpoint and pool locks, so
// it takes neither: the environment registry has its own.
func (s *Service) reclaimIdle() bool {
	var victim *envState
	s.dp.envMu.Lock()
	for es := range s.dp.envs {
		if es.isReady() && es.inflight.Load() == 0 && !es.draining.Load() &&
			(victim == nil || es.lastUsed.Load() < victim.lastUsed.Load()) {
			victim = es
		}
	}
	s.dp.envMu.Unlock()
	if victim == nil {
		return false
	}
	s.drainEnv(victim, true)
	return true
}

func (s *Service) registerEnv(es *envState) {
	s.dp.envMu.Lock()
	if s.dp.envs == nil {
		s.dp.envs = make(map[*envState]struct{})
	}
	s.dp.envs[es] = struct{}{}
	s.dp.envMu.Unlock()
}

// ─── Start, drain, stop ──────────────────────────────────────────────────────

// runEnv starts the environment and then waits for its process to exit.
func (s *Service) runEnv(es *envState, spec envSpec) {
	defer s.dp.wg.Done()
	ctx := s.dp.ctx
	if s.provider == nil {
		s.finishStart(es, nil, &initFailure{Type: errTypeInitCode, Detail: "no environment provider is configured"})
		return
	}
	env, fail := s.provider.Start(ctx, spec)
	s.finishStart(es, env, fail)
	if fail != nil || env == nil {
		return
	}
	select {
	case <-env.Done():
		s.environmentExited(es)
	case <-s.dp.stop:
	}
}

func (s *Service) finishStart(es *envState, env environment, fail *initFailure) {
	rt := es.pool.rt
	rt.inits.Add(-1)
	dur := s.clk.Since(es.started)
	if fail != nil || env == nil {
		if fail == nil {
			fail = &initFailure{Type: errTypeInitCode, Detail: "the provider returned no environment"}
		}
		es.failure = fail
		es.pool.lastFail.Store(&startFailure{at: s.clk.Now(), fail: fail})
		s.removeEnv(es, false)
		close(es.ready)
		s.telemetry().EnvironmentInit(envInitEvent{Env: es.ref, Started: es.started, Duration: dur, Failure: fail})
		s.log.Warn("lambda-web: environment failed to start",
			zap.String("function", rt.function), zap.String("revision", es.ref.RevisionID),
			zap.String("type", fail.Type), zap.String("detail", fail.Detail))
		return
	}
	es.env = env
	es.ref.Environment = env.ID()
	es.proxy, es.tr = s.newEnvProxy(env.Addr(), int(es.limit))
	close(es.ready)
	s.telemetry().EnvironmentInit(envInitEvent{Env: es.ref, Started: es.started, Duration: dur, Handle: env})
}

// environmentExited handles an environment whose process ended on its own.
func (s *Service) environmentExited(es *envState) {
	if es.draining.Load() {
		return
	}
	s.removeEnv(es, true)
	s.telemetrySpinDown(es, false)
}

// removeEnv takes es out of service and releases its counters, once. New
// requests stop reaching it at once (draining); the pool slice is updated
// inline, or in the background when the caller holds pool locks.
func (s *Service) removeEnv(es *envState, locked bool) {
	if !es.removed.CompareAndSwap(false, true) {
		return
	}
	es.draining.Store(true)
	p := es.pool
	p.rt.envs.Add(-1)
	s.dp.hostEnvs.Add(-1)
	s.dp.envMu.Lock()
	delete(s.dp.envs, es)
	s.dp.envMu.Unlock()
	drop := func() {
		p.mu.Lock()
		p.remove(es)
		p.mu.Unlock()
	}
	if locked {
		go drop()
	} else {
		drop()
	}
}

// drainEnv stops routing to es and, once its requests finish (or drainWait
// passes), stops it. locked says the caller holds endpoint or pool locks.
func (s *Service) drainEnv(es *envState, locked bool) {
	if !es.draining.CompareAndSwap(false, true) {
		return // already draining, failed, or exited
	}
	s.removeEnv(es, locked)
	if es.inflight.Load() == 0 {
		es.signalDrained()
	}
	s.dp.wg.Add(1)
	go func() {
		defer s.dp.wg.Done()
		select {
		case <-es.ready:
		case <-s.dp.stop:
			return
		}
		select {
		case <-es.drained:
		case <-s.clk.After(drainWait):
		case <-s.dp.stop:
		}
		s.stopEnv(es)
	}()
}

// stopEnv stops the environment's process and reports the spin-down.
func (s *Service) stopEnv(es *envState) {
	if es.env == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	begin := s.clk.Now()
	err := es.env.Stop(ctx, stopGrace)
	if es.tr != nil {
		es.tr.CloseIdleConnections()
	}
	s.telemetrySpinDown(es, err != nil || s.clk.Since(begin) > stopGrace+500*time.Millisecond)
}

func (s *Service) telemetrySpinDown(es *envState, timedOut bool) {
	if es.env == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.telemetry().EnvironmentSpinDown(spinDownEvent{
		Env: es.ref, Started: es.started, Duration: s.clk.Since(es.started),
		Reason: es.env.ExitReason(), Stats: es.env.Stats(ctx), Requests: es.served.Load(), Timeout: timedOut,
	})
}

// ─── Sweeper and shutdown ────────────────────────────────────────────────────

// sweepLoop scales idle environments away: scale to zero when there is no
// traffic.
func (s *Service) sweepLoop() {
	defer s.dp.wg.Done()
	t := s.clk.Ticker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.sweepIdle()
		case <-s.dp.stop:
			return
		}
	}
}

// sweepIdle drains every ready environment idle longer than the idle TTL.
func (s *Service) sweepIdle() {
	ttl := s.cfg.LambdaWebIdleTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	cutoff := s.clk.Now().Add(-ttl).UnixNano()
	var idle []*envState
	s.dp.envMu.Lock()
	for es := range s.dp.envs {
		if es.isReady() && es.inflight.Load() == 0 && es.lastUsed.Load() < cutoff {
			idle = append(idle, es)
		}
	}
	s.dp.envMu.Unlock()
	for _, es := range idle {
		// drainEnv re-checks nothing: a request that claimed the environment
		// between the scan and now makes tryAcquire/draining race, and the
		// drain waits for its in-flight count to reach zero before stopping.
		s.drainEnv(es, false)
	}
}

// Stop drains and stops every environment, then the provider. It satisfies
// router.Stopper.
func (s *Service) Stop(ctx context.Context) error {
	if s.dp.stop != nil {
		select {
		case <-s.dp.stop:
		default:
			close(s.dp.stop)
		}
		s.dp.cancel()
	}
	s.dp.envMu.Lock()
	all := make([]*envState, 0, len(s.dp.envs))
	for es := range s.dp.envs {
		all = append(all, es)
	}
	s.dp.envMu.Unlock()
	var wg sync.WaitGroup
	for _, es := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stopEnv(es)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); s.dp.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.StopRevisionWorker(ctx)
	var err error
	if st, ok := s.provider.(interface{ Stop(context.Context) error }); ok {
		err = st.Stop(ctx)
	}
	if st, ok := s.tel.(interface{ Stop(context.Context) }); ok {
		st.Stop(ctx)
	}
	return err
}

// telemetry returns the wired sink, or a no-op.
func (s *Service) telemetry() telemetry {
	if s.tel != nil {
		return s.tel
	}
	return noTelemetry{}
}

type noTelemetry struct{}

func (noTelemetry) EnvironmentInit(envInitEvent)      {}
func (noTelemetry) EnvironmentSpinDown(spinDownEvent) {}
func (noTelemetry) RequestDone(requestEvent)          {}
func (noTelemetry) Throttled(throttleEvent)           {}

// liveEnvironments counts the environments an endpoint has running or starting,
// across its domains and revisions.
func (s *Service) liveEnvironments(function, endpoint string) int {
	s.dp.rtMu.Lock()
	defer s.dp.rtMu.Unlock()
	n := 0
	for _, rt := range s.dp.rts {
		if rt.function == function && rt.endpoint == endpoint {
			n += int(rt.envs.Load())
		}
	}
	return n
}
