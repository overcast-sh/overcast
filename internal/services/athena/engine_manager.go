package athena

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// engine_manager.go — the one Trino container every query runs on.
//
// Nothing is started until a query needs it (the lazy singleton ECR's
// ensureRegistry uses): the first query waits, QUEUED, while the image is
// pulled and the engine starts, and every later one reuses it. After
// engineIdleTimeout with no query running it is stopped, and the next query
// starts it again. Docker is wired and torn down the way ElastiCache's
// containers are: SetDocker sweeps what an earlier run left, and Stop
// removes what this one started.

const (
	// engineIdleTimeout is how long the engine stays up with nothing to run.
	engineIdleTimeout = 15 * time.Minute
	// enginePullTimeout bounds the image pull; the image is over 1 GB.
	enginePullTimeout = 20 * time.Minute
	// engineReadyTimeout bounds the wait for a started engine to answer.
	engineReadyTimeout = 3 * time.Minute
	// engineReadyInterval is how often a starting engine is asked if it is up.
	engineReadyInterval = 500 * time.Millisecond
	// engineResourceID is the resource label the container carries.
	engineResourceID = "engine"
)

// Engine states, as the status endpoint reports them.
const (
	engineOff      = "off"      // ATHENA_ENGINE=inert, or no Docker: queries run inert
	engineStopped  = "stopped"  // not running; the next query starts it
	enginePulling  = "pulling"  // pulling the image
	engineStarting = "starting" // container started, engine not answering yet
	engineReady    = "ready"
	engineFailed   = "failed" // the last start failed; the next query tries again
)

var errEngineUnavailable = errors.New("athena: no Docker daemon to run the query engine on")

// engineBoot is one start of the engine: in flight until done closes, then
// either an endpoint or the reason there is none.
type engineBoot struct {
	done        chan struct{}
	endpoint    string
	containerID string
	err         error
}

type engineManager struct {
	cfg       *config.Config
	log       *serviceutil.ServiceLogger
	clk       clock.Clock
	client    *trinoClient
	instances *serviceutil.InstanceDomain
	gateway   engineGateway
	bgCtx     context.Context
	bgCancel  context.CancelFunc

	mu       sync.Mutex
	docker   *docker.Client
	puller   *docker.ImagePuller
	gc       *docker.GC
	boot     *engineBoot
	inflight int
	idle     *clock.Timer
	stopping bool
	status   engineStatus
}

func newEngineManager(cfg *config.Config, log *serviceutil.ServiceLogger, clk clock.Clock, instances *serviceutil.InstanceDomain) *engineManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &engineManager{cfg: cfg, log: log, clk: clk, client: newTrinoClient(), instances: instances,
		bgCtx: ctx, bgCancel: cancel, status: engineStatus{State: engineOff}}
}

// setDocker wires the daemon the engine runs on, and removes any engine an
// earlier run of this instance left behind.
func (m *engineManager) setDocker(dc *docker.Client) {
	gc := docker.NewGC(dc, m.log.ZapLogger(), m.cfg.AthenaKeepContainers, m.instances.Resolve)
	gc.StartRemoveLoop(m.bgCtx)
	gc.Sweep(serviceName)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docker, m.puller, m.gc = dc, docker.NewImagePuller(dc), gc
	m.status.State = engineStopped
}

// available reports whether queries can run on the engine at all.
func (m *engineManager) available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.docker != nil && !m.stopping
}

// acquire returns the engine's endpoint, starting it first if it is not
// running, and holds it up until release is called.
func (m *engineManager) acquire(ctx context.Context) (endpoint string, release func(), err error) {
	m.mu.Lock()
	if m.docker == nil || m.stopping {
		m.mu.Unlock()
		return "", nil, errEngineUnavailable
	}
	if m.boot == nil {
		m.boot = &engineBoot{done: make(chan struct{})}
		go m.start(m.boot)
	}
	b := m.boot
	m.inflight++
	if m.idle != nil {
		m.idle.Stop()
		m.idle = nil
	}
	m.mu.Unlock()

	select {
	case <-b.done:
	case <-ctx.Done():
		m.release()
		return "", nil, ctx.Err()
	}
	if b.err != nil {
		m.release()
		return "", nil, b.err
	}
	return b.endpoint, m.release, nil
}

