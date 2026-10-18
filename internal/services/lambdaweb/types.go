package lambdaweb

// Stored records and the wire shapes that are not plain request/response
// bodies. Field names are the model's (lambda-web 2025-03-07, camelCase), so a
// record, a response and the AWS shape read alike.

import "time"

// Lifecycle states, quoted from the model's FunctionState, RevisionState,
// EndpointState and EndpointUpdateStatus enums.
const (
	statePending  = "Pending"
	stateActive   = "Active"
	stateFailed   = "Failed"
	stateDeleting = "Deleting"

	updateSuccessful = "Successful"
)

// Endpoint types and auth types (EndpointType, AuthType).
const (
	endpointHomeRegion  = "HomeRegion"
	endpointMultiRegion = "MultiRegion"
	endpointPerRegion   = "PerRegion"

	authApplicationManaged = "ApplicationManaged"
	authIam                = "IamAuth"

	deployLatestRevision = "LatestRevision"
	deployDisabled       = "Disabled"
)

// webFunction is the top-level resource. It owns its revisions and endpoints,
// which are stored under its name.
type webFunction struct {
	Name      string    `json:"name"`
	ARN       string    `json:"arn"`
	Region    string    `json:"region"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// revision is an immutable snapshot of code and configuration. Everything but
// its state is fixed at creation.
type revision struct {
	Function    string        `json:"function"`
	Region      string        `json:"region"`
	ID          string        `json:"id"`
	ARN         string        `json:"arn"`
	Description string        `json:"description,omitempty"`
	KMSKeyARN   string        `json:"kmsKeyArn,omitempty"`
	BuildConfig buildConfig   `json:"buildConfig"`
	Service     serviceConfig `json:"serviceConfig"`
	CreatedAt   time.Time     `json:"createdAt"`
	// State, StateReason and Errors are the revision's lifecycle: Pending while
	// its package is prepared, then Active or Failed. A record stored before the
	// data plane has no State and reads as Active.
	State       string          `json:"state,omitempty"`
	StateReason string          `json:"stateReason,omitempty"`
	Errors      []revisionError `json:"errors,omitempty"`
	// Seq orders a function's revisions: 1 for its first. Timestamps tie when
	// two revisions are created within a clock tick, so they cannot say which
	// is newer.
	Seq int `json:"seq"`
}

// endpoint routes HTTP traffic to one or two revisions.
type endpoint struct {
	Function     string            `json:"function"`
	Region       string            `json:"region"`
	Name         string            `json:"name"`
	ARN          string            `json:"arn"`
	Description  string            `json:"description,omitempty"`
	Type         string            `json:"type"`
	AuthType     string            `json:"authType"`
	AutoDeploy   string            `json:"autoDeploymentMode"`
	Weights      []revisionWt      `json:"revisionWeights"`
	Regions      []string          `json:"regions"`
	Scaling      *scalingConfig    `json:"scalingConfig,omitempty"`
	Throttle     *throttleCfg      `json:"throttleConfig,omitempty"`
	DomainName   string            `json:"domainName,omitempty"`
	RegionDomain map[string]string `json:"regionDomains,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	// State, StateReason, UpdateStatus and UpdateStatusReason are the endpoint's
	// lifecycle. A record stored before the data plane has none and reads as
	// Active and Successful.
	State              string `json:"state,omitempty"`
	StateReason        string `json:"stateReason,omitempty"`
	UpdateStatus       string `json:"updateStatus,omitempty"`
	UpdateStatusReason string `json:"updateStatusReason,omitempty"`
}

// revisionError is one entry of a revision's errors list (RevisionError).
type revisionError struct {
	Attribute    string `json:"attribute"`
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
}

// policyRecord is a function's resource-based policy and its revision token.
type policyRecord struct {
	Policy     string `json:"policy"`
	RevisionID string `json:"revisionId"`
}

// ─── Model structures shared by requests, responses and records ──────────────

type buildConfig struct {
	CodeConfig    codeConfig    `json:"codeConfig"`
	RuntimeConfig runtimeConfig `json:"runtimeConfig"`
}

type codeConfig struct {
	S3Object s3Object `json:"s3Object"`
}

type s3Object struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"versionId,omitempty"`
}

type runtimeConfig struct {
	Runtime string `json:"runtime"`
}

type serviceConfig struct {
	ExecutionRoleARN             string            `json:"executionRoleArn"`
	TimeoutSeconds               *int              `json:"timeoutSeconds,omitempty"`
	EgressNetworkConnectorARN    string            `json:"egressNetworkConnectorArn,omitempty"`
	MaxConcurrencyPerEnvironment *int              `json:"maxConcurrencyPerEnvironment,omitempty"`
	EnvironmentVariables         map[string]string `json:"environmentVariables,omitempty"`
	TelemetryConfig              *telemetryConfig  `json:"telemetryConfig,omitempty"`
}

type telemetryConfig struct {
	LoggingConfig *loggingConfig `json:"loggingConfig,omitempty"`
}

type loggingConfig struct {
	LogGroup            string `json:"logGroup,omitempty"`
	ApplicationLogLevel string `json:"applicationLogLevel,omitempty"`
	SystemLogLevel      string `json:"systemLogLevel,omitempty"`
}

type scalingConfig struct {
	MaxEnvironments *int `json:"maxEnvironments,omitempty"`
}

type throttleCfg struct {
	RateLimit *int `json:"rateLimit,omitempty"`
}

type revisionWt struct {
	RevisionID string `json:"revisionId"`
	Weight     int    `json:"weight"`
}

type filter struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// ─── Summaries ───────────────────────────────────────────────────────────────

type functionSummary struct {
	FunctionName string `json:"functionName"`
	FunctionARN  string `json:"functionArn"`
	State        string `json:"state"`
	StateReason  string `json:"stateReason"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type revisionSummary struct {
	RevisionARN string `json:"revisionArn"`
	RevisionID  string `json:"revisionId"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	StateReason string `json:"stateReason"`
	CreatedAt   string `json:"createdAt"`
}

type endpointSummary struct {
	EndpointARN        string         `json:"endpointArn"`
	EndpointName       string         `json:"endpointName"`
	Description        string         `json:"description,omitempty"`
	EndpointType       string         `json:"endpointType"`
	DomainName         string         `json:"domainName"`
	AuthType           string         `json:"authType"`
	AutoDeploymentMode string         `json:"autoDeploymentMode"`
	RevisionWeights    []revisionWt   `json:"revisionWeights"`
	Regions            []string       `json:"regions"`
	ScalingConfig      *scalingConfig `json:"scalingConfig,omitempty"`
	ThrottleConfig     *throttleCfg   `json:"throttleConfig,omitempty"`
	State              string         `json:"state"`
	StateReason        string         `json:"stateReason"`
	UpdateStatus       string         `json:"updateStatus,omitempty"`
	UpdateStatusReason string         `json:"updateStatusReason,omitempty"`
	CreatedAt          string         `json:"createdAt"`
	UpdatedAt          string         `json:"updatedAt"`
}

type regionalEndpoint struct {
	DomainName         string         `json:"domainName,omitempty"`
	AuthType           string         `json:"authType"`
	RevisionWeights    []revisionWt   `json:"revisionWeights"`
	ScalingConfig      *scalingConfig `json:"scalingConfig,omitempty"`
	ThrottleConfig     *throttleCfg   `json:"throttleConfig,omitempty"`
	State              string         `json:"state"`
	StateReason        string         `json:"stateReason"`
	UpdateStatus       string         `json:"updateStatus,omitempty"`
	UpdateStatusReason string         `json:"updateStatusReason,omitempty"`
}
