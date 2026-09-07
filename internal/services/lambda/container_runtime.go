package lambda

// container_runtime.go — ContainerRuntime implements the Runtime interface using
// Docker containers with official AWS Lambda ECR images.
//
// Each Lambda function is executed inside a container from
// public.ecr.aws/lambda/{runtime}:{version}. The container communicates with
// Overcast's RuntimeAPIServer to receive invocations and return results.
//
// Warm instance reuse: after responding to an invocation, the container's built-in
// RIC loops back to GET /next and blocks — this IS warm reuse without restarting
// the container. The InstancePool manages the warm/cold lifecycle.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/logging"
	"github.com/overcast-sh/overcast/internal/services/lambda/initbin"
	"github.com/overcast-sh/overcast/internal/services/lambda/initproto"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ContainerRuntime implements Runtime by running Lambda functions in Docker
// containers using official AWS Lambda base images.
type ContainerRuntime struct {
	cfg              *config.Config
	clk              clock.Clock
	docker           *docker.Client
	gc               *docker.GC // async container cleanup with retries
	runtimeAPI       *RuntimeAPIServer
	logger           *zap.Logger
	network          string                    // control plane — containers are created here, then attached to a data plane
	overcastEndpoint string                    // http://host:port — AWS_ENDPOINT_URL for containers
	endpoint         *containerendpoint.Mapper // rewrites resource URLs and /etc/hosts for sibling containers
	// reach is how the Runtime API address was established — which candidate
	// won and whether a container proved it reachable. An INIT death is the
	// moment that verdict has to be repeated: "exit 139" names the runtime,
	// and the cause is almost always this. See SetRuntimeAPIReachability.
	reach containerendpoint.Listen
	// reachHintPath is where the probe result is remembered. A container that
	// died during INIT having opened no connection at all disproves the
	// remembered answer, so it is deleted and the next startup probes again
	// rather than inheriting a verdict this run just refuted.
	reachHintPath string
	pullOnce      sync.Map                             // image name → *sync.Once — ensures each image is pulled only once
	logWriter     events.LogWriter                     // nil until InitLogWriter is called
	bus           atomic.Pointer[events.Bus]           // nil until SetBus is called
	images        atomic.Pointer[docker.ImageResolver] // nil until SetImageResolver is called
	exitNotify    *exitNotifier                        // routes Docker watcher die events to per-container channels
	vpcResolver   atomic.Pointer[VPCNetworkResolver]   // resolves subnet → VPC → Docker network
	efsResolver   atomic.Pointer[EFSVolumeResolver]    // resolves EFS access point → Docker volume
	layerFetcher  LayerContentFetcher                  // resolves a layer ARN to zip bytes for /opt injection
	remoteFetcher *RemoteLayerFetcher                  // optional — fetches layers from real AWS
	codeFetcher   CodeFetcher                          // populates fn.CodeZip at cold start; nil in tests
	debugger      *debugger.Manager                    // debug targets per function; nil = debugger off (see debugger.go)
	tarCache      *tarCache                            // pre-built code/layer tars; nil = disabled
	imageVerified sync.Map                             // pull key → struct{}{}: image confirmed present, skip the per-acquire daemon check
	imageConfigs  sync.Map                             // pull key → docker.ImageConfig: the image's own ENTRYPOINT/CMD, which the init now has to reproduce
	// The in-container init is delivered in a named volume seeded once per
	// process and architecture, rather than copied into every container — see
	// init_volume.go. initVolumes holds one seeding attempt per volume name,
	// initVolumeWarned keeps the archive-fallback warning to one line, and
	// initVolumePruned makes the sweep of superseded volumes a once-per-process
	// job, gated per architecture (a mixed-arch deployment seeds two
	// simultaneously-current volumes under entirely different names).
	initVolumes      sync.Map // volume name → *initVolumeState
	initVolumeWarned sync.Map // volume name → struct{}
	initVolumePruned sync.Map // goarch → struct{}
	// initVolumeUnusable holds every init volume name this instance has
	// reused, found empty after a start failure, and could not remove
	// (foreign or unlabelled — see forgetInitVolume). ensureInitVolume
	// refuses to reuse a name in this set again, falling back to the archive
	// instead: without it, a labelled-but-empty volume this instance can
	// neither fix nor delete would be reused, fail, and be reused again on
	// every subsequent cold start.
	initVolumeUnusable sync.Map // volume name → struct{}
	// initVolumeProblems holds one docker.VolumeOwnershipProblem per init
	// volume this instance has reused that carries a different (or absent)
	// ownership label — see noteInitVolumeProblem and InitVolumeProblems.
	initVolumeProblems sync.Map      // volume name → docker.VolumeOwnershipProblem
	coldStartSem       chan struct{} // bounds concurrent container creation/INIT bursts
	// instances stamps each runtime container with this instance's identity, so
	// the GC's sweeps can tell them from another Overcast's on the same daemon.
	// The same value must reach the GC — see NewContainerRuntime.
	instances *serviceutil.InstanceDomain

	// initBurst tracks containers that are still in the INIT phase with burst
	// CPU. Keyed by container ID: a function can have several environments in
	// INIT at once, and the throttle-down belongs to the one whose RIC reported
	// it, not to the function.
	initBurstMu sync.Mutex
	initBurst   map[string]initBurstEntry
}

// SetLogWriter wires the CloudWatch Logs writer so container stdout/stderr is
// forwarded to CloudWatch. Safe to call at any time; the writer is picked up
// by the next Acquire call.
func (cr *ContainerRuntime) SetLogWriter(lw events.LogWriter) { cr.logWriter = lw }

// SetBus wires the event bus so image pull progress events are published.
// Safe to call at any time; picked up by the next ensureImage call.
func (cr *ContainerRuntime) SetBus(b *events.Bus) { cr.bus.Store(b) }

// SetImageResolver wires the registry resolver used when a function's image
// names a registry Overcast serves rather than a public one — a container
// image built and pushed by CDK, whose ImageUri points at real AWS.
// Implemented by the ECR service. Safe to call at any time; picked up by the
// next pull.
func (cr *ContainerRuntime) SetImageResolver(r docker.ImageResolver) {
	cr.images.Store(&r)
}

// SetVPCResolver wires the EC2 VPC resolver for connecting Lambda containers
// to VPC Docker networks.
func (cr *ContainerRuntime) SetVPCResolver(r VPCNetworkResolver) {
	cr.vpcResolver.Store(&r)
}

// SetEFSResolver wires the EFS volume resolver for mounting file-system
// volumes declared in FileSystemConfigs.
func (cr *ContainerRuntime) SetEFSResolver(r EFSVolumeResolver) {
	cr.efsResolver.Store(&r)
}

// LayerContentFetcher returns layer zip bytes for a layer version ARN.
// The returned bytes should be an immutable copy owned by the caller.
type LayerContentFetcher func(ctx context.Context, layerVersionARN string) ([]byte, error)

// CodeFetcher populates fn.CodeZip with the stored deployment package. The
// package lives under its own store key (see lambdaStore.loadFunctionCode) so
// the invoke path never carries it; a cold start is the point where the bytes
// are actually needed.
type CodeFetcher func(ctx context.Context, fn *Function) error

// SetCodeFetcher wires deployment-package retrieval for container cold starts.
func (cr *ContainerRuntime) SetCodeFetcher(fetcher CodeFetcher) {
	cr.codeFetcher = fetcher
}

// TarCacheStats reports the artifact cache's entry count, resident bytes,
// and byte budget, for the lambda debug endpoint.
func (cr *ContainerRuntime) TarCacheStats() (entries int, bytes, maxBytes int64) {
	return cr.tarCache.stats()
}

// PrefillArtifacts builds and caches fn's cold-start artifacts (code tar and
// layer tars) ahead of the next cold start. Called from the settle debounce
// after deploys — never on the invoke path — so the first cold start of a
// new code version skips the package fetch and conversion the same way later
// ones do. Best-effort: on any miss the cold start simply builds the
// artifact itself, as before.
func (cr *ContainerRuntime) PrefillArtifacts(ctx context.Context, fn *Function) {
	if cr.tarCache == nil || fn == nil || fn.PackageType == "Image" {
		return
	}
	if hotPath, err := hotReloadBindPath(fn, cr.cfg.LambdaHotReload); err == nil && hotPath == "" && fn.CodeHash != "" {
		if _, ok := cr.tarCache.get("code:" + fn.CodeHash); !ok {
			if len(fn.CodeZip) == 0 && cr.codeFetcher != nil {
				_ = cr.codeFetcher(ctx, fn)
			}
			if len(fn.CodeZip) > 0 {
				if tarData, err := zipToTarPrefixed(fn.CodeZip, "var/task/"); err == nil {
					cr.tarCache.put(&tarCacheEntry{key: "code:" + fn.CodeHash, data: tarData})
				}
			}
		}
	}
	// resolveLayerTars fills the layer cache as a side effect; skip-warnings
	// here are advisory only — the cold start re-resolves and reports them.
	_, _, _, _ = cr.resolveLayerTars(ctx, fn)
}

// SetLayerContentFetcher wires layer content retrieval for runtime injection.
func (cr *ContainerRuntime) SetLayerContentFetcher(fetcher LayerContentFetcher) {
	cr.layerFetcher = fetcher
}

// SetRemoteLayerFetcher wires the optional remote layer fetcher that downloads
// layers from real AWS when not available locally.
func (cr *ContainerRuntime) SetRemoteLayerFetcher(fetcher *RemoteLayerFetcher) {
	cr.remoteFetcher = fetcher
}

// connectDataPlane attaches a container to the data plane it belongs on: its
// VPC's Docker network when the function has a VpcConfig, the default plane
// otherwise. The container was created on the control plane, which carries the
// Runtime API and the emulator endpoint and is never withheld — see
// internal/dataplane.
//
// A function is compute, so it registers no aliases: nothing resolves a Lambda
// container by name.
func (cr *ContainerRuntime) connectDataPlane(ctx context.Context, containerID string, fn *Function) error {
	var vpcID string
	var subnetIDs []string
	if fn.VpcConfig != nil {
		vpcID, subnetIDs = fn.VpcConfig.VpcId, fn.VpcConfig.SubnetIds
	}

	// A nil pointer here means EC2 is not wired; the function then lands on the
	// default plane rather than failing, which is what it had before there was
	// a resolver to consult.
	var resolver dataplane.VPCResolver
	if rp := cr.vpcResolver.Load(); rp != nil {
		resolver = *rp
	}

	// The subnets go with the VPC: under OVERCAST_VPC_EGRESS=routed they are
	// what decides whether the function gets a route out.
	placement, err := dataplane.PlaceInSubnets(ctx, resolver, vpcID, subnetIDs)
	if err != nil {
		return fmt.Errorf("lambda %s: %w", fn.Name, err)
	}
	if placement.VPCNetwork != "" {
		cr.logger.Info("placing Lambda container on its VPC network",
			zap.String("function", fn.Name),
			zap.String("vpc", vpcID),
			zap.String("network", placement.VPCNetwork),
			zap.String("egress_network", placement.EgressNetwork))
	}

	return dataplane.Attach(ctx, cr.docker, cr.cfg, containerID, placement)
}

// NewContainerRuntime creates a ContainerRuntime.
// The Docker client and RuntimeAPIServer must already be initialised.
// maxConcurrentStarts bounds concurrent container creation/INIT bursts; the
// caller resolves it from LAMBDA_DOCKER_MAX_CONCURRENT_STARTS or the Docker
// host (see resolveRuntimeLimits).
func NewContainerRuntime(
	cfg *config.Config,
	clk clock.Clock,
	docker *docker.Client,
	gc *docker.GC,
	runtimeAPI *RuntimeAPIServer,
	logger *zap.Logger,
	maxConcurrentStarts int,
	instances *serviceutil.InstanceDomain,
) *ContainerRuntime {
	// Derive the Overcast emulator endpoint from the Runtime API address.
	// The Runtime API host is the IP that Lambda containers can route to on
	// the control plane — the same IP serves the main HTTP API on cfg.Port.
	// Setting AWS_ENDPOINT_URL lets function code call S3, SQS, DynamoDB, etc.
	// back into Overcast without any SDK configuration.
	runtimeHost, _, _ := net.SplitHostPort(runtimeAPI.Addr())
	// BaseURL rather than a hand-built string: the scheme must follow the
	// listener (https under OVERCAST_TLS), which only config knows.
	overcastEndpoint := containerendpoint.BaseURL(cfg, runtimeHost)

	limit := maxConcurrentStarts
	if limit < 1 {
		limit = 1
	}

	endpoint := containerendpoint.New(cfg, overcastEndpoint).WithPublishedPort(cfg.PublishedPort)
	return &ContainerRuntime{
		cfg:              cfg,
		clk:              clk,
		docker:           docker,
		gc:               gc,
		runtimeAPI:       runtimeAPI,
		logger:           logger,
		network:          dataplane.Primary(cfg),
		overcastEndpoint: overcastEndpoint,
		// The Runtime API address is already an address Lambda containers can
		// route to, so it is used directly rather than re-resolved. The mapper
		// applies the shared rewriting and /etc/hosts rules on top of it.
		endpoint:     endpoint,
		exitNotify:   newExitNotifier(),
		coldStartSem: make(chan struct{}, limit),
		tarCache:     newTarCache(int64(cfg.LambdaTarCacheMB) * 1024 * 1024),
		instances:    instances,
	}
}

func (cr *ContainerRuntime) acquireColdStartSlot(ctx context.Context) (func(), error) {
	if cr.coldStartSem == nil {
		return func() {}, nil
	}
	select {
	case cr.coldStartSem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-cr.coldStartSem })
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// errColdStartBusy reports that no cold-start slot was immediately free.
// Only proactive acquisition returns it — real invocations wait instead.
var errColdStartBusy = errors.New("lambda cold-start capacity busy")

