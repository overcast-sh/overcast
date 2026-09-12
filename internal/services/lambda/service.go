// Package lambda is a stub — handlers are implemented test-first.
//
// Architecture uses the Strategy pattern for runtime execution:
//
//	Runtime interface ← NodeRuntime (v1) | PythonRuntime (future) | GoRuntime (future)
//
// The Lambda handler never knows which runtime it's talking to. Adding a new
// runtime means implementing the Runtime interface and registering it in the
// RuntimeRegistry — nothing else changes.
//
// Implementation order (TDD):
//  1. CreateFunction / GetFunction / ListFunctions / DeleteFunction / UpdateFunctionCode
//  2. Invoke (synchronous) — stub response mode
//  3. Invoke (synchronous) — real Node.js execution via NodeRuntime
//  4. InvokeAsync
//  5. Event source mapping (SQS→Lambda)
package lambda

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/debugger"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/eventtarget"
	"github.com/overcast-sh/overcast/internal/listenstatus"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Runtime is the Strategy interface for Lambda execution environments.
// It follows a two-level lifecycle: the Runtime manages the pool of warm
// instances; a RuntimeInstance executes a single invocation.
//
// Sequence:
//
//	inst, err := runtime.Acquire(ctx, fn)        // get or start a warm container
//	result, err := inst.Invoke(ctx, event, opts) // run the handler
//	runtime.Release(ctx, inst, err == nil)       // return or discard the instance
type Runtime interface {
	// CanHandle returns true if this runtime can execute functions with the
	// given runtime identifier (e.g. "nodejs20.x", "nodejs22.x").
	CanHandle(runtimeID string) bool

	// Acquire returns a warm RuntimeInstance ready to serve one invocation.
	// It may start a new container if no warm instance is available.
	Acquire(ctx context.Context, fn *Function) (RuntimeInstance, error)

	// Release returns the instance to the pool (healthy=true) or destroys it
	// (healthy=false, e.g. after a crash or timeout).
	Release(ctx context.Context, inst RuntimeInstance, healthy bool)
}

// RuntimeInstance represents a single warm Lambda container (or process) that
// can execute exactly one invocation at a time.
type RuntimeInstance interface {
	// Invoke sends the event payload to the function handler and returns the
	// result. The instance is exclusive to the caller for the duration.
	Invoke(ctx context.Context, event []byte, opts InvokeOptions) (*InvokeResult, error)

	// LogStreamName returns the CloudWatch Logs stream name for this container
	// instance. The name is assigned when the instance starts and remains fixed
	// for its lifetime. Format: YYYY/MM/DD/[$LATEST]<32-char hex>
	LogStreamName() string

	// Healthy reports whether the instance is usable after the last invocation.
	Healthy() bool

	// FunctionName returns the name of the Lambda function this instance runs.
	// Used by InstancePool.Release to key the pool without requiring *Function.
	FunctionName() string

	// ConfigIdentity returns the fingerprint of the code and configuration this
	// instance was built from (see functionInstanceIdentity). Used by
	// InstancePool to detect instances made stale by a code or configuration
	// update.
	ConfigIdentity() string

	// ContainerID returns the Docker container backing this instance, or "" for
	// runtimes that are not container-backed. Used by InstancePool to drop a
	// pooled instance when the Docker watcher reports its container died.
	ContainerID() string

	// InstanceID returns a stable identifier for this execution environment,
	// assigned when it is created and preserved across warm reuse. It is what
	// the instance tracker keys its records by, so a function serving several
	// concurrent invocations reports one instance per environment.
	InstanceID() string

	// Close shuts down and removes the underlying container or process.
	Close() error
}

// InvokeOptions carries per-invocation switches that change what the execution
// environment does around the handler call, as opposed to configuration that
// belongs to the function itself.
type InvokeOptions struct {
	// LogTail asks for InvokeResult.LogResult to be populated. It costs invoke
	// latency: the container's stdout reaches the tail buffer through Docker's
	// log stream, which lands after the handler's response comes back over the
	// Runtime API, so producing a complete tail means waiting for it (see
	// containerInstance.waitForScannerIdle).
	//
	// Only two callers want it — InvokeFunction with X-Amz-Log-Type: Tail, and
	// the console's SSE invoke, which always renders the tail. Asynchronous
	// invokes, event-source mappings, function URLs and service-to-service
	// calls all discard LogResult, so they leave this false and pay nothing.
	LogTail bool
}

// InvokeResult holds the outcome of a Lambda invocation.
type InvokeResult struct {
	// StatusCode is the HTTP status code returned by the function handler.
	StatusCode int
	// Payload is the raw JSON response body.
	Payload []byte
	// FunctionError is non-empty if the function returned an error response
	// (i.e. X-Amz-Function-Error: Handled or Unhandled).
	FunctionError string
	// LogResult contains base64-encoded tail log output (last 4KB).
	LogResult string
	// LogGroupName is the CloudWatch log group for this function.
	LogGroupName string
	// LogStreamName is the specific log stream produced by this invocation.
	LogStreamName string
	// RequestID is the invocation's Lambda request ID — the one the runtime
	// handed the handler, and the one that appears in the function's own logs.
	// Empty for a runtime that never reached the Runtime API (the Docker-less
	// stub), and for a failure raised before the invocation was submitted.
	RequestID string

	// acquireFailed is true when rt.Acquire failed (Docker infrastructure
	// issue: image pull, container create/start, IP assignment). Used by
	// invokeSync to retry once. Never set for errors from inside a running
	// container (timeouts, init errors, handler crashes).
	acquireFailed bool

	// throttle is set when a concurrency limit refused the invocation. The
	// handler turns it into AWS's 429 TooManyRequestsException rather than a
	// 200 with a function error.
	throttle *throttleError

	// Duration is how long inst.Invoke's runtime round trip took — execution
	// time after function code begins, excluding cold-start/init (plan:
	// "Duration needs explicit start/end measurements in the execution
	// layer"). Zero when the function never ran (acquire/init failure,
	// throttle). Used only by recordInvocationOutcome; not part of any wire
	// response.
	Duration time.Duration
}

// LayerVersionLink is a reference to a specific layer version attached to a function.
// The struct mirrors the AWS FunctionConfiguration.Layers shape so it serialises
// directly into wire responses without a conversion step.
type LayerVersionLink struct {
	ARN                      string `json:"Arn"`
	CodeSize                 int64  `json:"CodeSize"`
	SigningProfileVersionARN string `json:"SigningProfileVersionArn,omitempty"`
	SigningJobARN            string `json:"SigningJobArn,omitempty"`
}

func (f *Function) GetTags() map[string]string  { return f.Tags }
func (f *Function) SetTags(t map[string]string) { f.Tags = t }

