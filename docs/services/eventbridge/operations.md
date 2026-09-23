---
title: "EventBridge operations"
description: "Every EventBridge operation Overcast declares — 20 of 31 implemented — with status, behaviour notes and a link to the AWS API reference for each."
section: "Service Reference"
tags:
  - docs
  - eventbridge
  - operations
  - services
---

<!-- BEGIN overcast:capabilities -->

# EventBridge operations

20 of 31 listed operations are implemented. Back to [EventBridge](../eventbridge.md).

## Summary

| Category    | ✅ Supported | ❌ Unsupported |
| ----------- | ------------ | -------------- |
| Event buses | 6            |                |
| Rules       | 7            |                |
| Targets     | 3            |                |
| Events      | 1            |                |
| Tags        | 3            |                |
| Archives    |              | 4              |
| Replays     |              | 3              |
| Connections |              | 4              |

---

## Endpoints

### Event buses

| Operation          | Status       | Notes                                                                                                                                                                                                            | AWS Docs                                                                                      |
| ------------------ | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `CreateEventBus`   | ✅ Supported | Creates a custom event bus; stores Description, DeadLetterConfig and KmsKeyIdentifier                                                                                                                            | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_CreateEventBus.html)   |
| `DescribeEventBus` | ✅ Supported | Returns bus details including Description, DeadLetterConfig, KmsKeyIdentifier and the Policy built from PutPermission grants; synthetic default bus                                                              | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DescribeEventBus.html) |
| `ListEventBuses`   | ✅ Supported | Always includes default bus                                                                                                                                                                                      | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListEventBuses.html)   |
| `DeleteEventBus`   | ✅ Supported |                                                                                                                                                                                                                  | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DeleteEventBus.html)   |
| `PutPermission`    | ✅ Supported | Stores a resource policy statement on the bus (or replaces it wholesale via the Policy parameter), returned by DescribeEventBus as Policy; stored, not enforced, the same as the rest of Overcast's IAM policies | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutPermission.html)    |
| `RemovePermission` | ✅ Supported | Removes one statement by StatementId, or the whole policy with RemoveAllPermissions                                                                                                                              | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_RemovePermission.html) |

### Rules

| Operation          | Status       | Notes                                                                                                                                                                                                                | AWS Docs                                                                                      |
| ------------------ | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `PutRule`          | ✅ Supported | Creates or updates a rule; refuses a rule with neither EventPattern nor ScheduleExpression, an unknown event bus, and an event pattern that is malformed or uses cidr, wildcard or $or                               | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutRule.html)          |
| `DescribeRule`     | ✅ Supported |                                                                                                                                                                                                                      | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DescribeRule.html)     |
| `ListRules`        | ✅ Supported | Lists rules for a bus                                                                                                                                                                                                | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListRules.html)        |
| `EnableRule`       | ✅ Supported | Sets rule state to ENABLED                                                                                                                                                                                           | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_EnableRule.html)       |
| `DisableRule`      | ✅ Supported | Sets rule state to DISABLED                                                                                                                                                                                          | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DisableRule.html)      |
| `DeleteRule`       | ✅ Supported | Refuses a rule that still has targets, as AWS does; Force is the managed-rule escape only and does not bypass that                                                                                                   | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DeleteRule.html)       |
| `TestEventPattern` | ✅ Supported | Evaluates an event against a pattern with the matcher rule delivery uses; malformed patterns and the cidr, wildcard and $or match types are InvalidEventPatternException, mandatory envelope fields are not enforced | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_TestEventPattern.html) |

### Targets

| Operation           | Status       | Notes                                                                                                                       | AWS Docs                                                                                       |
| ------------------- | ------------ | --------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `PutTargets`        | ✅ Supported | Adds Lambda, SQS, SNS, Step Functions, Kinesis, Firehose, ECS and event-bus targets; rejects other target types at add time | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutTargets.html)        |
| `ListTargetsByRule` | ✅ Supported | Lists targets including input transformers and ECS/Kinesis/SQS target parameters                                            | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListTargetsByRule.html) |
| `RemoveTargets`     | ✅ Supported | Removes targets from a rule                                                                                                 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_RemoveTargets.html)     |

### Events

| Operation   | Status       | Notes                                                                                                                                                                                                                                                                        | AWS Docs                                                                               |
| ----------- | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| `PutEvents` | ✅ Supported | Delivers matching rules to Lambda, SQS, SNS, Step Functions, Kinesis, Firehose, ECS and event-bus targets, applying InputPath/InputTransformer and RetryPolicy/DLQ; patterns match on exact values plus prefix, suffix, exists, equals-ignore-case, numeric and anything-but | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html) |

### Tags

| Operation             | Status       | Notes                        | AWS Docs                                                                                         |
| --------------------- | ------------ | ---------------------------- | ------------------------------------------------------------------------------------------------ |
| `TagResource`         | ✅ Supported | Tag buses and rules          | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_TagResource.html)         |
| `ListTagsForResource` | ✅ Supported | List tags for a resource     | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListTagsForResource.html) |
| `UntagResource`       | ✅ Supported | Removes tags from a resource | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_UntagResource.html)       |

### Archives

| Operation         | Status         | Notes       | AWS Docs                                                                                     |
| ----------------- | -------------- | ----------- | -------------------------------------------------------------------------------------------- |
| `CreateArchive`   | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_CreateArchive.html)   |
| `DescribeArchive` | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DescribeArchive.html) |
| `ListArchives`    | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListArchives.html)    |
| `DeleteArchive`   | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DeleteArchive.html)   |

### Replays

| Operation        | Status         | Notes       | AWS Docs                                                                                    |
| ---------------- | -------------- | ----------- | ------------------------------------------------------------------------------------------- |
| `StartReplay`    | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_StartReplay.html)    |
| `DescribeReplay` | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DescribeReplay.html) |
| `ListReplays`    | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListReplays.html)    |

### Connections

| Operation            | Status         | Notes       | AWS Docs                                                                                        |
| -------------------- | -------------- | ----------- | ----------------------------------------------------------------------------------------------- |
| `CreateConnection`   | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_CreateConnection.html)   |
| `DescribeConnection` | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DescribeConnection.html) |
| `ListConnections`    | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_ListConnections.html)    |
| `DeleteConnection`   | ❌ Unsupported | Returns 501 | [docs](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_DeleteConnection.html)   |

## Related

- [EventBridge](../eventbridge.md) — quick start, what works, and the differences from AWS
- [All service pages](../README.md)

<!-- END overcast:capabilities -->
