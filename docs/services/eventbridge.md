---
title: "EventBridge — Amazon EventBridge"
description: "Quick start, the eight target types delivery reaches, input shaping, retries and dead-lettering, and which event-pattern match types a rule can filter on."
section: "Service Reference"
tags:
  - docs
  - eventbridge
  - events
  - services
---

# EventBridge — Amazon EventBridge

Buses, rules and targets, with matched events delivered in-process to eight
target types, filtered by every event-pattern match type bar three.

**Status:** ⚠️ Partial

## Quick start

Route an event to an SQS queue:

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

QUEUE=$(aws sqs create-queue --queue-name orders --query QueueUrl --output text)
ARN=$(aws sqs get-queue-attributes --queue-url "$QUEUE" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

aws events put-rule --name orders --event-pattern '{"source":["app.orders"]}'
aws events put-targets --rule orders --targets "Id=1,Arn=$ARN"
aws events put-events --entries \
  '[{"Source":"app.orders","DetailType":"OrderPlaced","Detail":"{\"id\":1}"}]'

aws sqs receive-message --queue-url "$QUEUE"
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Buses and rules | Bus, rule, target and tag CRUD. `CreateEventBus` stores `Description`, `DeadLetterConfig` and `KmsKeyIdentifier`; `DescribeEventBus` answers for `default` whether or not it was created. |
| Bus permissions | `PutPermission` grants a statement, individually or as a whole `Policy` document that replaces the bus's policy; `RemovePermission` revokes one by `StatementId` or clears the policy with `RemoveAllPermissions`. `DescribeEventBus` reports the result as `Policy`. Stored, not enforced — like the rest of Overcast's IAM policies, nothing consults it to authorize a cross-account `PutEvents` call. |
| Event patterns | Exact values plus `prefix`, `suffix`, `exists`, `equals-ignore-case`, `numeric` and `anything-but`. |
| Rule validation | `PutRule` refuses a rule with no trigger, one naming an event bus that does not exist, and one whose pattern cannot be evaluated. |
| Rule deletion | `DeleteRule` refuses a rule that still has targets, so `RemoveTargets` comes first. CloudFormation detaches them for you. |
| Delivery | `PutEvents` evaluates every rule on the bus and delivers matches to Lambda, SQS, SNS, Step Functions, Kinesis, Firehose, ECS `RunTask` and another event bus. |
| Scheduled rules | `rate(...)` and AWS's six-field `cron(...)` fire on an in-process clock, through the same target dispatcher. |
| Input shaping | `Input`, `InputPath` and `InputTransformer`, at most one per target, over the JSONPath subset AWS uses (`$`, dotted members, array indexing). |
| Retries and dead-lettering | `RetryPolicy.MaximumRetryAttempts`, `MaximumEventAgeInSeconds` measured from the envelope's `time`, and a `DeadLetterConfig` SQS queue. |
| Service-originated events | CloudWatch alarm transitions, S3 object created/deleted, EC2 and ECS state changes, Auto Scaling launch and terminate events, and Step Functions execution status changes publish onto the default bus. |

`InputTransformer` templates may reference `<aws.events.rule-name>`,
`<aws.events.rule-arn>`, `<aws.events.event.json>` and
`<aws.events.event.ingestion-time>`. Recent per-target outcomes — delivered,
retried, dead-lettered, dropped — are readable at
`GET /_overcast/eventbridge/deliveries`, an emulator-only endpoint backed by an
in-memory ring that does not survive a restart.

## Differences from AWS

| Area                               | On AWS                                                                      | Overcast                                        |
| ---------------------------------- | --------------------------------------------------------------------------- | ----------------------------------------------- |
| Event patterns                     | Also has `cidr`, `wildcard` and `$or`                                       | Those three are refused by `PutRule`            |
| Target types                       | ~20                                                                         | Eight; anything else is refused by `PutTargets` |
| Retry timing                       | Exponential backoff over up to 24 hours                                     | Immediate, capped at 5 retries                  |
| Bus-to-bus forwarding              | Independent delivery, no hop budget                                         | Nested in-process call, capped at 4 hops        |
| Archives, replay, API destinations | Supported                                                                   | Not implemented                                 |
| Service-originated events          | Substantially more                                                          | Six publishers                                  |

A target ARN naming an unsupported service comes back in `PutTargets`'s
`FailedEntries` with `ErrorCode: UnsupportedTargetType`, rather than being
stored as a rule that provisions cleanly and never fires.

## Gotchas

> [!WARNING]
> **`cidr`, `wildcard` and `$or` are refused, not ignored.** `PutRule` and
> `TestEventPattern` answer `InvalidEventPatternException` naming the match
> type, so a rule that could never fire is never stored. Rewrite the clause, or
> filter on what the target receives.

`PutRule` applies the same check to a pattern's shape: a field whose value is a
bare string rather than an array, and a match type EventBridge does not define,
are both `InvalidEventPatternException` rather than a rule that provisions and
stays quiet.

> [!IMPORTANT]
> **Remove a rule's targets before deleting it.** `DeleteRule` answers
> `ValidationException` while any target is attached, as AWS does. `Force` does
> not help — it is the managed-rule escape, and AWS ignores it for every other
> rule. Deleting an `AWS::Events::Rule` through CloudFormation needs nothing
> extra; the stack detaches the targets it attached.

Scheduled rules reach the same targets on an in-process clock, and are as strict
about their expressions as AWS is.

> [!IMPORTANT]
> A `cron(...)` expression takes AWS's **six** fields, and day-of-week is 1-7
> from Sunday. The five-field Unix form is refused, as it is on AWS: every five
> minutes is `cron(*/5 * * * ? *)`. `L`, `LW`, `<day>W`, `<day>L`, `<day>#<n>`
> and the three-letter month and day names all work.

<!-- BEGIN overcast:capabilities -->

## Operations

20 of 31 listed operations are implemented.
Per-operation status, notes and AWS API links: [EventBridge operations](eventbridge/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Scheduler](./scheduler.md) — the same target dispatcher, on a clock
- [Pipes](./pipes.md) — point-to-point source → target wiring
- [Step Functions](./stepfunctions.md) — one of the eight target types
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/eventbridge/latest/APIReference/Welcome.html)