// Function is the domain model for a stored Lambda function definition.
type Function struct {
	Name        string            `json:"name"`
	ARN         string            `json:"arn"`
	Runtime     string            `json:"runtime"`
	Handler     string            `json:"handler"`
	Role        string            `json:"role"`
	Description string            `json:"description,omitempty"`
	Timeout     int               `json:"timeout"`
	MemorySize  int               `json:"memory_size"`
	Environment map[string]string `json:"environment,omitempty"`
	// CodeZip is the deployment package. Only two writers are legitimate:
	// setCode when the package *changes* (it keeps CodeSize and CodeHash in
	// step — identity checks trust CodeHash, so a bare assignment would leave
	// warm containers serving stale code), and lambdaStore.loadFunctionCode
	// when materializing the already-stored package. The package is persisted
	// under its own store key, so records read on the invoke path carry no
	// bytes here — call loadFunctionCode before paths that need them.
	CodeZip  []byte `json:"code_zip,omitempty"` // base64-decoded zip
	CodeSize int64  `json:"code_size,omitempty"`
	// CodeHash is the hex SHA-256 of CodeZip, maintained by setCode. It is what
	// functionCodeIdentity and CodeSha256 responses read, so the invoke path
	// never rehashes the package. "" only on records persisted before the field
	// existed, where readers fall back to hashing CodeZip.
	CodeHash string `json:"code_hash,omitempty"`
	// CodeGeneration changes only for explicit code-source writes. Reactive
	// S3 refreshes preserve it so concurrent object events can be ordered while
	// every manual/inline update—including one with identical bytes—fences out
	// fetches that started against the previous generation.
	CodeGeneration string `json:"code_generation,omitempty"`
	CodeS3Bucket   string `json:"code_s3_bucket,omitempty"`
	CodeS3Key      string `json:"code_s3_key,omitempty"`
	// CodeS3ObjectVersion pins the deployment package to one immutable version
	// of CodeS3Key. Empty means "whatever is current", which is also what makes
	// the function eligible for reactive S3 code sync — a pinned function must
	// not be refreshed when a new version lands at its key.
	CodeS3ObjectVersion string   `json:"code_s3_object_version,omitempty"`
	ImageUri            string   `json:"image_uri,omitempty"` // PackageType=Image only
	PackageType         string   `json:"package_type,omitempty"`
	Architectures       []string `json:"architectures,omitempty"`
	State               string   `json:"state"` // "Active", "Pending", "Inactive", "Failed"
	StateReason         string   `json:"state_reason,omitempty"`
	StateReasonCode     string   `json:"state_reason_code,omitempty"` // e.g. "Creating", "Idle", "ImagePullError"
	// LastUpdateStatus is the outcome of the most recent update to the
	// function — "InProgress", "Successful" or "Failed" — and is what AWS's
	// FunctionUpdated waiter polls. Empty only while creation is still
	// running: AWS first sets it to Successful once the create completes. The
	// update-lifecycle block in handler_functions.go says which updates pass
	// through InProgress and which are already done when they answer.
	LastUpdateStatus string `json:"last_update_status,omitempty"`
	// LastUpdateStatusReason and LastUpdateStatusReasonCode explain a
	// LastUpdateStatus that is not Successful, the way StateReason and
	// StateReasonCode explain a State that is not Active. The code is one of
	// AWS's own — ImageAccessDenied, InvalidImage, InternalError.
	LastUpdateStatusReason     string `json:"last_update_status_reason,omitempty"`
	LastUpdateStatusReasonCode string `json:"last_update_status_reason_code,omitempty"`

	RevisionId          string             `json:"revision_id,omitempty"`
	CreationID          string             `json:"creation_id,omitempty"`
	LastModified        string             `json:"last_modified,omitempty"`
	LogGroup            string             `json:"log_group,omitempty"` // Custom log group; defaults to /aws/lambda/{name}
	LogFormat           string             `json:"log_format,omitempty"`
	ApplicationLogLevel string             `json:"application_log_level,omitempty"`
	SystemLogLevel      string             `json:"system_log_level,omitempty"`
	Layers              []LayerVersionLink `json:"layers,omitempty"` // Attached layer versions (empty until layers are implemented)
	// CodeSigningConfigArn is the code signing configuration associated with
	// the function, or "" when there is none — the usual case, since code
	// signing is opt-in. Stored and echoed back so SDKs and CDK read the
	// association they set; signature validation itself is deliberately not
	// emulated (Overcast is not a security boundary).
	CodeSigningConfigArn string `json:"code_signing_config_arn,omitempty"`
	// SourceCode and SourceFilename are emulator-internal: they hold the raw
	// handler source text authored in the web UI. Not exposed in AWS wire responses.
	SourceCode     string `json:"source_code,omitempty"`
	SourceFilename string `json:"source_filename,omitempty"`
	// VpcConfig optionally associates the function with an EC2 VPC. When set,
	// the Lambda container is connected to the VPC's Docker network in addition
	// to the default Lambda network, so the function can communicate with other
	// resources in the VPC.
	VpcConfig *VpcConfig `json:"vpc_config,omitempty"`
	// ImageConfig overrides the container image's EntryPoint, Command, and
	// WorkingDirectory. Only applicable when PackageType=Image.
	ImageConfig *ImageConfig `json:"image_config,omitempty"`
	// FileSystemConfigs are the EFS access points mounted into the function.
	// Stored and echoed on the wire in every mode; when EFS live mode is
	// active, the container runtime binds each backing volume at
	// LocalMountPath so invocations share real file data.
	FileSystemConfigs []FileSystemConfig `json:"file_system_configs,omitempty"`
	Tags              map[string]string  `json:"tags,omitempty"`
	// ReservedConcurrency is the reserved concurrency limit. nil = unreserved,
	// 0 = throttled (no executions).
	ReservedConcurrency *int `json:"reserved_concurrency,omitempty"`
	// TracingMode is the X-Ray tracing mode ("Active" or "PassThrough"), or ""
	// for a function that never set one. There is no X-Ray service in Overcast,
	// so the mode is recorded and echoed and changes nothing about execution —
	// accepting it claims nothing a caller can observe as false, and rejecting
	// it broke every CDK deploy with tracing enabled.
	TracingMode string `json:"tracing_mode,omitempty"`
	// EphemeralStorageSize is the configured /tmp size in MB, or 0 for a
	// function that never set one (AWS's default is 512). Recorded and echoed;
	// the container's /tmp is not actually capped to it.
	EphemeralStorageSize int `json:"ephemeral_storage_size,omitempty"`
	// KMSKeyArn is the customer managed key recorded for the function.
	// Environment variables are stored in plaintext regardless — Overcast is
	// not a security boundary — so this is association metadata only.
	KMSKeyArn string `json:"kms_key_arn,omitempty"`
	// DeadLetterTargetArn is the SQS queue or SNS topic an asynchronous
	// invocation is sent to once it has failed, or "" for a function with no
	// dead-letter queue. See dead_letter.go for what is delivered to it.
	DeadLetterTargetArn string `json:"dead_letter_target_arn,omitempty"`
}

// functionImagePullGeneration identifies the exact deployment generation a
// background image pull prepared. State alone is insufficient: a function can
// remain Pending while its image, platform, or entire creation changes.
type functionImagePullGeneration struct {
	arn        string
	creationID string
	image      string
	platform   string
}

func imagePullGenerationForFunction(fn *Function) (functionImagePullGeneration, error) {
	image, err := imageForFunction(fn)
	if err != nil {
		return functionImagePullGeneration{}, err
	}
	return functionImagePullGeneration{
		arn:        fn.ARN,
		creationID: fn.CreationID,
		image:      image,
		platform:   dockerPlatformForLambdaArchitectures(fn.Architectures),
	}, nil
}

func (g functionImagePullGeneration) matches(fn *Function) bool {
	if fn == nil || fn.State != "Pending" || fn.ARN != g.arn || fn.CreationID != g.creationID {
		return false
	}
	current, err := imagePullGenerationForFunction(fn)
	return err == nil && current.image == g.image && current.platform == g.platform
}