// tryAcquireColdStartSlot is acquireColdStartSlot without the wait: proactive
// environment creation must never hold a queue position that a real
// invocation could have taken.
func (cr *ContainerRuntime) tryAcquireColdStartSlot() (func(), error) {
	if cr.coldStartSem == nil {
		return func() {}, nil
	}
	select {
	case cr.coldStartSem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-cr.coldStartSem })
		}, nil
	default:
		return nil, errColdStartBusy
	}
}

// ProgressFunc is called by AcquireWithProgress to report lifecycle steps to
// the caller (e.g. an SSE endpoint streaming progress to the UI).
type ProgressFunc func(step string)

// CanHandle returns true for every runtime the catalog maps to an official
// Lambda base image — activeRuntimes is derived from lambdaRuntimeCatalog in
// runtime_catalog.go — and for PackageType=Image functions (runtimeID "image").
func (cr *ContainerRuntime) CanHandle(runtimeID string) bool {
	if runtimeID == "image" {
		return true
	}
	_, ok := activeRuntimes[runtimeID]
	return ok
}

// imageForFunction resolves the Docker image that backs fn. Returns an
// error if PackageType=Image has no ImageUri, or if the zip runtime is
// unknown.
func imageForFunction(fn *Function) (string, error) {
	if fn.PackageType == "Image" {
		if fn.ImageUri == "" {
			return "", fmt.Errorf("PackageType=Image but no ImageUri set for %q", fn.Name)
		}
		return fn.ImageUri, nil
	}
	image, ok := activeRuntimes[fn.Runtime]
	if !ok {
		return "", fmt.Errorf("no image for runtime %q", fn.Runtime)
	}
	return image, nil
}

// imageRef pairs the image reference a function was deployed with — what the
// user asked for, and what every message about it must name — with the
// reference this daemon can actually fetch and run it as. The two differ only
// for an image in a registry Overcast serves: a CDK container asset's ImageUri
// addresses real AWS, while the bytes are in the local ECR registry.
type imageRef struct {
	requested string
	resolved  docker.ImageReference
}

// rewritten reports whether resolution moved the reference, which is what
// makes an error or a log line need to name both.
func (r imageRef) rewritten() bool { return r.resolved.Ref != r.requested }

// resolveImage maps image onto the reference this daemon can fetch. Without a
// resolver — and for every reference a resolver does not serve — the image is
// taken at face value, which is right for the public Lambda base images.
func (cr *ContainerRuntime) resolveImage(ctx context.Context, image string) imageRef {
	ref := imageRef{requested: image, resolved: docker.ImageReference{Ref: image}}
	if rp := cr.images.Load(); rp != nil {
		ref.resolved = (*rp).ResolveImage(ctx, image)
	}
	return ref
}

// PrewarmFunction starts a background pull of fn's Docker image so the
// first Invoke doesn't pay the cold-pull cost on the request path. Safe to
// call from CreateFunction — if the image is already cached or in flight,
// the sync.Once inside ensureImage coalesces the work. onReady is invoked
// (on the background goroutine) after the pull completes; it can be nil.
// err passed to onReady is the pull result.
func (cr *ContainerRuntime) PrewarmFunction(fn *Function, onReady func(err error)) {
	image, err := imageForFunction(fn)
	if err != nil {
		if onReady != nil {
			onReady(err)
		}
		return
	}
	platform := dockerPlatformForLambdaArchitectures(fn.Architectures)
	go func() {
		ctx := context.Background()
		pullErr := cr.ensureImage(ctx, cr.resolveImage(ctx, image), platform)
		if onReady != nil {
			onReady(pullErr)
		}
	}()
}

// Lambda's AWS_LAMBDA_INITIALIZATION_TYPE values. Observability libraries
// (Powertools among them) read this to classify cold starts.
const (
	initTypeOnDemand    = "on-demand"
	initTypeProvisioned = "provisioned-concurrency"
)

// lambdaInitOrigin classifies why acquireContainer was called, for the
// init_origin field on the "lambda container started" log line and the
// instance tracker's InitOrigin. This is strictly emulator-side telemetry:
// AWS gives an application no way to distinguish a proactively initialized
// environment from a real cold start — AWS_LAMBDA_INITIALIZATION_TYPE reads
// "on-demand" either way, and the only outward difference AWS documents is
// that a proactive environment's first REPORT line omits Init Duration (see
// AcquireProactive) — so origin must never be derived from, or leak into,
// anything an invocation can observe.
func lambdaInitOrigin(initType string, proactive bool) string {
	switch {
	case proactive:
		return instanceOriginProactive
	case initType == initTypeProvisioned:
		return instanceOriginProvisioned
	default:
		return instanceOriginOnDemand
	}
}

// Acquire creates and starts a Docker container for fn, then returns a
// containerInstance that can invoke the function via the Runtime API.
func (cr *ContainerRuntime) Acquire(ctx context.Context, fn *Function) (RuntimeInstance, error) {
	return cr.acquireContainer(ctx, fn, func(string) {}, initTypeOnDemand, false)
}

// AcquireProvisioned creates an environment for a provisioned concurrency
// reservation. Identical to Acquire except that the container reports
// AWS_LAMBDA_INITIALIZATION_TYPE=provisioned-concurrency, as it would on AWS.
func (cr *ContainerRuntime) AcquireProvisioned(ctx context.Context, fn *Function) (RuntimeInstance, error) {
	return cr.acquireContainer(ctx, fn, func(string) {}, initTypeProvisioned, false)
}

// AcquireProactive creates an environment ahead of any invocation, mirroring
// AWS's documented proactive initialization. Two deliberate differences from
// Acquire: cold-start capacity is try-acquired (errColdStartBusy instead of
// waiting — proactive work never queues ahead of real invocations), and the
// environment records no invoke-triggered init start, so its first REPORT
// line omits Init Duration — exactly what a proactively initialized
// environment looks like on AWS. AWS_LAMBDA_INITIALIZATION_TYPE stays
// "on-demand", also matching AWS.
func (cr *ContainerRuntime) AcquireProactive(ctx context.Context, fn *Function) (RuntimeInstance, error) {
	return cr.acquireContainer(ctx, fn, func(string) {}, initTypeOnDemand, true)
}

// Release is a no-op for ContainerRuntime itself — InstancePool wraps it and
// handles warm-instance storage and eviction.
func (cr *ContainerRuntime) Release(_ context.Context, _ RuntimeInstance, _ bool) {}

// AcquireWithProgress is like Acquire but calls progress at each lifecycle step
// so callers (e.g. the SSE invoke endpoint) can stream status to the UI.
func (cr *ContainerRuntime) AcquireWithProgress(ctx context.Context, fn *Function, progress ProgressFunc) (RuntimeInstance, error) {
	return cr.acquireContainer(ctx, fn, progress, initTypeOnDemand, false)
}

