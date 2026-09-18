---
title: "Step Functions operations"
description: "Every Step Functions operation Overcast declares — 37 of 37 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - operations
  - services
  - stepfunctions
---

<!-- BEGIN overcast:capabilities -->

# Step Functions operations

All 37 listed operations are implemented. Back to [Step Functions](../stepfunctions.md).

## Summary

| Category                   | ✅ Supported |
| -------------------------- | ------------ |
| State machines             | 7            |
| Versions and aliases       | 8            |
| Executions                 | 8            |
| Map runs                   | 3            |
| Activities and task tokens | 8            |
| Tags                       | 3            |

---

## Endpoints

### State machines

| Operation                          | Status       | Notes                                                                                                                                                                   | AWS Docs                                                                                                         |
| ---------------------------------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `CreateStateMachine`               | ✅ Supported | Validates the ASL; idempotent — returns existing if name+def match; publish/versionDescription publish version 1                                                        | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateStateMachine.html)               |
| `DescribeStateMachine`             | ✅ Supported | Returns revisionId once the state machine has been updated; a version ARN describes that version (its definition, role, description and revisionId)                     | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeStateMachine.html)             |
| `ListStateMachines`                | ✅ Supported | Sorted by name; maxResults/nextToken pagination                                                                                                                         | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListStateMachines.html)                |
| `DeleteStateMachine`               | ✅ Supported | Also deletes the state machine's versions and aliases                                                                                                                   | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DeleteStateMachine.html)               |
| `UpdateStateMachine`               | ✅ Supported | Definition/roleArn/loggingConfiguration/tracingConfiguration; a change starts a new revision (revisionId); publish/versionDescription publish it as a version           | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_UpdateStateMachine.html)               |
| `DescribeStateMachineForExecution` | ✅ Supported | Reports the version's definition for an execution that ran a version (the current definition if that version was since deleted)                                         | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeStateMachineForExecution.html) |
| `ValidateStateMachineDefinition`   | ✅ Supported | The same structural validation CreateStateMachine applies, as OK/FAIL with one ERROR diagnostic (code, message, location where known); no WARNING-level static analysis | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ValidateStateMachineDefinition.html)   |

### Versions and aliases

| Operation                    | Status       | Notes                                                                                                                                      | AWS Docs                                                                                                   |
| ---------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------- |
| `PublishStateMachineVersion` | ✅ Supported | Version ARN is <stateMachineArn>:<n>, numbers never reused; idempotent per revision; revisionId (or INITIAL) mismatch is ConflictException | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_PublishStateMachineVersion.html) |
| `ListStateMachineVersions`   | ✅ Supported | Newest first; maxResults/nextToken pagination                                                                                              | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListStateMachineVersions.html)   |
| `DeleteStateMachineVersion`  | ✅ Supported | ConflictException while an alias routes to the version; deleting a missing version succeeds                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DeleteStateMachineVersion.html)  |
| `CreateStateMachineAlias`    | ✅ Supported | Alias ARN is <stateMachineArn>:<name>; 1–2 versions with weights summing to 100; idempotent on name+description+routing                    | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateStateMachineAlias.html)    |
| `DescribeStateMachineAlias`  | ✅ Supported |                                                                                                                                            | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeStateMachineAlias.html)  |
| `UpdateStateMachineAlias`    | ✅ Supported | description and/or routingConfiguration; takes effect immediately (no eventual-consistency window)                                         | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_UpdateStateMachineAlias.html)    |
| `DeleteStateMachineAlias`    | ✅ Supported |                                                                                                                                            | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DeleteStateMachineAlias.html)    |
| `ListStateMachineAliases`    | ✅ Supported | Most recent first; a version ARN lists the aliases routing to it; maxResults/nextToken pagination                                          | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListStateMachineAliases.html)    |

### Executions

