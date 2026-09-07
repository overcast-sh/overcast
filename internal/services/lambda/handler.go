package lambda

// handler.go contains the Handler struct for Lambda.
// Lambda uses REST routing (not target-dispatch), so there is no ops map.
// Route registration is done in service.go's RegisterRoutes.
// Stub handlers live in handler_stubs.go; implemented handlers live in
// handler_<group>.go files as they are built out.

import (
	"context"
	"net/http"
	"sync"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/eventtarget"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

func lambdaFunctionNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Function not found: " + name,
		HTTPStatus: http.StatusNotFound,
	}
}

func lambdaFunctionAlreadyExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceConflictException",
		Message:    "Function already exist: " + name,
		HTTPStatus: http.StatusConflict,
	}
}

// Handler holds Lambda handler dependencies.
type Handler struct {
	cfg           *config.Config
	log           *serviceutil.ServiceLogger
	clk           clock.Clock
	runtimes      *runtimeRegistry
	ls            *lambdaStore
	logWriter     events.LogWriter
	bus           *events.Bus
	tracker       *instanceTracker
	esm           *esmStore
	esmDelivery   *esmDeliveryManager
	asyncWg       sync.WaitGroup // tracks in-flight async invocations
	asyncMu       sync.Mutex     // guards asyncClosed against a racing asyncWg.Add
	asyncClosed   bool           // set by StopAsync; startAsync accepts nothing after it
	vpcResolverMu sync.RWMutex
	vpcResolver   VPCNetworkResolver

	efsResolverMu sync.RWMutex
	efsResolver   EFSVolumeResolver

	// targets delivers a dead-lettered failure to the SQS queue or SNS topic
	// its ARN names. Wired by Service.InitRouter, so it is nil in a test
	// server built without a root router — a delivery attempted then is
	// reported as a wiring fault rather than pretending to have happened.
	targetsMu sync.RWMutex
	targets   *eventtarget.Dispatcher

	// prewarmer, when set, starts a background Docker image pull for fn so
	// the first Invoke does not pay the cold-pull cost on the request path.
	// Wired by Service.initDockerRuntime once ContainerRuntime is up; nil
	// before Docker is ready and in test servers that don't use containers.
	// onReady is called on the background goroutine with the pull result.
	prewarmer  func(fn *Function, onReady func(err error))
	prewarmMu  sync.Mutex
	prewarming map[string]functionImagePullGeneration
	// s3Fetch, when set, allows handlers to eagerly fetch code zips from S3
	// at CreateFunction / UpdateFunctionCode time. Without this, code that
	// is uploaded to S3 *before* the Lambda function is created (the typical
	// CDK deploy ordering: PutObject → CreateFunction) would never have its
	// CodeZip populated — the s3SyncWatcher only fires on subsequent
	// PutObject events. Wired by Service.InitS3Sync.
	s3Fetch S3FetchFunc
	// s3Sync tracks reactive S3 ordering state so successful DeleteFunction
	// calls can release the deleted function's slot. The mutex covers service
	// initialization racing an early request.
	s3SyncMu      sync.RWMutex
	s3SyncWatcher *s3SyncWatcher
	// proactive, when set, drives the post-deploy settle debounce: cold-start
	// artifact pre-fill always, plus execution-environment pre-creation when
	// LAMBDA_PROACTIVE_INIT is enabled (see proactive.go). Wired by
	// Service.initDockerRuntime; nil — and safe to call — before Docker is up.
	proactive *proactiveIniter
	// tarCacheStats reports the artifact cache for the debug endpoint; nil
	// until the container runtime is up.
	tarCacheStats func() (entries int, bytes, maxBytes int64)

	// debugger owns the debug targets functions are registered with at cold
	// start (see debugger.go). The handler only ever releases one, when the
	// function is deleted. Nil when the debugger is not wired.
	debugger *debugger.Manager

	// metrics is the shared service-metrics recorder (see metrics_lambda.go),
	// nil until Service.InitMetrics is called (or when automatic collection
	// is disabled). recordInvocationOutcome and recordConcurrency are both
	// nil-safe against it.
	metrics metricsRecorder
}