// acquireContainer is the single implementation behind Acquire and
// AcquireWithProgress. The progress callback is called at each lifecycle step;
// callers that don't need progress pass a no-op.
//
// Each phase's duration is captured (phases/mark) and reported on the
// "lambda container started" line, so where a cold start spends its time is
// always visible — the measurement Phase 0 of docs/plans/lambda-cold-start.md
// requires before any cold-path change.
func (cr *ContainerRuntime) acquireContainer(ctx context.Context, fn *Function, progress ProgressFunc, initType string, proactive bool) (RuntimeInstance, error) {
	isImage := fn.PackageType == "Image"

	acquireStart := cr.clk.Now()
	phaseStart := acquireStart
	var phases []zap.Field
	mark := func(name string) {
		now := cr.clk.Now()
		phases = append(phases, zap.Duration(name, now.Sub(phaseStart)))
		phaseStart = now
	}

	image, err := imageForFunction(fn)
	if err != nil {
		return nil, err
	}
	platform := dockerPlatformForLambdaArchitectures(fn.Architectures)

	// Resolve once, here: an image in a registry Overcast serves is fetched —
	// and run — under the reference that registry answers to, while progress
	// and errors keep naming the one the function was deployed with.
	ref := cr.resolveImage(ctx, image)

	// Ensure the image is present (lazy, once per image). Once verified, later
	// acquires skip this daemon round trip entirely; the create-time
	// missing-image retry below covers an image removed behind our back.
	pullKey := imagePullKey(ref.resolved.Ref, platform)
	if _, verified := cr.imageVerified.Load(pullKey); !verified {
		exists, _ := cr.docker.ImageMatchesPlatform(ctx, ref.resolved.Ref, platform)
		if !exists {
			progress("Pulling image " + image)
		}
		if err := cr.ensureImage(ctx, ref, platform); err != nil {
			return nil, fmt.Errorf("pull image: %w", err)
		}
		if !exists {
			progress("Image ready")
		}
		cr.imageVerified.Store(pullKey, struct{}{})
	}
	mark("image_check")

	progress("Waiting for cold-start capacity")
	var releaseColdStart func()
	if proactive {
		releaseColdStart, err = cr.tryAcquireColdStartSlot()
	} else {
		releaseColdStart, err = cr.acquireColdStartSlot(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("wait for lambda cold-start slot: %w", err)
	}
	defer releaseColdStart()
	mark("slot_wait")

	hotReloadPath, err := hotReloadBindPath(fn, cr.cfg.LambdaHotReload)
	if err != nil {
		return nil, err
	}
	if hotReloadPath != "" {
		// Both diagnostics read the tree from Overcast's own filesystem, which
		// is not always the Docker-form path handed to the daemon — on a
		// Windows host `/c/src/app` names nothing, so neither warning could
		// ever fire when read through it.
		localPath := hotReloadLocalPath(hotReloadTagPath(fn), hotReloadPath)
		if msg := typeScriptSourceDiagnostic(localPath, fn.Runtime); msg != "" {
			cr.logger.Warn(msg)
		}
		if msg := hotReloadVisibilityDiagnostic(localPath); msg != "" {
			cr.logger.Warn(msg)
		}
	}

	// For zip deployments, build a tar archive from the code zip.
	var codeTar []byte
	if !isImage && hotReloadPath == "" {
		progress("Preparing function code")
		cacheKey := ""
		if fn.CodeHash != "" {
			cacheKey = "code:" + fn.CodeHash
		}
		if cached, ok := cr.tarCache.get(cacheKey); ok {
			// A hit skips both materializing the package from the store and
			// the zip→tar conversion.
			codeTar = cached.data
		} else {
			// The function record travels without its package; materialize the
			// stored bytes now — the one place on the invoke path that needs them.
			if len(fn.CodeZip) == 0 && cr.codeFetcher != nil {
				if err := cr.codeFetcher(ctx, fn); err != nil {
					return nil, fmt.Errorf("fetch function code: %w", err)
				}
			}
			codeTar, err = zipToTarPrefixed(fn.CodeZip, "var/task/")
			if err != nil {
				return nil, fmt.Errorf("build code tar: %w", explainUnreadablePackage(fn, err))
			}
			// Re-derive the key: materializing a generation that has since been
			// superseded moves the snapshot onto the current one (see
			// lambdaStore.loadFunctionCode), and the entry must name the bytes
			// that were actually converted.
			if fn.CodeHash != "" {
				cacheKey = "code:" + fn.CodeHash
			}
			if cacheKey != "" {
				cr.tarCache.put(&tarCacheEntry{key: cacheKey, data: codeTar})
			}
		}
	}

	// Assemble the whole provisioning archive — code (var/task/…), TLS trust
	// root, layer contents (opt/…, in layer order, so later layers overwrite
	// earlier ones exactly as with the previous per-layer copies and as on
	// AWS), and the bootstrap — BEFORE the container exists: it is pure CPU
	// plus store reads, an error here wastes nothing, and it keeps the
	// create→start critical section as short as possible. Directories the
	// base image owns (/var, /var/task, /opt, /etc) are never emitted as
	// headers, so extraction cannot reset their modes.
	layerTars, expectedExtensions, skippedLayerLogLines, layerErr := cr.resolveLayerTars(ctx, fn)
	if layerErr != nil {
		return nil, fmt.Errorf("prepare layers: %w", layerErr)
	}
	var prov bytes.Buffer
	tw := tar.NewWriter(&prov)
	hasEntries := false
	appendComponent := func(what string, component []byte) error {
		if len(component) == 0 {
			return nil
		}
		if err := appendTarEntries(tw, component); err != nil {
			return fmt.Errorf("assemble %s entries: %w", what, err)
		}
		hasEntries = true
		return nil
	}
	if !isImage && hotReloadPath == "" {
		if err := appendComponent("code", codeTar); err != nil {
			return nil, err
		}
	}
	// With TLS on, the function must trust the CA that minted Overcast's
	// certificate before its first SDK call — otherwise every AWS call from
	// inside the function fails certificate verification. Injected by the same
	// mechanism as code: a dockerized Overcast has no host path to bind-mount.
	// The Mapper caches the trust-root tar after the first build.
	if caTar, caErr := cr.endpoint.CABundleTar(); caErr != nil {
		cr.logger.Warn("CA bundle unavailable; function TLS calls to Overcast will fail verification", zap.Error(caErr))
	} else if err := appendComponent("CA bundle", caTar); err != nil {
		return nil, err
	}
	for _, layerTar := range layerTars {
		if err := appendComponent("layer", layerTar); err != nil {
			return nil, err
		}
	}
	// The init, for every container: it is the entrypoint, so an execution
	// environment without it is not an execution environment. It normally
	// arrives in a volume seeded once per process (init_volume.go) rather than
	// in this archive, because copying ~6.5 MB per cold start costs more than
	// the rest of the cold start put together. A missing *artefact* is fatal —
	// the error names the file and the command that builds it — while a daemon
	// that will not manage a volume falls back to the archive here, which is
	// the only reason appendLambdaInit still exists.
	initDeliv, initErr := cr.resolveInitDelivery(ctx, fn, ref.resolved.Ref, platform)
	if initErr != nil {
		return nil, initErr
	}
	if initDeliv.volume == "" {
		// Written straight into the archive rather than through
		// appendComponent: a component tar would copy the binary twice.
		if err := appendLambdaInit(tw, fn.Architectures); err != nil {
			return nil, err
		}
		hasEntries = true
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close provisioning tar: %w", err)
	}
	mark("code_prep")

	// This execution environment's own Runtime API endpoint, allocated before
	// the container exists because its address is baked into the environment at
	// create time. The listener a request arrives on is what identifies the
	// container to the Runtime API — see containerListener for why its source
	// address is not enough. Handed to the containerInstance on success and
	// closed on every other path out of here.
	rapiListener, rapiErr := cr.runtimeAPI.AddContainerListener()
	if rapiErr != nil {
		return nil, fmt.Errorf("allocate runtime api endpoint: %w", rapiErr)
	}
	listenerHandedOff := false
	defer func() {
		if !listenerHandedOff {
			_ = rapiListener.Close()
		}
	}()

	logStream := lambdaLogStreamName(cr.clk)
	// The debug target, when the tags ask for one, is resolved here — at the
	// cold start — because everything it changes is baked into the container:
	// the injected flag and OVERCAST_DEBUG_PORT below, and the port binding on
	// the create request.
	target := cr.debugTarget(ctx, fn, ref.resolved.Ref, platform)
	env := cr.buildEnv(fn, logStream, initType, rapiListener.Addr(), target)
	containerName := fmt.Sprintf("overcast-lambda-%s-%d", sanitizeName(fn.Name), cr.clk.Now().UnixNano())

	// The log sink exists before the container does, for the same reason the
	// listener does: the init opens its log stream and starts publishing
	// INIT-phase frames the moment the container starts, which is long before
	// this function returns a containerInstance — and those frames are what the
	// INIT-timeout diagnostic quotes. Handed to the containerInstance on
	// success and closed on every other path out of here.
	sink := cr.newLogSink(fn, logStream, initType)
	// Indexed against this environment's own listener before the container
	// exists, so the init's first log connection is answered rather than made
	// to wait for Docker to report an address — see RuntimeAPIServer.logSinks.
	rapiListener.RegisterLogSink(sink)
	go sink.ensureStream()
	sinkHandedOff := false
	defer func() {
		if !sinkHandedOff {
			sink.close()
		}
	}()

	progress("Creating container")
	ccfg := &docker.ContainerConfig{
		// The resolved reference, not the requested one: the daemon stored the
		// bytes under that name, so creating from the original would fail with
		// "No such image" straight after a pull that worked.
		Image:  ref.resolved.Ref,
		Env:    env,
		Labels: cr.instances.ManagedLabels(ctx, "lambda", fn.Name),
	}
	// The init is PID 1 and the command the container would have run without it
	// is its argument list, exactly as RAPID launches ENTRYPOINT+CMD on AWS.
	child, childErr := cr.childCommand(ctx, fn, isImage, ref.resolved.Ref, platform)
	if childErr != nil {
		return nil, childErr
	}
	ccfg.Entrypoint = []string{initproto.InitPath}
	ccfg.Cmd = child
	if isImage && fn.ImageConfig != nil {
		// WorkingDir stays a container-config field: it is Docker's job, the
		// init inherits the cwd it is given, and the daemon still merges the
		// image's own WORKDIR when this is empty.
		ccfg.WorkingDir = fn.ImageConfig.WorkingDirectory
	}

	// Start with burst CPU for fast INIT, throttled to steady state once the
	// RIC issues its first GET /next (see ThrottleInitBurst).
	req := &docker.CreateContainerRequest{
		ContainerConfig: ccfg,
		Platform:        platform,
		HostConfig: &docker.HostConfig{
			Binds:       bindMountTaskDir(hotReloadPath),
			Mounts:      withInitVolume(cr.efsMounts(ctx, fn), initDeliv.volume),
			NetworkMode: cr.network,
			Memory:      int64(fn.MemorySize) * 1024 * 1024,
			MemorySwap:  int64(fn.MemorySize) * 1024 * 1024, // disable swap
			NanoCPUs:    int64(initBurstCPUs * 1e9),
			// Split-horizon hostnames resolve to 127.0.0.1 in public DNS for
			// host-side callers; inside the container they must point at
			// Overcast instead. See internal/containerendpoint.
			//
			// ExtraHosts covers the apex names exactly; Dns covers their
			// subdomains — virtual-hosted S3, execute-api — which /etc/hosts
			// cannot express. Dns is empty unless the resolver is listening.
			ExtraHosts: cr.extraHosts(),
			Dns:        cr.endpoint.DNSServers(),
		},
	}
	if target != nil {
		target.ApplyPortBinding(ccfg, req.HostConfig)
	}

	id, err := cr.docker.CreateContainer(ctx, containerName, req)
	if err != nil && docker.IsImageMissingErr(err) {
		// The image vanished after verification (docker rmi / prune). Drop
		// both caches — pullOnce too, or ensureImage's spent sync.Once would
		// skip the pull and this function could never run again without a
		// restart — then re-pull and retry the create once.
		cr.imageVerified.Delete(pullKey)
		cr.pullOnce.Delete(pullKey)
		cr.logger.Info("lambda image missing at container create — re-pulling",
			zap.String("image", image), zap.String("function", fn.Name))
		if pullErr := cr.ensureImage(ctx, ref, platform); pullErr == nil {
			cr.imageVerified.Store(pullKey, struct{}{})
			id, err = cr.docker.CreateContainer(ctx, containerName, req)
		}
	}
	if err != nil {
		// A create abandoned mid-flight — the function was deleted underneath
		// this cold start, or the invocation gave up — may still have been
		// completed by the daemon after we stopped waiting for the answer,
		// leaving a container whose ID nothing here ever learned. The name is
		// ours and unique to this acquire, so it is enough to remove by.
		if ctx.Err() != nil {
			_ = cr.docker.RemoveContainerForce(containerName)
		}
		return nil, fmt.Errorf("create container: %w", decorateHotReloadMountError(err, hotReloadPath))
	}

	var containerIP string
	var registeredIP string
	if inspect, inspectErr := cr.docker.InspectContainer(ctx, id); inspectErr == nil {
		containerIP = cr.extractContainerIP(inspect)
		if containerIP != "" {
			sink.attach(containerIP, id)
			cr.runtimeAPI.RegisterContainerConfig(containerIP, runtimeContainerConfig{
				FunctionARN:        fn.ARN,
				FunctionName:       fn.Name,
				ContainerID:        id,
				Handler:            fn.Handler,
				ExpectedExtensions: expectedExtensions,
				LogSink:            sink,
				LogFormat:          resolveLogFormat(fn),
			})
			rapiListener.Attach(containerIP)
			registeredIP = containerIP
		}
	} else {
		cr.logger.Debug("inspect lambda container before start", zap.String("container", id[:12]), zap.Error(inspectErr))
	}
	mark("create")

	// cleanup removes the container on any error after creation.
	cleanup := func() {
		if registeredIP != "" {
			cr.runtimeAPI.UnregisterContainer(registeredIP)
		}
		// A container that never reaches its first GET /next would otherwise
		// leave its burst entry behind — nothing else deletes one.
		cr.clearInitBurst(id)
		_ = cr.docker.RemoveContainerForce(id)
	}

	// One daemon round trip provisions the whole filesystem (assembled above,
	// before the container existed).
	progress("Provisioning container filesystem")
	if hasEntries {
		if err := cr.docker.CopyToContainer(ctx, id, "/", bytes.NewReader(prov.Bytes())); err != nil {
			cleanup()
			return nil, fmt.Errorf("provision container filesystem: %w", err)
		}
	}
	mark("copy")

	// Join the data plane before starting. Function code routinely opens a
	// database or cache connection during INIT, which begins the moment the
	// runtime starts — a name it dials before the attachment lands resolves
	// nowhere in Docker, falls through to Overcast's split-horizon resolver,
	// and hangs on the engine port.
	if err := cr.connectDataPlane(ctx, id, fn); err != nil {
		cleanup()
		return nil, err
	}

	progress("Starting container")
	if err := cr.docker.StartContainer(ctx, id); err != nil {
		// A start that failed because the init is not where the entrypoint
		// points means the volume we mounted is empty — the state a process
		// killed between creating the volume and filling it leaves behind, and
		// the one state the labels cannot tell apart from a good seed.
		// Dropping it is what makes that self-healing, and it is narrowed to
		// that error rather than run on every failed start: a start that failed
		// for its own reasons has a perfectly good volume, and Docker names the
		// path it could not exec.
		//
		// cleanup() runs first, deliberately: this container — created but
		// never started — still holds a live reference to the volume, and
		// Docker refuses to remove a volume any container references,
		// `force` included (see forgetInitVolume's doc comment). Asking to
		// forget the volume before the one thing referencing it is gone would
		// make even a volume this instance owns undeletable, defeating the
		// self-healing this exists for.
		cleanup()
		if initDeliv.volume != "" && strings.Contains(err.Error(), initproto.InitPath) {
			cr.forgetInitVolume(ctx, initDeliv.volume)
		}
		return nil, fmt.Errorf("start container: %w", decorateHotReloadMountError(err, hotReloadPath))
	}
	// Init Duration is measured from here to the RIC's first GET /next. Only
	// environments whose init was triggered by an invoke report it, exactly as
	// on AWS: provisioned environments never do, and neither do proactively
	// initialized ones — their init ran ahead of any invocation, so the first
	// REPORT line carries no Init Duration even though the environment still
	// reports AWS_LAMBDA_INITIALIZATION_TYPE=on-demand.
	var initStartedAt time.Time
	if initType == initTypeOnDemand && !proactive {
		initStartedAt = cr.clk.Now()
	}

	// Register for INIT-burst throttle-down when the RIC first polls /next.
	cr.registerInitBurst(id, fn.MemorySize)
	mark("start")

	progress("Waiting for runtime to initialize")
	if containerIP == "" {
		containerIP, err = cr.awaitContainerIP(ctx, id)
		if err != nil {
			cleanup()
			return nil, err
		}
		sink.attach(containerIP, id)
		cr.runtimeAPI.RegisterContainerConfig(containerIP, runtimeContainerConfig{
			FunctionARN:        fn.ARN,
			FunctionName:       fn.Name,
			ContainerID:        id,
			Handler:            fn.Handler,
			ExpectedExtensions: expectedExtensions,
			LogSink:            sink,
			LogFormat:          resolveLogFormat(fn),
		})
		rapiListener.Attach(containerIP)
		registeredIP = containerIP
	}

	mark("await_ip")
	if target != nil {
		target.Bind(ctx, cr.docker, cr.cfg, id, id)
	}
	// One line per cold start carrying the phase breakdown; INIT time (runtime
	// bootstrap to first GET /next) is reported separately as the REPORT line's
	// Init Duration.
	cr.logger.Info("lambda container started",
		append([]zap.Field{
			zap.String("function", fn.Name),
			zap.String("container", id[:12]),
			zap.String("image", image),
			zap.String("container_ip", containerIP),
			zap.String("runtime_api", rapiListener.Addr()),
			zap.String("log_stream", logStream),
			zap.String("init_origin", lambdaInitOrigin(initType, proactive)),
			zap.Duration("acquire_total", cr.clk.Now().Sub(acquireStart)),
		}, phases...)...,
	)

	ci := cr.newContainerInstance(id, containerIP, fn, logStream, rapiListener, sink, target)
	listenerHandedOff = true
	sinkHandedOff = true
	ci.initStartedAt = initStartedAt
	for _, line := range skippedLayerLogLines {
		ci.writeLogLine(ctx, line)
	}
	return ci, nil
}

func dockerPlatformForLambdaArchitectures(architectures []string) string {
	if len(architectures) == 0 {
		return "linux/amd64"
	}
	switch architectures[0] {
	case "arm64":
		return "linux/arm64"
	case "x86_64":
		return "linux/amd64"
	default:
		return ""
	}
}

// awaitContainerIP polls the Docker daemon for the container's IP address on
// the Lambda network. Uses exponential backoff starting at 25ms.
func (cr *ContainerRuntime) awaitContainerIP(ctx context.Context, containerID string) (string, error) {
	retryDelay := 25 * time.Millisecond
	for attempt := 0; attempt < 20; attempt++ {
		inspect, err := cr.docker.InspectContainer(ctx, containerID)
		if err != nil {
			return "", fmt.Errorf("inspect container: %w", err)
		}

		// Bail out immediately if the container already died (e.g. image
		// entrypoint crashed on boot).
		if !inspect.State.Running && inspect.State.Status != "created" {
			logs, _ := cr.docker.ContainerLogs(ctx, containerID, "50")
			if len(logs) > 0 {
				return "", fmt.Errorf("container %s exited during startup (status=%s exit=%d): %s",
					containerID[:12], inspect.State.Status, inspect.State.ExitCode, strings.TrimSpace(string(logs)))
			}
			return "", fmt.Errorf("container %s exited during startup (status=%s exit=%d)",
				containerID[:12], inspect.State.Status, inspect.State.ExitCode)
		}

		if ip := cr.extractContainerIP(inspect); ip != "" {
			return ip, nil
		}

		cr.logger.Debug("waiting for container IP assignment",
			zap.String("container", containerID[:12]),
			zap.Int("attempt", attempt+1))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-cr.clk.After(retryDelay):
		}
		if retryDelay < 250*time.Millisecond {
			retryDelay *= 2
		}
	}
	return "", fmt.Errorf("could not determine container IP for %s", containerID[:12])
}

// extractContainerIP returns the container's IP on the Lambda network, or
// falls back to the first available network IP.
func (cr *ContainerRuntime) extractContainerIP(inspect *docker.ContainerInspect) string {
	if nw, ok := inspect.NetworkSettings.Networks[cr.network]; ok && nw.IPAddress != "" {
		return nw.IPAddress
	}
	for _, nw := range inspect.NetworkSettings.Networks {
		if nw.IPAddress != "" {
			return nw.IPAddress
		}
	}
	return ""
}

// logRegion is where a function's output belongs: the region encoded in its
// ARN, so a cross-region invoke's lines land where the function lives, falling
// back to the emulator's own.
func (cr *ContainerRuntime) logRegion(fn *Function) string {
	if region := regionFromFunctionARN(fn.ARN); region != "" {
		return region
	}
	return cr.cfg.Region
}

