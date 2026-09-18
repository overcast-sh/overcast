---
title: "Step Functions limitations"
description: "Every known difference between Overcast's Step Functions and AWS's, and what each one means for a workflow."
section: "Service Reference"
tags:
  - docs
  - services
  - stepfunctions
---

# Step Functions limitations

Where Overcast's Step Functions behaves differently from AWS's. Anything not
listed here is meant to behave as AWS documents it; a difference that is not
listed is a bug.

## Integrations

| Area                     | On AWS                                   | Overcast                                                                  |
| ------------------------ | ---------------------------------------- | ------------------------------------------------------------------------- |
| Optimized integrations   | About 200 services                       | Lambda, SQS `sendMessage`, SNS `publish`, DynamoDB item actions, EventBridge `putEvents`, Step Functions `startExecution` |
| `aws-sdk:` integrations  | Every service and action                 | AWS JSON services, and S3 `getObject`, `putObject`, `headObject`, `deleteObject`, `listObjectsV2` |
| `.sync` pattern          | Many integrations                        | `states:startExecution` only                                              |
| `Credentials`            | Assumes the named role                   | Ignored: Overcast has one account                                         |

An `aws-sdk:` call against a service whose JSON members are camelCase (Step
Functions, ECS, …) has its result converted to PascalCase, as AWS does. The
conversion does not know which members are maps of user data, so map keys in
those responses are capitalised too. Services that already answer in PascalCase
— DynamoDB, SQS, Kinesis — are passed through untouched.

## Query languages

JSONata is evaluated by a JSONata 2.2 engine; AWS runs JSONata 2.0.6. The
whole 2.x function library and syntax behave as on AWS, AWS's own functions
(`$partition`, `$range`, `$hash`, `$random`, `$uuid`, `$parse`) are provided,
and `$eval` fails the state with `States.QueryEvaluationError`, as AWS does not
offer it. `$now()` and `$millis()` read Overcast's clock. The `??` and `?:`
operators arrived in JSONata 2.1, so Overcast accepts them where AWS may not.
Regular expressions use Go's syntax, which has no lookahead, lookbehind or
backreferences inside the pattern.

JSONPath filters support comparisons, `&&`, `||`, `!` and existence tests, not
the regex (`=~`) or `in`/`nin` operators. A multi-name union (`$['a','b']`)
returns an array of the values rather than an object. `States.JsonToString`
sorts object keys, where AWS keeps the input's order.

An unknown intrinsic function fails at run time with `States.IntrinsicFailure`;
AWS rejects it when the state machine is created.

## Map

| Area                      | On AWS                                   | Overcast                                      |
| ------------------------- | ---------------------------------------- | --------------------------------------------- |
| `ItemReader` input types  | JSON, JSONL, CSV, S3 inventory manifests, Parquet | JSON, JSONL, CSV, and S3 listings     |
| Distributed concurrency   | Up to 10,000 child executions            | Up to 1,000 at once                           |
| Child execution type      | `EXPRESS` children keep no history       | Both types keep a history, so they can be read |
| Children awaiting a map run redrive | Report `PENDING_REDRIVE`       | Keep their last status until relaunched; the map run's `pendingRedrive` counts them |

## Executions

| Area                       | On AWS                                            | Overcast                                                   |
| -------------------------- | ------------------------------------------------- | ---------------------------------------------------------- |
| Express executions         | No `DescribeExecution`, history in CloudWatch Logs | Described, listed and recorded like Standard executions   |
| Task tokens                | Survive for a year                                | Live as long as the execution's process                    |
| Definition an execution ran | Kept with the execution                          | Kept for a version; an unversioned execution reports and redrives the current definition |
| Logging and tracing        | Delivered to CloudWatch Logs and X-Ray             | Configuration is stored and echoed, nothing is delivered   |
| `TestState` inspection     | `TRACE` adds the HTTP request and response        | `TRACE` reports what `DEBUG` does                          |

A redrive resumes inside a failed Parallel or Map as AWS does: branches and
iterations that succeeded keep their outputs and are not run again, and the
rest resume at the state they stopped in. An inline Map works out its items
again from the input it was entered with; if they now number differently —
an `Items` expression using `$random` or `$uuid`, say — the whole Map runs
again. A distributed Map's child executions are redriven by redriving the
parent; `RedriveExecution` on a child itself is refused with
`ExecutionNotRedrivable`.

## Versions, aliases and validation

| Area                                  | On AWS                                        | Overcast                                          |
| ------------------------------------- | --------------------------------------------- | ------------------------------------------------- |
| `ValidateStateMachineDefinition`      | Every error, plus WARNING-level analysis      | The first ERROR only; `truncated` is never true   |
| Alias updates                         | Eventually consistent                         | Take effect immediately                           |
| CloudFormation `DeploymentPreference` | Linear or canary shifting with alarm rollback | An immediate, all-at-once shift                   |

`GetExecutionHistory` for a Standard execution caps at AWS's 25,000 events, and
an execution also stops at `OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT`
(default 15 minutes) — see the [landing page's gotchas](../stepfunctions.md#gotchas).

## Related

- [Step Functions](../stepfunctions.md)
- [Step Functions execution history](./execution-history.md)
- [Step Functions operations](./operations.md)