// release lets go of the engine, and schedules it to stop once nothing has
// held it for engineIdleTimeout.
func (m *engineManager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	m.status.LastUsedAt = m.clk.Now()
	if m.inflight == 0 && m.boot != nil && !m.stopping {
		m.idle = m.clk.AfterFunc(engineIdleTimeout, m.stopIdle)
	}
}

// stopIdle stops an engine nothing has used for engineIdleTimeout.
func (m *engineManager) stopIdle() {
	m.mu.Lock()
	b := m.boot
	if m.inflight > 0 || b == nil || m.stopping {
		m.mu.Unlock()
		return
	}
	select {
	case <-b.done:
	default: // still starting: it is not idle
		m.mu.Unlock()
		return
	}
	m.boot, m.idle = nil, nil
	m.status.State, m.status.ContainerID, m.status.Endpoint = engineStopped, "", ""
	gc := m.gc
	m.mu.Unlock()
	m.log.Info("stopping the idle query engine", zap.String("container", b.containerID))
	retire(gc, b.containerID)
}

// retire stops a container and removes it, unless ATHENA_KEEP_CONTAINERS
// asks for it to be kept.
func retire(gc *docker.GC, containerID string) {
	if containerID != "" {
		gc.StopNow(containerID)
		gc.ScheduleRemove(containerID)
	}
}

// invalidate forgets an engine that stopped answering, so the next query
// starts a new one rather than dialling a dead one.
func (m *engineManager) invalidate(endpoint string) {
	m.mu.Lock()
	b := m.boot
	if b == nil || b.endpoint != endpoint {
		m.mu.Unlock()
		return
	}
	m.boot = nil
	m.status.State, m.status.ContainerID, m.status.Endpoint = engineStopped, "", ""
	gc := m.gc
	m.mu.Unlock()
	m.log.Warn("the query engine stopped answering; the next query starts a new one", zap.String("container", b.containerID))
	retire(gc, b.containerID)
}

// start runs one boot of the engine: pull, create, configure, start, wait.
func (m *engineManager) start(b *engineBoot) {
	defer close(b.done)
	begun := m.clk.Now()
	m.setState(enginePulling, "")
	pullCtx, cancel := context.WithTimeout(m.bgCtx, enginePullTimeout)
	err := m.puller.Ensure(pullCtx, m.cfg.AthenaEngineImage)
	cancel()
	pulled := m.clk.Now()
	if err == nil {
		m.setState(engineStarting, "")
		b.containerID, b.endpoint, err = m.startContainer(m.bgCtx)
	}
	if err == nil {
		err = m.awaitReady(b)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.PullMillis = pulled.Sub(begun).Milliseconds()
	m.status.StartMillis = m.clk.Since(pulled).Milliseconds()
	if err != nil {
		b.err = fmt.Errorf("the query engine did not start: %w", err)
		m.boot = nil
		m.status.State, m.status.LastError = engineFailed, err.Error()
		m.log.Warn("query engine did not start", zap.Error(err))
		retire(m.gc, b.containerID)
		return
	}
	m.status.State, m.status.LastError = engineReady, ""
	m.status.ContainerID, m.status.Endpoint, m.status.StartedAt = b.containerID, b.endpoint, m.clk.Now()
	m.log.Info("query engine ready", zap.String("container", b.containerID), zap.Int64("pullMillis", m.status.PullMillis),
		zap.Int64("startMillis", m.status.StartMillis))
}

// awaitReady waits for a started engine to report it has finished starting,
// failing at once if its container exits.
func (m *engineManager) awaitReady(b *engineBoot) error {
	ctx, cancel := context.WithTimeout(m.bgCtx, engineReadyTimeout)
	defer cancel()
	tick := m.clk.Ticker(engineReadyInterval)
	defer tick.Stop()
	for {
		if starting, err := m.client.info(ctx, b.endpoint); err == nil && !starting {
			return nil
		}
		if exited := m.containerExited(ctx, b.containerID); exited != "" {
			return errors.New(exited)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the engine did not answer within %s", engineReadyTimeout)
		case <-tick.C:
		}
	}
}

func (m *engineManager) setState(state, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State, m.status.LastError = state, lastError
}

// stop fails no queries itself — the executor's stop does — and removes
// the engine's container.
func (m *engineManager) stop(ctx context.Context) {
	m.mu.Lock()
	m.stopping = true
	if m.idle != nil {
		m.idle.Stop()
	}
	gc := m.gc
	m.mu.Unlock()
	m.bgCancel()
	if gc != nil {
		gc.DrainAndSweep(ctx, serviceName)
	}
	m.gateway.close()
}