// newLogSink builds the host end of a container's log channel. It is called
// before the container exists — see the call site.
func (cr *ContainerRuntime) newLogSink(fn *Function, logStream, initType string) *logSink {
	appLogLevel, sysLogLevel := resolveLogLevels(fn)
	return newLogSink(logSinkConfig{
		logger:       cr.logger,
		clk:          cr.clk,
		logWriter:    cr.logWriter,
		group:        fn.logGroupName(),
		stream:       logStream,
		region:       cr.logRegion(fn),
		functionName: fn.Name,
		initType:     initType,
		logFormat:    resolveLogFormat(fn),
		appLogLevel:  appLogLevel,
		sysLogLevel:  sysLogLevel,
		runtimeAPI:   cr.runtimeAPI,
	})
}

// newContainerInstance builds a containerInstance from the created container.
func (cr *ContainerRuntime) newContainerInstance(id, containerIP string, fn *Function, logStream string, rapiListener *containerListener, sink *logSink, target *debugger.Target) *containerInstance {
	appLogLevel, sysLogLevel := resolveLogLevels(fn)
	ci := &containerInstance{
		id:             id,
		instanceID:     uuid.NewString(),
		containerIP:    containerIP,
		functionName:   fn.Name,
		functionARN:    fn.ARN,
		configIdentity: functionInstanceIdentity(fn),
		memorySize:     fn.MemorySize,
		logStream:      logStream,
		logGroupName:   fn.logGroupName(),
		logFormat:      resolveLogFormat(fn),
		appLogLevel:    appLogLevel,
		sysLogLevel:    sysLogLevel,
		docker:         cr.docker,
		gc:             cr.gc,
		runtimeAPI:     cr.runtimeAPI,
		logger:         cr.logger,
		clk:            cr.clk,
		logWriter:      cr.logWriter,
		logSink:        sink,
		logCtx:         sink.ctx,
		exitNotify:     cr.exitNotify,
		healthy:        true,
		keepContainers: cr.cfg.LambdaKeepContainers,
		readyCh:        cr.runtimeAPI.ReadyChan(containerIP),
		endpoint:       cr.endpoint,
		rapiListener:   rapiListener,
		reach:          cr.reach,
		reachHintPath:  cr.reachHintPath,
		debug:          target,
	}
	ci.forgetInitBurst = func() { cr.clearInitBurst(id) }
	return ci
}

// codeHashOf returns the SHA-256 hex digest of a zip payload.
// Used to detect when UpdateFunctionCode has replaced the deployment package.
func codeHashOf(zip []byte) string {
	h := sha256.Sum256(zip)
	return hex.EncodeToString(h[:])
}

// imageHash returns a SHA-256 hex digest of an image URI string.
// Used by InstancePool to detect stale instances after an image URI change.
func imageHash(uri string) string {
	h := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(h[:])
}

func bindMountTaskDir(hostPath string) []string {
	if hostPath == "" {
		return nil
	}
	return []string{hostPath + ":/var/task:ro"}
}

// sandboxLocaldomainHost is the /etc/hosts entry that makes
// `sandbox.localdomain` the execution environment's own loopback, as it is
// inside a real one.
//
// It is the name AWS puts in front of every telemetry destination it
// documents — the Telemetry API and the Logs API both describe the delivery
// endpoint as "a local HTTP endpoint (http://sandbox.localdomain:${PORT}/${PATH})"
// — so an extension ported from AWS subscribes it verbatim. Overcast accepts
// such a subscription like any other, and before this entry existed the
// in-container init dialled the name as subscribed and got "lookup
// sandbox.localdomain … no such host", which the host counted as a failed
// delivery and eventually a platform.logsDropped (#1837).
//
// An /etc/hosts entry rather than a special case in the init: the init is a
// relay that parses nothing it carries (docs/plans/lambda-in-container-init.md
// § 3.5), and teaching it this one name would leave every *other* process in
// the sandbox — the runtime, the function, an extension using the name for
// something of its own — unable to resolve it.
const sandboxLocaldomainHost = "sandbox.localdomain:127.0.0.1"

// extraHosts is the /etc/hosts set for a Lambda container: the split-horizon
// names that point at Overcast, plus the sandbox's own name.
//
// The Lambda-only entry is appended here rather than inside
// containerendpoint.Mapper.ExtraHosts because that method is shared with ECS
// (internal/services/ecs/task_netns.go) and answers one question: how does a
// container reach Overcast. This name answers a different one — it is a Lambda
// execution environment's name for itself, it points at the container rather
// than at Overcast, and it means nothing in an ECS task.
func (cr *ContainerRuntime) extraHosts() []string {
	return append(cr.endpoint.ExtraHosts(), sandboxLocaldomainHost)
}

// efsMounts resolves the function's FileSystemConfigs to Docker volume
// mounts, scoped to the access point's root directory via a volume Subpath
// when one is declared. Unresolvable configs — EFS mock mode, Docker
// unavailable, unknown access point — are skipped with a warning so the
// function still runs, just without shared storage (mirrors the best-effort
// posture of the EFS live mode itself).
func (cr *ContainerRuntime) efsMounts(ctx context.Context, fn *Function) []docker.Mount {
	if len(fn.FileSystemConfigs) == 0 {
		return nil
	}
	rp := cr.efsResolver.Load()
	if rp == nil {
		return nil
	}
	mounts := make([]docker.Mount, 0, len(fn.FileSystemConfigs))
	for _, fsc := range fn.FileSystemConfigs {
		volume, subpath, ok := (*rp).EFSVolumeForAccessPoint(ctx, fsc.Arn)
		if !ok {
			cr.logger.Warn("lambda: EFS mount skipped — access point has no backing volume (mock mode, Docker down, or unknown access point)",
				zap.String("function", fn.Name),
				zap.String("access_point", fsc.Arn),
				zap.String("mount_path", fsc.LocalMountPath))
			continue
		}
		mounts = append(mounts, volumeMount(volume, subpath, fsc.LocalMountPath, false))
	}
	if len(mounts) == 0 {
		return nil
	}
	return mounts
}

// volumeMount builds a named-volume Mount, attaching a Subpath option only
// when the mount is scoped below the volume root.
func volumeMount(volume, subpath, target string, readOnly bool) docker.Mount {
	m := docker.Mount{Type: "volume", Source: volume, Target: target, ReadOnly: readOnly}
	if subpath != "" {
		m.VolumeOptions = &docker.MountVolumeOptions{Subpath: subpath}
	}
	return m
}

// ─── Image management ──────────────────────────────────────────────────────

// SeedImages pre-pulls Docker images in parallel so the first cold start of a
// runtime skips the image pull entirely. The seed runs in a background goroutine
// with a detached context — it does not block startup or callers. Call after
// the ContainerRuntime is fully wired (i.e. after initDockerRuntime).
//
// Pre-pulling at startup is the single biggest lever for cold-start latency:
// the base images are 200–500 MB and pulling them on the first Invoke path can
// take minutes. By the time the user creates a function and invokes it the
// images are already cached locally.
//
// Only runtimes AWS still supports are seeded. Overcast can execute every
// runtime AWS still lets you deploy, including ones already past their
// deprecation date, but pre-pulling all of them would cost several extra
// gigabytes for images almost nobody deploys. A deprecated runtime is still
// pulled on demand at its first cold start.
func (cr *ContainerRuntime) SeedImages() {
	now := cr.clk.Now()
	// Deduplicate: distinct runtimes may share one underlying image.
	uniq := make(map[string]struct{}, len(activeRuntimes))
	for runtimeID, image := range activeRuntimes {
		if runtimeDeprecated(runtimeID, now) {
			continue
		}
		uniq[image] = struct{}{}
	}
	images := make([]string, 0, len(uniq))
	for image := range uniq {
		images = append(images, image)
	}

	// Concurrent pulls with a bounded worker pool so we don't flood the
	// Docker daemon or the user's network on startup. Return immediately —
	// workers drain the channel in background goroutines.
	const seedWorkers = 4
	jobs := make(chan string, len(images))

	for w := 0; w < seedWorkers && w < len(images); w++ {
		go func() {
			for image := range jobs {
				start := cr.clk.Now()
				// Seeded images are the fixed public Lambda base images; a
				// resolver has nothing to say about them, but going through the
				// same path keeps one way of pulling.
				ctx := context.Background()
				err := cr.ensureImage(ctx, cr.resolveImage(ctx, image), dockerPlatformForLambdaArchitectures(nil))
				if err != nil {
					cr.logger.Warn("seed pull failed",
						zap.String("image", image),
						zap.Duration("elapsed", cr.clk.Since(start)),
						zap.Error(err))
				} else {
					cr.logger.Info("seed pull complete",
						zap.String("image", image),
						zap.Duration("elapsed", cr.clk.Since(start)))
				}
			}
		}()
	}

	for _, img := range images {
		jobs <- img
	}
	close(jobs)
}

// imagePullKey identifies an image+platform pair in the pullOnce and
// imageVerified maps — the same key must be used to seed and to invalidate.
func imagePullKey(image, platform string) string {
	if platform == "" {
		return image
	}
	return image + "@" + platform
}

// ensureImage pulls ref unless the daemon already holds it. It works entirely
// in the resolved reference — that is the name the daemon knows the image by,
// so it is what the presence check, the pull and the sync.Once key must all
// use. Everything a user reads names the requested reference instead.
func (cr *ContainerRuntime) ensureImage(ctx context.Context, ref imageRef, platform string) error {
	image := ref.requested

	// Check if already pulled.
	exists, err := cr.docker.ImageMatchesPlatform(ctx, ref.resolved.Ref, platform)
	if err == nil && exists {
		return nil
	}

	// Use sync.Once per image+platform to avoid concurrent pulls while still
	// allowing arm64 and x86_64 variants of the same Lambda tag to be requested.
	pullKey := imagePullKey(ref.resolved.Ref, platform)
	once, _ := cr.pullOnce.LoadOrStore(pullKey, &sync.Once{})
	var pullErr error
	once.(*sync.Once).Do(func() {
		fields := []zap.Field{zap.String("image", image), zap.String("platform", platform)}
		if ref.rewritten() {
			fields = append(fields, zap.String("served_as", ref.resolved.Ref))
		}
		cr.logger.Info("pulling Lambda image (first use)", fields...)
		// Capture bus and clock once; either may be nil in tests or before wiring.
		bus := cr.bus.Load()
		clk := cr.clk
		if bus != nil && clk != nil {
			bus.Publish(context.Background(), events.Event{
				Type:    events.LambdaImagePulling,
				Time:    clk.Now(),
				Source:  "lambda",
				Payload: events.LambdaImagePullPayload{Image: image},
			})
		}
		var startTime time.Time
		if clk != nil {
			startTime = clk.Now()
		}
		pullErr = cr.docker.PullImageWithOptions(ctx, ref.resolved.Ref, docker.PullOptions{
			Platform: platform,
			Auth:     ref.resolved.Auth,
		})
		if pullErr != nil {
			// The failure reaches a user as a function's StateReason, so it has
			// to name the image they deployed as well as the one it was fetched
			// as — neither alone explains what happened.
			if ref.rewritten() {
				pullErr = fmt.Errorf("pull image %s, served here as %s: %w", image, ref.resolved.Ref, pullErr)
			}
			// Reset so we retry on next call.
			cr.pullOnce.Delete(pullKey)
		}
		if bus != nil && clk != nil {
			var errStr string
			if pullErr != nil {
				errStr = pullErr.Error()
			}
			bus.Publish(context.Background(), events.Event{
				Type:   events.LambdaImagePullComplete,
				Time:   clk.Now(),
				Source: "lambda",
				Payload: events.LambdaImagePullPayload{
					Image:     image,
					ElapsedMs: clk.Now().Sub(startTime).Milliseconds(),
					Error:     errStr,
				},
			})
		}
	})
	return pullErr
}

// ─── Environment variables ─────────────────────────────────────────────────

