# Lambda Web Functions data plane

> Status: implemented (#2302 step 2). The control plane shipped in #2307; this is the runtime behind it. Differences from AWS are in `docs/services/lambda-web/limitations.md`.

Goal: an endpoint of a web function serves HTTP. A revision's S3 package runs as a Node.js server in a Docker
container, Overcast reverse-proxies requests to it, and scales containers with traffic, to zero when idle — with the
data-plane behaviours AWS documents (error types, headers, limits, auth, logs, metrics) and a hot path that adds
microseconds, not milliseconds, to a request.

Sources: the Lambda Developer Guide's Web Functions chapter (`web-functions-http`, `-runtimes`, `-env-lifecycle`,
`-endpoint-access-control`, `-code-s3`, `-troubleshooting`, `-logging`, `-deployment`, `-monitoring-metrics`,
`-scaling`). Facts quoted below are from those pages; anything inferred is marked **(inferred)** and must be listed in
`docs/services/lambda-web/limitations.md`.

## Request path

```
client ── Host: {id}.{shard}.lambda-web.{region}.{base}[:port] ──► Overcast HTTP server
  HostAddressing (label "lambda-web") rewrites to /_overcast/lambdaweb/invoke/{id}/{region}/…   (Host kept)
  ► lambdaweb.serve                                                                  (handler.go)
      1. protocol checks        HTTP/1.1 only (505), method set (400), Host present (400), header block ≤128 KiB (431)
      2. route                  host → endpoint via the in-memory routing table (no store read)  (404 InvalidEndpoint)
      3. state                  endpoint Active (409 EndpointNotActive); rateLimit 0 → 503 EndpointDisabled
      4. auth                   IamAuth → invokeAuth.Authorize (403, no lambda-web-error-type); ApplicationManaged → strip SigV4
      5. throttle               endpoint rate (429 EndpointRequestThrottle), account rate (AccountRequestThrottle)
      6. revision               weighted pick of ≤2 revisions; revision Active else 409 RevisionNotActive
      7. environment            pool.acquire(revision) → env with spare concurrency, or start one (cold start), or 429
      8. proxy                  streamed reverse proxy; timeoutSeconds → 504 Runtime.RequestTimeout
      9. telemetry              request metrics, system log events
```

Response headers Overcast adds: `lambda-web-request-id` (always), `lambda-web-logstream` (when an environment handled it),
`lambda-web-error-type` (platform errors, not for internal errors or authorisation failures). Request headers removed:
`Connection, Keep-Alive, Upgrade, Expect, TE, Trailer, Proxy-Authenticate, Proxy-Authorization, Proxy-Connection, X-Real-IP,
X-HTTP-Method-Override, X-HTTP-Method, X-Method-Override`, anything named in `Connection`, and any `lambda-web-*` header.
Added: `lambda-web-request-id`, `lambda-web-endpoint-name`, `X-Forwarded-For` (client IP appended). `Host` is the endpoint
domain, lowercase, no port. `X-Forwarded-Host` is forwarded untouched. `Authorization` is removed on IamAuth endpoints after
validation; on ApplicationManaged endpoints it is forwarded unless it is an AWS signature (`AWS4-…`), in which case it and
`X-Amz-Security-Token` are removed.

Error types (`lambda-web-error-type`) and statuses are in `errors.go`; the table is the troubleshooting page's.

## Domains

`endpoint.DomainName` and `RegionDomain` are minted per request with `serviceutil.HostRoutedHostname` (the CloudFront
precedent) on the host the caller reached Overcast on, so the name returned by the API resolves back here. The ID is the
two labels `{domain-id}.{shard-id}` (global MultiRegion: `{domain-id}`). The stored value is only the id part; reads
re-render it. `HostRouteRow{Label: "lambda-web"}` resolves it; the rewrite maps `global` (not region-shaped) to region "".

## Execution environments

One environment = one container. Contract in `dataplane.go` (`environmentProvider`, `environment`).

- **Image**: the revision runtime's Lambda base image (`public.ecr.aws/lambda/nodejs:24` for `nodejs24.x`), the same image
  and pull path Lambda event functions use (`lambda`'s `imageForFunction`/`ensureImage` logic is the model; do not import
  its unexported pool). Entrypoint is overridden: `/var/lang/bin/node /opt/overcast-web/bootstrap.mjs`, no Lambda init,
  no Runtime API.
- **Code**: `var/task/**` from the pinned S3 zip, injected with `CopyToContainer` before start from a shared tar cache
  keyed by `bucket/key/versionId`. Package ≤512 MB unzipped.
