package lambdaweb

// provider_docker.go — the Docker environment provider.
//
// One execution environment is one container of the revision's Lambda base
// image, started without the Lambda init: the entrypoint is the embedded
// bootstrap (bootstrap.mjs), which imports the package's entry file and leaves
// the Node.js HTTP server it starts listening on 0.0.0.0:8080. Everything on
// the cold-start path is a round trip to the daemon or a copy, so the order is
// chosen to keep both few:
//
//	image ensured once per process   (no round trip after the first start)
//	code tar from the cache          (bytes shared by every start of a revision)
//	create ─► copy ‖ attach ─► start ─► [inspect] ─► TCP probe 2 ms → 50 ms
//
// Reading order: this file (lifecycle), provider_docker_image.go (pulls),
// provider_docker_code.go (the tar), provider_docker_env.go (environment and
// the create request), provider_docker_ready.go (readiness and init failure
// classification), provider_docker_env_impl.go (the running environment),
// provider_docker_logs.go (CloudWatch).

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const (
	// dockerService is the overcast.service label every container of this
	// provider carries. Lambda's own GC sweeps "lambda" and never sees it.
	dockerService = "lambdaweb"
	// labelRun marks the process that created a container, so the start-up
	// sweep of a previous run's orphans cannot reach this run's containers.
	labelRun = "overcast.lambdaweb.run"

	// instanceNamespace is where the sweep-domain identity is recorded. The
	// suffix is what the reset path recognises (serviceutil.IsInstanceNamespace).
	instanceNamespace = dockerService + serviceutil.InstanceNamespaceSuffix

	webTaskDir       = "/var/task"
	webBootstrapDir  = "/opt/overcast-web"
	webBootstrapPath = webBootstrapDir + "/bootstrap.mjs"
	webNodeBinary    = "/var/lang/bin/node"

	// webPort is the port the server listens on, AWS_LAMBDA_HTTP_ENDPOINT's.
	webPort         = 8080
	webPortKey      = "8080/tcp"
	webListenAddr   = "0.0.0.0:8080"
	defaultMemoryMB = 2048
	webCPUs         = 2

	// Exit codes bootstrap.mjs ends the process with when the entry file
	// cannot be loaded (EXIT_IMPORT_ERROR) or throws as it runs
	// (EXIT_INIT_ERROR).
	exitCodeImportError = 87
	exitCodeInitError   = 88

	// providerMaxPackageBytes bounds a package unzipped.
	providerMaxPackageBytes = 512 << 20
	// outputTailBytes bounds the process output quoted in an initFailure.
	outputTailBytes = 4 << 10
)

//go:embed bootstrap.mjs
var bootstrapSource []byte

// DockerProvider starts execution environments as Docker containers. Build it
// with NewDockerProvider, hand it the Docker client with SetDocker once the
// daemon has been probed, and give it the log writer, bus, code source and
// store with the Init* methods. Safe for concurrent use.
type DockerProvider struct {
	cfg *config.Config
	log *zap.Logger
	clk clock.Clock

	mu        sync.RWMutex
	dc        *docker.Client
	gc        *docker.GC
	logWriter events.LogWriter
	bus       *events.Bus
	code      codeSource
	instances *serviceutil.InstanceDomain
	run       string

	// endpoint is Overcast's address as containers see it, resolved on first
	// use: it may be an address on a network that exists only after the probe.
	epOnce   sync.Once
	endpoint *containerendpoint.Mapper

	images imageCache
	tars   *tarCache
	builds buildFlights

	envs    sync.Map // container id → *dockerEnv
	nameSeq atomic.Uint64
}

var _ environmentProvider = (*DockerProvider)(nil)

// NewDockerProvider returns a provider that does nothing until SetDocker is
// called. It assigns fields and starts nothing: no Docker probe, no goroutine.
func NewDockerProvider(cfg *config.Config, logger *zap.Logger, clk clock.Clock) *DockerProvider {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &DockerProvider{
		cfg:  cfg,
		log:  logger,
		clk:  clk,
		tars: newTarCache(int64(cfg.LambdaTarCacheMB) * 1024 * 1024),
	}
}