// runtimeAPIAddr is this execution environment's own Runtime API endpoint, not
// the shared one: the port it dials is what identifies it to the Runtime API.
// target, when not nil, adds its protocol's listen flag and OVERCAST_DEBUG_PORT
// last: it appends to a user's own value rather than replacing it, and it is
// the one thing here that must see the runtime variables already applied.
func (cr *ContainerRuntime) buildEnv(fn *Function, logStream, initType, runtimeAPIAddr string, target *debugger.Target) []string {
	// AWS_REGION must reflect the function's actual region (encoded in its
	// ARN), not the emulator's global default. SDKs sign requests with this
	// region; if we used the default (e.g. us-east-1) for a function deployed
	// to ap-southeast-2, downstream service calls (DynamoDB, S3, etc.) would
	// be routed to the wrong region's data and resources would appear missing.
	region := regionFromFunctionARN(fn.ARN)
	if region == "" {
		region = cr.cfg.Region
	}
	env := make(map[string]string, len(fn.Environment)+32)
	// User environment values are rewritten on the way in: a deploy performed
	// from the host bakes host-side URLs (a CDK stack passing queue.queueUrl)
	// into function env, and AWS SDKs resolve endpoints from those URLs rather
	// than from AWS_ENDPOINT_URL. See internal/containerendpoint.
	for k, v := range fn.Environment {
		env[k] = cr.endpoint.RewriteURLs(v)
	}

	// The endpoint containers are told to use. Falls back to the address form
	// when no split-horizon name can be pinned for this container.
	clientEndpoint := cr.endpoint.ClientEndpoint()
	if clientEndpoint == "" {
		clientEndpoint = cr.overcastEndpoint
	}

	// Runtime-provided variables are applied after user variables. This matches
	// Lambda's reserved-env behavior and guarantees extensions inherit the local
	// endpoint and credential values even if a deployment template contains empty
	// AWS_* placeholders.
	runtimeEnv := map[string]string{
		"AWS_LAMBDA_FUNCTION_NAME":        fn.Name,
		"AWS_LAMBDA_FUNCTION_VERSION":     "$LATEST",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": fmt.Sprint(fn.MemorySize),
		"AWS_LAMBDA_LOG_GROUP_NAME":       fn.logGroupName(),
		"AWS_LAMBDA_LOG_STREAM_NAME":      logStream,
		// Lambda's advanced logging controls are delivered to the runtime as
		// environment variables — the managed runtimes read them to decide
		// whether to structure their own output, and AWS documents them as the
		// integration point for custom runtimes.
		"AWS_LAMBDA_LOG_FORMAT": resolveLogFormat(fn),
		// Real Lambda always sets this; Powertools and other observability
		// libraries use it for cold-start classification.
		"AWS_LAMBDA_INITIALIZATION_TYPE": initType,
		"AWS_REGION":                     region,
		"AWS_DEFAULT_REGION":             region,
		"AWS_ACCOUNT_ID":                 cr.cfg.AccountID,
		"AWS_ACCESS_KEY_ID":              "overcast",
		"AWS_SECRET_ACCESS_KEY":          "overcast",
		// Real Lambda always sets a session token from execution-role
		// assumption. Some SDK credential providers (notably the JS v3
		// fromEnv chain) treat the absence of this variable as "not a
		// Lambda environment" and fall through to other providers, so we
		// always provide a placeholder.
		"AWS_SESSION_TOKEN": "overcast",
		// The AWS value, verbatim: the Runtime API the runtime and the
		// extensions talk to is the one Overcast's init serves on the
		// container's own loopback, and a runtime interface client cannot tell
		// it from RAPID's. The init proxies it through to this execution
		// environment's endpoint on the host, which it is told about
		// separately — the host's address is the init's business, not the
		// runtime's.
		"AWS_LAMBDA_RUNTIME_API": initproto.LambdaRuntimeAPI,
		// Overcast's own variable, with no AWS counterpart: on AWS the init
		// *is* the endpoint. The port is per-execution-environment and is what
		// identifies this container to the Runtime API, so it is baked in at
		// create time — see containerListener.
		initproto.EnvRuntimeAPI:       runtimeAPIAddr,
		"LAMBDA_TASK_ROOT":            "/var/task",
		"AWS_LAMBDA_FUNCTION_TIMEOUT": fmt.Sprint(fn.Timeout),
		"TZ":                          ":/etc/localtime",
		// Route all AWS SDK calls from the function back to the Overcast emulator.
		// AWS SDKs v2+ honour AWS_ENDPOINT_URL for every service automatically.
		//
		// A name rather than an address: it survives Overcast being recreated on
		// a different one, and it lets an SDK build the virtual-hosted URLs it
		// derives from the endpoint ({bucket}.s3.{host} and friends), which an IP
		// endpoint forces to path-style. See containerendpoint.ClientEndpoint.
		"AWS_ENDPOINT_URL": clientEndpoint,
		// Per AWS SDK reference for service-specific endpoints:
		// SSM and Secrets Manager use these variables, overriding the global endpoint.
		// Verified with AWS Parameters and Secrets Lambda Extension 1.0.342
		// (2026-07-14): SSM and Secrets Manager requests honor these vars.
		"AWS_ENDPOINT_URL_SSM":             clientEndpoint,
		"AWS_ENDPOINT_URL_SECRETS_MANAGER": clientEndpoint,
	}
	// With TLS on, point every SDK TLS stack at the injected CA bundle
	// (AWS_CA_BUNDLE for botocore/Go/CLI, NODE_EXTRA_CA_CERTS for the Node
	// runtimes, SSL_CERT_FILE/REQUESTS_CA_BUNDLE for OpenSSL and requests).
	for k, v := range cr.endpoint.CABundleEnv() {
		runtimeEnv[k] = v
	}
	for k, v := range runtimeEnv {
		env[k] = v
	}
	// _HANDLER and AWS_EXECUTION_ENV are set only for zip deployments where
	// Runtime and Handler are specified. Image functions use the image's
	// ENTRYPOINT and do not have a Runtime string.
	if fn.Handler != "" {
		env["_HANDLER"] = fn.Handler
	}
	if fn.Runtime != "" && fn.Runtime != "image" {
		env["AWS_EXECUTION_ENV"] = "AWS_Lambda_" + fn.Runtime
	}
	// Only JSON functions have an application log level to hand down; in Text
	// mode AWS does not set the variable at all, and setting it would tell a
	// runtime to filter output that Lambda has not asked it to filter.
	if resolveLogFormat(fn) == logFormatJSON {
		applicationLevel, _ := resolveLogLevels(fn)
		env["AWS_LAMBDA_LOG_LEVEL"] = applicationLevel.String()
	}
	if target != nil {
		target.Inject(env)
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// ─── containerInstance ─────────────────────────────────────────────────────

// containerInstance implements RuntimeInstance for a running Docker container.
type containerInstance struct {
	id             string
	containerIP    string // IP on the Lambda Docker network
	functionName   string
	functionARN    string
	instanceID     string // stable execution-environment ID, survives warm reuse
	configIdentity string // fingerprint of the code + config baked in at creation time
	memorySize     int    // configured memory in MB
	logStream      string
	logGroupName   string
	// Logging configuration, resolved from the function at creation time. The
	// zero value (empty logFormat) is Text, which is what every containerInstance
	// built outside newContainerInstance wants. See logging_json.go.
	logFormat   string
	appLogLevel logLevel
	sysLogLevel logLevel
	docker      *docker.Client
	gc          *docker.GC // async fallback when direct removal fails
	runtimeAPI  *RuntimeAPIServer
	logger      *zap.Logger
	clk         clock.Clock
	logWriter   events.LogWriter // nil if CWL not wired
	logCtx      context.Context  // the sink's context; ended by Close
	exitNotify  *exitNotifier    // Docker watcher exit notifications
	peakMemMB   atomic.Int64     // running peak memory (MB) — what REPORT's Max Memory Used reads
	// logSink is the host end of the in-container init's log channel: the
	// per-request tail buffers, the Telemetry API publish and the CloudWatch
	// batcher. It is created before the container is, and outlives every one of
	// the init's connections. Nil only for a containerInstance built by a test
	// that does not care about output. See container_logs.go.
	logSink        *logSink
	readyCh        <-chan struct{}
	endpoint       *containerendpoint.Mapper // re-points Overcast URLs inside invoke payloads
	rapiListener   *containerListener        // this environment's own Runtime API endpoint
	reach          containerendpoint.Listen  // how the Runtime API address was established — see ContainerRuntime.reach
	reachHintPath  string                    // the remembered probe result, dropped when this container disproves it
	healthy        bool
	keepContainers bool // when true, Close only stops the container instead of removing it
	// debug is the debugger target this environment was created for, whose
	// proxy dials this container; nil for the undebugged majority. It is what
	// lets an invocation on this environment suspend its clock — see
	// boundInvocation — without a lookup per invoke.
	debug *debugger.Target

	// forgetInitBurst drops this container's pending INIT-burst entry when the
	// environment is destroyed. An environment that dies before its first
	// GET /next never reports INIT complete, so nothing else would ever remove
	// the entry that throttle-down is waiting to act on. Set by
	// newContainerInstance.
	forgetInitBurst func()

	// initStartedAt is when the container was started, for environments whose
	// init was triggered by an invoke (on-demand cold starts). Zero for
	// proactively initialised environments (provisioned concurrency), which —
	// like real Lambda — never report an Init Duration.
	initStartedAt time.Time
	initReported  bool // true once Init Duration has been written to a REPORT line
}

func (ci *containerInstance) AwaitReady(ctx context.Context) error {
	if ci.readyCh == nil {
		return nil
	}
	var exitCh <-chan string
	var waitCancel context.CancelFunc
	if ci.exitNotify != nil && ci.id != "" {
		exitCh = ci.exitNotify.register(ci.id)
		defer ci.exitNotify.unregister(ci.id)
	} else if ci.docker != nil && ci.id != "" {
		strCh := make(chan string, 1)
		exitCh = strCh
		var waitCtx context.Context
		waitCtx, waitCancel = context.WithCancel(ctx)
		defer waitCancel()
		go func() {
			exitCode, err := ci.docker.WaitContainer(waitCtx, ci.id)
			if err == nil {
				strCh <- fmt.Sprintf("%d", exitCode)
			}
		}()
	}
	select {
	case <-ci.readyCh:
		if waitCancel != nil {
			waitCancel()
		}
		return nil
	case exitCode := <-exitCh:
		ci.healthy = false
		ci.logInitExit(exitCode)
		return fmt.Errorf("lambda container exited during init (exit code %s)%s", exitCode, ci.initExitHint())
	case <-ctx.Done():
		// Only the budget running out is a fault worth explaining. The caller
		// giving up — an abandoned invoke, a shutdown — cancels this same
		// context and has nothing to diagnose.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			ci.logInitTimeout()
		}
		return ctx.Err()
	}
}

const (
	// initTimeoutLogTail is how many lines of the container's output the
	// INIT-timeout diagnostic quotes.
	initTimeoutLogTail = "50"
	// initTimeoutLogTimeout bounds the wait for the daemon to hand them over.
	// The invocation has already failed by this point, so the diagnostic must
	// not be what keeps the caller waiting.
	initTimeoutLogTimeout = 3 * time.Second
)

// logInitTimeout explains an execution environment that never finished INIT.
//
// What the caller gets is "lambda runtime did not initialize within 10s", which
// names neither the container nor anything that separates the causes. #800 is
// what that costs: an entire investigation went into proving the Runtime API
// was reachable on the address in the startup log — the shared one — when the
// container had been handed a per-environment port that appears nowhere else,
// and nothing in the log said whether anything had ever connected to it.
//
// So this answers, for this environment specifically: the address it was told
// to dial, whether anything ever arrived there, and what it printed.
//
// Connections narrow it honestly rather than conclusively. The RIC does not
// dial until it has imported the handler, so none means either the import is
// still running or the container cannot route back to this host — two very
// different jobs, but both are now a named pair to choose between instead of
// nothing at all. Some, with no invocation served, rules the route out: what is
// left is identity, and the 403 sits next to this line.
func (ci *containerInstance) logInitTimeout() {
	if ci.logger == nil {
		return
	}
	connections := ci.rapiListener.Accepted()
	msg := "lambda INIT timed out — the container reached its Runtime API endpoint but never polled for work"
	if connections == 0 {
		msg = "lambda INIT timed out — the container never reached its Runtime API endpoint; it is either still initialising or cannot route back to this host"
	}
	ci.logger.Warn(msg, ci.initDiagnosticFields()...)
}

// logInitExit explains an execution environment whose container died before it
// was ready — the other way INIT ends, and until it was given this the only one
// with no account of itself.
//
// The caller gets "lambda container exited during init (exit code 139)", which
// is a number and a function name. Everything that would identify the cause —
// what the container printed, whether it ever reached its Runtime API endpoint
// — is already held here, and was being dropped on the floor because only the
// timeout branch asked for it. A container that cannot route back to this host
// is the case that costs most: its own [overcast-init] output names the address
// and the i/o timeout, but that output travels over the very connection that is
// broken, so the daemon's copy is the only place it survives — and nothing was
// reading it.
func (ci *containerInstance) logInitExit(exitCode string) {
	if ci.logger == nil {
		return
	}
	fields := append(ci.initDiagnosticFields(), zap.String("exit_code", exitCode))
	ci.logger.Warn("lambda container exited during INIT", fields...)
	ci.forgetReachabilityHint()
}

// initExitHint is the sentence that goes into the caller's own error when the
// exit is one the reachability probe already predicted.
//
// It reaches a user as the `Runtime.InitError` on their Invoke, which is the
// only place many of them will look. "exit code 139" alone points at the
// runtime — SIGSEGV, so a corrupt init binary is the obvious reading, and it is
// wrong. The truth is that the Node runtime segfaults when the Runtime API
// never answers `invocation/next`, and this says so where the number does not.
func (ci *containerInstance) initExitHint() string {
	if !ci.reach.Unreachable {
		return ""
	}
	return ": " + containerendpoint.RuntimeAPIUnreachableDetail(ci.reach.Attempts)
}

// reachabilityFields say how the Runtime API address in the line above was
// arrived at.
//
// Without them the diagnostic names an address and a connection count and
// stops, which is where #1572 hid: the count was 0, the address was the host's
// own routable interface, and nothing said that address had only ever been
// tested for bindability. Now the line carries the mode that chose it, whether
// a container was ever seen to reach it, and — when the probe found nothing
// reachable at all — every candidate it tried and what each one did.
func (ci *containerInstance) reachabilityFields() []zap.Field {
	if ci.reach.ContainerHost == "" {
		return nil
	}
	fields := []zap.Field{
		zap.String("runtime_api_mode", ci.reach.Mode),
		zap.Bool("runtime_api_container_verified", ci.reach.Verified),
	}
	if len(ci.reach.Attempts) > 0 {
		fields = append(fields, zap.Strings("runtime_api_candidates", containerendpoint.AttemptStrings(ci.reach.Attempts)))
	}
	if ci.reach.Unreachable {
		fields = append(fields, zap.String("runtime_api_diagnosis",
			containerendpoint.RuntimeAPIUnreachableDetail(ci.reach.Attempts)))
	}
	return fields
}

// forgetReachabilityHint drops the remembered probe result when this container
// is the evidence against it: it died during INIT without ever opening a
// connection to its Runtime API endpoint, which is what an unreachable address
// looks like from here.
//
// Only when the address was *remembered* rather than probed this run. A probe
// that ran and chose an address, and a pinned address, are both answers this
// run already stands behind — deleting a file neither of them reads would only
// hide which of the two happened.
//
// That guard is also what bounds the cost when the deaths are not the network
// at all. A genuinely broken function — one that segfaults during INIT before
// it ever polls — looks identical from here, so it drops the hint; but the next
// startup then probes for itself and records a non-hinted mode, and that run
// ignores every death. So the worst case is one extra probe every *other*
// startup, not one per cold start, and it converges the moment the function is
// fixed.
func (ci *containerInstance) forgetReachabilityHint() {
	if ci.reachHintPath == "" || !strings.HasPrefix(ci.reach.Mode, "hinted") {
		return
	}
	if ci.rapiListener != nil && ci.rapiListener.Accepted() > 0 {
		return
	}
	containerendpoint.ForgetHint(ci.reachHintPath)
	if ci.logger != nil {
		ci.logger.Warn("the remembered Runtime API address did not work — the next startup will probe again",
			zap.String("runtime_api", ci.rapiListener.Addr()),
			zap.String("mode", ci.reach.Mode))
	}
}

// SetRuntimeAPIReachability records how the Runtime API address was
// established, so an INIT failure can quote the verdict rather than leaving the
// reader to guess at the one number the runtime gives them.
func (cr *ContainerRuntime) SetRuntimeAPIReachability(l containerendpoint.Listen, hintPath string) {
	cr.reach = l
	cr.reachHintPath = hintPath
}

// initDiagnosticFields is the evidence both INIT failures are explained with:
// which environment, where its container was told to call back, whether
// anything ever arrived there, and what the container printed.
func (ci *containerInstance) initDiagnosticFields() []zap.Field {
	fields := []zap.Field{
		zap.String("function", ci.functionName),
		zap.String("container_ip", ci.containerIP),
		zap.String("runtime_api", ci.rapiListener.Addr()),
		zap.Int64("runtime_api_connections", ci.rapiListener.Accepted()),
	}
	fields = append(fields, ci.reachabilityFields()...)
	if ci.id != "" {
		fields = append(fields, zap.String("container", shortContainerID(ci.id)))
	}
	output, err := ci.initOutput()
	switch {
	case err != nil:
		fields = append(fields, zap.NamedError("container_output_error", err))
	case output == "":
		// Worth stating rather than omitting: an empty log is the normal state
		// for a healthy AWS base image, whose RIC prints nothing until it runs
		// the handler. Reading it as suppressed output is a dead end, and one
		// #800 went down.
		fields = append(fields, zap.String("container_output", "(the container printed nothing)"))
	default:
		fields = append(fields, zap.String("container_output", output))
	}
	return fields
}

// initOutput is a bounded tail of what the container printed before its first
// invocation.
//
// It comes from the INIT-phase buffer the host already holds: the in-container
// init tags every line it reads with the invocation in flight at the time, and
// during INIT there is none, so that buffer is exactly the INIT phase. No
// daemon round trip, and no wait, on a path that is already a failure.
//
// It is empty in one case: the init never got far enough to publish anything —
// it could not start, or could not reach this host. That is the case the
// daemon's copy answers, because the init tees every line it reads to the
// container's own stdout and writes its own [overcast-init] diagnostics to the
// container's stderr before it gives up. So the fallback is not a redundant
// second transport; it is the only place the evidence for that specific failure
// exists.
func (ci *containerInstance) initOutput() (string, error) {
	if ci.logSink != nil {
		if buffered := strings.TrimSpace(ci.logSink.initOutput()); buffered != "" {
			return buffered, nil
		}
	}
	if ci.docker == nil || ci.id == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeoutLogTimeout)
	defer cancel()
	raw, err := ci.docker.ContainerLogs(ctx, ci.id, initTimeoutLogTail)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(docker.DemuxStream(raw))), nil
}

