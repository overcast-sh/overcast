---
title: "Environment variable reference"
description: "Every environment variable Overcast reads, with its default: the OVERCAST_* settings, the per-service LAMBDA_*, ECS_*, RDS_*, ELASTICACHE_*, MSK_*, EKS_* and EFS_* overrides, and DOCKER_HOST."
section: "Reference"
tags:
  - configuration
  - docs
  - environment
  - reference
  - variable
  - variables
---

# Environment variable reference

Every variable Overcast reads, with its default. Find one with Ctrl+F; the
[configuration guides](../configuration.md) explain the ones that need
explaining.

<!-- docs-length-review: every environment variable in one table; splitting it
     by area would make readers guess which page a variable is on -->

> [!NOTE]
> LocalStack spellings are read directly and logged at startup — `EDGE_PORT`,
> `GATEWAY_LISTEN`, `LOCALSTACK_HOST`, `HOSTNAME_EXTERNAL`, `DATA_DIR`,
> `DEFAULT_REGION`, `DEBUG=1`, `LS_LOG`, `PERSISTENCE=1`, `ENFORCE_IAM`,
> `LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT`, `LAMBDA_REMOVE_CONTAINERS=0` and
> `DNS_ADDRESS=0`. Those, and the thirty-odd LocalStack variables Overcast
> recognises but ignores, are mapped in [LocalStack environment
> variables](../migration/environment-variables.md).

<!--
  This table mirrors the authoritative enumeration in the doc comment on
  Config.Load in internal/config/config.go. When a variable is added or removed
  there, update this table in the same change. Deliberately absent:
  OVERCAST_DATA_DIR_SOURCE (internal provenance marker set by the Docker image,
  not for end users) and removed variables that are no longer read
  (OVERCAST_SERVICES, OVERCAST_MCP_REPLAY_LIMIT, and the per-service
  LAMBDA_NETWORK/ECS_NETWORK/RDS_NETWORK/ELASTICACHE_NETWORK/MSK_NETWORK/
  EKS_NETWORK/EFS_NETWORK, replaced by OVERCAST_NETWORK). The LocalStack alias
  and ignored-variable tables live in docs/migration/environment-variables.md.
