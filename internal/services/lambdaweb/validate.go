package lambdaweb

// Modeled errors and the input constraints of the lambda-web 2025-03-07 model.
// The code and HTTP status of every error come from the model's error shapes
// (ValidationException 400, ResourceNotFoundException 404, ConflictException
// 409, ServiceQuotaExceededException 402); messages are this package's own
// where AWS's were not observed.

import (
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

func validationError(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: "ValidationException", Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusBadRequest}
}

func notFound(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: "ResourceNotFoundException", Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusNotFound}
}

func conflict(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: "ConflictException", Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusConflict}
}

// quotaExceeded is ServiceQuotaExceededException, which the model binds to
// HTTP 402.
func quotaExceeded(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: "ServiceQuotaExceededException", Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusPaymentRequired}
}

var errBadToken = validationError("The pagination token is not valid.")

// Account quotas. GetWebAccountSettings reports them, and the endpoint and
// revision counts are enforced against them.
//
// Per AWS docs (https://docs.aws.amazon.com/lambda/latest/dg/web-functions-scaling.md
// "Account quotas", and web-functions-endpoints.md): 2,000 vCPUs and 100,000
// RPS per account and Region; up to 10 endpoints per Web Function; 50 revisions
// per Web Function as the automatic-cleanup target.
const (
	maxTotalArmVCPUs       = 2000
	maxTotalRateLimit      = 100000
	maxRevisionsPerFn      = 50
	maxEndpointsPerFn      = 10
	maxPerEndpointRateRPS  = 10000
	defaultTimeoutSeconds  = 30
	defaultMaxConcurrency  = 64
	defaultListPageSize    = 50
	maxFunctionNameLength  = 64
	maxEndpointNameLength  = 64
	maxDescriptionLength   = 256
	maxRevisionWeightCount = 2
)

// nameRe is the documented name rule for functions and endpoints: letters,
// numbers, hyphens and underscores, not beginning or ending with a hyphen or
// an underscore, at most 64 characters.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func validateName(kind, name string, limit int) *protocol.AWSError {
	if name == "" {
		return validationError("%s is required.", kind)
	}
	if len(name) > limit || !nameRe.MatchString(name) {
		return validationError("%s %q is invalid: it can contain letters, numbers, hyphens (-) and underscores (_), "+
			"can't begin or end with a hyphen or an underscore, and is limited to %d characters.", kind, name, limit)
	}
	return nil
}

func validateDescription(d string) *protocol.AWSError {
	if len(d) > maxDescriptionLength {
		return validationError("Description is limited to %d characters.", maxDescriptionLength)
	}
	return nil
}

var (
	bucketRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{1,61}[a-z0-9]$`)
	runtimeRe = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z0-9]+)*$`)
	roleARNRe = regexp.MustCompile(`^arn:(aws[a-z-]*):iam::[0-9]{12}:role/[a-zA-Z0-9+=,.@_/-]+$`)
	kmsARNRe  = regexp.MustCompile(`^arn:(aws[a-z-]*):kms:[a-z0-9-]+:[0-9]{12}:key/[a-z0-9-]+$`)
	connRe    = regexp.MustCompile(`^arn:(aws[a-z-]*):lambda:[a-z0-9-]+:[0-9]{12}:network-connector:[a-zA-Z0-9_-]+:[0-9]+$`)
	regionRe  = regexp.MustCompile(`^([a-z]{2,}-){2,}[0-9]$`)
	logGrpRe  = regexp.MustCompile(`^[a-zA-Z0-9.\-_/#]+$`)
)

// reservedEnvVars are the environment variable names a revision can't set.
var reservedEnvVars = []string{
	"AWS_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY", "AWS_SECURITY_TOKEN",
	"AWS_SESSION_TOKEN", "AWS_DEFAULT_REGION", "AWS_REGION", "AWS_LAMBDA_HTTP_ENDPOINT", "AWS_LAMBDA_RUNTIME_API",
	"AWS_LAMBDA_INITIALIZATION_TYPE", "AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "AWS_LAMBDA_METADATA_API", "AWS_LAMBDA_METADATA_TOKEN",
}

var (
	appLogLevels = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}
	sysLogLevels = []string{"DEBUG", "INFO", "WARN"}
)

// supportedRuntime is the one runtime Web Functions run
// (web-functions-runtimes.md; the AWS CLI model lists nodejs24.x alone).
const supportedRuntime = "nodejs24.x"

// maxEnvVarBytes is the quota on a revision's environment variables, summed
// over names and values (the Lambda quotas page, Web Functions section).
const maxEnvVarBytes = 32 * 1024