// LogStreamName returns the AWS-style log stream assigned to this container.
func (ci *containerInstance) LogStreamName() string { return ci.logStream }

// FunctionName returns the name of the Lambda function this container runs.
func (ci *containerInstance) FunctionName() string { return ci.functionName }

// ConfigIdentity returns the fingerprint of the code and configuration this
// container was created with.
func (ci *containerInstance) ConfigIdentity() string { return ci.configIdentity }

// ContainerID returns the Docker container ID backing this instance.
func (ci *containerInstance) ContainerID() string { return ci.id }

// InstanceID returns the stable execution-environment ID for this container.
func (ci *containerInstance) InstanceID() string { return ci.instanceID }

// invokeTimeoutError reports that a function ran past its configured timeout.
// It carries what real Lambda puts in the timeout response so the invoke
// handlers can shape the payload the way AWS does rather than leaking a Go
// error string into the caller's response.
type invokeTimeoutError struct {
	RequestID string
	Timeout   time.Duration
	At        time.Time
}

func (e *invokeTimeoutError) Error() string {
	return fmt.Sprintf("Task timed out after %.2f seconds", e.Timeout.Seconds())
}

// Payload is the response body real Lambda returns for a timed-out
// invocation: a lone errorMessage of the form
// "<UTC timestamp> <request id> Task timed out after N.NN seconds", with the
// error type carried by X-Amz-Function-Error rather than the body.
func (e *invokeTimeoutError) Payload() []byte {
	out, _ := json.Marshal(map[string]string{
		"errorMessage": fmt.Sprintf("%s %s Task timed out after %.2f seconds",
			e.At.UTC().Format("2006-01-02T15:04:05.000Z"), e.RequestID, e.Timeout.Seconds()),
	})
	return out
}

// Invoke sends the event to the container via the Runtime API and waits for
// the result. The container's RIC picks up the event from GET /next, runs the
// handler, and POSTs the result back to /response or /error.
//
// The caller is responsible for bounding ctx with the function's configured
// Timeout; this method trusts ctx.Deadline() to derive the Lambda-Runtime-Deadline-Ms
// header, which is what the RIC uses to implement context.getRemainingTimeInMillis().
func (ci *containerInstance) Invoke(ctx context.Context, event []byte, opts InvokeOptions) (*InvokeResult, error) {
	// A payload can carry Overcast URLs a host-side caller minted — a queue URL
	// passed to Invoke is the usual one. AWS SDKs resolve the service endpoint
	// from such a URL rather than from AWS_ENDPOINT_URL, so an origin this
	// container cannot dial would send its client to its own loopback. Function
	// environment is rewritten for exactly this reason; a payload is the same
	// value arriving by another route.
	event = ci.endpoint.RewriteBytes(event)

	deadline, ok := ctx.Deadline()
	if !ok {
		// Fallback: should not happen in normal operation because invokeSync
		// always applies a timeout, but kept as a safety net.
		deadline = ci.clk.Now().Add(900 * time.Second)
	}

	if reason, ok := ci.runtimeAPI.ContainerError(ci.containerIP); ok {
		ci.healthy = false
		return extensionInvokeError(reason), nil
	}

	start := ci.clk.Now()
	// Prepared, not yet released: the runtime must not be able to see this
	// invocation until its START record is down. A warm runtime is already
	// parked on GET /next, and the handler's first print can be read, framed
	// and ingested within a millisecond of the hand-off — under load, sooner
	// than this goroutine is rescheduled. Submitting first therefore let the
	// handler's own lines beat START into the tail and CloudWatch (the third
	// life of this defect: #1160 and #1325 each widened a wait in the old
	// silence-based pipeline; the in-container init removed that mechanism,
	// and this host-side ordering was the race it left behind). Preparing,
	// writing START, then releasing removes the class: a frame cannot carry
	// this request ID before the runtime has been handed the invocation.
	reqID, resultCh := ci.runtimeAPI.PrepareInvocation(ci.functionARN, event, deadline)
	// Overlap the memory sample with handler execution; REPORT waits on it
	// only briefly (see reportMemoryMB).
	memSample := ci.startMemorySample()

	ci.logger.Debug("invoke submitted",
		zap.String("container", ci.id[:12]),
		zap.String("request_id", reqID),
		zap.Int("event_bytes", len(event)),
	)

	// Everything the container printed before it asked for work belongs in
	// front of this invocation's START: the INIT phase for a cold start, and
	// anything the runtime printed after answering the previous invocation for
	// a warm one. The init stamped that boundary on its GET /next, so this is
	// the same wait as the response-side one, at the other end of the
	// invocation — and normally zero, because those frames were published long
	// before the poll that named them. (With the release below, this wait now
	// sits ahead of the hand-off, so a broken log channel costs its bound in
	// invoke latency rather than in misordered output — the right side of that
	// trade, and only a degraded environment ever pays it.)
	ci.awaitIdleLog()

	// The trace header this invocation's runtime will receive; the records
	// that open and close the invocation carry it so a subscriber can
	// correlate them with the function's own traced calls.
	traceID := ci.runtimeAPI.InvocationTraceID(reqID)

	// Emit the start record that real Lambda writes before every invocation.
	ci.emitInvocationStart(reqID, traceID)

	// Monitor container exit via the Docker event watcher (if wired) or fall
	// back to a per-invocation WaitContainer goroutine. The watcher path
	// avoids spawning a goroutine + blocking HTTP connection per invocation.
	var exitCh <-chan string
	var waitCancel context.CancelFunc
	if ci.exitNotify != nil {
		exitCh = ci.exitNotify.register(ci.id)
		defer ci.exitNotify.unregister(ci.id)
	} else {
		// Fallback: no watcher (e.g. bus not wired). Use WaitContainer.
		strCh := make(chan string, 1)
		exitCh = strCh
		var waitCtx context.Context
		waitCtx, waitCancel = context.WithCancel(ctx)
		go func() {
			exitCode, err := ci.docker.WaitContainer(waitCtx, ci.id)
			if err == nil {
				strCh <- fmt.Sprintf("%d", exitCode)
			}
		}()
	}

	// START is written and the exit watcher is armed — only now does the
	// runtime get the invocation.
	ci.runtimeAPI.ReleaseInvocation(reqID)

	var resp invokeResponse
	select {
	case resp = <-resultCh:
		if waitCancel != nil {
			waitCancel() // stop WaitContainer goroutine — container stays alive for reuse
		}
	case exitCode := <-exitCh:
		if waitCancel != nil {
			waitCancel()
		}
		ci.healthy = false
		// Cancel the pending invocation so its ResultCh is closed and no
		// drain goroutine is needed. This also removes the map entry.
		ci.runtimeAPI.CancelInvocation(reqID)
		// Whatever the function printed on its way out is the output most
		// worth keeping, and the container is about to be torn down. The init
		// drains both pipes, flushes its log stream and closes it when its
		// child exits, so the stream ending is the event that says everything
		// has arrived — no silence to time, only a bound on an init that is
		// itself gone.
		ci.awaitContainerOutputEnd()
		elapsed := ci.clk.Now().Sub(start)
		// The buffer is not released here, and on neither of the other two
		// failure paths: all three mark the environment unhealthy, so it is
		// closed moments later and the whole sink goes with it. What the
		// buffer still holds until then is the evidence for what happened.
		ci.emitInvocationEnd(reqID, outcomeCrashed, elapsed, ci.reportMemoryMB(memSample), 0, traceID)
		return nil, fmt.Errorf("lambda container exited unexpectedly (exit code %s) — check container logs for details", exitCode)
	case <-ctx.Done():
		if waitCancel != nil {
			waitCancel()
		}
		ci.healthy = false
		// Cancel the pending invocation so its ResultCh is closed and no
		// drain goroutine is needed. This also removes the map entry.
		ci.runtimeAPI.CancelInvocation(reqID)
		// Same as the crash path: wait for the init's stream to end rather
		// than for its output to go quiet. A container that is merely being
		// abandoned by its caller has an init that is still running, so this
		// returns on its bound — which is why the bound is short.
		ci.awaitContainerOutputEnd()
		elapsed := ci.clk.Now().Sub(start)
		// Only a deadline is a Lambda timeout. A plain cancellation means the
		// caller went away — the console closing its progress stream, an SDK
		// client disconnecting — and labelling that "timeout" sends people
		// hunting for a handler bug that isn't there, with a REPORT line that
		// contradicts the function's configured timeout.
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		outcome := outcomeCrashed
		if timedOut {
			outcome = outcomeTimedOut
		}
		ci.emitInvocationEnd(reqID, outcome, elapsed, ci.reportMemoryMB(memSample), 0, traceID)
		if timedOut {
			return nil, &invokeTimeoutError{RequestID: reqID, Timeout: deadline.Sub(start), At: ci.clk.Now()}
		}
		return nil, fmt.Errorf("lambda invoke cancelled before the function returned: %w", ctx.Err())
	}

	elapsed := ci.clk.Now().Sub(start)
	if reason, ok := ci.runtimeAPI.ContainerError(ci.containerIP); ok {
		ci.healthy = false
		return extensionInvokeError(reason), nil
	}

	// Build the result before reading the tail so the function has had the
	// maximum opportunity to flush its stdout/stderr.
	var result *InvokeResult
	if resp.IsInitError {
		ci.healthy = false
		result = &InvokeResult{
			StatusCode:    200,
			Payload:       resp.ErrorPayload,
			FunctionError: "Unhandled",
		}
	} else if resp.FunctionError != "" {
		payload := resp.ErrorPayload
		if payload == nil {
			payload = resp.Payload
		}
		result = &InvokeResult{
			StatusCode:    200,
			Payload:       payload,
			FunctionError: resp.FunctionError,
		}
	} else {
		result = &InvokeResult{
			StatusCode: 200,
			Payload:    resp.Payload,
		}
	}
	// Every branch above describes the same invocation, so the request ID is
	// attached once here rather than three times. A dead-letter queue reports
	// it so the message can be correlated with the function's logs.
	result.RequestID = reqID

	// Wait for this invocation's output to have arrived, then close the
	// invocation. resp.LogSeq is what the in-container init put on the response
	// as it forwarded it: "every log frame at or below this was published on
	// the log stream before this request". The init publishes to a connection
	// that is already open, before it sends the response on another, so this is
	// normally satisfied the moment it is entered — there is no round trip
	// here, and no clock.
	//
	// Every invoke pays it, not only tail-requesting ones, because it is what
	// puts END and REPORT strictly after the request's last line in CloudWatch
	// as well as in the tail. logSeqWaitMax bounds a channel that is broken,
	// not a handler that is slow.
	logWaitStart := ci.clk.Now()
	ci.awaitRequestLog(resp.LogSeq)
	logWait := ci.clk.Now().Sub(logWaitStart)

	// Emit the END + REPORT records that real Lambda writes after every
	// invocation. Writing them flushes whatever the ingest has batched for this
	// request first, which is what puts them strictly after the request's last
	// line in CloudWatch — the wait above having established that the last line
	// is in hand.
	memWaitStart := ci.clk.Now()
	memMB := ci.reportMemoryMB(memSample)
	memWait := ci.clk.Now().Sub(memWaitStart)
	outcome := outcomeSuccess
	if result.FunctionError != "" {
		outcome = outcomeHandlerError
	}
	ci.emitInvocationEnd(reqID, outcome, elapsed, memMB, len(result.Payload), traceID)

	// Per-invocation timing breakdown (Phase 0 of
	// docs/plans/lambda-cold-start.md): what the emulator added around the
	// handler's own runtime. TRACE — it fires on every invoke.
	ci.logger.Log(logging.TraceLevel, "lambda invoke timings",
		zap.String("container", shortContainerID(ci.id)),
		zap.Duration("handler", elapsed),
		zap.Duration("log_wait", logWait),
		zap.Duration("mem_wait", memWait),
		zap.Duration("total", ci.clk.Now().Sub(start)),
	)

	// Snapshot this request's log for X-Amz-Log-Result: START, its own lines in
	// the order the init read them, END, REPORT — and nothing from any other
	// invocation, because nothing else was ever put in this buffer.
	if opts.LogTail && ci.logSink != nil {
		if snap := ci.logSink.snapshot(reqID); len(snap) > 0 {
			result.LogResult = base64.StdEncoding.EncodeToString(snap)
		}
	}
	ci.releaseRequestLog(reqID)

	return result, nil
}

