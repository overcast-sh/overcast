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
	"github.com/overcast-sh/overcast/internal/events"
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

// engineState is the engine's state, as the status endpoint reports it. An
// alias, so cmd/tsgen renders the constants below as the console's union.
type engineState = string

// Engine states.
const (
	engineOff      engineState = "off"      // ATHENA_ENGINE=inert, or no Docker: queries run inert
	engineProbing  engineState = "probing"  // waiting to learn whether Docker is there
	engineStopped  engineState = "stopped"  // not running; the next query starts it
	enginePulling  engineState = "pulling"  // pulling the image
	engineStarting engineState = "starting" // container started, engine not answering yet
	engineReady    engineState = "ready"
	engineFailed   engineState = "failed" // the last start failed; the next query tries again
)

var errEngineUnavailable = errors.New("athena: no Docker daemon to run the query engine on")

// engineBoot is one start of the engine: in flight until done closes, then
// either an endpoint or the reason there is none.
type engineBoot struct {
	done        chan struct{}
	endpoint    string
	containerID string
	settings    engineSettings
	err         error

	// catalogsMu serialises syncing the S3 Tables catalogs, and guards
	// tableBuckets: the buckets this engine has a catalog for.
	catalogsMu   sync.Mutex
	tableBuckets map[string]bool
}

type engineManager struct {
	cfg       *config.Config
	log       *serviceutil.ServiceLogger
	clk       clock.Clock
	client    *trinoClient
	instances *serviceutil.InstanceDomain
	gateway   engineGateway
	// tables lists the table buckets the engine gives a catalog each; until
	// InitS3Tables sets it, the engine has no table bucket catalogs.
	tables   events.S3TablesCatalog
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// settled closes once it is known whether Docker is there: SetDocker, or
	// the probe failing. A query waits for it rather than running inert in
	// the moments after Overcast starts.
	settled    chan struct{}
	settleOnce sync.Once

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
	m := &engineManager{cfg: cfg, log: log, clk: clk, client: newTrinoClient(), instances: instances,
		bgCtx: ctx, bgCancel: cancel, settled: make(chan struct{}), status: engineStatus{State: engineOff}}
	if cfg.AthenaDockerSocket == "" { // no probe will run
		m.settle()
	} else {
		m.status.State = engineProbing
	}
	return m
}

func (m *engineManager) settle() { m.settleOnce.Do(func() { close(m.settled) }) }

// setDocker wires the daemon the engine runs on, and removes any engine an
// earlier run of this instance left behind.
func (m *engineManager) setDocker(dc *docker.Client) {
	gc := docker.NewGC(dc, m.log.ZapLogger(), m.cfg.AthenaKeepContainers, m.instances.Resolve)
	gc.StartRemoveLoop(m.bgCtx)
	if !m.cfg.AthenaKeepContainers {
		go m.reclaim(dc)
	}
	m.mu.Lock()
	m.docker, m.puller, m.gc = dc, docker.NewImagePuller(dc), gc
	m.status.State = engineStopped
	m.mu.Unlock()
	m.settle()
}

// dockerUnavailable records that no daemon answered: queries run inert.
func (m *engineManager) dockerUnavailable() {
	m.mu.Lock()
	m.status.State = engineOff
	m.mu.Unlock()
	m.settle()
}

// reclaim removes this instance's engines from an earlier run, running or
// not: a crash leaves one running, and the engine keeps nothing worth
// adopting.
func (m *engineManager) reclaim(dc *docker.Client) {
	containers, err := dc.ListContainers(m.bgCtx, serviceName)
	if err != nil {
		m.log.Debug("could not list earlier engines", zap.Error(err))
		return
	}
	domain := m.instances.Resolve(m.bgCtx)
	for _, c := range containers {
		if domain != "" && c.Instance() == domain && c.ResourceID() == engineResourceID {
			if err := dc.RemoveContainer(m.bgCtx, c.ID, true); err != nil {
				m.log.Warn("could not remove an earlier engine", zap.String("container", c.ID), zap.Error(err))
			}
		}
	}
}

// available reports whether queries can run on the engine: Docker is
// wired, or it may yet be.
func (m *engineManager) available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.stopping && (m.docker != nil || m.status.State == engineProbing)
}

// acquire returns the engine's endpoint, starting it first if it is not
// running, and holds it up until release is called.
func (m *engineManager) acquire(ctx context.Context) (endpoint string, release func(), err error) {
	select {
	case <-m.settled:
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	m.mu.Lock()
	if m.docker == nil || m.stopping {
		m.mu.Unlock()
		return "", nil, errEngineUnavailable
	}
	if m.boot == nil {
		m.boot = &engineBoot{done: make(chan struct{}), tableBuckets: map[string]bool{}}
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
	if m.inflight == 0 && m.boot != nil && isDone(m.boot.done) {
		m.armIdleLocked()
	}
}

// armIdleLocked schedules the idle stop, replacing any scheduled before. A
// boot still in flight arms it itself when it finishes (start). The caller
// holds mu.
func (m *engineManager) armIdleLocked() {
	if m.stopping {
		return
	}
	if m.idle != nil {
		m.idle.Stop()
	}
	m.idle = m.clk.AfterFunc(engineIdleTimeout, m.stopIdle)
}

func isDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// stopIdle stops an engine nothing has used for engineIdleTimeout.
func (m *engineManager) stopIdle() {
	m.mu.Lock()
	b := m.boot
	if m.inflight > 0 || b == nil || m.stopping || !isDone(b.done) {
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
	b := m.bootAtLocked(endpoint)
	if b == nil {
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

// bootAt is the running boot of the engine at endpoint, or nil when the
// engine there has since been stopped or replaced.
func (m *engineManager) bootAt(endpoint string) *engineBoot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bootAtLocked(endpoint)
}

// bootAtLocked is bootAt for a caller that holds mu. A boot still starting
// is not yet anyone's, and its endpoint not yet written.
func (m *engineManager) bootAtLocked(endpoint string) *engineBoot {
	if m.boot == nil || !isDone(m.boot.done) || m.boot.endpoint != endpoint {
		return nil
	}
	return m.boot
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
		var c engineContainer
		c, err = m.startContainer(m.bgCtx)
		b.containerID, b.endpoint, b.settings = c.id, c.endpoint, c.settings
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
	if m.inflight == 0 { // every query that wanted it gave up while it started
		m.armIdleLocked()
	}
	m.status.ContainerID, m.status.Endpoint, m.status.StartedAt = b.containerID, b.endpoint, m.clk.Now()
	m.log.Info("query engine ready", zap.String("container", b.containerID), zap.Int64("pullMillis", m.status.PullMillis),
		zap.Int64("startMillis", m.status.StartMillis))
}

// awaitReady waits for a started engine to report it has finished starting,
// failing at once if its container exits, and then gives it the Glue
// catalogs, all within engineReadyTimeout.
func (m *engineManager) awaitReady(b *engineBoot) error {
	ctx, cancel := context.WithTimeout(m.bgCtx, engineReadyTimeout)
	defer cancel()
	tick := m.clk.Ticker(engineReadyInterval)
	defer tick.Stop()
	for {
		if starting, err := m.client.info(ctx, b.endpoint); err == nil && !starting {
			return m.createCatalogs(ctx, b.endpoint, glueCatalogs(b.settings))
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

func (m *engineManager) setState(state engineState, lastError string) {
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
	m.settle()
	gc := m.gc
	m.mu.Unlock()
	m.bgCancel()
	if gc != nil {
		gc.DrainAndSweep(ctx, serviceName)
	}
	m.gateway.close()
}
