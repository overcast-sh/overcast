//go:build dev

package stepfunctions

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// State machines
		capabilities.Capability{Service: "stepfunctions", Operation: "CreateStateMachine", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Validates the ASL; idempotent — returns existing if name+def match; publish/versionDescription publish version 1"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeStateMachine", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Returns revisionId once the state machine has been updated; a version ARN describes that version (its definition, role, description and revisionId)"},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListStateMachines", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Sorted by name; maxResults/nextToken pagination"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DeleteStateMachine", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Also deletes the state machine's versions and aliases"},
		capabilities.Capability{Service: "stepfunctions", Operation: "UpdateStateMachine", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Definition/roleArn/loggingConfiguration/tracingConfiguration; a change starts a new revision (revisionId); publish/versionDescription publish it as a version"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeStateMachineForExecution", Category: "State machines", Status: capabilities.StatusSupported, Notes: "Reports the version's definition for an execution that ran a version (the current definition if that version was since deleted)"},
		capabilities.Capability{Service: "stepfunctions", Operation: "ValidateStateMachineDefinition", Category: "State machines", Status: capabilities.StatusSupported, Notes: "The same structural validation CreateStateMachine applies, as OK/FAIL with one ERROR diagnostic (code, message, location where known); no WARNING-level static analysis"},
		// Versions
		capabilities.Capability{Service: "stepfunctions", Operation: "PublishStateMachineVersion", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "Version ARN is <stateMachineArn>:<n>, numbers never reused; idempotent per revision; revisionId (or INITIAL) mismatch is ConflictException"},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListStateMachineVersions", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "Newest first; maxResults/nextToken pagination"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DeleteStateMachineVersion", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "ConflictException while an alias routes to the version; deleting a missing version succeeds"},
		// Aliases
		capabilities.Capability{Service: "stepfunctions", Operation: "CreateStateMachineAlias", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "Alias ARN is <stateMachineArn>:<name>; 1–2 versions with weights summing to 100; idempotent on name+description+routing"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeStateMachineAlias", Category: "Versions and aliases", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "UpdateStateMachineAlias", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "description and/or routingConfiguration; takes effect immediately (no eventual-consistency window)"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DeleteStateMachineAlias", Category: "Versions and aliases", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListStateMachineAliases", Category: "Versions and aliases", Status: capabilities.StatusSupported, Notes: "Most recent first; a version ARN lists the aliases routing to it; maxResults/nextToken pagination"},
		// Executions
		capabilities.Capability{Service: "stepfunctions", Operation: "StartExecution", Category: "Executions", Status: capabilities.StatusSupported, Notes: "Interprets the ASL; returns while the execution is RUNNING, as AWS does; accepts a version or alias ARN (an alias picks a version at random by weight); a standard workflow's RUNNING and terminal status transitions each emit a Step Functions Execution Status Change event to the default EventBridge bus"},
		capabilities.Capability{Service: "stepfunctions", Operation: "StartSyncExecution", Category: "Executions", Status: capabilities.StatusSupported, Notes: "EXPRESS only — same interpreter, run to completion before returning; accepts a version or alias ARN; EXPRESS executions do not emit EventBridge events, matching AWS"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeExecution", Category: "Executions", Status: capabilities.StatusSupported, Notes: "Real status, output, error and cause; redriveCount/redriveStatus; mapRunArn for a distributed Map child; stateMachineVersionArn/stateMachineAliasArn for an execution started through a version or alias"},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListExecutions", Category: "Executions", Status: capabilities.StatusSupported, Notes: "statusFilter, redriveFilter, mapRunArn (a distributed Map's child executions) and maxResults/nextToken pagination; a version or alias ARN lists the executions that ran through it"},
		capabilities.Capability{Service: "stepfunctions", Operation: "GetExecutionHistory", Category: "Executions", Status: capabilities.StatusSupported, Notes: "AWS's event vocabulary for every state type, with causal previousEventId linkage (each Parallel branch and Map iteration chains back to its own start event); readable while RUNNING; reverseOrder, includeExecutionData, maxResults/nextToken"},
		capabilities.Capability{Service: "stepfunctions", Operation: "StopExecution", Category: "Executions", Status: capabilities.StatusSupported, Notes: "Interrupts a running execution; it reaches ABORTED asynchronously; a standard workflow's ABORTED transition emits a Step Functions Execution Status Change event to the default EventBridge bus"},
		capabilities.Capability{Service: "stepfunctions", Operation: "RedriveExecution", Category: "Executions", Status: capabilities.StatusSupported, Notes: "Standard executions that FAILED, TIMED_OUT or were ABORTED within 14 days; resumes at the state that did not succeed, and inside a Parallel or Map re-runs only the branches, iterations or map-run children that did not succeed; history continues after ExecutionRedriven"},
		capabilities.Capability{Service: "stepfunctions", Operation: "TestState", Category: "Executions", Status: capabilities.StatusSupported, Notes: "One state, alone or named from a whole definition; a single attempt (RETRIABLE / CAUGHT_ERROR); mock result or errorOutput; variables and context; DEBUG inspectionData"},
		// Map runs
		capabilities.Capability{Service: "stepfunctions", Operation: "ListMapRuns", Category: "Map runs", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeMapRun", Category: "Map runs", Status: capabilities.StatusSupported, Notes: "Live item and child-execution counts while the run is in progress; redriveCount and redriveDate once the parent is redriven"},
		capabilities.Capability{Service: "stepfunctions", Operation: "UpdateMapRun", Category: "Map runs", Status: capabilities.StatusSupported, Notes: "maxConcurrency and the failure tolerances; applies to children not yet started on a running Map Run"},
		// Activities and callbacks
		capabilities.Capability{Service: "stepfunctions", Operation: "CreateActivity", Category: "Activities and task tokens", Status: capabilities.StatusSupported, Notes: "Idempotent on the name, as AWS documents"},
		capabilities.Capability{Service: "stepfunctions", Operation: "DescribeActivity", Category: "Activities and task tokens", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "DeleteActivity", Category: "Activities and task tokens", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListActivities", Category: "Activities and task tokens", Status: capabilities.StatusSupported, Notes: "maxResults/nextToken pagination"},
		capabilities.Capability{Service: "stepfunctions", Operation: "GetActivityTask", Category: "Activities and task tokens", Status: capabilities.StatusSupported, Notes: "Long-polls up to 60 seconds for a scheduled task"},
		capabilities.Capability{Service: "stepfunctions", Operation: "SendTaskSuccess", Category: "Activities and task tokens", Status: capabilities.StatusSupported, Notes: "Completes an activity task or a .waitForTaskToken task; tokens live while their execution does"},
		capabilities.Capability{Service: "stepfunctions", Operation: "SendTaskFailure", Category: "Activities and task tokens", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "SendTaskHeartbeat", Category: "Activities and task tokens", Status: capabilities.StatusSupported, Notes: "Resets HeartbeatSeconds; a missed heartbeat is States.HeartbeatTimeout"},
		// Tags
		capabilities.Capability{Service: "stepfunctions", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "State machines and activities; a version or alias ARN is InvalidArn, as on AWS"},
		capabilities.Capability{Service: "stepfunctions", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "stepfunctions", Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported},
	)
}
