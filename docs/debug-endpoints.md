---
title: "Debug endpoints"
description: "The /_overcast/debug/* namespace and always-on diagnostics: health, metrics, state dump, request tracing, pprof, and how long traces are retained."
section: "Reference"
tags:
  - docs
  - debug
  - debugging
  - reference
  - tracing
  - pprof
---

# Debug endpoints

Set `OVERCAST_DEBUG=true` to enable the `/_overcast/debug` namespace and
request tracing. Every response carries a request ID (`x-amzn-requestid` for
most services, `x-amz-request-id` for S3) which the trace endpoints below can
look up.

| Endpoint                    | Method | Description                                           |
| --------------------------- | ------ | ----------------------------------------------------- |
| `/_overcast/health`                  | GET    | Health, service tiers, resolved storage backend, auxiliary listeners, the Athena query engine's state (always enabled) |
| `/_overcast/info`                    | GET    | Effective region, account ID and accepted credentials (always enabled) |
| `/_overcast/init`                    | GET    | Init-hook results, all stages or one at `/_overcast/init/{stage}` (always enabled) |
| `/_overcast/events`                  | GET    | SSE stream of internal events (always enabled)        |
| `/_overcast/events/request/{requestId}` | GET | Every internal event tied to one request ID, as a JSON list rather than a stream (always enabled) |
| `/_overcast/metrics`                 | GET    | Go runtime memory/GC/goroutine stats (always enabled) |
| `/_overcast/topology`                | GET    | Full cross-region resource graph (always enabled)     |
| `/_overcast/preflight/region`        | GET    | Whether resources of a `?kind=` exist in some region other than the caller's, and how many (always enabled). Answers with nothing to report when the caller's own region has any — it explains an empty list, it is not a census |
| `/_overcast/reset`                   | POST   | Wipe all emulated state (always enabled — not expensive or leaky like the rest of this namespace). The instance identity survives, so the Docker resources this instance created are still recognisably its own and still swept |
| `/_overcast/reset/{service}`         | POST   | Wipe state for one service (always enabled)           |
| `/_overcast/samples/{dataset}`       | POST   | Load a sample dataset, as `overcast samples load` does (always enabled) |
| `/_overcast/debug/health`            | GET    | Detailed: uptime, services, state backend and health  |
| `/_overcast/debug/config`            | GET    | Effective configuration (secrets redacted)            |
| `/_overcast/debug/state`             | GET    | Every namespace and its keys (no values)              |
| `/_overcast/debug/state/{namespace}` | GET    | Paginated key/value pages for one namespace (`?after=` cursor, `?limit=` ≤ 5000, default 500); `?key=` fetches one raw value |
| `/_overcast/debug/metrics`           | GET    | Storage diagnostics: flush history, seed duration, pending-log size; `?includeRowCounts=true` adds per-namespace row counts |
| `/_overcast/debug/pprof/`            | GET    | Go pprof index (goroutine, heap, CPU profiles, etc.)  |
| `/_overcast/debug/trace/{requestId}` | GET    | Full trace for one request: bodies, headers, log entries, AWS errors |
| `/_overcast/debug/traces`            | GET    | Paginated list of recent traces; filterable by `?service=`, `?method=`, `?path=`, `?status=`, `?search=` |
| `/_overcast/debug/traces/count`      | GET    | Current trace buffer count and capacity               |
| `/_overcast/debug/traces/search`     | GET    | Free-text search over retained traces                 |
| `/_overcast/debug/ec2/vpcs`          | GET    | EC2 VPC-to-Docker-network wiring, for debugging VPC-backed networking. Service-specific debug routes live under `/_overcast/debug/<service>/…`; this is the only one today |

## Compatibility aliases

`/_overcast/*` is what to point new tooling at. Seven older URLs are served as
well, because they are already written into healthchecks, wait strategies and
test suites that predate them:

