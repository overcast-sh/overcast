<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://overcast.sh/brand/overcast-logo-dark.svg">
    <img alt="Overcast" src="https://overcast.sh/brand/overcast-logo-light.svg" width="360">
  </picture>
</p>
<p align="center"><em>A fast, free, open-source local cloud service emulator.</em></p>

Overcast emulates the APIs of popular cloud services so you can develop and test
locally without an internet connection, a cloud account, or a bill.

[![CI](https://github.com/overcast-sh/overcast/actions/workflows/test.yml/badge.svg)](https://github.com/overcast-sh/overcast/actions)
[![GitHub release](https://img.shields.io/github/v/release/overcast-sh/overcast?include_prereleases)](https://github.com/overcast-sh/overcast/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Container image](https://img.shields.io/badge/ghcr.io-overcast--sh%2Fovercast-blue?logo=docker&logoColor=white)](https://github.com/overcast-sh/overcast/pkgs/container/overcast)

Every change is tested against **eight official AWS clients** — the AWS CLI,
the CDK, and the Go, JavaScript, Python, Java, .NET, and Rust SDKs — via the
[compatibility suite](https://github.com/overcast-sh/overcast/tree/main/docs/dev/compatibility).

---

## Project goals

1. **Works with the official AWS CLI** — `aws s3 mb s3://my-bucket --endpoint-url http://localhost:4566` just works.
2. **Works with all official AWS SDK clients** — Go, JavaScript/TypeScript, Python, Java, .NET without code changes.
3. **Drop-in replacement for LocalStack** — same port (4566), LocalStack's own env vars honoured, same path conventions, no auth token. Switching is one image line, and nothing Overcast emulates sits behind a plan; the [compatibility matrix](./docs/localstack-compatibility.md) is the item-by-item audit.
4. **Zero configuration** — `docker run -p 4566:4566 ghcr.io/overcast-sh/overcast:latest` is the full getting-started guide.
5. **Fast** — sub-50ms startup (~22ms p50, hybrid backend), <15 MiB idle memory, tiny Docker image. CI pipelines should not wait for the emulator.
6. **Honest about gaps** — unimplemented endpoints return `501 Not Implemented` with a clear message and a link to the support matrix. Silent failures are worse than loud ones.
7. **Fully open** — MIT licensed, no auth tokens, no telemetry, no usage limits, no feature gates. Free forever for every use case including CI/CD.
8. **Production-quality internals** — race-safe, well-tested, well-documented, easy to contribute to.

---

> [!CAUTION]
> Overcast is a local development and CI tool only. Never expose it on a public network,
> use it as a staging environment, or make production go/no-go decisions based on its behaviour.
> Details: [What Overcast is NOT](#what-overcast-is-not).

## Contents

- [Project goals](#project-goals)
- [Contents](#contents)
- [Quick start](#quick-start)
- [What Overcast is NOT](#what-overcast-is-not)
- [Running with Docker](#running-with-docker)
  - [docker run](#docker-run)
  - [docker compose (recommended for local dev)](#docker-compose-recommended-for-local-dev)
- [Native binaries](#native-binaries)
  - [Binary variants](#binary-variants)
  - [Installation](#installation)
  - [Commands](#commands)
  - [overcast serve](#overcast-serve)
  - [Browser-trusted HTTPS](#browser-trusted-https)
  - [Reaching it by name](#reaching-it-by-name)
- [Supported services](#supported-services)
- [Documentation](#documentation)
- [Contributing](#contributing)

---

## Quick start

Two images are published to GHCR:

| Image                         | Description                                                | Size   |
| ----------------------------- | ---------------------------------------------------------- | ------ |
| `ghcr.io/overcast-sh/overcast`      | Full image with web management console (ports 4566 + 4567) | ~50 MB |
| `ghcr.io/overcast-sh/overcast-slim` | Headless — Go binary only, no UI, no SQLite (port 4566)    | ~20 MB |

The slim image leaves out SQLite as well as the UI, so it is **memory-only
unless you set `OVERCAST_STATE=wal`** — see
[Storage and persistence](./docs/storage.md#builds-without-sqlite).

Overcast is pre-1.0, so every build publishes to the `:alpha` channel tag and to
an exact version tag such as `:0.0.1-alpha.25`. `:latest` also moves with every
build for now — tracking the newest alpha — and switches to tracking stable
releases once the first one ships. Pin the exact version in CI; use `:latest`
or `:alpha` to track the newest build.

```bash
# Full image (with web console on :4567)
docker run --rm -p 4566:4566 -p 4567:4567 ghcr.io/overcast-sh/overcast:latest

# Slim image (CI pipelines, no UI)
docker run --rm -p 4566:4566 ghcr.io/overcast-sh/overcast-slim:latest
```

Point any AWS SDK or the AWS CLI at it:

```bash
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1

# AWS CLI
aws s3 mb s3://my-bucket
aws sqs create-queue --queue-name my-queue
aws dynamodb list-tables

# No other changes needed — use the SDK exactly as you would against real AWS.
```

---

## What Overcast is NOT

| Not for                          | Why                                                                                                                                                                               |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Staging environments**         | API parity is not 100%. Differences are documented but exist.                                                                                                                     |
| **Production traffic**           | Overcast is not hardened, not monitored, not replicated.                                                                                                                          |
| **Self-hosted AWS replacement**  | Not a platform you host for others: no security boundary, no durability guarantees.                                                                                               |
| **Security testing**             | Credentials are accepted. SigV4 validation is optional, and IAM policies are not enforced as an authorization layer.                                                               |
| **Performance / load testing**   | AWS throttling, quotas, and latency are not emulated.                                                                                                                             |
| **IAM policy testing**           | Enforcement is off by default and covers identity policies only ([details](./docs/services/iam.md#request-time-enforcement-opt-in)). A development aid, not a security boundary.                                                                    |
| **CloudFormation / CDK deploys** | CloudFormation emulation supports 130+ resource types. `cdk deploy` works for stacks using [supported types](./docs/cdk/resource-types.md). Coverage is not exhaustive.  |

## Running with Docker

### docker run

```bash
# Full image with web console
docker run --rm \
  -p 4566:4566 \
  -p 4567:4567 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e OVERCAST_LOG_LEVEL=debug \
  ghcr.io/overcast-sh/overcast:latest

# With persistent data: mounting a volume at /data is the whole of it
docker run --rm \
  -p 4566:4566 \
  -p 4567:4567 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ~/.overcast:/data \
  ghcr.io/overcast-sh/overcast:latest

# Slim image (no web console) — no Docker socket needed for the services that
# start no containers (S3, SQS, DynamoDB, SNS, ...)
docker run --rm \
  -p 4566:4566 \
  ghcr.io/overcast-sh/overcast-slim:latest
```

Which backend a run gets, what survives a restart, and why a volume does
nothing on the slim image are in
[Storage and persistence](./docs/storage.md).

### docker compose (recommended for local dev)

```yaml
# docker-compose.yml
services:
  overcast:
    image: ghcr.io/overcast-sh/overcast:latest
    ports:
      - "4566:4566"
      - "4567:4567"
    environment:
      OVERCAST_LOG_LEVEL: debug
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock # required for Lambda, ECS, RDS, EC2
      - overcast-data:/data # persistence; see docs/storage.md
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:4566/_overcast/health"]
      interval: 5s
      timeout: 3s
      retries: 5

volumes:
  overcast-data:
```

```bash
docker compose up
```

### Testcontainers

Integration tests start Overcast per-test through the Testcontainers module for
Go: three lines to a running emulator and an endpoint to point an SDK client
at. [Testcontainers](./docs/testcontainers.md) has the module, the pinned-image
advice and the other-language plans.

> [!NOTE]
> **Docker socket and container-based services**
>
> Lambda, ECS, RDS, and EC2 launch sibling containers on the host's Docker
> daemon. This requires bind-mounting the Docker socket (`/var/run/docker.sock`).
> If the socket is not mounted, these services degrade gracefully — metadata
> operations (create, describe, list, delete) still work, but Lambda invocations
> return mock responses and ECS/RDS containers won't start.
>
> Services that **don't** need the Docker socket (S3, SQS, DynamoDB, SNS,
> CloudWatch Logs, SES, Secrets Manager, KMS, SSM, STS, IAM, etc.) work without it.
>
> **CI environments** where socket mounting is restricted can use a
> [Docker-in-Docker (DinD) sidecar](https://hub.docker.com/_/docker) instead.
> Set `LAMBDA_DOCKER_SOCKET` (and optionally `ECS_DOCKER_SOCKET` / `RDS_DOCKER_SOCKET`)
> to a `tcp://` endpoint:
>
> ```yaml
> services:
>   dind:
>     image: docker:dind
>     privileged: true
>     environment:
>       DOCKER_TLS_CERTDIR: "" # disable TLS for simplicity
>   overcast:
>     image: ghcr.io/overcast-sh/overcast:latest
>     ports:
>       - "4566:4566"
>     environment:
>       LAMBDA_DOCKER_SOCKET: tcp://dind:2375
>     depends_on:
>       - dind
> ```

---

## Native binaries

Download pre-built binaries from the [GitHub releases page](https://github.com/overcast-sh/overcast/releases).
No runtime dependencies — a single static binary is all you need.

### Binary variants

Two binaries are published for every release:

| Binary      | Platforms                                           | Description                                                                        |
| ----------- | --------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `overcast`  | Linux amd64/arm64, macOS amd64/arm64, Windows amd64 | Full binary — emulator + embedded web console + Go BFF. All subcommands available. |
| `overcastd` | Linux amd64/arm64, macOS amd64/arm64, Windows amd64 | Slim binary — emulator only, no web console. Smaller footprint for CI and servers.      |

Both binaries share the same `overcast serve` entrypoint and respond identically to AWS SDK clients. The only difference is that `overcastd` returns `404` for web console requests.

### Installation

**macOS / Linux:**

```bash
curl -fsSL https://overcast.sh/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://overcast.sh/install.ps1 | iex
```

The installer detects your OS and CPU, verifies the download against the release's `SHA256SUMS`, installs to a per-user directory (`~/.local/bin`, `%LOCALAPPDATA%\Programs\overcast\bin`) and never uses `sudo`. Add `--slim` (or set `OVERCAST_INSTALL_FLAVOR=slim`) for `overcastd`, `--version <tag>` to pin a release. Every flag, the environment variables for `irm | iex`, and installing by hand are in [docs/install.md](./docs/install.md); the scripts themselves are in [install/](./install/).

**Build from source:**

```bash
git clone https://github.com/overcast-sh/overcast.git && cd overcast
# Full binary (builds web console first)
cd web && pnpm install --frozen-lockfile && pnpm run build && cd ..
go build -trimpath -o overcast ./cmd/overcast

# Slim binary (no Node.js needed) — this is exactly how the released overcastd
# binaries are built. Drop `,nosqlite` to keep SQLite (and with it the hybrid
# and persistent backends) in your own build.
go build -trimpath -tags slim,nosqlite -o overcastd ./cmd/overcast
```

### Commands

All subcommands are available in both `overcast` and `overcastd` (the web console is absent in the slim binary). Run `overcast --help` or `overcast <command> --help` for the full flag reference, or see the [CLI reference](./docs/cli.md) for every command's flags, defaults, and examples in one place.

| Command                       | Description                                                              |
| ------------------------------ | ---------------------------------------------------------------------------- |
| `overcast serve`              | Start the AWS service emulator                                           |
| `overcast start` / `stop` / `restart` | Run `serve` as a named background instance (native or `--docker`)   |
| `overcast status`             | Check a running daemon is reachable (version, state backend)             |
| `overcast wait`               | Block until a daemon reports healthy (CI-friendly)                       |
| `overcast logs`               | Tail a background instance's output                                      |
| `overcast services`           | List enabled services and their emulation tiers                          |
| `overcast reset`              | Wipe emulated state, all or one service                                  |
| `overcast network`            | Report Docker networks that have drifted from your configuration, and rebuild them |
| `overcast config`             | Show the daemon's effective configuration (needs `OVERCAST_DEBUG=true`)  |
| `overcast env`                | Print AWS environment exports for pointing tools at Overcast             |
| `overcast aws`                | Run the host AWS CLI against Overcast, environment scrubbed first        |
| `overcast import cognito-users` | Import Cognito users from real AWS into Overcast                       |
| `overcast bridge`             | Publish `.local` domains via mDNS and start a port-80 reverse proxy      |
| `overcast https`              | One-shot browser-trusted HTTPS setup (CA + trust store + certificate)    |
| `overcast trust`              | Manage the local trust store for self-signed TLS certificates            |

### overcast serve

Starts the emulator on port 4566. All emulator configuration is environment
variables — the [environment variable
reference](./docs/configuration/reference.md) has every one:

```bash
overcast serve

OVERCAST_PORT=4566 OVERCAST_STATE=hybrid OVERCAST_LOG_LEVEL=debug   overcast serve
```

The web console (full binary only) is served on port 4567 and loads lazily on
first request. `--ui-port 0` disables it. Storage is the other place the two
binaries differ — the released `overcastd` is built without SQLite; see
[Builds without SQLite](./docs/storage.md#builds-without-sqlite).

### Browser-trusted HTTPS

Serving the API and console over TLS unlocks browser HTTP/2, which keeps the
console responsive under load:

```bash
overcast https enable            # once per machine; approve the OS prompt
OVERCAST_TLS=auto overcast serve # → https://localhost.overcast.sh:4567
```

Docker, WSL, bringing your own certificate, and installing the CA by hand are all
in [docs/https.md](./docs/https.md). The lower-level `overcast trust` subcommands
are there too.

### Reaching it by name

`overcast bridge` publishes `overcast.local` and `overcast-app.local` over mDNS
and proxies port 80 by `Host` header, so `.local` names work with no hosts-file
edits and no port numbers. Flags and the per-platform mDNS/port-80 setup are in
the [CLI reference](./docs/cli/bridge.md).


---

## Supported services

<!-- BEGIN overcast:root-service-list -->

Overcast currently registers **51 AWS services**. Coverage ranges from broad
service emulation to minimal discovery/IaC stubs; check the per-service docs for
exact endpoint support.

[ACM](./docs/services/acm.md), [API Gateway](./docs/services/apigateway.md), [AppConfig](./docs/services/appconfig.md), [AppConfigData](./docs/services/appconfigdata.md),
[AppRegistry](./docs/services/appregistry.md), [AppSync](./docs/services/appsync.md), [Athena](./docs/services/athena.md), [Auto Scaling](./docs/services/autoscaling.md),
[Backup](./docs/services/backup.md), [Bedrock](./docs/services/bedrock.md), [CloudFormation](./docs/services/cloudformation.md), [CloudFront](./docs/services/cloudfront.md),
[CloudTrail](./docs/services/cloudtrail.md), [CloudWatch](./docs/services/cloudwatch.md), [CloudWatch Logs](./docs/services/cloudwatch-logs.md), [Cognito](./docs/services/cognito.md),
[DynamoDB](./docs/services/dynamodb.md), [DynamoDB Streams](./docs/services/dynamodbstreams.md), [EC2 / VPC](./docs/services/ec2.md), [ECR](./docs/services/ecr.md),
[ECS](./docs/services/ecs.md), [EFS](./docs/services/efs.md), [EKS](./docs/services/eks.md), [ElastiCache](./docs/services/elasticache.md),
[ELBv2](./docs/services/elb.md), [EventBridge](./docs/services/eventbridge.md), [Firehose](./docs/services/firehose.md), [Glue](./docs/services/glue.md),
[IAM](./docs/services/iam.md), [Kinesis](./docs/services/kinesis.md), [KMS](./docs/services/kms.md), [Lambda](./docs/services/lambda.md),
[MSK](./docs/services/msk.md), [OpenSearch](./docs/services/opensearch.md), [Organizations](./docs/services/organizations.md), [Pipes](./docs/services/pipes.md),
[RDS](./docs/services/rds.md), [Route 53](./docs/services/route53.md), [S3](./docs/services/s3.md), [S3 Tables](./docs/services/s3tables.md),
[Scheduler](./docs/services/scheduler.md), [Secrets Manager](./docs/services/secretsmanager.md), [SES](./docs/services/ses.md), [Shield](./docs/services/shield.md),
[SNS](./docs/services/sns.md), [SQS](./docs/services/sqs.md), [SSM](./docs/services/ssm.md), [Step Functions](./docs/services/stepfunctions.md),
[STS](./docs/services/sts.md), [Transfer Family](./docs/services/transfer.md), [WAF v2](./docs/services/waf.md).

Some services require Docker socket access for full runtime behaviour:

- Lambda, ECS, RDS, EC2/VPC, and ElastiCache can launch sibling containers.
- Without Docker, their metadata/control-plane APIs still work where possible,
  but runtime execution falls back to metadata-only or stub behaviour.

IAM is implemented for local development and CloudFormation/CDK compatibility,
but IAM policies are not enforced as an authorization layer.

See the [service emulation reference](./docs/services/) for per-endpoint
coverage tables, or browse the generated summary in [STATUS.md](./STATUS.md#service-coverage).

<!-- END overcast:root-service-list -->

---

## Documentation

**[The reference index](./docs/README.md)** routes every guide by the job you
are doing — getting running, building against it, tuning and inspecting it.
Four of them answer most first questions:

- [Using AWS SDKs and CLI](./docs/sdk-cli.md) — pointing the CLI, or any SDK, at Overcast
- [Service reference](./docs/services/README.md) — what each service supports, operation by operation
- [Configuration](./docs/configuration.md) — where each setting lives, and every variable with its default
- [Troubleshooting](./docs/troubleshooting.md) — a symptom, and where its answer lives

---

## Contributing

See [CONTRIBUTING.md](https://github.com/overcast-sh/overcast/blob/main/CONTRIBUTING.md) for coding standards, workflow, and how
to build from source.

## Disclaimer

Overcast is an independent open-source project. It is **not affiliated with,
endorsed by, or sponsored by Amazon Web Services**. "AWS" and all AWS service
names are trademarks of Amazon.com, Inc. or its affiliates, used here solely to
describe compatibility.

Overcast is a **work in progress**, provided **as-is** and on a
**best-effort basis**, without warranty of any kind, under the
[MIT License](LICENSE). It aims for high fidelity on the most-used AWS API
surface, but it is not a perfect replica: there are compatibility gaps we know
about (documented in the per-service [support matrices](docs/services/)) and
inevitably some we haven't found yet. Fidelity improves all the time — and
discrepancy reports are what drive that work. If you find behaviour that
differs from real AWS, please
[open a compatibility issue](https://github.com/overcast-sh/overcast/issues/new?template=compat_review.md).