// ImageConfig overrides for container image Lambda functions.
type ImageConfig struct {
	EntryPoint       []string `json:"entry_point,omitempty"`
	Command          []string `json:"command,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

// VpcConfig associates a Lambda function with a VPC.
type VpcConfig struct {
	SubnetIds               []string `json:"SubnetIds,omitempty"`
	SecurityGroupIds        []string `json:"SecurityGroupIds,omitempty"`
	Ipv6AllowedForDualStack bool     `json:"Ipv6AllowedForDualStack,omitempty"`
	VpcId                   string   `json:"VpcId,omitempty"`
}

// FileSystemConfig mirrors the AWS FileSystemConfig shape: an EFS access
// point mounted at a /mnt path inside the function's execution environment.
// https://docs.aws.amazon.com/lambda/latest/api/API_FileSystemConfig.html
type FileSystemConfig struct {
	Arn            string `json:"Arn"`
	LocalMountPath string `json:"LocalMountPath"`
}

// setCode replaces the function's deployment package, keeping the derived
// CodeSize and CodeHash fields in step. Every site that changes CodeZip must
// go through here — see the CodeZip field comment for why.
func (f *Function) setCode(zip []byte) {
	f.setCodeBytes(zip)
	f.CodeGeneration = uuid.NewString()
}

func (f *Function) setSyncedCode(zip []byte) {
	f.setCodeBytes(zip)
}

func (f *Function) setCodeBytes(zip []byte) {
	f.CodeZip = zip
	f.CodeSize = int64(len(zip))
	f.CodeHash = codeHashOf(zip)
}

// logGroupName returns the CloudWatch Logs log group for this function.
// Uses the custom LogGroup if set, otherwise the AWS default /aws/lambda/{name}.
func (f *Function) logGroupName() string {
	if f.LogGroup != "" {
		return f.LogGroup
	}
	return "/aws/lambda/" + f.Name
}

// runtimeRegistry holds the active set of Runtimes behind an atomic pointer so
// the Docker initialisation goroutine can upgrade from stub→container runtimes
// after startup without locking any callers.
//
// It also carries the answer to "is that upgrade still coming?". The stub
// runtime replies to an invocation with Runtime.DockerUnavailable, which is the
// truth only once the Docker probe has reported; before that it is a guess, and
// on a machine where Docker is running it is the wrong one. settled is closed
// when the first probe has decided, and runtimeFor waits for it — so an
// invocation that arrives during startup gets the container runtime rather than
// a report that Docker is missing.
type runtimeRegistry struct {
	p atomic.Pointer[[]Runtime]

	settled    chan struct{}
	settleOnce sync.Once
}

// newRuntimeRegistry returns a registry whose runtime set is already final.
func newRuntimeRegistry(initial []Runtime) *runtimeRegistry {
	rr := newPendingRuntimeRegistry(initial)
	rr.settle()
	return rr
}

// newPendingRuntimeRegistry returns a registry whose runtime set is not final
// yet: runtimeFor blocks until settle is called. Only New uses it — every other
// construction site knows its runtimes up front.
func newPendingRuntimeRegistry(initial []Runtime) *runtimeRegistry {
	rr := &runtimeRegistry{settled: make(chan struct{})}
	rr.p.Store(&initial)
	return rr
}

// get returns the current runtime list. The slice must not be modified.
func (rr *runtimeRegistry) get() []Runtime {
	return *rr.p.Load()
}

// set atomically replaces the runtime list.
func (rr *runtimeRegistry) set(runtimes []Runtime) {
	rr.p.Store(&runtimes)
}

// settle marks the runtime set final for the current Docker verdict and
// releases anything waiting in runtimeFor. Idempotent, because
// initDockerRuntime calls it from every exit path.
func (rr *runtimeRegistry) settle() {
	rr.settleOnce.Do(func() { close(rr.settled) })
}

// runtimeFor returns the first registered runtime that can execute runtimeID,
// waiting for the initial Docker verdict before it chooses. Returns nil when
// nothing registered can handle the runtime.
func (rr *runtimeRegistry) runtimeFor(ctx context.Context, runtimeID string) Runtime {
	select {
	case <-rr.settled:
	case <-ctx.Done():
		// The caller has gone, so answer from whatever is registered now
		// rather than inventing a new failure mode for a response that nobody
		// is left to read.
	}
	for _, rt := range rr.get() {
		if rt.CanHandle(runtimeID) {
			return rt
		}
	}
	return nil
}

// poolFor returns the registered InstancePool, waiting for the initial Docker
// verdict the way runtimeFor does. Returns nil when there is no container
// runtime — which, once the verdict is in, means Docker is genuinely absent
// rather than not yet asked about.
//
// The wait is what makes that distinction hold. Callers read a nil pool as
// "nothing can be pre-warmed" and report it to the client as a settled fact:
// provisioned concurrency answers FAILED, which no client is obliged to poll
// again. Answering it from a registry that is still only holding the stub turns
// the emulator's own startup ordering into a verdict about the user's Docker
// installation.
func (rr *runtimeRegistry) poolFor(ctx context.Context) *InstancePool {
	select {
	case <-rr.settled:
	case <-ctx.Done():
		// The caller has gone; answer from whatever is registered now rather
		// than holding a request nobody is left to read.
	}
	for _, rt := range rr.get() {
		if pool, ok := rt.(*InstancePool); ok {
			return pool
		}
	}
	return nil
}

// Service implements router.Service for Lambda.
type Service struct {
	cfg         *config.Config
	clk         clock.Clock
	store       state.Store
	ls          *lambdaStore
	log         *serviceutil.ServiceLogger
	handler     *Handler
	invoker     *ServiceInvoker
	logWriter   events.LogWriter
	tracker     *instanceTracker
	esmDelivery *esmDeliveryManager // nil until InitESMDelivery is called
	initWg      sync.WaitGroup      // signals when the first Docker probe has reported
	gc          *docker.GC          // nil until Docker init completes
	// instances scopes container sweeps to the containers this instance
	// created, so one Overcast's shutdown does not kill another's warm runtime
	// containers mid-invocation. See docker.LabelInstance.
	instances *serviceutil.InstanceDomain
	// debugger is the shared debug-target manager the router owns; handed to
	// the container runtime and the pool once Docker is up. Nil when the
	// service was built without one, which turns the feature off.
	debugger *debugger.Manager
	// stop is closed by Stop; it ends the background re-probe loop that runs
	// when Docker was not available at startup.
	stop     chan struct{}
	stopOnce sync.Once
	// probe is docker.Probe, indirected so tests can drive the re-probe loop
	// without a daemon. dockerRetryInterval is how long that loop waits
	// between attempts.
	probe               dockerProbeFunc
	dockerRetryInterval time.Duration
	// mu protects the fields below, which are written by initDockerRuntime.
	mu               sync.Mutex
	imageResolver    docker.ImageResolver // set by SetImageResolver; read when the container runtime is built
	bus              *events.Bus          // set by InitBus; read by initDockerRuntime
	runtimeAPI       *RuntimeAPIServer    // nil until Docker init completes
	docker           *docker.Client       // nil until Docker init completes
	containerRuntime *ContainerRuntime    // nil until Docker init completes
	pool             *InstancePool        // nil until Docker init completes
	proactive        *proactiveIniter     // nil until Docker init completes
	// triggerSources are other services that can attest a function is wired
	// to traffic (API Gateway integrations, AppSync data sources). Queried
	// only at proactive-init settle time.
	triggerSources []TriggerSource
	// dockerEventsSubscribed guards subscribeDockerEvents, which InitBus and
	// initDockerRuntime both call because either may complete first.
	dockerEventsSubscribed bool
	// metricsRec mirrors handler.metrics under s.mu so initDockerRuntime's
	// background goroutine — which may finish before or after InitMetrics is
	// called, same race as bus/imageResolver above — can tell whether to wire
	// a freshly-created InstancePool's concurrencyObserver.
	metricsRec metricsRecorder
	// runtimeAPIListen is how the shared Runtime API listener's bind went, for
	// /_overcast/health. runtimeAPIListenSet stays false until a bind has been
	// attempted, which needs Docker: without a daemon there is nothing for the
	// listener to serve, and the health endpoint's docker section says so.
	runtimeAPIListen    listenstatus.Status
	runtimeAPIListenSet bool
	// runtimeAPIReach is how the Runtime API address was established — which
	// candidate won, whether a container proved it, and what every candidate
	// did. Kept beside the bind status because the two answer different
	// questions ("did it bind" vs "can anything reach it") and the second is
	// the one an INIT death needs quoted back at it.
	runtimeAPIReach containerendpoint.Listen
}

// WaitReady blocks until the first Docker probe has reported (successfully or
// not). Production callers should never need this; it exists so integration
// tests can ensure the ContainerRuntime is wired before invoking functions.
func (s *Service) WaitReady() { s.initWg.Wait() }

// DescribeUntagged implements debugger.Describer for functions: an existing
// function no tag mentions gets the synthesised "not tagged" entry, with its
// ARN in the setup block so the tagging command is copy-paste ready. A
// function that does not exist, or another service's resource, is not ours
// to describe.
//
// A *tagged* function that has not cold-started yet has no target either —
// registration used to happen only at the first container start — and it
// read "not tagged" here, which told the reader to add the tag they had
// already added and offered no console session until they had invoked once.
// So a tagged function is registered on describe, exactly as its cold start
// would register it (the same Ensure, so the cold start finds the target and
// binds the container to it): the Debug tab then reads unbound, offers the
// console, and a session opened before the first invoke waits for the
// container instead of not existing.
func (s *Service) DescribeUntagged(ctx context.Context, service debugger.Service, resource string) (debugger.Descriptor, bool) {
	if service != debugger.ServiceLambda {
		return debugger.Descriptor{}, false
	}
	fn, aerr := s.ls.getFunction(ctx, resource)
	if aerr != nil || fn == nil {
		return debugger.Descriptor{}, false
	}
	if t := s.registerTaggedDebugTarget(ctx, fn); t != nil {
		return t.Descriptor(), true
	}
	return debugger.UntaggedDescriptor(debugger.ServiceLambda, fn.Name, "", fn.ARN), true
}

// registerTaggedDebugTarget registers fn's debug target ahead of its first
// cold start when a tag asks for one, and returns nil for an untagged
// function or a service with no container runtime (Docker not probed yet).
// The image working directory an image function's remote root wants is not
// known before a pull, so the target carries the zip default until the cold
// start sets the real one.
func (s *Service) registerTaggedDebugTarget(ctx context.Context, fn *Function) *debugger.Target {
	s.mu.Lock()
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr == nil || cr.debugger == nil {
		return nil
	}
	if spec, _ := debugger.SpecFromTags(debugger.ServiceLambda, fn.Tags, cr.cfg.LambdaDebugger); !spec.Tagged {
		return nil
	}
	return cr.debugTarget(ctx, fn, "", "")
}

// RuntimeAPIListenStatus reports how the shared Runtime API listener's bind
// went, for /_overcast/health. ok is false until a bind has been attempted.
func (s *Service) RuntimeAPIListenStatus() (status listenstatus.Status, ok bool) {
	if s == nil {
		return listenstatus.Status{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeAPIListen, s.runtimeAPIListenSet
}

func (s *Service) setRuntimeAPIListen(status listenstatus.Status) {
	s.mu.Lock()
	s.runtimeAPIListen = status
	s.runtimeAPIListenSet = true
	s.mu.Unlock()
}

// InitVolumeProblems returns every Lambda init volume this instance has
// reused that turned out to be labelled for a different Overcast instance, or
// not labelled with an owner at all — see ContainerRuntime.InitVolumeProblems
// and issue #1573. Consumed by the router's debug/advisory endpoint (see
// debugLambdaProvider in internal/router/debug.go), which is why this returns
// docker.VolumeOwnershipProblem — a neutral type outside this package,
// mirroring how ec2.Service.NetworkProblems returns dataplane's type rather
// than one of its own — instead of a lambda-only type the router would have
// to import this package to read.
//
// Empty before Docker has been probed (there is no ContainerRuntime yet to
// have reused anything), same as a genuinely clean instance.
func (s *Service) InitVolumeProblems() []docker.VolumeOwnershipProblem {
	s.mu.Lock()
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr == nil {
		return nil
	}
	return cr.InitVolumeProblems()
}

// RuntimeAPIReachability reports how the Runtime API address was established,
// for the console advisory and for the INIT-death diagnostic. The zero value
// means the Docker probe has not reported yet.
func (s *Service) RuntimeAPIReachability() containerendpoint.Listen {
	if s == nil {
		return containerendpoint.Listen{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeAPIReach
}

func (s *Service) setRuntimeAPIReach(l containerendpoint.Listen) {
	s.mu.Lock()
	s.runtimeAPIReach = l
	s.mu.Unlock()
}

// InitLogWriter wires the CloudWatch Logs writer so Lambda invocations can
// write START/log/END/REPORT lines without importing the logs package.
// Called by the router after all services are constructed.
func (s *Service) InitLogWriter(lw events.LogWriter) {
	s.mu.Lock()
	s.logWriter = lw
	s.mu.Unlock()
	s.handler.logWriter = lw
	s.invoker.logWriter = lw
	s.invoker.cfg = s.cfg
	// Forward to ContainerRuntime so the log-streaming goroutine can write
	// container stdout/stderr (including Powertools JSON lines) to CloudWatch.
	s.mu.Lock()
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr != nil {
		cr.SetLogWriter(lw)
	}
}

// InitBus wires the event bus so Lambda lifecycle events (FunctionCreated,
// FunctionDeleted, FunctionUpdated) are published for topology and UI consumers.
// Called by the router after all services are constructed.
func (s *Service) InitBus(b *events.Bus) {
	s.handler.bus = b
	s.tracker.SetBus(b)
	s.invoker.InitBus(b, s.clk)
	s.mu.Lock()
	s.bus = b
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr != nil {
		cr.SetBus(b)
	}
	// Docker init runs on a background goroutine and may have finished before
	// the bus existed, in which case it could not subscribe. Do it now.
	s.subscribeDockerEvents(b)
}

// InitMetrics wires the shared service-metrics recorder
// (docs/plans/service-metrics-platform.md) so every invocation path records
// AWS/Lambda's Invocations/Errors/Duration/Throttles/ConcurrentExecutions
// through recordInvocationOutcome (metrics_lambda.go). Called once from
// router.New, after metrics.NewRecorder — mirrors InitBus/InitLogWriter's
// "may run before or after Docker init completes" contract.
func (s *Service) InitMetrics(m metricsRecorder) {
	s.handler.metrics = m
	s.mu.Lock()
	s.metricsRec = m
	pool := s.pool
	s.mu.Unlock()
	if pool != nil {
		pool.concurrencyObserver = s.handler.recordConcurrency
	}
}

// reloadProvisionedConcurrency re-applies every stored provisioned concurrency
// reservation to the pool, so allocations survive a restart. Runs in the
// background; a function whose config cannot be read is skipped and logged
// rather than blocking the others.
func (s *Service) reloadProvisionedConcurrency(ctx context.Context, pool *InstancePool) {
	if w, ok := s.store.(state.ReadyAwaiter); ok {
		if err := w.WaitReady(ctx); err != nil {
			s.log.Warn("lambda: provisioned concurrency reload: store not ready", zap.Error(err))
			return
		}
	}
	cfgs, aerr := s.ls.listAllProvisionedConcurrencyConfigs(ctx)
	if aerr != nil {
		s.log.Warn("lambda: provisioned concurrency reload failed", zap.String("error", aerr.Message))
		return
	}
	for _, rc := range cfgs {
		cfg := rc.Config
		if cfg.RequestedProvisionedConcurrentExecutions < 1 {
			continue
		}
		region := rc.Region
		if region == "" {
			region = s.cfg.Region
		}
		fnCtx := middleware.ContextWithRegion(ctx, region)
		fn, aerr := s.ls.getFunction(fnCtx, cfg.FunctionName)
		if aerr != nil || fn == nil {
			continue
		}
		pool.SetProvisionedConcurrency(fn, cfg.RequestedProvisionedConcurrentExecutions)
		s.log.Info("lambda: restored provisioned concurrency",
			zap.String("function", cfg.FunctionName),
			zap.String("region", region),
			zap.Int("provisioned", cfg.RequestedProvisionedConcurrentExecutions))
	}
}

// subscribeDockerEvents wires the Docker watcher's container-died events to the
// two consumers that need them, once the container runtime exists:
//
//   - exitNotifier — fails an in-flight invocation immediately when its
//     container dies, instead of waiting out the function timeout.
//   - InstancePool — drops a warm instance whose container died while idle, so
//     the next invocation cold starts rather than submitting to a container
//     that will never poll for it.
//
// The Docker event watcher itself is owned by the router (started once for all
// services); Lambda only subscribes. Safe to call repeatedly and from either
// InitBus or initDockerRuntime, whichever completes second — it subscribes at
// most once and is a no-op until the container runtime is up.
func (s *Service) subscribeDockerEvents(b *events.Bus) {
	if b == nil {
		return
	}
	s.mu.Lock()
	cr, pool := s.containerRuntime, s.pool
	if cr == nil || pool == nil || s.dockerEventsSubscribed {
		s.mu.Unlock()
		return
	}
	s.dockerEventsSubscribed = true
	s.mu.Unlock()

	b.Subscribe(events.DockerContainerDied, cr.exitNotify.handleContainerDied)
	b.Subscribe(events.DockerContainerDied, pool.handleContainerDied)
}

// InitS3Sync wires S3-reactive code sync. When an S3 object that matches a
// function's CodeS3Bucket/CodeS3Key is uploaded, the function's CodeZip is
// refreshed automatically and the warm instance running the old code is
// retired.
//
// Must be called after InitBus; if the bus has not been set this is a no-op.
func (s *Service) InitS3Sync(fetch S3FetchFunc) {
	if fetch == nil {
		return
	}
	// Make the fetcher available to CreateFunction / UpdateFunctionCode for
	// eager retrieval, in addition to the reactive watcher.
	s.handler.s3Fetch = fetch
	s.mu.Lock()
	bus := s.bus
	s.mu.Unlock()
	if bus == nil {
		return
	}
	w := newS3SyncWatcher(s.ls, fetch, s.log.Logger(), s.clk)
	w.retire = s.handler.retireExecutionEnvironment
	s.handler.setS3SyncWatcher(w)
	w.register(bus)
}

// SetVPCResolver wires the EC2 VPC resolver so Lambda can look up subnet→VPC
// mappings and connect containers to VPC Docker networks.
func (s *Service) SetVPCResolver(r VPCNetworkResolver) {
	s.handler.setVPCResolver(r)
	// If the container runtime is already initialized, wire it there too.
	s.mu.Lock()
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr != nil {
		cr.SetVPCResolver(r)
	}
}

// SetEFSResolver wires the EFS volume resolver so the container runtime can
// bind file-system volumes declared in FileSystemConfigs.
func (s *Service) SetEFSResolver(r EFSVolumeResolver) {
	s.handler.setEFSResolver(r)
	s.mu.Lock()
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr != nil {
		cr.SetEFSResolver(r)
	}
}

// SetImageResolver wires the registry resolver used when a PackageType=Image
// function's ImageUri names a registry Overcast serves rather than a public
// one — a container image built and pushed by CDK, whose reference points at
// real AWS. Implemented by the ECR service.
//
// Callable before or after the Docker probe builds the container runtime: the
// resolver is recorded here and applied to every runtime built afterwards.
func (s *Service) SetImageResolver(r docker.ImageResolver) {
	s.mu.Lock()
	s.imageResolver = r
	cr := s.containerRuntime
	s.mu.Unlock()
	if cr != nil {
		cr.SetImageResolver(r)
	}
}

// New returns a configured Lambda Service with all supported runtimes registered.
// Docker availability is checked in the background — the service starts
// immediately using the stub NodeRuntime and upgrades to ContainerRuntime once
// Docker is confirmed reachable. Other services are never blocked.
//
// dbg is the router-owned debug-target manager (docs/plans/compute-debugger.md);
// nil leaves the debugger off, which is what every test that does not exercise
// it wants.
func New(cfg *config.Config, store state.Store, logger *zap.Logger, clk clock.Clock, dbg *debugger.Manager) *Service {
	log := serviceutil.NewServiceLogger(logger, "lambda")
	ls := newLambdaStore(store, cfg.Region, clk)
	tracker := newInstanceTracker(clk, logger)

	// Start with the stub runtime so the service is immediately available, but
	// leave the registry unsettled: until the probe has reported, "Docker is
	// not available" is a guess rather than an answer, and an invocation that
	// lands in that window must wait for the verdict instead of being told
	// Docker is missing on a machine where it is running.
	rr := newPendingRuntimeRegistry([]Runtime{newNodeRuntime(clk, logger)})

	esmSt := newESMStore(ls)
	h := newHandler(cfg, log, clk, rr, ls, tracker)
	h.esm = esmSt
	h.debugger = dbg

	s := &Service{
		cfg:                 cfg,
		clk:                 clk,
		store:               store,
		ls:                  ls,
		log:                 log,
		tracker:             tracker,
		handler:             h,
		invoker:             newServiceInvoker(h, ls, rr, logger, tracker),
		stop:                make(chan struct{}),
		probe:               docker.Probe,
		dockerRetryInterval: dockerRetryInterval,
		instances:           serviceutil.NewAnchoredInstanceDomain(store, nsInstance, serviceutil.DataDirAnchor(cfg.DataDir)),
		debugger:            dbg,
	}

	// Probe Docker in the background so startup of other services is not delayed.
	s.initWg.Add(1)
	go func() {
		defer s.initWg.Done()
		s.initDockerRuntime(cfg, clk, rr)
	}()

	return s
}

// dockerProbeFunc is docker.Probe's signature, indirected through a Service
// field so tests can drive the re-probe loop without a daemon.
type dockerProbeFunc func(socketPath string, networks []docker.NetworkSpec, logger *zap.Logger) (*docker.ProbeResult, error)

// dockerRetryInterval is how long the Lambda service waits between attempts
// when Docker was not reachable at startup. Starting the emulator before Docker
// Desktop has finished booting is an ordinary sequence on Windows and macOS, so
// that verdict has to be revisited rather than latched for the process's life.
const dockerRetryInterval = 5 * time.Second

// initDockerRuntime probes the Docker daemon and, if available, replaces the
// stub runtime in rr with ContainerRuntime. Called once from a goroutine
// spawned by New.
//
// The registry is settled on every exit path: invocations block until this
// function has decided, and must never block on a decision that is not coming.
// When the probe fails the verdict is "no Docker" — invocations get the stub's
// honest answer straight away — and a background loop keeps re-probing,
// upgrading the registry if Docker appears later.
func (s *Service) initDockerRuntime(cfg *config.Config, clk clock.Clock, rr *runtimeRegistry) {
	log := s.log.Logger()
	// However this returns — no socket, a failed probe, a panic — the verdict
	// is published. An unsettled registry with nothing left to settle it would
	// park every invocation for the life of the process.
	defer rr.settle()

	// Skip Docker entirely when no socket is configured. This avoids
	// unnecessary probe retries in test servers that don't need containers.
	if cfg.LambdaDockerSocket == "" {
		log.Debug("LambdaDockerSocket is empty — skipping Docker init")
		return
	}

	result, probeErr := s.probe(cfg.LambdaDockerSocket, dataplane.PlaneSpecs(cfg), log)
	if probeErr != nil {
		s.log.Warn("Docker not available — using stub runtime for now; re-probing in the background",
			zap.String("socket", cfg.LambdaDockerSocket),
			zap.Duration("retry_interval", s.dockerRetryInterval),
			zap.Error(probeErr))
		go func() {
			if late := s.awaitDockerProbe(cfg, clk, log); late != nil {
				s.log.Info("Docker became available after startup — enabling the container runtime",
					zap.String("socket", cfg.LambdaDockerSocket))
				s.wireDockerRuntime(cfg, clk, rr, late)
			}
		}()
		return
	}

	s.wireDockerRuntime(cfg, clk, rr, result)
}

// awaitDockerProbe re-probes Docker until it answers or the service stops.
// Returns nil when Stop happened first.
func (s *Service) awaitDockerProbe(cfg *config.Config, clk clock.Clock, log *zap.Logger) *docker.ProbeResult {
	for {
		// A fresh timer per attempt rather than a ticker: a ticker registered
		// against a mock clock replays every interval a test winds past, and
		// this loop can outlive startup by the whole life of the process.
		t := clk.Timer(s.dockerRetryInterval)
		select {
		case <-s.stop:
			t.Stop()
			return nil
		case <-t.C:
		}

		result, err := s.probe(cfg.LambdaDockerSocket, dataplane.PlaneSpecs(cfg), log)
		if err != nil {
			log.Debug("lambda: Docker still not available",
				zap.String("socket", cfg.LambdaDockerSocket), zap.Error(err))
			continue
		}
		return result
	}
}

// wireDockerRuntime builds the ContainerRuntime around a successful probe and
// installs it in rr, replacing the stub. Reached from the startup probe and
// from the background re-probe, so it must be safe to run after requests have
// already been served by the stub.
func (s *Service) wireDockerRuntime(cfg *config.Config, clk clock.Clock, rr *runtimeRegistry, result *docker.ProbeResult) {
	log := s.log.Logger()
	// Every exit below leaves the stub in place; publish that verdict so an
	// invocation waiting on it is not parked indefinitely.
	defer rr.settle()

	dc := result.Client

	// Create Docker GC so Stop() can sweep orphaned Lambda containers.
	s.gc = docker.NewGC(dc, s.log.ZapLogger(), false, s.instances.Resolve)
	s.gc.StartRemoveLoop(context.Background())
	// At startup, immediately clean up any orphaned containers from prior
	// crashes that left containers stuck in "created" or "exited" state.
	s.gc.Sweep("lambda")

	s.log.Info("Docker available — initialising container runtime",
		zap.String("socket", cfg.LambdaDockerSocket))

	// Where containers reach the Runtime API, and the local addresses that
	// implies. Resolved before binding rather than after, because the answer is
	// what decides the bind set — a wildcard would put an unauthenticated
	// control channel for every Lambda container on whatever network this
	// machine is attached to, and nothing off this machine is a legitimate
	// caller.
	listen := runtimeAPIListen(cfg, dc, log)

	// Bind first so we know the actual port (important when port is 0, and
	// when the default port was busy and an ephemeral one was taken instead).
	lns, fellBack, lnErr := listenRuntimeAPI(listen.BindHosts, cfg.LambdaRuntimeAPIPort, config.DefaultLambdaRuntimeAPIPort, log)
	if lnErr != nil {
		s.log.Warn("Runtime API listener failed to bind — container runtime disabled: functions cannot execute until it is fixed",
			zap.Int("port", cfg.LambdaRuntimeAPIPort),
			zap.Error(lnErr),
			zap.String("fix", runtimeAPIListenFix))
		s.setRuntimeAPIListen(listenstatus.Status{State: listenstatus.Failed, Error: lnErr.Error(), Fix: runtimeAPIListenFix})
		return
	}
	actualPort := lns[0].Addr().(*net.TCPAddr).Port

	containerAddr := net.JoinHostPort(listen.ContainerHost, strconv.Itoa(actualPort))
	log.Info("lambda: Runtime API address",
		zap.String("addr", containerAddr),
		zap.String("mode", listen.Mode),
		zap.Bool("container_verified", listen.Verified))

	runtimeAPI, apiErr := NewRuntimeAPIServerFromListeners(lns, containerAddr, lambdaInitTimeout(cfg), log, clk)
	if apiErr != nil {
		s.log.Warn("failed to start Runtime API server — container runtime disabled",
			zap.Error(apiErr))
		s.setRuntimeAPIListen(listenstatus.Status{State: listenstatus.Failed, Error: apiErr.Error(), Fix: runtimeAPIListenFix})
		return
	}
	// A bound listener nothing can reach is not "listening", whatever the bind
	// said. Reporting it as such is what let #1572 present as a working
	// configuration while every invocation died at INIT with exit 139 — the
	// one number in the whole failure, and it points at the runtime rather than
	// at the network. The state, the detail naming every candidate tried, and
	// the fix all land on /_overcast/health and in the console advisories.
	if listen.Unreachable {
		detail := containerendpoint.RuntimeAPIUnreachableDetail(listen.Attempts)
		s.setRuntimeAPIListen(listenstatus.Status{
			State: listenstatus.Unreachable,
			Addr:  containerAddr,
			Error: detail,
			Fix:   containerendpoint.RuntimeAPIUnreachableFix,
		})
	} else {
		s.setRuntimeAPIListen(listenstatus.Status{State: listenstatus.Listening, Addr: containerAddr, FellBack: fellBack})
	}
	s.setRuntimeAPIReach(listen)

	// Resolve instance limits now that the Docker client exists: env-pinned
	// values are used as-is, unset ones are derived from the daemon's /info
	// (this goroutine is already off the startup path, so the call never
	// delays other services).
	limits := resolveRuntimeLimits(context.Background(), cfg, dc, log)

	containerRuntime := NewContainerRuntime(cfg, clk, dc, s.gc, runtimeAPI, log, limits.maxConcurrentStarts, s.instances)
	containerRuntime.SetRuntimeAPIReachability(listen, containerendpoint.HintPath(cfg.DataDir, dataplane.Primary(cfg)))
	containerRuntime.SetDebugger(s.debugger)

	// When a container's RIC issues its first GET /next, throttle that
	// container's INIT-burst CPU down to the steady-state proportional
	// allocation. The instance tracker's "running" transition is driven from
	// the invocation path instead (awaitRuntimeReady returns at the same
	// moment, and only the invocation knows which of a function's environments
	// this is).
	runtimeAPI.OnFirstNext = func(_ string, containerID string) {
		containerRuntime.ThrottleInitBurst(containerID)
	}

	containerRuntime.SetLayerContentFetcher(func(ctx context.Context, layerVersionARN string) ([]byte, error) {
		lv, aerr := s.ls.getLayerVersionByARN(ctx, layerVersionARN)
		if aerr != nil {
			return nil, fmt.Errorf("get layer %s: %s", layerVersionARN, aerr.Message)
		}
		if lv == nil {
			return nil, fmt.Errorf("layer version not found: %s", layerVersionARN)
		}
		// Records travel byte-free; materialize the archive for injection.
		if aerr := s.ls.loadLayerContent(ctx, lv); aerr != nil {
			return nil, fmt.Errorf("load layer content %s: %s", layerVersionARN, aerr.Message)
		}
		if len(lv.Content) == 0 {
			return nil, fmt.Errorf("layer version has no content: %s", layerVersionARN)
		}
		return append([]byte(nil), lv.Content...), nil
	})

	// Always wire the remote layer fetcher — it checks the layer cache
	// dir (LAMBDA_LAYER_CACHE_DIR or {DataDir}/layers) for pre-downloaded
	// zips and optionally fetches from real AWS when credentials are configured.
	{
		fetcher := NewRemoteLayerFetcher(cfg, log, clk)
		containerRuntime.SetRemoteLayerFetcher(fetcher)
		cacheDir := cfg.LambdaLayerCacheDir
		if cacheDir == "" {
			cacheDir = filepath.Join(cfg.DataDir, "layers")
		}
		log.Info("lambda layer cache configured",
			zap.String("cache_dir", cacheDir),
			zap.Bool("remote_fetch", cfg.LambdaFetchRemoteLayers && cfg.LambdaRemoteAWSAccessKeyID != ""))
	}

	// Wire the log writer if InitLogWriter was already called.
	s.mu.Lock()
	lw := s.logWriter
	s.mu.Unlock()
	if lw != nil {
		containerRuntime.SetLogWriter(lw)
	}

	// Wire the VPC resolver if SetVPCResolver was already called.
	if r := s.handler.getVPCResolver(); r != nil {
		containerRuntime.SetVPCResolver(r)
	}

	// Wire the EFS resolver if SetEFSResolver was already called.
	if r := s.handler.getEFSResolver(); r != nil {
		containerRuntime.SetEFSResolver(r)
	}

	// Wire the registry resolver if SetImageResolver was already called. The
	// router always calls it first — this runtime is built from a background
	// Docker probe — but the runtime is also rebuilt by the re-probe loop, so
	// the resolver has to be re-applied rather than assumed still attached.
	s.mu.Lock()
	images := s.imageResolver
	s.mu.Unlock()
	if images != nil {
		containerRuntime.SetImageResolver(images)
	}

	// Atomically upgrade to ContainerRuntime. NodeRuntime stays as fallback.
	pool := NewInstancePool(containerRuntime, log, clk, limits.pool)
	// Keep the instance tracker in step with the containers that actually exist.
	pool.observer = s.tracker
	// A function an editor can attach to runs one execution environment.
	// Only with the flag on: with it off no target can ever be bound, so the
	// pin's lookup at admission is not paid for.
	if cfg.LambdaDebugger {
		pool.debugger = s.debugger
	}
	// Wire ConcurrentExecutions sampling if InitMetrics already ran (it may
	// run before or after Docker init completes — same race InitBus/InitLogWriter
	// already handle for this goroutine's other optional wiring).
	s.mu.Lock()
	m := s.metricsRec
	s.mu.Unlock()
	if m != nil {
		pool.concurrencyObserver = s.handler.recordConcurrency
	}
	// Let the tracker sample per-container memory/CPU for the instances UI.
	s.tracker.SetStatsFunc(dc.ContainerStatsOneShot)
	// Rebuild provisioned concurrency reservations from the store — an
	// allocation must survive a restart, or a function configured for
	// provisioned concurrency would silently cold start until someone
	// reconfigured it.
	go s.reloadProvisionedConcurrency(context.Background(), pool)
	rr.set([]Runtime{pool, newNodeRuntime(clk, log)})
	// The verdict is in as soon as the pool is registered — release any
	// invocation waiting on it without making it sit through the rest of the
	// wiring below.
	rr.settle()

	// Wire the image prewarmer so CreateFunction can kick off image pulls
	// in the background instead of paying the cost on the first Invoke.
	s.handler.setPrewarmer(containerRuntime.PrewarmFunction)

	// Cold starts materialize the deployment package from its own store key —
	// function records travel without the bytes (see lambdaStore.putFunction).
	containerRuntime.SetCodeFetcher(func(ctx context.Context, fn *Function) error {
		if aerr := s.handler.ls.loadFunctionCode(ctx, fn); aerr != nil {
			return fmt.Errorf("%s", aerr.Message)
		}
		return nil
	})

	// The settle debounce always runs: it drives the cold-start artifact
	// pre-fill after every deploy. Environment creation on top of it is the
	// opt-in proactive initialization — see proactive.go for the candidacy
	// and fidelity rules.
	wired := func(ctx context.Context, name string) bool {
		if urls, aerr := s.handler.ls.listFunctionURLConfigs(ctx, name); aerr == nil && len(urls) > 0 {
			return true
		}
		if esms, aerr := s.handler.esm.listESMs(ctx, name, ""); aerr == nil && len(esms) > 0 {
			return true
		}
		// Other services (API Gateway, AppSync) know their own wiring; ask
		// them. Runs only on settle attempts — never per invocation.
		fn, aerr := s.handler.ls.getFunction(ctx, name)
		if aerr != nil || fn == nil || fn.ARN == "" {
			return false
		}
		s.mu.Lock()
		sources := s.triggerSources
		s.mu.Unlock()
		for _, src := range sources {
			if src.ReferencesFunction(ctx, fn.ARN) {
				return true
			}
		}
		return false
	}
	proactive := newProactiveIniter(func() *InstancePool { return pool }, s.handler.ls, wired, log, clk)
	proactive.prefill = containerRuntime.PrefillArtifacts
	proactive.createEnvs = cfg.LambdaProactiveInit
	s.handler.proactive = proactive
	s.handler.tarCacheStats = containerRuntime.TarCacheStats
	pool.onInvoked = proactive.NoteInvoked
	s.mu.Lock()
	s.proactive = proactive
	s.mu.Unlock()
	if cfg.LambdaProactiveInit {
		s.log.Info("lambda proactive initialization enabled")
	}

	// Store references for Shutdown/InitLogWriter to use.
	s.mu.Lock()
	s.docker = dc
	s.runtimeAPI = runtimeAPI
	s.containerRuntime = containerRuntime
	s.pool = pool
	// Read the bus only after publishing those references. InitBus runs
	// concurrently with this goroutine and bails out while they are still nil;
	// reading the bus earlier would let both sides skip the wiring below and
	// silently leave the process with no Docker event handling at all.
	bus := s.bus
	s.mu.Unlock()

	if bus != nil {
		containerRuntime.SetBus(bus)
	}
	// Whichever of InitBus / initDockerRuntime finishes second performs the
	// subscription; subscribeDockerEvents is idempotent and nil-safe.
	s.subscribeDockerEvents(bus)

	// Optionally pre-pull all active runtime images. This is disabled by default
	// because pulling every runtime on each restart can overload Docker Desktop;
	// images are still pulled lazily on first use.
	if cfg.LambdaSeedRuntimeImages {
		go containerRuntime.SeedImages()
	}

	// Pre-pull images for any functions that were persisted from a previous
	// session (e.g. after a restart with SQLite store). This ensures their
	// first invocation after restart is warm too.
	go s.seedPersistedFunctionImages(containerRuntime)

	s.log.Info("container runtime enabled")
}

// seedPersistedFunctionImages pre-pulls Docker images for functions that were
// persisted from a previous session (e.g. after a restart with SQLite store).
// This ensures the first invocation after restart is warm for those functions
// regardless of runtime. Active runtime images are already covered by SeedImages;
// this handles custom ImageUri functions where the image is not in activeRuntimes.
//
// It also reconciles functions stuck in "Pending" state from a previous session
// (e.g. the server restarted before the prewarmer callback could transition
// them). Once the image is confirmed available the function is flipped to
// "Active"; if the image pull fails it is marked "Failed".
func (s *Service) seedPersistedFunctionImages(cr *ContainerRuntime) {
	ctx := context.Background()
	fns, aerr := s.ls.listAllFunctions(ctx)
	if aerr != nil {
		s.log.Warn("cannot list persisted functions for image seeding", zap.Error(aerr))
		return
	}
	for _, fn := range fns {
		pullGeneration, err := imagePullGenerationForFunction(fn)
		if err != nil {
			s.log.Debug("skip seed: cannot resolve image for persisted function",
				zap.String("function", fn.Name),
				zap.Error(err))
			continue
		}
		pullErr := cr.ensureImage(ctx, cr.resolveImage(ctx, pullGeneration.image), pullGeneration.platform)
		if pullErr != nil {
			s.log.Warn("seed pull failed for persisted function",
				zap.String("function", fn.Name),
				zap.String("image", pullGeneration.image),
				zap.Error(pullErr))
		}

		// Reconcile an update left mid-flight by the last session. Its pull is
		// the one just re-run, so its result settles the record — and the
		// record has to settle: an update stuck InProgress refuses every later
		// one with ResourceConflictException, for ever. Same fresh-read merge
		// as the Pending case below, keyed on the revision the update wrote.
		if fn.LastUpdateStatus == lastUpdateInProgress && s.handler != nil {
			fnCtx := middleware.ContextWithRegion(ctx, regionFromFunctionARN(fn.ARN))
			status, reason, reasonCode := lastUpdateSuccessful, "", ""
			if pullErr != nil {
				status = lastUpdateFailed
				reason = "Failed to pull container image: " + pullErr.Error()
				reasonCode = imagePullReasonCode(pullErr)
			}
			if settled := s.handler.settleFunctionUpdate(fnCtx, fn.Name, fn.RevisionId, status, reason, reasonCode); settled != nil {
				s.log.Info("settled function update interrupted by restart",
					zap.String("function", fn.Name),
					zap.String("last_update_status", status))
			}
		}

		// Reconcile functions stuck in Pending from a previous session. The
		// pulls above can take a long time and requests are already being
		// served, so the startup snapshot may be stale: the function can have
		// been deleted or revised since the list was taken. Merge the
		// transition into a fresh read — writing the snapshot back would
		// resurrect a deleted record (#414) — and only transition a record
		// that is still Pending.
		if fn.State == "Pending" {
			// Use the function's own region for the store key.
			fnCtx := middleware.ContextWithRegion(ctx, regionFromFunctionARN(fn.ARN))
			fresh, changed, aerr := s.ls.mutateFunction(fnCtx, fn.Name, func(current *Function) (bool, *protocol.AWSError) {
				if !pullGeneration.matches(current) {
					return false, nil
				}
				if pullErr != nil {
					current.State = "Failed"
					current.StateReason = "Failed to pull container image: " + pullErr.Error()
					current.StateReasonCode = "ImagePullError"
					current.LastUpdateStatus = lastUpdateFailed
					current.LastUpdateStatusReason = current.StateReason
					current.LastUpdateStatusReasonCode = imagePullReasonCode(pullErr)
				} else {
					current.State = "Active"
					current.StateReason = ""
					current.StateReasonCode = ""
					// Creation completing is what first sets LastUpdateStatus,
					// on this path as much as on CreateFunction's own.
					current.LastUpdateStatus = lastUpdateSuccessful
					current.LastUpdateStatusReason = ""
					current.LastUpdateStatusReasonCode = ""
				}
				return true, nil
			})
			if aerr != nil {
				s.log.Warn("failed to re-read pending function for reconciliation",
					zap.String("function", fn.Name),
					zap.String("error", aerr.Message))
				continue
			}
			if !changed {
				// A different Pending generation replaced the startup snapshot.
				// Arrange its own pull outside mutateFunction's lock.
				if fresh != nil && fresh.State == "Pending" && s.handler != nil {
					s.handler.startFunctionPrewarm(fresh)
				}
				continue
			}
			s.log.Info("reconciled pending function",
				zap.String("function", fn.Name),
				zap.String("state", fresh.State))
		}
	}
}

// InitRouter wires the root router so a dead-lettered failure can be delivered
// to the SQS queue or SNS topic its ARN names — a function's
// DeadLetterConfig.TargetArn, or an event-source mapping's
// DestinationConfig.OnFailure.Destination. Called by the router after all
// services are constructed.
func (s *Service) InitRouter(router http.Handler) {
	s.handler.setDeadLetterTargets(eventtarget.NewDispatcher(router, s.cfg.Region))
}

// InitESMDelivery wires SQS→Lambda and DynamoDB Streams→Lambda event delivery.
// Called by the router after all services are constructed and the event bus is
// available. receiver may be nil when the SQS service is not loaded.
func (s *Service) InitESMDelivery(receiver events.MessageReceiver, bus *events.Bus) {
	mgr := newESMDeliveryManager(
		s.handler.esm,
		s.invoker,
		receiver,
		s.handler.deadLetterTargets,
		bus,
		s.log,
		s.handler.clk,
		s.cfg,
		context.Background(),
	)
	s.esmDelivery = mgr
	s.handler.esmDelivery = mgr
	// Resume delivery for any ESMs that were Enabled before restart.
	// Run asynchronously: with the hybrid store this would block startup
	// until the SQLite seed completes. Tracked via mgr.wg so Stop()'s
	// StopAll drain waits for it, and given mgr.baseCtx so StopAll can
	// actually cancel it — this goroutine has no m.stop entry, so baseCtx is
	// its only stop signal.
	mgr.wg.Add(1)
	go func() {
		defer mgr.wg.Done()
		mgr.ReloadAll(mgr.baseCtx)
	}()
}

// TriggerSource is implemented by services that can attest a Lambda function
// is wired to receive traffic (an API Gateway integration, an AppSync data
// source, …). Used as proactive-initialization trigger evidence; called only
// when a function's configuration settles after a deploy, never on the
// invoke path, so implementations may scan their stored state.
type TriggerSource interface {
	// ReferencesFunction reports whether any of the service's routing
	// configuration targets the given Lambda function ARN.
	ReferencesFunction(ctx context.Context, functionARN string) bool
}

// AddTriggerSource registers a service as proactive-init trigger evidence.
// Called by the router during cross-service wiring.
func (s *Service) AddTriggerSource(src TriggerSource) {
	if src == nil {
		return
	}
	s.mu.Lock()
	s.triggerSources = append(s.triggerSources, src)
	s.mu.Unlock()
}

// Stop shuts down the Runtime API server and any background resources.
func (s *Service) Stop(ctx context.Context) {
	// End the background Docker re-probe loop, if one is running.
	s.stopOnce.Do(func() {
		if s.stop != nil {
			close(s.stop)
		}
	})
	if s.esmDelivery != nil {
		s.esmDelivery.StopAll(ctx)
	}
	// Wait for in-flight async invocations to finish.
	s.handler.StopAsync(ctx)
	// Stop background sweepers.
	s.tracker.Stop()
	s.mu.Lock()
	rapi := s.runtimeAPI
	pool := s.pool
	gc := s.gc
	proactive := s.proactive
	s.mu.Unlock()
	// Cancel pending proactive-init timers before the pool stops; in-flight
	// creations are tracked by the pool's own WaitGroup.
	proactive.Stop()
	// (Docker watcher is owned by the router; no cancel needed here.)
	if pool != nil {
		pool.Stop()
	}
	if rapi != nil {
		if err := rapi.Stop(ctx); err != nil {
			s.log.Error("lambda: runtime API shutdown error", zap.Error(err))
		}
	}
	// Clean up any Docker containers (warm pool, stuck, or orphaned).
	if gc != nil {
		gc.DrainAndSweep(ctx, "lambda")
	}
}

func (s *Service) Name() string { return "lambda" }

// PathPrefixes lists every Lambda API version, so a router built with only a
// subset of services (test-only) answers these with a 501 rather than letting
// them fall through to S3's /{bucket}/* wildcard.
func (s *Service) PathPrefixes() []string {
	return []string{
		"/2015-03-31",
		"/2017-03-31",
		"/2017-10-31",
		"/2018-10-31",
		"/2019-09-30",
		"/2020-04-22",
		"/2020-06-30",
		"/2021-10-31",
		"/2021-11-15",
		"/2026-07-09",
	}
}

// Invoker returns the FunctionInvoker for this Lambda service.
// Used by other services (e.g. S3 notifications) to invoke Lambda functions
// without creating an import cycle.
func (s *Service) Invoker() *ServiceInvoker { return s.invoker }

// SyncInvoker returns the FunctionSyncInvoker for this Lambda service.
// Used by API Gateway to invoke Lambda functions synchronously and receive
// the response payload.
func (s *Service) SyncInvoker() events.FunctionSyncInvoker { return s.invoker }

// RegisterRoutes mounts Lambda REST endpoints.
// Lambda uses versioned REST paths, not a single-dispatch target header.
func (s *Service) RegisterRoutes(r chi.Router) {
	const apiBase = "/2015-03-31"
	// emulatorBase is where Overcast's own Lambda endpoints live. They used to
	// hang off apiBase, which put invented paths inside a prefix AWS models —
	// invisible to a "/_" grep, and misleading to anyone reading the routing
	// table, which is the worse half. See
	// docs/plans/non-canonical-url-namespace.md.
	const emulatorBase = "/_overcast/lambda"
	// The concurrency operation families are the ones AWS serves under API
	// versions other than the 2015-03-31 base. Registering them on the base
	// makes every SDK call miss the handler and fall through to the S3
	// catch-all, which parses the version segment as a bucket name.
	const reservedConcurrencyBase = "/2017-10-31"
	const provisionedConcurrencyBase = "/2019-09-30"
	// Asynchronous invocation settings sit on their own version, five days
	// before provisioned concurrency's.
	const eventInvokeConfigBase = "/2019-09-25"
	const codeSigningBase = "/2020-06-30"
	// The code signing configuration resource itself is on yet another version.
	const codeSigningConfigBase = "/2020-04-22"

	r.Post(apiBase+"/functions", s.handler.CreateFunction)
	r.Post(apiBase+"/functions/", s.handler.CreateFunction)
	r.Get(apiBase+"/functions", s.handler.ListFunctions)
	r.Get(apiBase+"/functions/", s.handler.ListFunctions)
	r.Post(apiBase+"/event-source-mappings", s.handler.CreateEventSourceMapping)
	r.Post(apiBase+"/event-source-mappings/", s.handler.CreateEventSourceMapping)
	r.Get(apiBase+"/event-source-mappings", s.handler.ListEventSourceMappings)
	r.Get(apiBase+"/event-source-mappings/", s.handler.ListEventSourceMappings)
	r.Get(apiBase+"/event-source-mappings/{uuid}", s.handler.GetEventSourceMapping)
	r.Put(apiBase+"/event-source-mappings/{uuid}", s.handler.UpdateEventSourceMapping)
	r.Delete(apiBase+"/event-source-mappings/{uuid}", s.handler.DeleteEventSourceMapping)
	r.Get(apiBase+"/functions/{name}", s.handler.GetFunction)
	r.Post(apiBase+"/functions/{name}/policy", s.handler.AddPermission)
	r.Get(apiBase+"/functions/{name}/policy", s.handler.GetPolicy)
	r.Delete(apiBase+"/functions/{name}/policy/{statementId}", s.handler.RemovePermission)
	// The full-JSON view of the same policy, addressed by ARN rather than by
	// function name, is its own API vintage.
	const resourcePolicyBase = "/2026-07-09"
	r.Get(resourcePolicyBase+"/resource-policy/{ResourceArn}", s.handler.GetResourcePolicy)
	r.Put(resourcePolicyBase+"/resource-policy/{ResourceArn}", s.handler.PutResourcePolicy)
	r.Delete(resourcePolicyBase+"/resource-policy/{ResourceArn}", s.handler.DeleteResourcePolicy)
	r.Get(codeSigningBase+"/functions/{name}/code-signing-config", s.handler.GetFunctionCodeSigningConfig)
	r.Put(codeSigningBase+"/functions/{name}/code-signing-config", s.handler.PutFunctionCodeSigningConfig)
	r.Delete(codeSigningBase+"/functions/{name}/code-signing-config", s.handler.DeleteFunctionCodeSigningConfig)
	// Code signing configuration resource. AWS's paths carry a trailing slash
	// on the collection; register both so either form reaches the handler.
	r.Post(codeSigningConfigBase+"/code-signing-configs", s.handler.CreateCodeSigningConfig)
	r.Post(codeSigningConfigBase+"/code-signing-configs/", s.handler.CreateCodeSigningConfig)
	r.Get(codeSigningConfigBase+"/code-signing-configs", s.handler.ListCodeSigningConfigs)
	r.Get(codeSigningConfigBase+"/code-signing-configs/", s.handler.ListCodeSigningConfigs)
	r.Get(codeSigningConfigBase+"/code-signing-configs/{arn}", s.handler.GetCodeSigningConfig)
	r.Put(codeSigningConfigBase+"/code-signing-configs/{arn}", s.handler.UpdateCodeSigningConfig)
	r.Delete(codeSigningConfigBase+"/code-signing-configs/{arn}", s.handler.DeleteCodeSigningConfig)
	r.Get(codeSigningConfigBase+"/code-signing-configs/{arn}/functions", s.handler.ListFunctionsByCodeSigningConfig)
	r.Delete(apiBase+"/functions/{name}", s.handler.DeleteFunction)
	r.Put(apiBase+"/functions/{name}/code", s.handler.UpdateFunctionCode)
	r.Get(apiBase+"/functions/{name}/configuration", s.handler.GetFunctionConfiguration)
	r.Put(apiBase+"/functions/{name}/configuration", s.handler.UpdateFunctionConfiguration)
	// Reserved concurrency: Put/Delete on 2017-10-31, Get on 2019-09-30 — the
	// three are split across two API versions, which is what AWS serves.
	r.Put(reservedConcurrencyBase+"/functions/{name}/concurrency", s.handler.PutFunctionConcurrency)
	r.Delete(reservedConcurrencyBase+"/functions/{name}/concurrency", s.handler.DeleteFunctionConcurrency)
	r.Get(provisionedConcurrencyBase+"/functions/{name}/concurrency", s.handler.GetFunctionConcurrency)
	// Provisioned concurrency lives under the 2019-09-30 API version, not the
	// 2015-03-31 base the rest of Lambda uses — that is the path the AWS SDKs
	// call. GET is overloaded: ?List=ALL lists, otherwise it gets one config.
	r.Put(provisionedConcurrencyBase+"/functions/{name}/provisioned-concurrency", s.handler.PutProvisionedConcurrencyConfig)
	r.Get(provisionedConcurrencyBase+"/functions/{name}/provisioned-concurrency", s.handler.GetOrListProvisionedConcurrency)
	r.Delete(provisionedConcurrencyBase+"/functions/{name}/provisioned-concurrency", s.handler.DeleteProvisionedConcurrencyConfig)
	// Asynchronous invocation settings. Put overwrites and Update (POST) merges,
	// which is the only difference between them; the /list suffix is AWS's own
	// and is registered before nothing else claims it.
	r.Put(eventInvokeConfigBase+"/functions/{name}/event-invoke-config", s.handler.PutFunctionEventInvokeConfig)
	r.Post(eventInvokeConfigBase+"/functions/{name}/event-invoke-config", s.handler.UpdateFunctionEventInvokeConfig)
	r.Get(eventInvokeConfigBase+"/functions/{name}/event-invoke-config", s.handler.GetFunctionEventInvokeConfig)
	r.Delete(eventInvokeConfigBase+"/functions/{name}/event-invoke-config", s.handler.DeleteFunctionEventInvokeConfig)
	r.Get(eventInvokeConfigBase+"/functions/{name}/event-invoke-config/list", s.handler.ListFunctionEventInvokeConfigs)
	r.Post(apiBase+"/functions/{name}/invocations", s.handler.InvokeFunction)
	// InvokeWithResponseStream uses a different API version path.
	const streamBase = "/2021-11-15"
	r.Post(streamBase+"/functions/{name}/response-streaming-invocations", s.handler.InvokeWithResponseStream)
	// Emulator-only: SSE invoke with progress events for the web UI.
	r.Post(emulatorBase+"/functions/{name}/invoke-with-progress", s.handler.InvokeFunctionSSE)
	// Versions.
	r.Post(apiBase+"/functions/{name}/versions", s.handler.PublishVersion)
	r.Get(apiBase+"/functions/{name}/versions", s.handler.ListVersionsByFunction)
	// Aliases.
	r.Post(apiBase+"/functions/{name}/aliases", s.handler.CreateAlias)
	r.Get(apiBase+"/functions/{name}/aliases", s.handler.ListAliases)
	r.Get(apiBase+"/functions/{name}/aliases/{aliasName}", s.handler.GetAlias)
	r.Put(apiBase+"/functions/{name}/aliases/{aliasName}", s.handler.UpdateAlias)
	r.Delete(apiBase+"/functions/{name}/aliases/{aliasName}", s.handler.DeleteAlias)
	// Emulator-only: plain-text source code storage for the web UI editor.
	r.Get(emulatorBase+"/functions/{name}/source", s.handler.GetFunctionSource)
	r.Put(emulatorBase+"/functions/{name}/source", s.handler.PutFunctionSource)
	// Emulator-only: saved test events for the web UI Test tab.
	r.Get(emulatorBase+"/functions/{name}/test-events", s.handler.ListTestEvents)
	r.Put(emulatorBase+"/functions/{name}/test-events/{eventName}", s.handler.PutTestEvent)
	r.Delete(emulatorBase+"/functions/{name}/test-events/{eventName}", s.handler.DeleteTestEvent)
	// Layers — introduced 2018-10-31, separate API base from functions.
	const layerBase = "/2018-10-31"
	r.Get(layerBase+"/layers", s.handler.ListLayers)
	r.Get(layerBase+"/layers/", s.handler.ListLayers)
	r.Post(layerBase+"/layers/{layerName}/versions", s.handler.PublishLayerVersion)
	r.Get(layerBase+"/layers/{layerName}/versions", s.handler.ListLayerVersions)
	r.Get(layerBase+"/layers/{layerName}/versions/{versionNumber}", s.handler.GetLayerVersion)
	r.Delete(layerBase+"/layers/{layerName}/versions/{versionNumber}", s.handler.DeleteLayerVersion)

	// Function URLs — introduced 2021-10-31, same API vintage as
	// response-streaming invocations above.
	const urlBase = "/2021-10-31"
	r.Post(urlBase+"/functions/{name}/url", s.handler.CreateFunctionUrlConfig)
	r.Get(urlBase+"/functions/{name}/url", s.handler.GetFunctionUrlConfig)
	r.Put(urlBase+"/functions/{name}/url", s.handler.UpdateFunctionUrlConfig)
	r.Delete(urlBase+"/functions/{name}/url", s.handler.DeleteFunctionUrlConfig)
	r.Get(urlBase+"/functions/{name}/urls", s.handler.ListFunctionUrlConfigs)

	// Tags — introduced 2017-03-31.
	const tagBase = "/2017-03-31"
	r.Post(tagBase+"/tags/{ResourceARN}", s.handler.tagResource)
	r.Get(tagBase+"/tags/{ResourceARN}", s.handler.listTags)
	r.Delete(tagBase+"/tags/{ResourceARN}", s.handler.untagResource)

	// Emulator-specific: list warm/running instances for the topology map UI.
	r.Get("/_overcast/lambda/instances", s.handler.ListInstances)
	// Emulator-specific: the web UI Monitor tab's metrics read-through
	// (docs/plans/service-metrics-platform.md phase 3).
	r.Get(emulatorBase+"/functions/{name}/metrics", s.handler.GetFunctionMetrics)
	// Emulator-specific: runtime catalog for the web UI.
	r.Get("/_overcast/lambda/runtimes", s.handler.ListRuntimes)
	// Emulator-specific: layer zip metadata for the web UI.
	r.Get("/_overcast/lambda/layers/{layerName}/versions/{versionNumber}/metadata", s.handler.GetLayerVersionMetadata)

	// Host-based invoke (lambda-url Host header) — see handler_url.go's
	// Service.HostRouteRewrite. Matches any HTTP method: real Lambda
	// function URLs accept arbitrary methods and let the function decide.
	r.HandleFunc("/_overcast/lambda/url-invoke/{urlId}/*", s.handler.InvokeFunctionURL)

}

// runtimeAPIListen determines the host Lambda containers use to reach the
// Runtime API server, and the local addresses it must bind for that to work.
//
// The decision lives in internal/containerendpoint, next to the answer ECS task
// containers get, so the two cannot drift. Lambda used to make it here, from
// whatever non-loopback IPv4 address net.InterfaceAddrs listed first; on a
// multi-homed host that is as likely to be a 169.254/16 address left by an
// adapter that failed DHCP, or a hypervisor switch no container can route to,
// as it is the right one. Only the host is resolved: the Runtime API and the
// emulator API are two ports at the same address.
//
// The timeout bounds a Docker daemon that accepts the connection and then does
// not answer — this runs on the container-runtime init goroutine, and a hang
// here leaves Lambda on the stub runtime with no error to show for it. It is
// larger than the 10s it replaced because the answer is now measured rather
// than guessed: the first run against a daemon may also have to pull busybox.
// It is an outer bound, not a budget anyone expects to spend — the walk itself
// is capped well below this (containerendpoint's probeTotalBudget), because the
// runtime registry does not settle until this returns and an Invoke arriving
// meanwhile parks rather than getting the stub's honest answer.
//
// **It has to stay above probeImagePullTimeout + probeTotalBudget**, which run
// in series under it: the image is fetched once against this context before the
// walk's own clock starts (#1586). At 60s + 45s that leaves 15s of slack, and
// cutting into it reintroduces #1586 in a new shape — a pull truncated by an
// outer bound instead of an inner one, every candidate unmeasured, and the
// address chosen with nothing established. containerendpoint pins the
// relationship from its side; this is the number it is pinned against. The ordinary
// path is one candidate and a second or two, and the result is remembered per
// daemon and plane (containerendpoint.HintPath), so only the first startup
// against a given daemon pays even that.
func runtimeAPIListen(cfg *config.Config, dc *docker.Client, logger *zap.Logger) containerendpoint.Listen {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	return containerendpoint.ResolveListen(ctx, dc, containerendpoint.ListenOptions{
		Network:    dataplane.Primary(cfg),
		PinnedHost: cfg.LambdaRuntimeAPIHost,
		HintPath:   containerendpoint.HintPath(cfg.DataDir, dataplane.Primary(cfg)),
		Logger:     logger,
	})
}
