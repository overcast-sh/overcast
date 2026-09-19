---
title: "DynamoDB — Amazon DynamoDB"
description: "Quick start, item, expression, index and transaction coverage, the metrics recorded, and the divergences: immediately consistent GSIs, swept TTL, no PartiQL, no global tables."
section: "Service Reference"
tags:
  - docs
  - dynamodb
  - services
---

# DynamoDB — Amazon DynamoDB

Full item, expression, index and transaction support, with tables scoped to a
region exactly as on AWS.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

aws dynamodb create-table --table-name orders \
  --attribute-definitions AttributeName=id,AttributeType=S \
  --key-schema AttributeName=id,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST

aws dynamodb put-item --table-name orders \
  --item '{"id": {"S": "o-1"}, "total": {"N": "42"}}'
aws dynamodb get-item --table-name orders --key '{"id": {"S": "o-1"}}'
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area | Behaviour |
| --- | --- |
| Items | `PutItem`, `GetItem`, `UpdateItem`, `DeleteItem`, `BatchGetItem` (100), `BatchWriteItem` (25) |
| Expressions | Condition, filter, key-condition, projection and update expressions, with `SET`/`REMOVE`/`ADD`/`DELETE` and every `ReturnValues` variant; a [reserved word](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ReservedWords.html) used as a bare attribute name is rejected as AWS rejects it, so reach it through `ExpressionAttributeNames` |
| Query and scan | `Limit` applied before `FilterExpression` as AWS does, `ExclusiveStartKey` pagination, `ScanIndexForward`, `Select=COUNT`, parallel scan by `Segment`/`TotalSegments` |
| Indexes | GSIs and LSIs, including `ALL`/`KEYS_ONLY`/`INCLUDE` projections and GSI create/delete/throughput updates through `UpdateTable` |
| Request validation | The rejections AWS documents, with AWS's wording: `AttributeDefinitions` must match the key schemas exactly, key attributes may not be empty, a `Query` must constrain the partition key, items are capped at 400 KB by AWS's size accounting, `BatchWriteItem` and `TransactWriteItems` refuse two operations on one item, `BatchGetItem` refuses more than 100 keys, and `Scan` refuses a `Segment` at or past `TotalSegments` |
| Transactions | `TransactGetItems` and `TransactWriteItems`, all-or-nothing, with `ConditionCheck` |
| Billing modes | An omitted `BillingMode` defaults to `PROVISIONED`, which requires `ProvisionedThroughput` on the table and every GSI; `PAY_PER_REQUEST` rejects it |
| Data types | Every attribute type; items are stored in DynamoDB's JSON wire format, so nothing is lost to a round trip |
| Streams | `StreamSpecification` on create and update — see [DynamoDB Streams](./dynamodbstreams.md) |
| TTL | `UpdateTimeToLive` and `DescribeTimeToLive`, with the asynchronous `ENABLING` → `ENABLED` and `DISABLING` → `DISABLED` lifecycle; a second update inside the transition window is rejected as AWS rejects it |
| Metrics | `SuccessfulRequestLatency`, `ConsumedRead`/`WriteCapacityUnits` and `UserErrors`/`SystemErrors` are recorded to CloudWatch, transactional operations at AWS's 2× weighting |
| Protocols | AWS JSON 1.0 (`X-Amz-Target: DynamoDB_20120810.<Operation>`) and Smithy RPC v2 CBOR |

## Differences from AWS

| Area                                                                         | On AWS                                                               | Overcast                                                                             |
| ---------------------------------------------------------------------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| GSI consistency                                                              | Eventually consistent                                                | Immediately consistent — an item is visible to a GSI query the instant it is written |
| `ConsistentRead=true` with an `IndexName`                                    | Rejected                                                             | Rejected, exactly as AWS rejects it                                                  |
| TTL expiry                                                                   | Expired items are hidden on read                                     | Deleted by an hourly sweeper instead, so an expired item can still be returned       |
| TTL status changes                                                           | Take up to one hour to leave `ENABLING`/`DISABLING`                  | Settle after 30 seconds — long enough to be observed, short enough not to block      |
| PartiQL                                                                      | `ExecuteStatement`, `ExecuteTransaction` and `BatchExecuteStatement` | Out of scope                                                                         |
| Global tables                                                                | Replicate a table across regions                                     | Overcast emulates one region per request; the global-table operations answer `501`   |
| Backups, exports, imports, restores, resource policies, contributor insights | Full API                                                             | Answer `501`                                                                         |
| Throughput                                                                   | Provisioned capacity is enforced                                     | Recorded and reported; nothing is throttled                                          |

Unimplemented-but-modelled operations answer `501 Not Implemented` with
`x-emulator-unsupported: true`, in DynamoDB's own error envelope. An
`X-Amz-Target` naming no AWS operation at all gets `400
UnknownOperationException`.

## Gotchas

> [!IMPORTANT]
> Tables are regional. A table created in `us-east-1` is invisible from
> `eu-west-1` — `ListTables` omits it, `DescribeTable` answers
> `ResourceNotFoundException`, and the same name can exist in both regions with
> entirely separate items, index entries and streams. The region comes from the
> request: the SigV4 credential scope, a regional endpoint hostname, or
> `OVERCAST_DEFAULT_REGION` when the request names none.

`UpdateTimeToLive` is asynchronous and not idempotent, exactly as on AWS. The
call returns while `DescribeTimeToLive` still reports `ENABLING` or
`DISABLING`, and items only start expiring once the status reaches `ENABLED`.
A second `UpdateTimeToLive` for the same table inside the transition window is
a `ValidationException` ("Time to live has been modified multiple times within
a fixed interval"), and so is re-enabling an already-`ENABLED` table — which is
why the TTL attribute name can only be changed by disabling TTL first and
enabling it again afterwards. AWS's window is up to an hour; Overcast's is 30
seconds, so the lifecycle is observable without a poll loop taking one.

<!-- BEGIN overcast:capabilities -->

## Operations

21 of 28 listed operations are implemented.
Per-operation status, notes and AWS API links: [DynamoDB operations](dynamodb/operations.md).

<!-- END overcast:capabilities -->

## Related

- [DynamoDB Streams](./dynamodbstreams.md)
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [Storage and persistence](../storage.md#what-survives-a-restart-or-crash) — where table data lives between restarts
- [AWS API reference](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/Welcome.html)
