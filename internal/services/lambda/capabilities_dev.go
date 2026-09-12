//go:build dev

package lambda

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Function management
		capabilities.Capability{Service: "lambda", Operation: "ListFunctions", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Returns all stored functions; empty list if none"},
		capabilities.Capability{Service: "lambda", Operation: "CreateFunction", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Stores metadata; validates Runtime against the pinned model and refuses runtimes past AWS's block-create date; a modeled runtime with no execution image returns 501 and persists nothing; auto-creates CWL log group; VpcConfig supported; PackageType=Image runs the image Code.ImageUri names, resolving an `{account}.dkr.ecr.{region}.amazonaws.com` URI to the registry the emulated ECR serves, with ImageConfig EntryPoint/Command/WorkingDirectory overriding the image's own; LoggingConfig honoured for both Text and JSON LogFormat, with ApplicationLogLevel/SystemLogLevel filtering in JSON mode; FileSystemConfigs round-trip (EFS mounts in live mode; S3 Files runtime mounts tracked in #647); TracingConfig, EphemeralStorage and KMSKeyArn are validated, stored and echoed but change nothing — X-Ray tracing is not emulated, the ephemeral storage size is not enforced on the container, and environment variables are not encrypted at rest; DeadLetterConfig is validated, stored, echoed and honoured — a failed asynchronous invocation is delivered to the SQS queue or SNS topic it names; Code.S3ObjectVersion fetches that version of the object; an unfetchable Code.S3Bucket/S3Key/S3ObjectVersion fails the create and persists nothing; answers Pending and reaches Active once the image is pulled — Failed with the reason when it cannot be — so `aws lambda wait function-active` returns, and LastUpdateStatus is first set when the create completes, as on AWS"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteFunction", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Qualifier deletes only that published version, with its qualified policy and provisioned concurrency; refuses $LATEST and versions an alias references; unqualified deletes the function"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunction", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Returns FunctionConfiguration + Code location block; State/StateReason/StateReasonCode and LastUpdateStatus/LastUpdateStatusReason/LastUpdateStatusReasonCode both report, so `aws lambda wait function-active` and `wait function-updated` return; TracingConfig and EphemeralStorage always present, defaulting to PassThrough and 512 MB as on AWS; DeadLetterConfig reported only when the function has one"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunctionConfiguration", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Returns FunctionConfiguration only (no Code block); State/StateReason/StateReasonCode and LastUpdateStatus/LastUpdateStatusReason/LastUpdateStatusReasonCode both report, which is what the FunctionActive and FunctionUpdated waiters poll; TracingConfig and EphemeralStorage always present, defaulting to PassThrough and 512 MB as on AWS; DeadLetterConfig reported only when the function has one"},
		capabilities.Capability{Service: "lambda", Operation: "UpdateFunctionCode", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Updates code zip or image URI and Architectures; a new image URI is pulled and the warm environment retired; S3ObjectVersion fetches that version of the object, and a version that cannot be read leaves the function untouched; generates new RevisionId; reports LastUpdateStatus, so `aws lambda wait function-updated` returns — a zip deployment answers Successful because it is already applied, while a new image URI answers InProgress and settles when the pull does, to Successful or to Failed with ImageAccessDenied/InvalidImage/InternalError, and a second update or a PublishVersion inside that window is refused with ResourceConflictException"},
		capabilities.Capability{Service: "lambda", Operation: "UpdateFunctionConfiguration", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Presence-aware updates for supported configuration, including TracingConfig, EphemeralStorage and KMSKeyArn (recorded and echoed, never enforced) and DeadLetterConfig (an explicit empty TargetArn removes the target); a Runtime past AWS's block-update date is refused; LoggingConfig with explicit members applies, including LogFormat JSON, but an explicitly empty LoggingConfig object still returns 501 because AWS's semantics for it are uncaptured (#660); unsupported advanced fields fail before mutation; reports LastUpdateStatus, always Successful because the new configuration is stored before the call answers"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunctionCodeSigningConfig", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Returns the associated config; ResourceNotFoundException when the function has none"},
		capabilities.Capability{Service: "lambda", Operation: "PutFunctionCodeSigningConfig", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Stores the association and validates the ARN shape; signature validation is not emulated"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteFunctionCodeSigningConfig", Category: "Function management",
			Status: capabilities.StatusSupported, Notes: "Removes the association; idempotent"},

		// Resource-based policies
		capabilities.Capability{Service: "lambda", Operation: "AddPermission", Category: "Resource-based policies",
			Status: capabilities.StatusPartial, Notes: "Appends a validated statement to the function's, version's or alias's policy, rendering Principal, Resource and the ArnLike/StringEquals conditions AWS builds from SourceArn, SourceAccount, EventSourceToken, PrincipalOrgID and the function-URL members; duplicate statement IDs answer ResourceConflictException, a stale RevisionId PreconditionFailedException, a policy over 20 KB PolicyLengthExceededException, and Qualifier=$LATEST is refused as it is on AWS; statements are consulted at invoke time only for service-originated invocations and only when OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY is set (#629) — a direct client Invoke is never authorised against them, because credentials are accepted without being validated"},
		capabilities.Capability{Service: "lambda", Operation: "GetPolicy", Category: "Resource-based policies",
			Status: capabilities.StatusSupported, Notes: "Returns the stored AWS policy document and revision ID, for the function, a version or an alias"},
		capabilities.Capability{Service: "lambda", Operation: "RemovePermission", Category: "Resource-based policies",
			Status: capabilities.StatusSupported, Notes: "Removes a statement by ID; supports revision preconditions"},
		capabilities.Capability{Service: "lambda", Operation: "GetResourcePolicy", Category: "Resource-based policies",
			Status: capabilities.StatusSupported, Notes: "Returns the same policy document and revision ID GetPolicy does, addressed by function, version or alias ARN; ResourceNotFoundException when the resource carries no policy"},
		capabilities.Capability{Service: "lambda", Operation: "PutResourcePolicy", Category: "Resource-based policies",
			Status: capabilities.StatusPartial, Notes: "Replaces the whole policy, statements AddPermission wrote included, and preserves the full IAM statement grammar — explicit Deny, list-valued Action/Resource/Principal, arbitrary condition keys — so a document survives a Put/Get round trip unchanged; a stale RevisionId answers PreconditionFailedException and a policy over 20 KB PolicyLengthExceededException; a statement allowing every principal with no condition is refused with PublicPolicyException, unconditionally, because there is no PutPublicAccessBlockConfig here to relax it with; statements are consulted at invoke time only for service-originated invocations and only when OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY is set (#629)"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteResourcePolicy", Category: "Resource-based policies",
			Status: capabilities.StatusSupported, Notes: "Deletes the whole policy, statements added by AddPermission included; the RevisionId query parameter is a precondition; ResourceNotFoundException when there is no policy to delete"},

		// Code signing configurations
		capabilities.Capability{Service: "lambda", Operation: "CreateCodeSigningConfig", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "Stored as a real resource; AllowedPublishers required, UntrustedArtifactOnDeployment defaults to Warn"},
		capabilities.Capability{Service: "lambda", Operation: "GetCodeSigningConfig", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "Returns the stored configuration"},
		capabilities.Capability{Service: "lambda", Operation: "UpdateCodeSigningConfig", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "Partial update; omitted members keep their stored value"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteCodeSigningConfig", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "ResourceConflictException while a function still references it"},
		capabilities.Capability{Service: "lambda", Operation: "ListCodeSigningConfigs", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "Region-scoped; pagination not implemented"},
		capabilities.Capability{Service: "lambda", Operation: "ListFunctionsByCodeSigningConfig", Category: "Code signing",
			Status: capabilities.StatusSupported, Notes: "Returns the ARNs of functions referencing the configuration"},

		// Invocation
		capabilities.Capability{Service: "lambda", Operation: "Invoke", Category: "Invocation",
			Status: capabilities.StatusSupported, Notes: "Container-based execution via Docker; falls back to stub when Docker unavailable; under LogFormat JSON the START/END/REPORT lines become Telemetry-API-shaped platform.start, platform.runtimeDone and platform.report records, filtered by SystemLogLevel, and function output is filtered by ApplicationLogLevel; the in-container init also reports the INIT phase as platform.initStart, platform.initRuntimeDone and platform.initReport, ordered against the phase's own output, and Telemetry/Logs API subscribers receive every platform record in either log format — subscriptions via the Logs API (2020-08-15) and the Telemetry API (2022-07-01, schemaVersion-aware, with the documented cross-API exclusivity), invocation records carrying the runtime's real X-Amzn-Trace-Id, platform.runtimeDone metrics and its responseLatency span measured by the in-container init, errorType Runtime.ExitError on a crashed runtime's records, deliveries batched per the subscription's buffering configuration, and a lost delivery reported to its subscriber as platform.logsDropped; an InvocationType=Event invocation whose function errors is retried per the function's FunctionEventInvokeConfig (AWS's default of twice, waiting AWS's one minute then two, when unconfigured) and then delivered to its on-failure destination and its DeadLetterConfig target; MaximumEventAgeInSeconds is measured from acceptance and discards the event before the next attempt rather than after it, but the resulting record's condition reads RetriesExhausted because AWS does not document a distinct value for an aged-out event; records AWS/Lambda CloudWatch metrics (Invocations, Errors, Duration, Throttles, ConcurrentExecutions) at this outcome boundary for every invocation mechanism (sync, async, function URLs, event source mappings); DryRun never invokes and so never records; Resource/ExecutedVersion metric dimensions are not recorded yet; a direct Invoke is never authorised against the function's resource-based policy, which gates only the invocations Overcast originates for another service and only under OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY (#629); emulator-only and off by default, a function tagged overcast:debug=true under OVERCAST_LAMBDA_DEBUGGER=true gets a step debugger port, and while a debugger client is attached the invocation clock is suspended per OVERCAST_DEBUGGER_TIMEOUT (attached, paused or strict, which keeps AWS's timeout) — the one behavioural divergence, never observable without a debugger attached (#1939); the console's Code tab can open that same session itself, over a WebSocket bridge on the emulator's own port, and counts as one such client while it does; a function also tagged overcast:debug-wait=true has each invocation that finds no client attached held, after INIT and before the event is dispatched, until one attaches plus a 750 ms settle, bounded by OVERCAST_DEBUGGER_WAIT_TIMEOUT (120s) after which it runs anyway with a WARN — the function's clock and its Lambda-Runtime-Deadline-Ms start at dispatch, so the hold spends none of the budget; never under the strict policy, and never observable without the tag (#1944)"},
		capabilities.Capability{Service: "lambda", Operation: "InvokeAsync", Category: "Invocation",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "lambda", Operation: "InvokeWithResponseStream", Category: "Invocation",
			Status: capabilities.StatusSupported, Notes: "Invokes synchronously, wraps result in AWS event stream binary encoding (PayloadChunk → InvokeComplete); RequestResponse only"},

		// Aliases & versions
		capabilities.Capability{Service: "lambda", Operation: "PublishVersion", Category: "Aliases & versions",
			Status: capabilities.StatusSupported, Notes: "Immutable snapshot of function config, reporting LastUpdateStatus Successful because nothing can update it; version numbers are monotonically incrementing integers; refused with ResourceConflictException while an update is still in progress"},
		capabilities.Capability{Service: "lambda", Operation: "ListVersionsByFunction", Category: "Aliases & versions",
			Status: capabilities.StatusSupported, Notes: "Always includes `$LATEST` as first entry"},
		capabilities.Capability{Service: "lambda", Operation: "CreateAlias", Category: "Aliases & versions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "UpdateAlias", Category: "Aliases & versions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "DeleteAlias", Category: "Aliases & versions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "GetAlias", Category: "Aliases & versions",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "ListAliases", Category: "Aliases & versions",
			Status: capabilities.StatusSupported},

		// Function URLs
		capabilities.Capability{Service: "lambda", Operation: "CreateFunctionUrlConfig", Category: "Function URLs",
			Status: capabilities.StatusSupported, Notes: "FunctionUrl always echoes the caller's Host (see docs/networking.md); AuthType stored but never enforced"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunctionUrlConfig", Category: "Function URLs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "UpdateFunctionUrlConfig", Category: "Function URLs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "DeleteFunctionUrlConfig", Category: "Function URLs",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "ListFunctionUrlConfigs", Category: "Function URLs",
			Status: capabilities.StatusSupported},

		// Event source mappings
		capabilities.Capability{Service: "lambda", Operation: "CreateEventSourceMapping", Category: "Event source mappings",
			Status: capabilities.StatusSupported, Notes: "SQS→Lambda, DynamoDB Streams→Lambda; `FunctionResponseTypes: [\"ReportBatchItemFailures\"]` is honoured; Tags are stored and readable through ListTags"},
		capabilities.Capability{Service: "lambda", Operation: "GetEventSourceMapping", Category: "Event source mappings",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "UpdateEventSourceMapping", Category: "Event source mappings",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "DeleteEventSourceMapping", Category: "Event source mappings",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "lambda", Operation: "ListEventSourceMappings", Category: "Event source mappings",
			Status: capabilities.StatusSupported, Notes: "Filters by `FunctionName` and `EventSourceArn`"},

		// Layers
		capabilities.Capability{Service: "lambda", Operation: "PublishLayerVersion", Category: "Layers",
			Status: capabilities.StatusSupported, Notes: "Increments per-layer version counter; stores zip content"},
		capabilities.Capability{Service: "lambda", Operation: "GetLayerVersion", Category: "Layers",
			Status: capabilities.StatusSupported, Notes: "Returns metadata and content info for the specified version"},
		capabilities.Capability{Service: "lambda", Operation: "ListLayerVersions", Category: "Layers",
			Status: capabilities.StatusSupported, Notes: "Returns all versions for a layer, newest first"},
		capabilities.Capability{Service: "lambda", Operation: "ListLayers", Category: "Layers",
			Status: capabilities.StatusSupported, Notes: "Returns distinct layer names with their latest matching version"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteLayerVersion", Category: "Layers",
			Status: capabilities.StatusSupported, Notes: "Removes the specific layer version; 404 if not found"},

		// Asynchronous invocation configuration
		capabilities.Capability{Service: "lambda", Operation: "PutFunctionEventInvokeConfig", Category: "Asynchronous invocation",
			Status: capabilities.StatusSupported, Notes: "Overwrites the configuration, removing members the request omits; MaximumRetryAttempts and MaximumEventAgeInSeconds are validated against AWS's ranges and honoured by the async invoke path; SQS, SNS, Lambda and EventBridge destinations receive AWS's invocation record; an S3 on-failure destination returns 501 because the record is not written to S3, and an S3 on-success destination is rejected as AWS rejects it"},
		capabilities.Capability{Service: "lambda", Operation: "UpdateFunctionEventInvokeConfig", Category: "Asynchronous invocation",
			Status: capabilities.StatusSupported, Notes: "Partial update; members the request omits keep their stored value, which is the only difference from Put"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunctionEventInvokeConfig", Category: "Asynchronous invocation",
			Status: capabilities.StatusSupported, Notes: "ResourceNotFoundException when the function has no configuration; LastModified is Unix seconds, as AWS returns for this resource"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteFunctionEventInvokeConfig", Category: "Asynchronous invocation",
			Status: capabilities.StatusSupported, Notes: "Returns 204; ResourceNotFoundException when there is no configuration to delete"},
		capabilities.Capability{Service: "lambda", Operation: "ListFunctionEventInvokeConfigs", Category: "Asynchronous invocation",
			Status: capabilities.StatusSupported, Notes: "Every qualifier's configuration for the function; MaxItems is validated but the result is a single page, so NextMarker is never returned"},

		// Concurrency & configuration
		capabilities.Capability{Service: "lambda", Operation: "PutFunctionConcurrency", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Enforced: over-limit invokes get 429 TooManyRequestsException; 0 throttles the function entirely"},
		capabilities.Capability{Service: "lambda", Operation: "GetFunctionConcurrency", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "A function with no reservation answers 200 with an empty body, as on AWS; a reservation of 0 is reported rather than omitted, since 0 is the documented way to switch a function off. ResourceNotFoundException is for the function itself"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteFunctionConcurrency", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Clears reserved concurrency limit; returns 204"},
		capabilities.Capability{Service: "lambda", Operation: "PutProvisionedConcurrencyConfig", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Pre-warms the requested execution environments in the background (IN_PROGRESS then READY); FAILED when Docker is unavailable"},
		capabilities.Capability{Service: "lambda", Operation: "GetProvisionedConcurrencyConfig", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Reports live Allocated/Available; ProvisionedConcurrencyConfigNotFoundException if not set"},
		capabilities.Capability{Service: "lambda", Operation: "DeleteProvisionedConcurrencyConfig", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Releases the reservation; the environments age out on the idle TTL rather than being killed"},
		capabilities.Capability{Service: "lambda", Operation: "ListProvisionedConcurrencyConfigs", Category: "Concurrency & configuration",
			Status: capabilities.StatusSupported, Notes: "Single page; NextMarker is always null"},

		// Tags
		capabilities.Capability{Service: "lambda", Operation: "TagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Function and event-source-mapping ARNs; merges tags; max 50; validates key/value lengths; rejects qualified ARNs and non-ARN resources"},
		capabilities.Capability{Service: "lambda", Operation: "UntagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Removes specified keys; idempotent on missing keys; tagKeys is required"},
		capabilities.Capability{Service: "lambda", Operation: "ListTags", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Returns the resource's tags; code-signing-config, capacity-provider and network-connector ARNs return 501"},
	)
}