// validateRevision checks a revision's build and service configuration. Web
// Functions run Node.js only at launch, so any other runtime family is refused
// (https://docs.aws.amazon.com/lambda/latest/dg/web-functions.md).
func validateRevision(bc *buildConfig, sc *serviceConfig, kmsKeyARN, description string) *protocol.AWSError {
	if bc == nil || bc.CodeConfig.S3Object.Bucket == "" && bc.CodeConfig.S3Object.Key == "" && bc.RuntimeConfig.Runtime == "" {
		return validationError("buildConfig is required.")
	}
	s3 := bc.CodeConfig.S3Object
	if len(s3.Bucket) < 3 || len(s3.Bucket) > 63 || !bucketRe.MatchString(s3.Bucket) {
		return validationError("buildConfig.codeConfig.s3Object.bucket %q is not a valid bucket name.", s3.Bucket)
	}
	if s3.Key == "" || len(s3.Key) > 1024 {
		return validationError("buildConfig.codeConfig.s3Object.key must be between 1 and 1024 characters.")
	}
	if len(s3.VersionID) > 1024 {
		return validationError("buildConfig.codeConfig.s3Object.versionId must be at most 1024 characters.")
	}
	rt := bc.RuntimeConfig.Runtime
	if rt == "" || len(rt) > 30 || !runtimeRe.MatchString(rt) {
		return validationError("buildConfig.runtimeConfig.runtime %q is not a valid runtime identifier.", rt)
	}
	if rt != supportedRuntime {
		return validationError("Runtime %q is not supported: Web Functions support %s only.", rt, supportedRuntime)
	}
	if sc == nil || sc.ExecutionRoleARN == "" {
		return validationError("serviceConfig.executionRoleArn is required.")
	}
	if len(sc.ExecutionRoleARN) < 20 || len(sc.ExecutionRoleARN) > 2048 || !roleARNRe.MatchString(sc.ExecutionRoleARN) {
		return validationError("serviceConfig.executionRoleArn %q is not a valid IAM role ARN.", sc.ExecutionRoleARN)
	}
	if sc.TimeoutSeconds != nil && (*sc.TimeoutSeconds < 3 || *sc.TimeoutSeconds > 900) {
		return validationError("serviceConfig.timeoutSeconds must be between 3 and 900.")
	}
	if sc.MaxConcurrencyPerEnvironment != nil && (*sc.MaxConcurrencyPerEnvironment < 1 || *sc.MaxConcurrencyPerEnvironment > 128) {
		return validationError("serviceConfig.maxConcurrencyPerEnvironment must be between 1 and 128.")
	}
	// Per AWS docs (web-functions-revisions-env-vars.md): these names are
	// reserved for Web Functions; specifying one makes Lambda reject the
	// request to create a Web Function or revision. The quotas page limits all
	// environment variables of a revision to 32 KB in aggregate.
	envBytes := 0
	for name, val := range sc.EnvironmentVariables {
		envBytes += len(name) + len(val)
	}
	if envBytes > maxEnvVarBytes {
		return validationError("serviceConfig.environmentVariables are %d bytes in aggregate; the limit is %d (32 KB).", envBytes, maxEnvVarBytes)
	}
	for _, name := range slices.Sorted(maps.Keys(sc.EnvironmentVariables)) {
		if slices.Contains(reservedEnvVars, name) {
			return validationError("The environment variable %s is reserved and can't be set.", name)
		}
	}
	if sc.EgressNetworkConnectorARN != "" && (len(sc.EgressNetworkConnectorARN) > 2048 || !connRe.MatchString(sc.EgressNetworkConnectorARN)) {
		return validationError("serviceConfig.egressNetworkConnectorArn must be a fully qualified network connector ARN, including its version.")
	}
	if kmsKeyARN != "" && (len(kmsKeyARN) < 20 || len(kmsKeyARN) > 2048 || !kmsARNRe.MatchString(kmsKeyARN)) {
		return validationError("kmsKeyArn %q is not a valid KMS key ARN.", kmsKeyARN)
	}
	if sc.TelemetryConfig != nil && sc.TelemetryConfig.LoggingConfig != nil {
		lc := sc.TelemetryConfig.LoggingConfig
		if lc.LogGroup != "" {
			if len(lc.LogGroup) > 500 || !logGrpRe.MatchString(lc.LogGroup) {
				return validationError("loggingConfig.logGroup %q is not a valid log group name.", lc.LogGroup)
			}
			// Per AWS docs (web-functions-revisions.md, "logGroup"): names that
			// start with aws/ are reserved and rejected.
			if strings.HasPrefix(strings.TrimPrefix(lc.LogGroup, "/"), "aws/") {
				return validationError("Log group names that start with aws/ are reserved.")
			}
		}
		if lc.ApplicationLogLevel != "" && !slices.Contains(appLogLevels, lc.ApplicationLogLevel) {
			return validationError("loggingConfig.applicationLogLevel must be one of: %s.", strings.Join(appLogLevels, ", "))
		}
		if lc.SystemLogLevel != "" && !slices.Contains(sysLogLevels, lc.SystemLogLevel) {
			return validationError("loggingConfig.systemLogLevel must be one of: %s.", strings.Join(sysLogLevels, ", "))
		}
	}
	return validateDescription(description)
}