// SetDocker hands the provider the Docker client, once the daemon has been
// probed. It creates the provider's own GC and starts the clean-up of what an
// earlier run of this instance left behind, in the background.
func (p *DockerProvider) SetDocker(dc *docker.Client) {
	if dc == nil {
		return
	}
	gc := docker.NewGC(dc, p.log, p.cfg.LambdaKeepContainers, func(ctx context.Context) string {
		return p.instanceDomain().Resolve(ctx)
	})
	gc.StartRemoveLoop(context.Background())

	p.mu.Lock()
	p.dc = dc
	p.gc = gc
	if p.run == "" {
		p.run = randomHex(8)
	}
	run := p.run
	p.mu.Unlock()

	go p.sweepOrphans(dc, run)
}

// InitLogWriter wires the CloudWatch Logs writer application logs go to.
func (p *DockerProvider) InitLogWriter(w events.LogWriter) {
	p.mu.Lock()
	p.logWriter = w
	p.mu.Unlock()
}

// InitBus wires the event bus image pulls are announced on.
func (p *DockerProvider) InitBus(b *events.Bus) {
	p.mu.Lock()
	p.bus = b
	p.mu.Unlock()
}

// InitCode wires the reader of revision packages.
func (p *DockerProvider) InitCode(c codeSource) {
	p.mu.Lock()
	p.code = c
	p.mu.Unlock()
}

// InitStore wires the state store the sweep-domain identity is recorded in.
// Without it containers carry no overcast.instance label and every sweep
// removes nothing, which errs on the safe side.
func (p *DockerProvider) InitStore(st state.Store) {
	d := serviceutil.NewAnchoredInstanceDomain(st, instanceNamespace, serviceutil.DataDirAnchor(p.cfg.DataDir))
	p.mu.Lock()
	p.instances = d
	p.mu.Unlock()
}

func (p *DockerProvider) client() (*docker.Client, *docker.GC) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dc, p.gc
}

func (p *DockerProvider) instanceDomain() *serviceutil.InstanceDomain {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.instances
}

func (p *DockerProvider) codeSource() codeSource {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.code
}

func (p *DockerProvider) writer() events.LogWriter {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.logWriter
}

func (p *DockerProvider) eventBus() *events.Bus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bus
}

func (p *DockerProvider) runID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.run
}

// Start creates, provisions and starts one environment and returns it once its
// server accepts a connection. Any failure leaves no container behind.
func (p *DockerProvider) Start(ctx context.Context, spec envSpec) (environment, *initFailure) {
	dc, gc := p.client()
	if dc == nil {
		return nil, internalFailure("Docker is not available")
	}
	if spec.Revision == nil {
		return nil, internalFailure("environment spec has no revision")
	}
	image, err := imageForRuntime(spec.Revision.BuildConfig.RuntimeConfig.Runtime)
	if err != nil {
		return nil, internalFailure(err.Error())
	}

	// The code is prepared while the image is checked: on a warm process both
	// are memory lookups, on the first start of a revision the fetch and the
	// pull overlap.
	code := p.prepareCode(ctx, spec.Code)
	if err := p.ensureImage(ctx, dc, image); err != nil {
		return nil, internalFailure(fmt.Sprintf("pull image %s: %v", image, err))
	}
	mapper := p.endpointMapper(ctx, dc)
	prov, fail := code.wait(ctx)
	if fail != nil {
		return nil, fail
	}

	t0 := p.clk.Now()
	req := p.createRequest(ctx, spec, image, mapper)
	name := p.containerName(spec.Function)
	id, err := dc.CreateContainer(ctx, name, req)
	if err != nil && docker.IsImageMissingErr(err) {
		// Removed from the daemon after it was verified: forget the record,
		// pull again, create once more.
		p.images.forget(image)
		if perr := p.ensureImage(ctx, dc, image); perr != nil {
			return nil, internalFailure(fmt.Sprintf("pull image %s: %v", image, perr))
		}
		id, err = dc.CreateContainer(ctx, name, req)
	}
	if err != nil {
		if ctx.Err() != nil {
			// The daemon may still finish a create we stopped waiting for; the
			// name is ours alone.
			_ = dc.RemoveContainerForce(name)
		}
		return nil, internalFailure("create container: " + err.Error())
	}

	tCreated := p.clk.Since(t0)
	e := newDockerEnv(p, dc, gc, id, spec)
	cleanup := func() { e.discard() }

	if err := p.provision(ctx, dc, id, prov, mapper); err != nil {
		cleanup()
		return nil, internalFailure(err.Error())
	}
	tProvisioned := p.clk.Since(t0)
	if err := dc.StartContainer(ctx, id); err != nil {
		cleanup()
		return nil, internalFailure("start container: " + err.Error())
	}
	e.started = p.clk.Now()
	p.envs.Store(id, e)
	e.begin()

	tStarted := p.clk.Since(t0)
	if fail := p.awaitReady(ctx, e, spec.InitTimeout); fail != nil {
		cleanup()
		return nil, fail
	}
	p.log.Debug("lambda-web: environment ready",
		zap.String("function", spec.Function), zap.String("container", id[:min(len(id), 12)]),
		zap.Duration("create", tCreated), zap.Duration("provisioned", tProvisioned),
		zap.Duration("started", tStarted), zap.Duration("ready", p.clk.Since(t0)))
	return e, nil
}