// logSeqWaitMax bounds awaitRequestLog. It is a fact about a *broken* log
// channel, not a guess about a slow one: the frames it waits for were written
// to an already-open connection before the response that names them was sent,
// so the ordinary case is satisfied without blocking at all. What is left for
// this bound to catch is the channel being down — the init reconnecting, the
// host's ingest handler gone — and in that case the tail is simply what
// arrived, which is exactly what "the last 4 KB of the execution log" promises.
const logSeqWaitMax = 100 * time.Millisecond

// containerOutputEndMax bounds the wait for the init's log stream to close on
// the crash and timeout paths. The init drains, flushes and closes on child
// exit, so this is the time a dying container is allowed for that; a container
// whose init is still running (an abandoned invoke) pays it in full, which is
// why it is short.
const containerOutputEndMax = 2 * time.Second

// awaitRequestLog waits for the ingest to have seen every frame the init had
// published when it forwarded the response.
func (ci *containerInstance) awaitRequestLog(seq uint64) {
	if ci.logSink == nil || seq == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), logSeqWaitMax)
	defer cancel()
	if !ci.logSink.awaitLogSeq(ctx, seq) {
		// Nothing else to mark: the tail is what arrived, and the container is
		// still perfectly able to serve the next invocation.
		ci.logger.Debug("lambda container logs: the invocation's last frames had not arrived when it answered",
			zap.String("container", shortContainerID(ci.id)),
			zap.Uint64("awaited_seq", seq),
			zap.Duration("waited", logSeqWaitMax),
		)
	}
}

// awaitIdleLog waits for everything the container printed before it asked for
// work to have been ingested, so the START written next follows it.
func (ci *containerInstance) awaitIdleLog() {
	if ci.logSink == nil {
		return
	}
	seq := ci.logSink.idleSequence()
	if seq == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), logSeqWaitMax)
	defer cancel()
	if !ci.logSink.awaitLogSeq(ctx, seq) {
		ci.logger.Debug("lambda container logs: the output preceding an invocation had not arrived when it started",
			zap.String("container", shortContainerID(ci.id)),
			zap.Uint64("awaited_seq", seq),
			zap.Duration("waited", logSeqWaitMax),
		)
	}
}

// awaitContainerOutputEnd waits for the init's log stream to end, which is the
// event that means the container has said everything it is ever going to.
func (ci *containerInstance) awaitContainerOutputEnd() {
	if ci.logSink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerOutputEndMax)
	defer cancel()
	_ = ci.logSink.awaitStreamEnd(ctx)
}

// releaseRequestLog drops an invocation's tail buffer once it has answered.
func (ci *containerInstance) releaseRequestLog(reqID string) {
	if ci.logSink != nil {
		ci.logSink.release(reqID)
	}
}

func extensionInvokeError(reason string) *InvokeResult {
	payload, _ := json.Marshal(map[string]string{
		"errorMessage": reason,
		"errorType":    "Runtime.ExtensionError",
	})
	return &InvokeResult{StatusCode: 200, Payload: payload, FunctionError: "Unhandled"}
}

// reportMemoryWaitMax bounds how long a REPORT line waits for the in-flight
// memory sample before falling back to the peak recorded so far.
const reportMemoryWaitMax = 50 * time.Millisecond

// startMemorySample kicks off an asynchronous memory read that folds into the
// instance's running peak. AWS's Max Memory Used is the execution
// environment's monotonic peak across warm reuses, so REPORT reads the peak,
// never a lone point sample. Started at invocation submit so the stats round
// trip overlaps handler execution instead of sitting on the response path.
// The returned channel closes once the sample has been folded in.
func (ci *containerInstance) startMemorySample() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := int64(ci.currentMemoryMB())
		if m <= 0 {
			return // best-effort: a failed sample never lowers the peak
		}
		for {
			cur := ci.peakMemMB.Load()
			if m <= cur || ci.peakMemMB.CompareAndSwap(cur, m) {
				return
			}
		}
	}()
	return done
}

// reportMemoryMB waits briefly for the in-flight sample and returns the peak
// memory recorded for this environment. The wait is capped so writing the
// REPORT line never stalls the invoke response on a slow stats endpoint
// (pre-one-shot this was 1–2 s, and Docker-in-Docker hosts can still take
// hundreds of ms); an unresolved sample reports the previous peak, and the
// still-running goroutine folds its result in for the next invocation.
func (ci *containerInstance) reportMemoryMB(sample <-chan struct{}) int {
	if sample != nil {
		timer := ci.clk.Timer(reportMemoryWaitMax)
		defer timer.Stop()
		select {
		case <-sample:
		case <-timer.C:
		}
	}
	return int(ci.peakMemMB.Load())
}

// currentMemoryMB queries Docker for the container's current memory usage
// and returns it in megabytes. Returns 0 on error (best-effort). Callers on
// the invoke path go through startMemorySample/reportMemoryMB rather than
// calling this synchronously.
func (ci *containerInstance) currentMemoryMB() int {
	// The stats call uses one-shot=true, so it returns the current sample
	// immediately instead of waiting a daemon collection cycle (~1–2 s) —
	// which used to blow this timeout in Docker-in-Docker setups and report 0.
	// The 2 s timeout stays as a bound for genuinely slow daemons.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bytes, err := ci.docker.ContainerMemoryUsage(ctx, ci.id)
	if err != nil {
		ci.logger.Debug("container stats: could not get memory usage (best-effort, non-fatal)", zap.Error(err))
		return 0
	}
	return int(bytes / (1024 * 1024))
}

// takeInitDuration returns how long this environment's initialization took, and
// consumes the right to report it: only the first REPORT of an on-demand cold
// start carries Init Duration, exactly as on AWS. Warm invokes and proactively
// initialised (provisioned) environments report nothing, and neither does an
// environment whose RIC never polled (init failure) — the duration runs from
// container start to the RIC's first GET /next, the same signal that drives
// readiness and the INIT-burst throttle.
func (ci *containerInstance) takeInitDuration() (time.Duration, bool) {
	if ci.initReported || ci.initStartedAt.IsZero() {
		return 0, false
	}
	firstNext, ok := ci.runtimeAPI.FirstNextAt(ci.containerIP)
	if !ok {
		return 0, false
	}
	ci.initReported = true
	initDur := firstNext.Sub(ci.initStartedAt)
	if initDur < 0 {
		initDur = 0
	}
	return initDur, true
}

// durationMillis renders a duration in the fractional milliseconds Lambda uses
// for every duration it reports.
func durationMillis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// writeLogLine emits one synthesised log line that belongs to no invocation —
// an Overcast warning about the environment itself, such as a layer that could
// not be loaded. It lands in the INIT-phase buffer, which is where the
// container's own pre-invocation output is.
func (ci *containerInstance) writeLogLine(_ context.Context, line string) {
	ci.publishRuntimeLog("platform", line)
	ci.deliverSynthLine("", line)
}

// publishRuntimeLog hands a record to Telemetry/Logs API subscribers. It is
// deliberately separate from delivery: subscribers always see the complete set
// of records, whatever SystemLogLevel filters out of CloudWatch.
func (ci *containerInstance) publishRuntimeLog(logType, line string) {
	if ci.containerIP != "" {
		ci.runtimeAPI.PublishExtensionLog(ci.containerIP, logType, line)
	}
}

// publishPlatformRecord hands one marshalled Telemetry API platform event to
// subscribers. Same separation as publishRuntimeLog, and for the same reason:
// what a subscriber sees is the complete set of records in the shape AWS
// documents, whatever the log format and the system log level do to the
// CloudWatch copy.
func (ci *containerInstance) publishPlatformRecord(event string) {
	if ci.containerIP != "" {
		ci.runtimeAPI.PublishPlatformRecord(ci.containerIP, event)
	}
}

// hasPlatformSubscribers reports whether marshalling a platform record for the
// Telemetry/Logs API would reach anybody.
func (ci *containerInstance) hasPlatformSubscribers() bool {
	return ci.containerIP != "" && ci.runtimeAPI.hasTelemetrySubscribers(ci.containerIP, "platform")
}

// deliverSynthLine writes a line the execution environment produced — START,
// END, REPORT — into requestID's tail buffer and to CloudWatch Logs.
//
// These lines never come off the container at all, so they are written rather
// than batched: END and REPORT are the two lines an invocation most needs on
// the record, and the invoke path has already established that everything
// before them has arrived. The sink flushes whatever the ingest has batched
// first, which is what makes the CloudWatch order exact.
func (ci *containerInstance) deliverSynthLine(requestID, line string) {
	if ci.logSink == nil {
		return
	}
	ci.logSink.synth(requestID, line)
}

// billedDuration rounds elapsed time up to the nearest millisecond, matching
// how AWS reports billed duration in REPORT lines.
func billedDuration(d time.Duration) int64 {
	ms := d.Milliseconds()
	if d > time.Duration(ms)*time.Millisecond {
		ms++
	}
	return ms
}

// Healthy reports whether this container is safe to reuse.
func (ci *containerInstance) Healthy() bool { return ci.healthy }

// DebugTarget is the debugger target this environment was created for, nil
// for an undebugged one. Implements debugTargetInstance.
func (ci *containerInstance) DebugTarget() *debugger.Target { return ci.debug }

// Close drains logs, deregisters from the Runtime API, stops the container
// immediately (to halt any code running inside), and schedules deferred removal
// via the GC with exponential backoff.
func (ci *containerInstance) Close() error {
	ci.logger.Debug("stopping lambda container", zap.String("container", ci.id[:12]))
	if ci.forgetInitBurst != nil {
		ci.forgetInitBurst()
	}
	// The port outlives the container — that is what lets an editor reconnect
	// to the replacement hot reload brings up — but nothing may be dialled
	// behind it once this container is gone. Keyed by id so a container
	// retired after its replacement was bound leaves the replacement alone.
	if ci.debug != nil {
		ci.debug.ClearContainer(ci.id)
	}
	if ci.containerIP != "" {
		if queued := ci.runtimeAPI.EnqueueExtensionShutdown(ci.containerIP, "SPINDOWN", ci.clk.Now().Add(2*time.Second)); queued > 0 {
			select {
			case <-ci.clk.After(100 * time.Millisecond):
			case <-ci.logCtx.Done():
			}
		}
	}
	// Flush whatever the ingest has batched and end the sink's context. The
	// init's own connection ends when the container does; nothing here has to
	// wait for it, because a line that arrives afterwards is still written on a
	// fresh region-carrying context by the batcher.
	if ci.logSink != nil {
		ci.logSink.close()
	}
	if ci.containerIP != "" {
		ci.runtimeAPI.UnregisterContainer(ci.containerIP)
	}
	// The environment's Runtime API endpoint goes with the environment: the
	// port is per-execution-environment, and leaving it bound would accumulate
	// one dead listener per container the pool retires.
	_ = ci.rapiListener.Close()
	// Stop immediately + queue deferred removal via GC.
	if ci.gc != nil && !ci.keepContainers {
		ci.gc.StopAndScheduleRemove(ci.id)
	} else if ci.keepContainers {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return ci.docker.StopContainer(ctx, ci.id, 5)
	}
	return nil
}

func shortContainerID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// lambdaLogStreamName generates an AWS-style log stream name. AWS names the
// stream after the date and the execution environment's GUID, so the suffix is
// 32 hex characters — 2019/07/12/[$LATEST]312c2d81e2e64af58dbe557754f9aa13 —
// and a shorter one fails anything matching stream names against that shape.
// Format: YYYY/MM/DD/[$LATEST]<32-char lowercase hex>.
func lambdaLogStreamName(clk clock.Clock) string {
	date := clk.Now().UTC().Format("2006/01/02")
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d", clk.Now().UnixNano())))
	return date + "/[$LATEST]" + hex.EncodeToString(hash[:16])
}

// cpuAllocation calculates the fractional CPU count for a Lambda function based
// on its memory setting. AWS allocates CPU proportional to memory:
// 1769 MB = 1 vCPU, linear scaling. This is the steady-state allocation used
// after the INIT phase completes.
func cpuAllocation(memoryMB int) float64 {
	cpus := float64(memoryMB) / 1769.0
	if cpus < 0.0625 {
		cpus = 0.0625 // minimum: 1/16 vCPU
	}
	return cpus
}

// initBurstCPUs is the CPU allocation given to Lambda containers during the
// INIT phase (runtime bootstrap, SDK loading, global-scope execution). Real
// AWS gives functions burst CPU during INIT regardless of memory configuration;
// we emulate this by starting containers with generous CPU and throttling down
// to the proportional allocation once the RIC issues its first GET /next.
const initBurstCPUs = 2.0

