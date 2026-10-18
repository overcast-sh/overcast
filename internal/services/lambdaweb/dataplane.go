package lambdaweb

// dataplane.go — the contracts between the data plane's parts. Every track in
// docs/plans/lambda-web-data-plane.md implements or consumes one of these; a
// change here is a change to all of them.

import (
	"context"
	"net/http"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── Execution environments ──────────────────────────────────────────────────

// codeRef names a revision's package in S3. After Resolve, VersionID is the
// pinned version.
type codeRef struct {
	Bucket, Key, VersionID string
}

// envSpec is everything a provider needs to start one execution environment.
type envSpec struct {
	Region   string
	Function string
	// Endpoint is the endpoint whose traffic triggered the start; it labels
	// logs only. Environments belong to revisions, not endpoints.
	Endpoint string
	Revision *revision
	Code     codeRef
	// Env is the revision's environmentVariables. The provider adds the
	// runtime variables (AWS_LAMBDA_HTTP_ENDPOINT, AWS_REGION, …).
	Env map[string]string
	// MemoryMB is the environment's memory (2048 on AWS).
	MemoryMB int
	// InitTimeout is how long the server may take to start listening (10 s).
	InitTimeout time.Duration
	// LogGroup and LogStream are where the environment's application logs go.
	LogGroup  string
	LogStream string
	// AppLogLevel and SysLogLevel are the revision's loggingConfig levels,
	// defaulted (INFO, INFO).
	AppLogLevel string
	SysLogLevel string
}

// environment is one running execution environment: a container whose Node.js
// server is already listening.
type environment interface {
	// ID identifies the environment in logs and events (the container id).
	ID() string
	// Addr is the host:port Overcast dials to reach the server.
	Addr() string
	// Done is closed when the process has exited, whoever stopped it.
	Done() <-chan struct{}
	// ExitReason describes why the process exited, once Done is closed.
	ExitReason() exitReason
	// Stats samples memory and CPU use; it may return zeros when unknown.
	Stats(ctx context.Context) envStats
	// Stop sends SIGTERM, waits up to grace for the process to exit, then
	// kills and removes the container. It is idempotent.
	Stop(ctx context.Context, grace time.Duration) error
}

// exitReason says how an environment's process ended.
type exitReason struct {
	// OOM is true when the kernel killed it for memory.
	OOM bool
	// Code is the exit status; 0 for a clean exit.
	Code int
	// Requested is true when Stop ended it.
	Requested bool
}

// envStats is a sample of an environment's resource use.
type envStats struct {
	MaxMemoryMB float64
	CPUTime     time.Duration
}

// initFailure is why an environment never became ready. Type is a
// lambda-web-error-type value (Runtime.ImportCodeError,
// Application.TimeoutListenError, Runtime.InitTimeout, …) and Detail is a
// bounded excerpt of the process output for the system log.
type initFailure struct {
	Type   string
	Detail string
}

func (f *initFailure) Error() string { return f.Type + ": " + f.Detail }

// environmentProvider starts environments. Start returns once the server
// accepts a connection, or an *initFailure; it honours ctx cancellation. It
// must be safe for concurrent use.
type environmentProvider interface {
	Start(ctx context.Context, spec envSpec) (environment, *initFailure)
}

// ─── Code ────────────────────────────────────────────────────────────────────

// codeSource reads revision packages from S3.
type codeSource interface {
	// Resolve checks that the bucket exists and is versioned and that the
	// object (or the version in ref) exists, and returns ref with VersionID
	// set to the pinned version, plus the object size in bytes. Errors are
	// the control-plane errors CreateWebFunctionRevision returns.
	Resolve(ctx context.Context, ref codeRef) (pinned codeRef, size int64, aerr *protocol.AWSError)
	// Fetch returns the pinned version's bytes.
	Fetch(ctx context.Context, ref codeRef) ([]byte, *protocol.AWSError)
}

// ─── Authorisation ───────────────────────────────────────────────────────────

// authContext is what an authoriser needs to know about the request's target.
type authContext struct {
	Function *webFunction
	Endpoint *endpoint
	// EndpointARN is the resource lambda:InvokeWebFunctionEndpoint is
	// evaluated on.
	EndpointARN string
	// Policy is the function's resource policy, nil when none is attached.
	Policy *policyRecord
	// Region is the Region of the domain the request arrived on.
	Region string
}

// invokeAuth decides whether a request may reach an IamAuth endpoint. It is
// called only for IamAuth endpoints. A nil result admits the request; the
// returned error is written as the response (errForbidden for every
// authentication or authorisation failure).
type invokeAuth interface {
	Authorize(ctx context.Context, r *http.Request, c authContext) *platformError
}

// ─── Telemetry ───────────────────────────────────────────────────────────────

// telemetry receives the data plane's lifecycle and request facts and turns
// them into CloudWatch metrics, system logs and bus events. Implementations
// must not block the request path: they queue and return.
type telemetry interface {
	EnvironmentInit(e envInitEvent)
	EnvironmentSpinDown(e spinDownEvent)
	RequestDone(e requestEvent)
	// Throttled records a request the platform refused before an environment
	// saw it (rate limits, environment caps).
	Throttled(e throttleEvent)
}

// envRef identifies an environment and what it runs.
type envRef struct {
	Region      string
	Function    string
	Endpoint    string
	RevisionID  string
	Environment string
	LogGroup    string
	LogStream   string
	SysLogLevel string
}

type envInitEvent struct {
	Env      envRef
	Started  time.Time
	Duration time.Duration
	// Failure is nil on success.
	Failure *initFailure
}

type spinDownEvent struct {
	Env      envRef
	Started  time.Time
	Duration time.Duration
	Reason   exitReason
	// Stats are the final samples.
	Stats envStats
	// Requests is how many requests the environment served in its life.
	Requests int64
	// Timeout is true when the process did not exit within the 1 s grace.
	Timeout bool
}

type requestEvent struct {
	Env       envRef
	RequestID string
	Method    string
	Path      string
	Status    int
	// ErrorType is the lambda-web-error-type, empty for a plain response.
	ErrorType string
	Start     time.Time
	Duration  time.Duration
	// ColdStart is true when the request waited for its environment to start.
	ColdStart     bool
	RequestBytes  int64
	ResponseBytes int64
	// Concurrent is the environment's in-flight count when the request started.
	Concurrent int
}

type throttleEvent struct {
	Region    string
	Function  string
	Endpoint  string
	ErrorType string
	At        time.Time
}