func validateScaling(sc *scalingConfig) *protocol.AWSError {
	if sc != nil && sc.MaxEnvironments != nil && (*sc.MaxEnvironments < 2 || *sc.MaxEnvironments > 10000) {
		return validationError("scalingConfig.maxEnvironments must be between 2 and 10000.")
	}
	return nil
}

// validateThrottle checks rateLimit against the values the model documents:
// 0, 100 through 1000 in steps of 100, then 2000 through 10000 in steps of
// 1000.
func validateThrottle(tc *throttleCfg) *protocol.AWSError {
	if tc == nil || tc.RateLimit == nil {
		return nil
	}
	v := *tc.RateLimit
	ok := v == 0 || (v >= 100 && v <= 1000 && v%100 == 0) || (v > 1000 && v <= maxPerEndpointRateRPS && v%1000 == 0)
	if !ok {
		return validationError("throttleConfig.rateLimit must be one of 0, 100, 200, ... 1000, 2000, ... 10000.")
	}
	return nil
}

func validateEndpointEnums(typ, auth, deploy string) *protocol.AWSError {
	if !slices.Contains([]string{endpointHomeRegion, endpointMultiRegion, endpointPerRegion}, typ) {
		return validationError("endpointType must be one of HomeRegion, MultiRegion, PerRegion.")
	}
	if auth != authApplicationManaged && auth != authIam {
		return validationError("authType must be one of ApplicationManaged, IamAuth.")
	}
	if deploy != deployLatestRevision && deploy != deployDisabled {
		return validationError("autoDeploymentMode must be one of LatestRevision, Disabled.")
	}
	return nil
}

// resolveRegions applies the documented region rules: HomeRegion takes no
// regions or only the home Region; MultiRegion and PerRegion take at least one
// Region besides home, and the home Region is added when left out. The result
// lists home first.
func resolveRegions(typ, home string, in []string) ([]string, *protocol.AWSError) {
	if len(in) > 100 {
		return nil, validationError("regions can list at most 100 Regions.")
	}
	seen := map[string]bool{}
	for _, r := range in {
		if !regionRe.MatchString(r) || len(r) > 50 {
			return nil, validationError("Region %q is not a valid Region name.", r)
		}
		if seen[r] {
			return nil, validationError("regions must not contain duplicates.")
		}
		seen[r] = true
	}
	others := make([]string, 0, len(in))
	for _, r := range in {
		if r != home {
			others = append(others, r)
		}
	}
	switch typ {
	case endpointHomeRegion:
		if len(others) > 0 {
			return nil, validationError("A HomeRegion endpoint can't list Regions other than the home Region.")
		}
		return []string{home}, nil
	default:
		if len(others) == 0 {
			return nil, validationError("A %s endpoint must list at least one Region other than the home Region.", typ)
		}
		return append([]string{home}, others...), nil
	}
}

// filterable are the attributes each list operation filters on, from the
// model's documentation of ListWebFunctions, ListWebFunctionRevisions and
// ListWebFunctionEndpoints.
var (
	functionFilterNames = []string{"state"}
	revisionFilterNames = []string{"state"}
	endpointFilterNames = []string{"authType", "autoDeploymentMode", "endpointType", "state", "updateStatus"}
)

func validateFilters(filters []filter, allowed []string) *protocol.AWSError {
	if len(filters) > 10 {
		return validationError("filters can have at most 10 entries.")
	}
	for _, f := range filters {
		if !slices.Contains(allowed, f.Name) {
			return validationError("Unsupported filter name %q. Supported filter names: %s.", f.Name, strings.Join(allowed, ", "))
		}
		if len(f.Values) == 0 || len(f.Values) > 10 {
			return validationError("Filter %q must have between 1 and 10 values.", f.Name)
		}
	}
	return nil
}

// matches reports whether attrs satisfies every filter: a filter matches when
// the attribute equals any of its values, and filters are ANDed.
func matches(filters []filter, attrs map[string]string) bool {
	for _, f := range filters {
		if !slices.Contains(f.Values, attrs[f.Name]) {
			return false
		}
	}
	return true
}