- **Bootstrap** (`bootstrap.mjs`, embedded): dynamic-`import()`s `AWS_LAMBDA_NODEJS_ENTRYPOINT` (default `index.js`,
  relative to `/var/task`; CJS and ESM both work); on import failure prints a marker line and exits non-zero
  (→ `Runtime.ImportCodeError`); patches `console.*` to emit the JSON application-log shape with the current request id
  (an `AsyncLocalStorage` set from the `lambda-web-request-id` header via a patch on `http.Server`'s `request` emit),
  filtered by `applicationLogLevel`; forwards `SIGTERM`.
- **Listen address**: `AWS_LAMBDA_HTTP_ENDPOINT=0.0.0.0:8080` **(inferred)**. Ready = TCP connect succeeds. Not listening
  in 10 s → `Application.TimeoutListenError`; import error → `Runtime.ImportCodeError`; exit during init →
  `Runtime.ExitError`/`Application.InitCodeError`.
- **Env**: user `environmentVariables`, plus `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_LAMBDA_FUNCTION_NAME`,
  `AWS_LAMBDA_FUNCTION_MEMORY_SIZE=2048`, `AWS_LAMBDA_HTTP_ENDPOINT`, credentials for the execution role (Overcast's
  usual placeholder), `AWS_ENDPOINT_URL*` pointing back at Overcast, `NODE_OPTIONS=--max-old-space-size=1024` unless the
  user sets `NODE_OPTIONS`, `TZ`, `LANG`. Reserved names (`AWS_ACCESS_KEY_ID`, `AWS_REGION`, `AWS_LAMBDA_HTTP_ENDPOINT`, …)
  are rejected at revision create (control plane).
- **Reaching it**: `dataplane.ContainerAddr` when Overcast runs in Docker, else a published `127.0.0.1:<random>` port
  (Docker Desktop cannot route to container IPs).
- **Limits**: 2 GiB memory, no swap, 2 vCPU (`NanoCPUs`).
- **Shutdown**: stop routing, let in-flight requests finish (bounded), `SIGTERM`, ≤1 s, then kill and remove.
- **Ownership**: label `overcast.service=lambdaweb`, own GC sweep at start and shutdown, never touching Lambda's.

## Scaling (`pool.go`)

State per `(region, endpoint?, revision)` — environments are per revision, shared by every endpoint that serves it in a
region. Per-environment capacity is `maxConcurrencyPerEnvironment` (default 64) via an atomic in-flight counter.

- **Acquire**: pick the most-loaded environment below its limit (pack, so idle ones can drain). None → start one if the
  endpoint is below `maxEnvironments` (else 429 `EndpointMaxEnvThrottle`), initialising environments fewer than the
  concurrent-init cap (else 429 `EndpointConcurrentInitThrottle`), and the host is under its environment cap
  (`LAMBDA_WEB_MAX_ENVIRONMENTS`, else 429 `AccountActiveVCPUArm64Throttle`). The request waits on the initialising
  environment (cold start); waiters share one start. Requests wait at most their `timeoutSeconds`.
- **Headroom**: when an environment passes 80 % of its limit and there is room, start the next one in the background so
  small bursts do not see a cold start.
- **Scale down**: an environment idle for `LAMBDA_WEB_IDLE_TTL` (default 5 m) is drained; zero requests → zero environments.
- **Revision/weight changes**: pools are keyed by revision id, so a weight change is a routing-table swap; environments of a
  revision no endpoint routes to drain after the idle TTL (in-flight requests finish). Deleting an endpoint/revision/function
  drains and stops its environments.
- **Failure**: an environment whose process exits is removed; in-flight requests get 503 `Runtime.Unreachable`/`Runtime.Exit`
  (by whether it was mid-request).

## Hot-path performance rules

- No store read, JSON decode, or allocation-heavy work per request after routing: the routing table
  (`atomic.Pointer` to an immutable snapshot, host → `*route`) is rebuilt on control-plane writes and at first use.
- Streaming proxy (`httputil.ReverseProxy`, `FlushInterval: -1`, pooled buffers), one tuned `http.Transport` per
  environment (`MaxIdleConnsPerHost = maxConcurrency`, no compression, keep-alive). Never buffer a body.
- Weighted pick: one `rand` draw over ≤2 entries. Rate limit: lock-free token bucket per endpoint.
- `New()` is pure assignment; Docker probing, GC loops and the sweeper start lazily (startup budget rule).
- Cold start is the cost to attack: shared tar cache, image ensured once, create/inject/start in the fewest Docker calls,
  readiness probe backs off 2 ms → 50 ms.

## IamAuth (`auth.go`)

`IamAuth` endpoints require a SigV4 (HomeRegion/PerRegion; service `lambda`, the endpoint's region) or SigV4a
(MultiRegion; region set `*`) signature and `X-Amz-Content-SHA256` (else 403, no error-type header). Overcast's non-goal
is being a security boundary, so cryptographic validation and IAM evaluation follow the repo's existing switches
(SigV4 validation and IAM enforcement): with them off, a well-formed signed request is accepted; with them on, the
signature is verified against the stored secret and `lambda:InvokeWebFunctionEndpoint` is evaluated on the endpoint ARN
against the principal's identity policies and the function's resource policy (same account: either allows; cross-account:
both). `ApplicationManaged` skips all of it and never evaluates the resource policy.

## Revisions and endpoints become real (`lifecycle.go`)

- `CreateWebFunctionRevision` validates synchronously (bucket exists and is versioned, object/version exists, ≤ limits,
  KMS arn shape), **pins** the S3 version id, stores the revision `Pending`, and a worker fetches and unzips it
  (corrupt zip, oversize → `Failed` with `errors[]`), then `Active`. `Failed` is permanent. A missing entry file leaves the
  revision `Active` (AWS does not check) and fails at init with `Runtime.ImportCodeError`. If the S3 object is gone when an
  environment starts, the revision moves to `Failed` and requests get 409 `RevisionNotActive`.
- Endpoints go `Pending` → `Active` once their domain is registered in the routing table; updates are `updateStatus`
  `InProgress` → `Successful` when the new snapshot is live; delete is `Deleting` until its environments drain.
- `ListWebFunctionRevisions` returns newest first.
- Switching `Disabled → LatestRevision` requires the latest revision `Active` (409 otherwise); weights may reference only
  `Active` revisions.

## Telemetry (`telemetry.go`)

- **Application logs**: container stdout/stderr → `containerlogs` follower → CloudWatch Logs, group
  `/aws/lambda/web/{function}` (or `loggingConfig.logGroup`) and its `.local` companion, stream
  `YYYY/MM/DD/[{revisionId}]{32 hex}` **(inferred)**; events are the bootstrap's JSON
  (`timestamp, level, message, requestId, endpointName, region`).
- **System logs** (`systemLogLevel`): `platform.initStart`, `initReport`, `invokeStart`, `requestComplete` (DEBUG),
  `invokeSummary` (every 60 s per active environment), `invokeReport`, `spinDownStart`, `spinDownReport`, `logsDropped`.
  Field schema from `web-functions-logging-system-log-schema.md`.
- **Metrics**: `AWS/Lambda/Web` (`ExecutedRequests`, `HTTPResponseStatusClass4XX/5XX` sparse; `EnvironmentActiveTime`,
  `EnvironmentCPUTime`, `EnvironmentMaxMemoryUsage`, `EnvironmentConcurrentRequests`, `ResponseBytes`) and
  `AWS/Lambda/Web/Usage` (`ExecutedRequests`, `EndpointActiveEnvironments`, the four throttle metrics) through the
  CloudWatch service's in-process writer, dimensions as documented.
- **Bus events** for the console and map: `lambda-web:EnvironmentStarted|EnvironmentStopped|RequestCompleted`
  (`RequestCompleted` is classified noise, like `RequestReceived`).

## Work split (file ownership)

| Track | Owns | Contract |
| --- | --- | --- |
| Engine & proxy | `engine.go`, `pool.go`, `handler.go`, `routing.go`, `ratelimit.go`, `errors.go`, `service.go` wiring, host-route label, router wiring, tests with a fake provider | `dataplane.go` |
| Docker provider | `provider_docker.go`, `bootstrap.mjs`, `provider_docker_test.go`, `tests/integration/lambdawebdocker/` | `environmentProvider` |
| Auth | `auth.go`, `sigv4a.go`, tests | `invokeAuth` |
| Telemetry | `telemetry.go`, `metrics.go`, `systemlog.go` | `telemetry` |
| Lifecycle & control-plane fidelity | `lifecycle.go`, `code.go`, `typed_logic.go`, `validate.go`, `types.go` state fields, `s3` accessors | `codeSource`, record fields in `dataplane.go` |
| CloudFront origin | `internal/services/cloudfront` | endpoint domain minting |
| Console & map | `web/` | events + `topology.WebEndpoint` additions |

Integration is by cherry-pick onto the feature branch; shared files (`service.go`, `router.go`, `types.go`) are touched only
by their owner, other tracks add new files or send the owner a patch description.