// provision puts the code into the container and joins it to the data plane.
// The two are independent and run together.
func (p *DockerProvider) provision(ctx context.Context, dc *docker.Client, id string, tar []byte, mapper *containerendpoint.Mapper) error {
	var attachErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Joined before the start, as Lambda does: the application may dial a
		// data-plane name the moment it runs.
		attachErr = dataplane.Attach(ctx, dc, p.cfg, id, dataplane.Placement{})
	}()
	copyErr := dc.CopyToContainer(ctx, id, "/", bytes.NewReader(tar))
	if copyErr == nil {
		// With TLS on, the CA the SDKs are pointed at (see buildEnv) goes in as
		// a second, small archive; the code archive is shared and cached.
		if ca, err := mapper.CABundleTar(); err != nil {
			copyErr = err
		} else if len(ca) > 0 {
			copyErr = dc.CopyToContainer(ctx, id, "/", bytes.NewReader(ca))
		}
	}
	wg.Wait()
	if copyErr != nil {
		return fmt.Errorf("provision container filesystem: %w", copyErr)
	}
	return attachErr
}

func (p *DockerProvider) containerName(function string) string {
	return fmt.Sprintf("overcast-%s-%s-%d-%s", dockerService, sanitizeContainerName(function), p.nameSeq.Add(1), randomHex(3))
}

// endpointMapper returns the mapper for containers started here, resolving
// Overcast's container-reachable address on first use.
func (p *DockerProvider) endpointMapper(ctx context.Context, dc *docker.Client) *containerendpoint.Mapper {
	p.epOnce.Do(func() {
		host := containerendpoint.ResolveHost(ctx, dc, dataplane.Primary(p.cfg), p.log)
		p.endpoint = containerendpoint.New(p.cfg, containerendpoint.BaseURL(p.cfg, host)).WithPublishedPort(p.cfg.PublishedPort)
	})
	return p.endpoint
}

// Stop drains the provider at shutdown: every environment still running is
// stopped with the usual grace, then whatever of this instance's containers
// remains is swept. After it returns the provider's GC is inert.
func (p *DockerProvider) Stop(ctx context.Context) {
	var wg sync.WaitGroup
	p.envs.Range(func(_, v any) bool {
		e := v.(*dockerEnv)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.Stop(ctx, time.Second)
		}()
		return true
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if _, gc := p.client(); gc != nil {
		gc.DrainAndSweep(ctx, dockerService)
	}
}

// sweepOrphans removes the containers an earlier run of this instance left
// behind. Unlike docker.GC's start-up sweep it also takes running ones: an
// environment is not recreated by anything but traffic, so a running container
// of another run is serving nobody. The run label keeps it off this run's own.
func (p *DockerProvider) sweepOrphans(dc *docker.Client, run string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	domain := p.instanceDomain().Resolve(ctx)
	if domain == "" {
		return
	}
	containers, err := dc.ListContainers(ctx, dockerService)
	if err != nil {
		p.log.Debug("lambda-web: orphan sweep list failed", zap.Error(err))
		return
	}
	removed := 0
	for _, c := range containers {
		if c.Instance() != domain || c.Labels[labelRun] == run {
			continue
		}
		if c.State == "running" {
			_ = dc.StopContainer(ctx, c.ID, 1)
		}
		if err := dc.RemoveContainer(ctx, c.ID, true); err == nil {
			removed++
		}
	}
	if removed > 0 {
		p.log.Info("lambda-web: removed environments left by an earlier run", zap.Int("count", removed))
	}
}

// internalFailure is an environment that could not be started for a reason of
// the platform's, not the application's. It has no error type, as AWS sends
// none for internal errors.
func internalFailure(detail string) *initFailure {
	return &initFailure{Detail: boundedTail(detail, outputTailBytes)}
}

// boundedTail keeps the last max bytes of s, on a character boundary.
func boundedTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-max:], "")
}