-->
| Variable                         | Default                | Description                                                                          |
| --------------------------------- | ---------------------- | ------------------------------------------------------------------------------------ |
| `OVERCAST_UI_PORT`               | `4567`                 | Port for the web management console (`--ui-port`); `0` disables it. Falls back to an ephemeral port when 4567 is taken. Full binary and full image only |
| `OVERCAST_LISTEN`                | `0.0.0.0` containerised, `127.0.0.1` native | Address(es) to bind the AWS API to; comma-separate to bind several. See [Bind address and port](./ports.md) |
| `OVERCAST_HOSTNAME`              | `localhost`            | Hostname embedded in client-facing URLs. **Set it to `localhost.overcast.sh`** unless you are offline — see [Networking](../networking.md) |
| `OVERCAST_SPLIT_HORIZON_HOSTS`   | _(none)_               | Extra comma-separated hostnames remapped to Overcast inside the containers it starts, on top of the three built-in ones |
| `OVERCAST_PORT`                  | `4566`                 | TCP port for the AWS API |
| `OVERCAST_STATE`                 | `auto`                 | Storage backend: `auto`, `memory`, `hybrid`, `persistent` or `wal` — see [Storage and persistence](../storage.md) for how `auto` picks and the durability tradeoffs |
| `OVERCAST_STATE_<SERVICE>`       | _(global)_             | Per-service backend override, e.g. `OVERCAST_STATE_S3=memory`, keyed by [service name](../configuration.md#service-names) — see [Per-service storage overrides](../storage.md#per-service-storage-overrides) |
| `OVERCAST_HYBRID_FLUSH_INTERVAL` | `5s`                   | How often the hybrid backend flushes in-memory state to disk                         |
| `OVERCAST_HYBRID_SYNC`           | `interval`             | Hybrid pending-log fsync policy: `always`, `interval`, or `never`                    |
| `OVERCAST_HYBRID_SYNC_INTERVAL`  | `100ms`                | Periodic fsync interval used when `OVERCAST_HYBRID_SYNC=interval`                    |
| `OVERCAST_HYBRID_DIRTY_ENTRY_THRESHOLD` | `10000`         | Unflushed-write count that triggers an early hybrid flush ahead of the timer (`<= 0` disables) |
| `OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD`  | `8388608`       | Approximate unflushed-write bytes that trigger an early hybrid flush (default 8 MiB; `<= 0` disables) |
| `OVERCAST_HYBRID_MAINTENANCE_INTERVAL`  | `5m`            | How often the hybrid backend runs background SQLite housekeeping (passive WAL checkpoint + conditional incremental vacuum) |
| `OVERCAST_WAL_FSYNC`             | `interval`             | WAL fsync policy: `always`, `interval`, or `never`                                   |
| `OVERCAST_WAL_FSYNC_INTERVAL`    | `100ms`                | Periodic fsync interval used when `OVERCAST_WAL_FSYNC=interval`                      |
| `OVERCAST_WAL_MAX_LOG_BYTES`     | `67108864`             | WAL log compaction threshold in bytes (default 64 MiB)                               |
| `OVERCAST_DATA_DIR`              | `~/.overcast/data`     | Directory for store files and other on-disk state; the Docker images bake `/data`. LocalStack's `DATA_DIR` is an alias, and setting either counts as an explicit data directory for `OVERCAST_STATE=auto` |
| `OVERCAST_CA_DIR`                | `$OVERCAST_DATA_DIR/ca` | Where the local CA lives — separable from the data dir because a CA outlives disposable state. May be read-only; see [Overcast in Docker over HTTPS](../https/docker.md) |
| `OVERCAST_DEFAULT_REGION`        | `us-east-1`            | Fallback region used in ARNs when the SigV4 header carries none. LocalStack's `DEFAULT_REGION` is an alias |
| `OVERCAST_ACCOUNT_ID`            | `000000000000`         | Account ID embedded in ARNs; must be exactly 12 ASCII digits or startup fails         |
| `OVERCAST_LOG_LEVEL`             | `info`                 | `trace`, `debug`, `info`, `warn`, `error` — see [Log levels](./log-levels.md). LocalStack's `DEBUG=1` is an alias for `debug` |
| `OVERCAST_DEBUG`                 | `false`                | Enable `/_overcast/debug/*` endpoints — see [Debug endpoints](../debug-endpoints.md)  |
| `OVERCAST_DEBUG_TRACE_BUFFER`    | `1000`                 | Request traces always retained — the floor. Only read when `OVERCAST_DEBUG=true`; see [Debug endpoints § Trace retention](../debug-endpoints.md#trace-retention) |
| `OVERCAST_DEBUG_TRACE_CEILING`   | `10000`                | How far a burst may grow retention past the floor |
| `OVERCAST_DEBUG_TRACE_WINDOW`    | `1h`                   | How long traces above the floor survive before being reclaimed |
| `OVERCAST_DEBUG_TRACE_PINNED`    | `1000`                 | Traces kept because they went wrong, exempt from the floor and the window |
| `OVERCAST_DEBUG_TRACE_BYTES_MB`  | `512`                  | Retained request/response body budget. Overflow is reclaimed before kept failures, and never below the floor |
| `OVERCAST_SERVICE_METRICS`       | `auto`                 | Whether emulated services record CloudWatch metrics for their own activity: `auto`, `enabled` or `disabled`. `PutMetricData` from your own code is unaffected either way |
| `OVERCAST_SIGV4_VALIDATE`        | `false`                | Verify SigV4 signatures (header-signed and presigned) and reject invalid or expired ones with `403 InvalidSignatureException`. Unsigned requests still pass through |
| `OVERCAST_ENFORCE_IAM`           | `false`                | Evaluate the calling principal's IAM policies and return AWS-shaped `AccessDenied` when they do not allow the request — see [IAM § Request-time enforcement](../services/iam.md#request-time-enforcement-opt-in) |
| `OVERCAST_ENFORCE_APIGATEWAY_THROTTLE` | `false`          | Reject API Gateway requests over their usage plan's throttle or quota with `429`. Off by default: the limits are measured and reported, never enforced — see [API Gateway](../services/apigateway.md#usage-plan-throttling-and-quotas) |
| `OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH` | `false`         | Verify `AMAZON_COGNITO_USER_POOLS` bearer tokens on AppSync GraphQL requests against the local Cognito user pool the API names — RS256 signature, issuer, `token_use`, expiry and `appIdClientRegex` — instead of only requiring a token to be present. Off by default so resolver tests can keep minting unsigned JWTs — see [AppSync § Cognito authorization](../services/appsync.md#cognito-authorization-relaxed-by-default) |
| `OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY` | `false`       | Check a function's resource-based policy before an S3 notification, SNS delivery, API Gateway integration or EventBridge target invokes it, and fail the way that caller fails on AWS. Off by default: statements are stored and returned, never consulted — see [Lambda limitations § Resource policies](../services/lambda/limitations.md#resource-policies) |
| `OVERCAST_CFN_SYNC_WAIT_MS`      | `1000`                 | Milliseconds CloudFormation waits for fast stack provisioning before returning (`0` disables) |
| `OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT` | `15m`        | Runaway guard on one execution, never on `StartExecution` itself. A state machine's own `TimeoutSeconds` can lower it but not raise it |
| `OVERCAST_TLS`                   | —                      | `auto` = serve API **and** web console over HTTPS with a certificate minted from the local Overcast CA (unlocks browser HTTP/2) — see [HTTPS and HTTP/2](../https.md) |
| `OVERCAST_TLS_CERT`              | —                      | Path to your own TLS certificate (enables HTTPS for API and web console; mutually exclusive with `OVERCAST_TLS=auto`) |
| `OVERCAST_TLS_KEY`               | —                      | Path to the matching TLS private key                                                 |
| `OVERCAST_SHUTDOWN_TIMEOUT`      | `5s`                   | Graceful shutdown wait, which also budgets the final store flush. Nothing is lost when it runs out — unflushed writes replay from the pending log |
| `OVERCAST_PROTOCOL_STRICT`       | `false`                | Return `415` when a request arrives in a protocol the target service does not declare, instead of attempting the decode anyway |
| `OVERCAST_DNS`                   | `true`                 | Run the built-in DNS resolver that serves the split-horizon names to the containers Overcast starts. Failing to bind the port is not fatal |
| `OVERCAST_DNS_PORT`              | `53`                   | Port for the built-in DNS resolver. Docker's `--dns` cannot express a port, so anything other than `53` is only useful for tests |
| `OVERCAST_HOT_RELOAD`            | `false`                | Umbrella switch for hot reload across every compute service — see [The inner loop](../local-dev.md) |
| `OVERCAST_LAMBDA_HOT_RELOAD`     | _(`OVERCAST_HOT_RELOAD`)_ | Per-service override: hot reload for Lambda functions                             |
| `OVERCAST_ECS_HOT_RELOAD`        | _(`OVERCAST_HOT_RELOAD`)_ | Per-service override: hot reload for ECS tasks                                    |
| `OVERCAST_DEBUGGER`              | `false`                | Umbrella switch for step debugging of user code across every compute service — see [Step debugging inside emulated compute](../debugger.md) |
| `OVERCAST_LAMBDA_DEBUGGER`       | _(`OVERCAST_DEBUGGER`)_ | Per-service override: debug ports for Lambda functions                              |
| `OVERCAST_ECS_DEBUGGER`          | _(`OVERCAST_DEBUGGER`)_ | Per-service override: debug ports for ECS tasks                                     |
| `OVERCAST_DEBUGGER_LISTEN`       | _(`OVERCAST_LISTEN` host)_ | Address the debug ports bind on: `127.0.0.1` native, `0.0.0.0` in a container so `-p 9229-9329:9229-9329` reaches them |
| `OVERCAST_DEBUGGER_PORTS`        | `9229-9329`            | Range auto-allocated debug ports are taken from, lowest free first                  |
| `OVERCAST_DEBUGGER_TIMEOUT`      | `attached`             | When a Lambda function's timeout clock stops: `attached` (a client is connected), `paused` (only at a breakpoint; Node.js so far), `strict` (never) — see [Timeouts while paused](../debugger.md#timeouts-while-paused) |
| `OVERCAST_EC2_VPC_STRATEGY`      | `shared`               | How VPCs map to Docker networks when their CIDRs overlap: `shared`, `strict` or `remapped` — see [How a VPC is backed by a Docker network](../networking/vpc-backing.md#overlapping-cidrs) |
| `OVERCAST_MCP_REMOTE_EXPOSURE`   | `false`                | **Security-relevant.** Declares that `/_overcast/mcp` will be reachable by non-local clients, and requires `OVERCAST_MCP_AUTH_TOKEN`. See [Exposing MCP](./mcp.md) |
| `OVERCAST_MCP_AUTH_TOKEN`        | —                      | Bearer token every MCP request must present once set. Treat it like any other credential |
| `OVERCAST_NETWORK`               | `overcast`             | Docker network every container Overcast starts joins when it belongs to no VPC. Overcast derives `<name>_control` from it for the Lambda Runtime API — see [Networking](../networking.md) |
| `OVERCAST_VPC_EGRESS`            | `open`                 | Whether the containers Overcast starts reach anything outside this machine: `open`, `routed` or `none`. Any other value fails startup — see [Egress modes](../networking/egress.md) |
| `OVERCAST_VPC_EGRESS_POOL`       | `198.18.0.0/16`        | IPv4 range the per-VPC egress networks of `routed` are carved from, one `/24` each. `/8` to `/24`; the default supports 256 VPCs with egress — see [The address-pool ceiling](../networking/routed-egress.md#the-address-pool-ceiling) |
| `OVERCAST_CONTROL_PLANE_INTERNAL` | `auto`                | **Deprecated**, use `OVERCAST_VPC_EGRESS`. Pins whether `<name>_control` alone is created `--internal`: `auto`, `true`, `false`. Still honoured; setting it logs a deprecation notice — see [Control-plane isolation](../networking/egress.md#control-plane-isolation) |
| `DOCKER_HOST`                    | —                      | Docker's own variable, read when `LAMBDA_DOCKER_SOCKET` is unset — the one Colima, Rancher Desktop, Podman and rootless Docker tell you to set. `ssh://` and `https://` are not dialable, and warn |
| `LAMBDA_DOCKER_SOCKET`           | _(`DOCKER_HOST`, else `/var/run/docker.sock` on Linux/macOS, `npipe:////./pipe/docker_engine` on Windows)_ | Docker endpoint — Unix path, Windows named pipe, or `tcp://host:port` for DinD. Every per-service socket override below must address the **same** daemon |
| `LAMBDA_RUNTIME_API_PORT`        | `9001`                 | Port of the shared Lambda Runtime API listener; `0` = ephemeral. A taken default falls back to an ephemeral port — see [Running two instances on one host](./two-instances.md) |
| `LAMBDA_RUNTIME_API_HOST`        | `auto`                 | Address Lambda containers dial for the Runtime API. `auto` probes the candidates; a bare address (`host.docker.internal`, or an IP) pins it, and a scheme, port or path is rejected — see [Containers cannot reach the Runtime API](../services/lambda/troubleshooting.md#containers-cannot-reach-the-runtime-api) |
| `LAMBDA_DOCKER_MAX_CONCURRENT_STARTS` | _(auto)_               | Max concurrent Docker-backed Lambda container starts. Unset: derived from the Docker host as `clamp(NCPU/2, 2, 8)` (each start bursts ~2 CPUs during INIT); `4` when Docker `/info` is unavailable |
| `LAMBDA_MAX_INSTANCES`           | _(auto)_               | Max Lambda containers across all functions. Unset: derived from the Docker host as `clamp(MemTotal×0.65 / 256 MiB, 4, 32)`; `25` when `/info` is unavailable |
| `LAMBDA_MAX_INSTANCES_PER_FUNCTION` | _(auto)_            | Max concurrent containers for one function. Unset: `clamp(maxInstances/2, 2, maxInstances)`; `10` when `/info` is unavailable |
| `LAMBDA_MAX_MEMORY_MB`           | _(auto)_               | Aggregate memory budget for live Lambda containers (Σ `MemorySize`, in MB). Unset: 65% of the Docker host's `MemTotal`; unlimited when `/info` is unavailable |
| `LAMBDA_MAX_WARM_INSTANCES`      | `10`                   | Idle containers kept warm per function after a burst                                 |
| `LAMBDA_SEED_RUNTIME_IMAGES`     | `false`                | Pre-pull every currently-supported Lambda runtime image at startup                   |
| `LAMBDA_INIT_TIMEOUT_SECONDS`    | `10`                   | Max seconds to wait for a Lambda runtime to finish INIT. LocalStack's `LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT` is an alias |
| `LAMBDA_KEEP_CONTAINERS`         | `false`                | Keep stopped Lambda containers after expiry/delete (useful for debugging)            |
| `LAMBDA_TAR_CACHE_MB`            | `256`                  | In-memory cache of pre-built cold-start code and layer tars; `0` disables it         |
| `LAMBDA_PROACTIVE_INIT`          | `true`                 | Pre-initialise one execution environment once a function's configuration settles; set `false` to opt out |
| `LAMBDA_FETCH_REMOTE_LAYERS`     | `false`                | Download layers missing locally from real AWS (needs the `LAMBDA_REMOTE_AWS_*` credentials) |
| `LAMBDA_LAYER_CACHE_DIR`         | `$OVERCAST_DATA_DIR/layers` | Where layer zips are looked up and cached, named `{sha256(arn)}.zip`            |
| `LAMBDA_REMOTE_AWS_ACCESS_KEY_ID` | —                     | AWS access key ID used by `LAMBDA_FETCH_REMOTE_LAYERS`                               |
| `LAMBDA_REMOTE_AWS_SECRET_ACCESS_KEY` | —                 | AWS secret access key used by `LAMBDA_FETCH_REMOTE_LAYERS`                           |
| `LAMBDA_REMOTE_AWS_SESSION_TOKEN` | —                     | Optional AWS session token used by `LAMBDA_FETCH_REMOTE_LAYERS`                      |
| `ECS_DOCKER_SOCKET`              | _(Lambda socket)_      | Docker endpoint for ECS — Unix path or `tcp://host:port`                             |
| `ECS_KEEP_CONTAINERS`            | `false`                | Keep stopped ECS task containers after they exit                                     |
| `OVERCAST_RDS_MODE`              | `live`                 | `live` runs a real engine container per instance; `mock` is metadata-only            |
| `RDS_DOCKER_SOCKET`              | _(Lambda socket)_      | Docker endpoint for RDS — Unix path or `tcp://host:port`                             |
| `RDS_PORT_BASE`                  | `33060`                | Starting host port for RDS containers (each instance gets the next available port)   |
| `RDS_KEEP_CONTAINERS`            | `false`                | Keep stopped RDS containers after instance deletion                                  |
| `ELASTICACHE_DOCKER_SOCKET`      | _(Lambda socket)_      | Docker endpoint for ElastiCache — Unix path or `tcp://host:port`                     |
| `ELASTICACHE_PORT_BASE`          | `63790`                | Starting host port for ElastiCache engine containers                                 |
| `ELASTICACHE_KEEP_CONTAINERS`    | `false`                | Keep stopped ElastiCache containers after deletion                                   |
| `MSK_DOCKER_SOCKET`              | _(Lambda socket)_      | Docker endpoint for MSK — Unix path or `tcp://host:port`                             |
| `MSK_PORT_BASE`                  | `49092`                | Starting host port for MSK broker containers                                         |
| `MSK_KEEP_CONTAINERS`            | `false`                | Keep stopped MSK containers after cluster deletion                                   |
| `OVERCAST_EKS_MODE`              | `mock`                 | `mock` is metadata-only; `live` runs real cluster containers — see [EKS](../services/eks.md) |
| `EKS_DOCKER_SOCKET`              | _(Lambda socket)_      | Docker endpoint for EKS — Unix path or `tcp://host:port`                             |
| `OVERCAST_EFS_MODE`              | `live`                 | `live` backs file systems with real storage (inert without Docker); `mock` is metadata-only — see [EFS](../services/efs.md) |
| `EFS_DOCKER_SOCKET`              | _(Lambda socket)_      | Docker endpoint for EFS — Unix path or `tcp://host:port`                             |
| `OVERCAST_EFS_NFS`               | `false`                | Run one NFS-Ganesha export container per mount target (live mode only) — see [EFS](../services/efs.md) |
| `EFS_NFS_PORT_BASE`              | `22049`                | Starting host port for the NFS export containers                                     |
| `EFS_NFS_IMAGE`                  | `registry.k8s.io/sig-storage/nfs-provisioner@sha256:…` | Digest-pinned image used for the NFS export containers               |
| `OVERCAST_ECR_REGISTRY_PORT`     | `4510`                 | Host port the shared ECR registry container asks for; `0`, or a port already taken, falls back to an ephemeral port |
| `OVERCAST_ECR_REGISTRY_PERSIST`  | `true`                 | Back the fixed-port registry with a named Docker volume, so pushed images survive a restart |
| `OVERCAST_SMTP_MOCK`             | `true`                 | Enable built-in SMTP capture server (auto-disabled when `OVERCAST_SMTP_HOST` is set) |
| `OVERCAST_SMTP_PORT`             | `1025`                 | Port for the mock SMTP server; `0` = ephemeral. A taken default falls back to an ephemeral port — see [Running two instances on one host](./two-instances.md) |
| `OVERCAST_SMTP_HOST`             | —                      | External SMTP relay hostname (disables the mock server)                              |
| `OVERCAST_SMTP_FROM`             | `overcast@localhost`   | Envelope From address for outbound SNS email notifications                           |
| `OVERCAST_SMTP_USERNAME`         | —                      | SMTP AUTH PLAIN username for external relay                                          |
| `OVERCAST_SMTP_PASSWORD`         | —                      | SMTP AUTH PLAIN password for external relay                                          |
| `OVERCAST_SMTP_TLS`              | `false`                | Enable implicit TLS (port 465) for external relay                                    |
| `OVERCAST_SMTP_INBOX_MAX`        | `500`                  | Maximum number of captured messages retained before eviction                         |
| `OVERCAST_INIT_ENABLED`          | `true`                 | Run init-hook scripts found in `OVERCAST_INIT_DIRS` at startup; set `false` to disable |
| `OVERCAST_INIT_DIRS`             | `/etc/localstack/init,/etc/overcast/init` | Comma-separated base directories scanned for init-hook scripts in `boot.d/`, `start.d/`, `ready.d/` and `shutdown.d/` — see [Endpoints and init hooks](../migration/endpoints.md#init-hooks) |
| `OVERCAST_INIT_TIMEOUT`          | `30s`                  | Per-script timeout for init hooks                                                    |

## Related

- [Configuration](../configuration.md) — where each setting lives, and the handful most people change
- [Bind address and port](./ports.md)
- [Log levels](./log-levels.md)
- [CLI reference](../cli.md) — the flags that mirror these variables
