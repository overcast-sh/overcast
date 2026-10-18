package lambdaweb

// provider_docker_env_impl.go — one running environment: a container whose
// server is listening.

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
)

// dockerEnv is the environment implementation for a Docker container. Its
// exit is observed by one goroutine that waits on the container; everything
// that needs to know whether the process is gone (the readiness probe, the
// engine's pool) reads Done.
type dockerEnv struct {
	p    *DockerProvider
	dc   *docker.Client
	gc   *docker.GC
	id   string
	spec envSpec

	// started is when the container was started; addr/direct are set by the
	// readiness phase before the environment is handed out.
	started time.Time
	addr    string
	direct  bool

	done       chan struct{}
	finishOnce sync.Once
	exitMu     sync.Mutex
	exitCode   int
	oom        bool
	requested  atomic.Bool

	waitCancel context.CancelFunc
	pump       *logPump

	stopOnce sync.Once
	stopDone chan struct{}
	stopErr  error

	statsMu   sync.Mutex
	peakBytes int64
	cpuNanos  uint64
}

var _ environment = (*dockerEnv)(nil)

func newDockerEnv(p *DockerProvider, dc *docker.Client, gc *docker.GC, id string, spec envSpec) *dockerEnv {
	return &dockerEnv{
		p: p, dc: dc, gc: gc, id: id, spec: spec,
		done:     make(chan struct{}),
		stopDone: make(chan struct{}),
	}
}

// begin starts watching the container: the single waiter that closes Done and
// the log follower.
func (e *dockerEnv) begin() {
	ctx, cancel := context.WithCancel(context.Background())
	e.waitCancel = cancel
	go e.waitLoop(ctx)
	e.pump = e.p.startLogs(e.dc, e.id, e.spec)
}

// waitLoop blocks on the daemon's wait for the container and records how it
// ended. A broken connection is retried after asking the daemon whether the
// container is still there.
func (e *dockerEnv) waitLoop(ctx context.Context) {
	for attempt := 0; ; attempt++ {
		code, err := e.dc.WaitContainer(ctx, e.id)
		if err == nil {
			e.finish(code, nil)
			return
		}
		if ctx.Err() != nil {
			e.finish(-1, nil)
			return
		}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
		info, ierr := e.dc.InspectContainer(ictx, e.id)
		cancel()
		if ierr != nil && docker.IsNotFound(ierr) {
			e.finish(-1, nil)
			return
		}
		if ierr == nil && !info.State.Running {
			e.finish(info.State.ExitCode, info)
			return
		}
		backoff := time.Duration(attempt+1) * 100 * time.Millisecond
		select {
		case <-e.p.clk.After(min(backoff, time.Second)):
		case <-ctx.Done():
			e.finish(-1, nil)
			return
		}
	}
}

// finish records the exit and closes Done. The out-of-memory flag lives in the
// daemon's inspect, so it is read before Done closes: a reader of Done must be
// able to trust ExitReason.
func (e *dockerEnv) finish(code int, info *docker.ContainerInspect) {
	e.finishOnce.Do(func() {
		if info == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			info, _ = e.dc.InspectContainer(ctx, e.id)
			cancel()
		}
		e.exitMu.Lock()
		e.exitCode = code
		if info != nil {
			e.oom = info.State.OOMKilled
			if code < 0 {
				e.exitCode = info.State.ExitCode
			}
		}
		e.exitMu.Unlock()
		close(e.done)
	})
}

func (e *dockerEnv) ID() string            { return e.id }
func (e *dockerEnv) Addr() string          { return e.addr }
func (e *dockerEnv) Done() <-chan struct{} { return e.done }
func (e *dockerEnv) exited() bool          { return isClosed(e.done) }
func (e *dockerEnv) exitStatus() (int, bool) {
	e.exitMu.Lock()
	defer e.exitMu.Unlock()
	return e.exitCode, e.oom
}

// ExitReason says why the process ended; meaningful once Done is closed.
func (e *dockerEnv) ExitReason() exitReason {
	code, oom := e.exitStatus()
	if code < 0 {
		code = 0
	}
	return exitReason{OOM: oom, Code: code, Requested: e.requested.Load()}
}

// Stats samples the container: one non-streaming stats call. The memory figure
// is the highest sample seen, so a spike between two samples that the later one
// no longer shows is not lost. After the process exits the last sample stands.
func (e *dockerEnv) Stats(ctx context.Context) envStats {
	if !e.exited() {
		if s, err := e.dc.ContainerStatsOneShot(ctx, e.id); err == nil {
			e.statsMu.Lock()
			e.peakBytes = max(e.peakBytes, s.MemoryUsageBytes)
			e.cpuNanos = max(e.cpuNanos, s.CPUTotalUsage)
			e.statsMu.Unlock()
		}
	}
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return envStats{
		MaxMemoryMB: float64(e.peakBytes) / (1024 * 1024),
		CPUTime:     time.Duration(e.cpuNanos),
	}
}

// Stop sends SIGTERM, waits up to grace for the process to exit, kills it, and
// removes the container. The daemon's stop does the first three: SIGTERM, the
// wait (whole seconds, rounded up), SIGKILL. Safe to call concurrently and more
// than once; later calls wait for the first and return its result.
func (e *dockerEnv) Stop(ctx context.Context, grace time.Duration) error {
	e.stopOnce.Do(func() {
		// The work outlives a caller that gives up: a half-stopped environment
		// is worse than one stopped a moment late.
		go func() {
			defer close(e.stopDone)
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace+15*time.Second)
			defer cancel()
			e.stopErr = e.stop(sctx, grace)
		}()
	})
	select {
	case <-e.stopDone:
		return e.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *dockerEnv) stop(ctx context.Context, grace time.Duration) error {
	e.requested.Store(true)
	var err error
	if !e.exited() {
		secs := int(math.Ceil(grace.Seconds()))
		err = e.dc.StopContainer(ctx, e.id, max(secs, 0))
		// The stop has returned, so the process is gone; the waiter learns of
		// it a moment later.
		select {
		case <-e.done:
		case <-time.After(2 * time.Second):
		}
	}
	e.teardown()
	return err
}

// teardown ends what hangs off the container, then removes it. The log
// follower goes first and is waited for - its last pass reads the daemon's copy
// of the output, which removal destroys.
func (e *dockerEnv) teardown() {
	e.pump.stop(3 * time.Second)
	if e.waitCancel != nil {
		e.waitCancel()
	}
	e.p.envs.Delete(e.id)
	// Removed before Stop returns: the engine counts the environment gone only
	// when its container is.
	if err := e.dc.RemoveContainerForce(e.id); err != nil {
		e.p.log.Warn("lambda-web: remove environment", zap.String("container", e.id), zap.Error(err))
		if e.gc != nil {
			e.gc.ScheduleRemove(e.id) // retried with backoff
		}
	}
}

// discard removes an environment that never became ready (or never started):
// synchronously, so that a failed Start leaves nothing behind when it returns.
func (e *dockerEnv) discard() {
	e.pump.stop(3 * time.Second)
	if e.waitCancel != nil {
		e.waitCancel()
	}
	e.p.envs.Delete(e.id)
	if err := e.dc.RemoveContainerForce(e.id); err != nil {
		e.p.log.Warn("lambda-web: could not remove failed environment",
			zap.String("container", e.id), zap.Error(err))
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