| Alias                           | Serves                            | Body                                                                    |
| ------------------------------- | --------------------------------- | ----------------------------------------------------------------------- |
| `/_health`                      | Overcast's own path before v0.0.1-alpha.35 | Identical to `/_overcast/health`                              |
| `/_localstack/health`           | LocalStack's health endpoint      | LocalStack's shape — a `services` map plus `edition` and `version` |
| `/_localstack/init`             | LocalStack's init-hook status     | Identical to `/_overcast/init` — the shapes already matched      |
| `/_localstack/init/{stage}`     | One stage of the above            | Identical to `/_overcast/init/{stage}`                          |
| `POST /_localstack/state/reset` | LocalStack's state reset          | `{"status":"reset"}`; LocalStack returns an empty body          |
| `GET`/`DELETE /_aws/ses`        | LocalStack's captured emails      | LocalStack's `{"messages": [...]}` shape, over the same inbox as `/_overcast/ses/inbox/messages` |
| `/_aws/sqs/messages`            | LocalStack's queue peek           | An SQS `ReceiveMessageResponse`, XML by default and JSON under `Accept: application/json` |

The health pair returns 200 whenever `/_overcast/health` does. This matters
more than it looks: a container healthcheck that 404s marks Overcast unhealthy,
and an orchestrator restarts it — which, on the default in-memory state
backend, wipes every resource a deploy in flight had created.

The init pair needs no translation because Overcast's status endpoint was built
to LocalStack's contract: the same `BOOT`/`START`/`READY`/`SHUTDOWN` stage
names, the same `UNKNOWN`/`RUNNING`/`SUCCESSFUL`/`ERROR` script states. Both
paths run the same handler, so they cannot drift apart.

The `/_aws/` pair is what a test suite carried over from LocalStack asserts
against, one assertion per test that sends an email or a message. The queue
peek never consumes anything.

| Path | Query parameters |
| --- | --- |
| `GET /_aws/ses` | `?id=` and `?email=` (source address) filter it. Emails only — the inbox also holds SMS, webhook and push captures |
| `DELETE /_aws/ses` | `?id=` clears one email, no parameter clears them all. Answers 204 either way |
| `GET /_aws/sqs/messages` | `?QueueUrl=`, or `?QueueName=` with an optional `?QueueRegion=`, or the path form `/{region}/{account}/{queue}` |
| `GET /_aws/sqs/messages` | `?ShowInvisible=true` and `?ShowDelayed=true` reveal in-flight and delayed messages, hidden by default as in LocalStack |

Three shapes differ from LocalStack's, and a test asserting on them literally
will notice:

- `Region` and `RawData` are absent from an `/_aws/ses` body. The capture does not hold a region, and cannot tell a raw send from a composed one.
- An empty queue is `"Message": []` in the JSON form, not null.
- Receipt handles are the messages' real ones rather than LocalStack's placeholder, so one read from the peek can be passed to `DeleteMessage`.

Anything else under `/_localstack/` or `/_aws/` answers 404 with the Overcast
endpoint that replaces it, rather than falling through to S3's `NoSuchBucket`
— see [Endpoints and init hooks](./migration/endpoints.md#endpoint-mapping)
for the full table.

## Trace retention

Traces are retained under three rules, so that the request explaining a failure is
still there when you go looking, without your having configured anything first:

1. The newest `OVERCAST_DEBUG_TRACE_BUFFER` traces (default 1000) are always kept.
2. Beyond that, a burst is kept for `OVERCAST_DEBUG_TRACE_WINDOW` (default 1h), up to
   `OVERCAST_DEBUG_TRACE_CEILING` (default 10000). A `cdk deploy` pushes thousands of
   requests through in a couple of minutes, and the floor alone would keep the
   rollback traffic and discard the error that started it.
3. Traces that went wrong — a 4xx/5xx, an AWS error code, or a failed internal hop —
   are exempt from both, up to `OVERCAST_DEBUG_TRACE_PINNED` (default 1000). They are
   not exempt from the memory budget: under real pressure the oldest kept failures are
   surrendered last, after every ordinary trace above the floor.

Internal polling (health checks, the console's own requests) is retained separately
and can never evict a request you made. A trace records each
internal service-to-service hop a request made, and captures a goroutine stack
for the first 20 hops plus the first 20 hops that failed — a CloudFormation or
CDK deploy dispatches hundreds of hops through one trace, and a stack for every
one of them would cost more than it tells you. Hops past that budget show
"Stack trace not captured" in the console.

See the [environment variable reference](./configuration/reference.md) for the
full list of `OVERCAST_DEBUG_TRACE_*` variables and their defaults.

## Related

- [Troubleshooting](./troubleshooting.md) — a symptom, and where its answer lives
- [Log levels](./configuration/log-levels.md) — `OVERCAST_DEBUG` and `OVERCAST_LOG_LEVEL`
- [Checking a running daemon](./cli/inspect.md) — the CLI over this surface