// initBurstEntry tracks a container awaiting INIT-burst throttle-down.
type initBurstEntry struct {
	containerID string
	memorySize  int // configured memory — used to compute steady-state CPU
}

// registerInitBurst records that a container was started with burst CPU and
// should be throttled to the proportional allocation after INIT completes.
//
// Keyed by container, because a function routinely has more than one
// environment in INIT at the same time — two event source mappings on one
// stream deliver to the same function at the same instant, and each concurrent
// invocation cold starts its own. Keying this by function ARN meant the second
// registration displaced the first: the environment that finished INIT first
// threw the throttle at whichever container had registered last, cutting a
// container still in INIT from 2 CPUs to a fraction of one (#1336).
func (cr *ContainerRuntime) registerInitBurst(containerID string, memoryMB int) {
	// Skip registration if the function already gets burst-level CPU or more
	// from its memory allocation — throttling would be a no-op.
	if cpuAllocation(memoryMB) >= initBurstCPUs {
		return
	}
	cr.initBurstMu.Lock()
	defer cr.initBurstMu.Unlock()
	if cr.initBurst == nil {
		cr.initBurst = make(map[string]initBurstEntry)
	}
	cr.initBurst[containerID] = initBurstEntry{
		containerID: containerID,
		memorySize:  memoryMB,
	}
}

// clearInitBurst removes a pending init-burst entry without throttling.
// Called on error paths when the container is being torn down.
func (cr *ContainerRuntime) clearInitBurst(containerID string) {
	cr.initBurstMu.Lock()
	delete(cr.initBurst, containerID)
	cr.initBurstMu.Unlock()
}

// ThrottleInitBurst reduces a container's CPU allocation from the INIT burst
// level to the steady-state proportional allocation. Called when that
// container's RIC issues its first GET /next, signalling that its INIT phase is
// complete. Safe to call for a container with no pending burst entry (no-op).
func (cr *ContainerRuntime) ThrottleInitBurst(containerID string) {
	cr.initBurstMu.Lock()
	entry, ok := cr.initBurst[containerID]
	if ok {
		delete(cr.initBurst, containerID)
	}
	cr.initBurstMu.Unlock()

	if !ok {
		return
	}

	steadyCPUs := cpuAllocation(entry.memorySize)
	steadyNano := int64(steadyCPUs * 1e9)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := cr.docker.UpdateContainerResources(ctx, entry.containerID, &docker.UpdateResourcesRequest{
		NanoCPUs: steadyNano,
	})
	if err != nil {
		cr.logger.Warn("failed to throttle INIT burst CPU — container keeps burst allocation",
			zap.String("container", entry.containerID[:12]),
			zap.Float64("burst_cpus", initBurstCPUs),
			zap.Float64("steady_cpus", steadyCPUs),
			zap.Error(err))
		return
	}
	cr.logger.Info("throttled INIT burst CPU to steady state",
		zap.String("container", entry.containerID[:12]),
		zap.Float64("steady_cpus", steadyCPUs))
}

// sanitizeName makes a function name safe for use in Docker container names.
func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
}

// resolveLayerTars fetches (or serves from cache) each attached layer as an
// opt/-prefixed tar for the single provisioning archive, in layer order —
// later layers overwrite earlier ones at extraction, matching AWS layer
// merge semantics. No Docker calls happen here; assembly is pure CPU.
// Returns the ordered tars, discovered external extension names, and one
// synthesized warning log line per layer that had to be skipped.
func (cr *ContainerRuntime) resolveLayerTars(ctx context.Context, fn *Function) ([][]byte, []string, []string, error) {
	if len(fn.Layers) == 0 {
		return nil, nil, nil, nil
	}
	if cr.layerFetcher == nil {
		return nil, nil, nil, fmt.Errorf("layer content fetcher is not configured")
	}
	tars := make([][]byte, 0, len(fn.Layers))
	extensionSet := make(map[string]bool)
	skippedLayerLogLines := make([]string, 0)
	for _, layer := range fn.Layers {
		arn := strings.TrimSpace(layer.ARN)
		if arn == "" {
			continue
		}
		// Layer versions are immutable, so ARN-keyed tars (and the extension
		// scan) are cached across cold starts and functions.
		if cached, ok := cr.tarCache.get("layer:" + arn); ok {
			tars = append(tars, cached.data)
			for _, name := range cached.extensions {
				extensionSet[name] = true
			}
			continue
		}
		zipData, err := cr.layerFetcher(ctx, arn)
		if err != nil {
			// Try remote fetch if enabled.
			if cr.remoteFetcher != nil {
				remoteData, remoteErr := cr.remoteFetcher.FetchLayer(ctx, arn)
				if remoteErr != nil {
					cr.logger.Warn("layer not available locally or remotely — skipping",
						zap.String("arn", arn), zap.Error(err), zap.NamedError("remote_error", remoteErr))
					skippedLayerLogLines = append(skippedLayerLogLines, skippedLayerWarningLogLine(arn, remoteErr))
					continue
				}
				zipData = remoteData
			} else {
				cr.logger.Warn("layer not available locally — skipping",
					zap.String("arn", arn), zap.Error(err))
				skippedLayerLogLines = append(skippedLayerLogLines, skippedLayerWarningLogLine(arn, err))
				continue
			}
		}
		layerTar, err := zipToTarPrefixed(zipData, "opt/")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build tar for layer %s: %w", arn, err)
		}
		tars = append(tars, layerTar)
		discovered := discoverExternalExtensions(zipData)
		for _, name := range discovered {
			extensionSet[name] = true
		}
		cr.tarCache.put(&tarCacheEntry{key: "layer:" + arn, data: layerTar, extensions: discovered})
	}
	extensions := make([]string, 0, len(extensionSet))
	for name := range extensionSet {
		extensions = append(extensions, name)
	}
	slices.Sort(extensions)
	if len(extensions) == 0 {
		extensions = nil
	}
	return tars, extensions, skippedLayerLogLines, nil
}

func skippedLayerWarningLogLine(layerVersionARN string, cause error) string {
	name := layerVersionARN
	version := ""
	if parsed, err := parseLayerARN(layerVersionARN); err == nil {
		name = parsed.LayerName
		version = parsed.Version
	}
	if version != "" {
		return fmt.Sprintf("[Overcast Lambda] WARNING: Lambda layer %s:%s (%s) was not loaded because it is not available locally and remote AWS layer fetching is not configured or failed: %v", name, version, layerVersionARN, cause)
	}
	return fmt.Sprintf("[Overcast Lambda] WARNING: Lambda layer %s was not loaded because it is not available locally and remote AWS layer fetching is not configured or failed: %v", layerVersionARN, cause)
}

func discoverExternalExtensions(zipData []byte) []string {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil
	}
	extensions := make([]string, 0)
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if strings.Count(name, "/") != 1 || !strings.HasPrefix(name, "extensions/") || strings.HasSuffix(name, "/") {
			continue
		}
		if f.FileInfo().Mode().Perm()&0o111 == 0 {
			continue
		}
		extensions = append(extensions, strings.TrimPrefix(name, "extensions/"))
	}
	slices.Sort(extensions)
	return extensions
}

// lambdaEntrypointPath is the entrypoint every AWS Lambda base image ships:
// the shell shim that execs the runtime interface client with the handler as
// its argument. It is what a zip function's container ran before the init
// existed, and therefore what the init runs as its child.
const lambdaEntrypointPath = "/lambda-entrypoint.sh"

// initBinaryResult caches one architecture's init bytes. embed.FS.ReadFile
// copies, and the artefact is ~6.5 MB, so without this every cold start would
// allocate one.
type initBinaryResult struct {
	data []byte
	err  error
}

var initBinaries sync.Map // Architectures value → initBinaryResult

// lambdaInitBinary returns the in-container init built for a function's
// architecture. The error is initbin's, which names the missing artefact and
// the command that produces it.
func lambdaInitBinary(arch string) ([]byte, error) {
	if cached, ok := initBinaries.Load(arch); ok {
		res := cached.(initBinaryResult)
		return res.data, res.err
	}
	data, err := initbin.For(arch)
	initBinaries.Store(arch, initBinaryResult{data: data, err: err})
	return data, err
}

// withInitVolume adds the read-only mount that puts the init at
// initproto.InitPath, when a volume is what is delivering it.
func withInitVolume(mounts []docker.Mount, volume string) []docker.Mount {
	if volume == "" {
		return mounts
	}
	return append(mounts, docker.Mount{
		Type:     "volume",
		Source:   volume,
		Target:   initVolumeTarget,
		ReadOnly: true,
	})
}

// appendLambdaInit writes the init into the provisioning archive at
// initproto.InitPath. It is the fallback for a daemon that will not give us a
// volume to mount the init from — see init_volume.go.
//
// The directory header is emitted because /var/overcast is Overcast's own and
// exists in no base image — unlike /var, /var/task and /opt, whose headers are
// deliberately omitted so extraction cannot reset the modes the image set.
func appendLambdaInit(tw *tar.Writer, architectures []string) error {
	arch := ""
	if len(architectures) > 0 {
		arch = architectures[0]
	}
	binary, err := lambdaInitBinary(arch)
	if err != nil {
		return err
	}
	dir, name, ok := strings.Cut(strings.TrimPrefix(initproto.InitPath, "/"), "/")
	if !ok {
		return fmt.Errorf("lambda init path %q must name a file under a directory", initproto.InitPath)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     dir + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: dir + "/" + name,
		Size: int64(len(binary)),
		Mode: 0o755,
	}); err != nil {
		return err
	}
	_, err = tw.Write(binary)
	return err
}

// childCommand is the command the container would have run if the init were not
// there — the argument list the init execs as its child, exactly as RAPID
// launches ENTRYPOINT+CMD on AWS.
//
// Zip functions are simple: the base image's shim with the handler after it.
// Image functions are where the daemon used to do the work, merging the image's
// own ENTRYPOINT and CMD into a create request that left them empty. The create
// request now carries the init instead, so that merge happens here, and it
// follows Docker's own rule so an image function runs exactly the process it
// ran before: an ImageConfig entrypoint replaces the image's and drops the
// image's CMD with it, while an ImageConfig command on its own replaces only
// the CMD.
func (cr *ContainerRuntime) childCommand(ctx context.Context, fn *Function, isImage bool, resolvedRef, platform string) ([]string, error) {
	if !isImage {
		child := []string{lambdaEntrypointPath}
		if fn.Handler != "" {
			child = append(child, fn.Handler)
		}
		return child, nil
	}

	var entrypoint, command []string
	if ic := fn.ImageConfig; ic != nil && len(ic.EntryPoint) > 0 {
		entrypoint, command = ic.EntryPoint, ic.Command
	} else {
		imageCfg, err := cr.imageConfig(ctx, resolvedRef, platform)
		if err != nil {
			return nil, fmt.Errorf("read the entrypoint of image %s: %w", fn.ImageUri, err)
		}
		entrypoint, command = imageCfg.Entrypoint, imageCfg.Cmd
		if ic != nil && len(ic.Command) > 0 {
			command = ic.Command
		}
	}

	child := append(append([]string{}, entrypoint...), command...)
	if len(child) == 0 {
		return nil, fmt.Errorf("image %s declares neither ENTRYPOINT nor CMD and the function's ImageConfig sets neither, so there is nothing for the execution environment to run", fn.ImageUri)
	}
	return child, nil
}

// imageConfig reads an image's baked-in run configuration, cached for the life
// of the process beside the verification that put the image there: one daemon
// round trip per image and platform, not one per cold start.
func (cr *ContainerRuntime) imageConfig(ctx context.Context, resolvedRef, platform string) (docker.ImageConfig, error) {
	key := imagePullKey(resolvedRef, platform)
	if cached, ok := cr.imageConfigs.Load(key); ok {
		return cached.(docker.ImageConfig), nil
	}
	inspect, err := cr.docker.InspectImage(ctx, resolvedRef)
	if err != nil {
		return docker.ImageConfig{}, err
	}
	cr.imageConfigs.Store(key, inspect.Config)
	return inspect.Config, nil
}

// appendTarEntries re-writes every entry of a component tar into tw, so
// independently built (and cached) component archives can be merged into the
// single provisioning archive without re-reading their sources.
func appendTarEntries(tw *tar.Writer, component []byte) error {
	tr := tar.NewReader(bytes.NewReader(component))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

// zipToTar converts a zip archive (raw bytes) into a tar archive suitable for
// the Docker "Put Archive" API. This avoids extracting to a local temp
// directory, which breaks when running inside a devcontainer (the Docker daemon
// runs on the host and cannot see the devcontainer's filesystem).
func zipToTar(zipData []byte) ([]byte, error) {
	return zipToTarPrefixed(zipData, "")
}

// zipToTarPrefixed converts a zip archive to a tar archive whose entry names
// carry prefix (e.g. "var/task/"), so the result can be extracted at the
// container root as part of the single provisioning archive. The prefix root
// itself is never emitted as a directory header — the base image owns those
// directories, and a header would reset their modes on extraction.
func zipToTarPrefixed(zipData []byte, prefix string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, f := range zr.File {
		// Zip tools sometimes emit "./"-relative names; normalize before
		// prefixing so "./index.js" becomes "var/task/index.js", not
		// "var/task/./index.js".
		name := prefix + strings.TrimPrefix(f.Name, "./")
		if name == prefix || name == "" {
			continue // a bare "./" entry would emit the prefix root itself
		}
		mode := int64(f.FileInfo().Mode().Perm())
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:    name,
			Size:    int64(f.UncompressedSize64),
			Mode:    mode,
			ModTime: f.Modified,
		}

		if f.FileInfo().IsDir() {
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
			if err := tw.WriteHeader(header); err != nil {
				return nil, fmt.Errorf("tar header %q: %w", f.Name, err)
			}
			continue
		}

		header.Typeflag = tar.TypeReg
		if err := tw.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("tar header %q: %w", f.Name, err)
		}

		src, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %q in zip: %w", f.Name, err)
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return nil, fmt.Errorf("write %q to tar: %w", f.Name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}

	return buf.Bytes(), nil
}
