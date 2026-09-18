---
title: "Step Functions — AWS Step Functions"
description: "Quick start, the ASL the interpreter runs in both query languages — states, data flow, variables, error handling and integrations — and where it still differs from AWS."
section: "Service Reference"
tags:
  - docs
  - services
  - stepfunctions
  - workflows
---

# Step Functions — AWS Step Functions

A real Amazon States Language interpreter — executions run the definition, call
other emulated services and record a state-by-state history.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

SM=$(aws stepfunctions create-state-machine --name hello \
  --role-arn arn:aws:iam::000000000000:role/sfn \
  --definition '{"StartAt":"Greet","States":{"Greet":{"Type":"Pass","Result":"hi","End":true}}}' \
  --query stateMachineArn --output text)

EX=$(aws stepfunctions start-execution --state-machine-arn "$SM" \
  --input '{}' --query executionArn --output text)

aws stepfunctions describe-execution --execution-arn "$EX"
aws stepfunctions get-execution-history --execution-arn "$EX"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

> [!IMPORTANT]
> Anything Overcast cannot interpret **fails the execution loudly** — never a
> silent pass-through, and never a fake `SUCCEEDED`. The error is
> `States.Runtime` and the `cause` names the feature. `States.Runtime` is
> neither retriable nor catchable, matching AWS, so a `Catch` on
> `States.ALL` cannot swallow an Overcast gap.

## What works

| Area              | Behaviour                                                                                                         |
| ----------------- | ----------------------------------------------------------------------------------------------------------------- |
| State types       | All eight. `Parallel` branches and `Map` iterations run concurrently; `MaxConcurrency` is honoured                |
| Query languages   | JSONPath, and JSONata per definition or per state: `Arguments`, `Output`, `Items`, `Condition`, `$states`         |
| Variables         | `Assign` in both languages, scoped to Parallel branches and Map iterations as on AWS                              |
| Data flow         | Every JSONPath field, full JSONPath (wildcards, filters, slices) and all 18 intrinsic functions                   |
| Error handling    | `Retry` (including `MaxDelaySeconds` and `JitterStrategy`) and `Catch`; errors keep their names out of Parallel and Map |
| Timeouts          | `TimeoutSeconds` and `HeartbeatSeconds` really bound a Task and raise `States.Timeout` / `States.HeartbeatTimeout` |
| Callbacks         | `.waitForTaskToken`, activities, `SendTaskSuccess` / `SendTaskFailure` / `SendTaskHeartbeat`                      |
| Distributed Map   | Child executions, `ItemReader` (S3 JSON, JSONL, CSV, listing), `ItemBatcher`, failure tolerance, `ResultWriter`, map runs |
| Integrations      | Lambda, SQS, SNS, DynamoDB, EventBridge, nested executions, and `aws-sdk:` for JSON-protocol services and S3        |
| Executions        | `StartExecution` returns while `RUNNING`; `StopExecution`, `RedriveExecution`, `TestState`, versions and aliases  |
| History           | AWS's event vocabulary with causal `previousEventId` links — see [Execution history](stepfunctions/execution-history.md) |

Task states dispatch through Overcast's own router, so a workflow step runs
exactly the handler an SDK call would — there is no second code path that could
drift from the service it targets.

## Differences from AWS

| Area                 | On AWS                        | Overcast                                                                                  |
| -------------------- | ----------------------------- | ----------------------------------------------------------------------------------------- |
| JSONata engine       | JSONata 2.x                   | JSONata 1.5 plus AWS's added functions; 2.x-only functions fail with `States.QueryEvaluationError` |
| `aws-sdk:` integrations | Every service              | Services that speak AWS JSON, and S3's object actions; Query and REST services fail       |
| Optimized integrations | ~200 services               | Lambda, SQS, SNS, DynamoDB, EventBridge and Step Functions                                |
| `ItemReader`         | JSON, JSONL, CSV, manifests, Parquet | JSON, JSONL, CSV and S3 listings                                                   |
| Express workflows    | No history, no `ListExecutions` | Recorded like Standard ones, so they can be inspected                                   |

The full list, with the reasons, is on
[Step Functions limitations](stepfunctions/limitations.md).

## Gotchas

> [!WARNING]
> `OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT` (default `15m`) is a runaway guard,
> not a request timeout — it never sits on the wire, so ordinary `Wait` states are
> unaffected. A state machine's own top-level `TimeoutSeconds` can lower the
> budget but never raise it. Exceeding it ends the execution `TIMED_OUT` with
> `States.Timeout`, which is also what stops a non-terminating `Choice` loop
> (alongside the 25,000-event history cap AWS itself applies).

A `Task`'s own `TimeoutSeconds` is a different thing: it bounds that attempt, and
an uncaught task timeout is a `FAILED` execution rather than a `TIMED_OUT` one, as
on AWS. A local cold start can be slower than AWS's, so a tight
`TimeoutSeconds` may fire here where it would not in the cloud.

Task tokens and activity tasks live in memory with the execution waiting on
them: restarting Overcast ends that execution, so a token issued before the
restart is `TaskDoesNotExist` afterwards.

State names must be unique across the whole state machine, nested Parallel
branches and Map processors included — `CreateStateMachine` rejects a duplicate
with `InvalidDefinition`, as AWS does.

## In the console

Each state machine is drawn as a flow diagram laid out from its definition:
Parallel branches and a Map's item processor sit in lanes inside their state,
Choice edges carry their conditions, and Catch edges are dashed. A state
machine that has never run opens on its diagram; select a state to see what it
calls, where it goes next, and how it retries and catches.

An execution plays on the same diagram while it runs. The running state pulses,
the paths taken light up, retries and repeated runs are badged, and a Map's
progress strip lets you view one iteration at a time. Selecting a state shows
each of its runs with input, output, error and events. Below the diagram the
history also reads as a timeline of state runs by branch and iteration, and as
an event list you can filter by category and search. For a distributed Map, the
Map state lists its child executions, each with its own live view.

The diagram exports as SVG or PNG, with the execution's statuses when one is on
screen. Executions can be stopped, redriven after a failure, or run again with
the same input.

<!-- BEGIN overcast:capabilities -->

## Operations

All 37 listed operations are implemented.
Per-operation status, notes and AWS API links: [Step Functions operations](stepfunctions/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Step Functions limitations](./stepfunctions/limitations.md)
- [Step Functions execution history](./stepfunctions/execution-history.md)
- [Lambda](./lambda.md) — the most common `Task` target
- [EventBridge](./eventbridge.md) and [Scheduler](./scheduler.md) — what starts executions on a schedule
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/step-functions/latest/apireference/Welcome.html)