| Operation             | Status       | Notes                                                                                                                                                                                                                                                                                                         | AWS Docs                                                                                            |
| --------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `StartExecution`      | ✅ Supported | Interprets the ASL; returns while the execution is RUNNING, as AWS does; accepts a version or alias ARN (an alias picks a version at random by weight); a standard workflow's RUNNING and terminal status transitions each emit a Step Functions Execution Status Change event to the default EventBridge bus | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_StartExecution.html)      |
| `StartSyncExecution`  | ✅ Supported | EXPRESS only — same interpreter, run to completion before returning; accepts a version or alias ARN; EXPRESS executions do not emit EventBridge events, matching AWS                                                                                                                                          | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_StartSyncExecution.html)  |
| `DescribeExecution`   | ✅ Supported | Real status, output, error and cause; redriveCount/redriveStatus; mapRunArn for a distributed Map child; stateMachineVersionArn/stateMachineAliasArn for an execution started through a version or alias                                                                                                      | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeExecution.html)   |
| `ListExecutions`      | ✅ Supported | statusFilter, redriveFilter, mapRunArn (a distributed Map's child executions) and maxResults/nextToken pagination; a version or alias ARN lists the executions that ran through it                                                                                                                            | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListExecutions.html)      |
| `GetExecutionHistory` | ✅ Supported | AWS's event vocabulary for every state type, with causal previousEventId linkage (each Parallel branch and Map iteration chains back to its own start event); readable while RUNNING; reverseOrder, includeExecutionData, maxResults/nextToken                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html) |
| `StopExecution`       | ✅ Supported | Interrupts a running execution; it reaches ABORTED asynchronously; a standard workflow's ABORTED transition emits a Step Functions Execution Status Change event to the default EventBridge bus                                                                                                               | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_StopExecution.html)       |
| `RedriveExecution`    | ✅ Supported | Standard executions that FAILED, TIMED_OUT or were ABORTED within 14 days; resumes at the state that did not succeed, and inside a Parallel or Map re-runs only the branches, iterations or map-run children that did not succeed; history continues after ExecutionRedriven                                  | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_RedriveExecution.html)    |
| `TestState`           | ✅ Supported | One state, alone or named from a whole definition; a single attempt (RETRIABLE / CAUGHT_ERROR); mock result or errorOutput; variables and context; DEBUG inspectionData                                                                                                                                       | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_TestState.html)           |

### Map runs

| Operation        | Status       | Notes                                                                                                                       | AWS Docs                                                                                       |
| ---------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `ListMapRuns`    | ✅ Supported |                                                                                                                             | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListMapRuns.html)    |
| `DescribeMapRun` | ✅ Supported | Live item and child-execution counts while the run is in progress; redriveCount and redriveDate once the parent is redriven | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeMapRun.html) |
| `UpdateMapRun`   | ✅ Supported | maxConcurrency and the failure tolerances; applies to children not yet started on a running Map Run                         | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_UpdateMapRun.html)   |

### Activities and task tokens

| Operation           | Status       | Notes                                                                                          | AWS Docs                                                                                          |
| ------------------- | ------------ | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `CreateActivity`    | ✅ Supported | Idempotent on the name, as AWS documents                                                       | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateActivity.html)    |
| `DescribeActivity`  | ✅ Supported |                                                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeActivity.html)  |
| `DeleteActivity`    | ✅ Supported |                                                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DeleteActivity.html)    |
| `ListActivities`    | ✅ Supported | maxResults/nextToken pagination                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListActivities.html)    |
| `GetActivityTask`   | ✅ Supported | Long-polls up to 60 seconds for a scheduled task                                               | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetActivityTask.html)   |
| `SendTaskSuccess`   | ✅ Supported | Completes an activity task or a .waitForTaskToken task; tokens live while their execution does | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskSuccess.html)   |
| `SendTaskFailure`   | ✅ Supported |                                                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskFailure.html)   |
| `SendTaskHeartbeat` | ✅ Supported | Resets HeartbeatSeconds; a missed heartbeat is States.HeartbeatTimeout                         | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskHeartbeat.html) |

### Tags

| Operation             | Status       | Notes                                                                          | AWS Docs                                                                                            |
| --------------------- | ------------ | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| `TagResource`         | ✅ Supported | State machines and activities; a version or alias ARN is InvalidArn, as on AWS | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_TagResource.html)         |
| `UntagResource`       | ✅ Supported |                                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_UntagResource.html)       |
| `ListTagsForResource` | ✅ Supported |                                                                                | [docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListTagsForResource.html) |

## Related

- [Step Functions](../stepfunctions.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