func (h *Handler) setVPCResolver(r VPCNetworkResolver) {
	h.vpcResolverMu.Lock()
	h.vpcResolver = r
	h.vpcResolverMu.Unlock()
}

func (h *Handler) setDeadLetterTargets(d *eventtarget.Dispatcher) {
	h.targetsMu.Lock()
	h.targets = d
	h.targetsMu.Unlock()
}

// deadLetterTargets is passed to the ESM delivery manager as a method value, so
// the two do not have to be wired in a particular order.
func (h *Handler) deadLetterTargets() *eventtarget.Dispatcher {
	h.targetsMu.RLock()
	defer h.targetsMu.RUnlock()
	return h.targets
}

func (h *Handler) getVPCResolver() VPCNetworkResolver {
	h.vpcResolverMu.RLock()
	r := h.vpcResolver
	h.vpcResolverMu.RUnlock()
	return r
}

func (h *Handler) setEFSResolver(r EFSVolumeResolver) {
	h.efsResolverMu.Lock()
	h.efsResolver = r
	h.efsResolverMu.Unlock()
}

func (h *Handler) getEFSResolver() EFSVolumeResolver {
	h.efsResolverMu.RLock()
	r := h.efsResolver
	h.efsResolverMu.RUnlock()
	return r
}

func (h *Handler) setPrewarmer(prewarmer func(*Function, func(error))) {
	h.prewarmMu.Lock()
	h.prewarmer = prewarmer
	h.prewarmMu.Unlock()
}

// EFSVolumeResolver maps an EFS access-point ARN to the Docker volume backing
// its file system plus the access point's root directory as a volume subpath
// ("" for the volume root). Implemented by the EFS service; ok is false in
// mock mode, while Docker is unavailable, or for an unknown access point —
// callers then skip the mount and the function runs without shared storage.
type EFSVolumeResolver interface {
	EFSVolumeForAccessPoint(ctx context.Context, accessPointARN string) (volume, subpath string, ok bool)
}

// VPCNetworkResolver resolves VPC configuration for Lambda functions.
// Implemented by the EC2 service; nil when EC2 is not enabled.
type VPCNetworkResolver interface {
	// VpcIDForSubnet returns the VPC ID that owns the given subnet.
	VpcIDForSubnet(ctx context.Context, subnetID string) string
	// VPCNetworkStatus returns the launchability status for the VPC.
	VPCNetworkStatus(ctx context.Context, vpcID string) string
	// DockerNetworkForVpc returns the Docker network ID for the given VPC.
	// Returns empty string if the VPC has no Docker network.
	DockerNetworkForVpc(ctx context.Context, vpcID string) string
}

func newHandler(cfg *config.Config, log *serviceutil.ServiceLogger, clk clock.Clock, runtimes *runtimeRegistry, ls *lambdaStore, tracker *instanceTracker) *Handler {
	return &Handler{cfg: cfg, log: log, clk: clk, runtimes: runtimes, ls: ls, tracker: tracker}
}

// accountID returns the configured AWS account ID, falling back to the standard
// emulator account when no config is attached (unit-constructed handlers).
func (h *Handler) accountID() string {
	if h.cfg != nil && h.cfg.AccountID != "" {
		return h.cfg.AccountID
	}
	return "000000000000"
}

// StopAsync waits for all in-flight async invocations to complete, with a
// timeout provided by ctx. This prevents goroutine leaks on shutdown.
//
// It also closes the async path: startAsync refuses events from here on. That
// matters because Lambda is not the last service to stop — S3, Scheduler and
// the event bus all shut down after it, and any of them can still raise an
// event. Without the gate such an event would start a goroutine on a WaitGroup
// nobody is waiting on any more, against a pool and tracker this function is
// about to tear down.
func (h *Handler) StopAsync(ctx context.Context) {
	h.asyncMu.Lock()
	h.asyncClosed = true
	h.asyncMu.Unlock()

	done := make(chan struct{})
	go func() {
		h.asyncWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
