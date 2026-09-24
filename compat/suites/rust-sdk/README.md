# rust-sdk suite

Runs part of the Overcast AWS compatibility matrix using the **AWS SDK for
Rust** (edition 2021, Tokio, built and run as a Docker image).

> **Status: implemented.** See [AGENTS.md](AGENTS.md) for code conventions.

Every test the registry declares is reported: the ones this suite implements
run, and the rest come back as skips. Failures on services Overcast has not
implemented are expected and are the coverage metric, not a problem to fix.

---

## What it covers

Ten services, deliberately fewer than the SDK suites that aim at full parity:
S3, SQS, DynamoDB, SNS, Lambda, STS, KMS, Secrets Manager, SSM and
EventBridge. `all_service_groups` in [src/main.rs](src/main.rs) is the
authoritative list, and `Cargo.toml` carries one pinned `aws-sdk-*` crate per
service. Every registry test this suite has not implemented is emitted as a
`skip`, so the coverage gap is visible rather than silent.

The suite also loads [compat/suites/registry.generated.json](../registry.generated.json)
and runs every group marked there for `rust-sdk`. Those are **generated**: this
is a source-emitting backend, so `cmd/compatgen` writes one Rust function per
scenario test into `src/groups/scenarios_*_gen.rs` and `cargo build` compiles
it, while the semantics they call into are hand-written once in
[src/scenario/](src/scenario/). Never hand-edit a generated file, and never run
`cargo fmt` over the whole crate — see
[AGENTS.md § Generated groups](AGENTS.md#generated-groups-and-the-scenariobackend-hook).

---

## Prerequisites

- **Docker.** This suite has no local-toolchain path: `run.sh` builds and runs
  an image, and `cmd/compat` invokes `sh run.sh`. A Rust toolchain on the host
  is optional and used only by `cargo` commands you run yourself.
- Overcast running somewhere reachable — see
  [compat/AGENTS.md § Running a session](../../AGENTS.md#running-a-session--ports-are-chosen-never-assumed)
  for why `4566`/`4567` are off-limits for a test instance you start yourself.

No AWS credentials are needed: the clients are built with the fixed pair
`test`/`test`, which the emulator accepts without validating.

---

## Running the suite

### Via the Go CLI (recommended — runs all suites, or just this one)

```bash
# Starts its own Overcast instance on a free port and stops it afterwards:
go run ./cmd/compat
# or just this suite:
go run ./cmd/compat --suite rust-sdk
# or against an instance you are already running:
go run ./cmd/compat --endpoint http://localhost:4566 --suite rust-sdk
```

`cmd/compat` spawns `sh run.sh` for this suite. That script resolves the image
in three steps — a local image with the exact tag, then a `docker pull` of the
same tag from GHCR, then a local build — and chooses the container's network
mode for you, including the `host.docker.internal` rewrite a Docker Desktop
host needs. Set `OVERCAST_RUST_SKIP_PULL=1` to skip the registry step.

### Via Docker, by hand

The **build context is `compat/suites/`**, not this directory: the image also
copies in the shared `registry.json` and `registry.generated.json` (see
[compat/AGENTS.md § Running suites](../../AGENTS.md#running-suites-docker--ci)).

```bash
docker build -f compat/suites/rust-sdk/Dockerfile \
  -t "oc-rust-sdk-compat:$(sh scripts/image-tag.sh)" compat/suites

docker run --rm \
  -e OVERCAST_ENDPOINT=http://host.docker.internal:4566 \
  "oc-rust-sdk-compat:$(sh scripts/image-tag.sh)"
```

`scripts/image-tag.sh` at the repo root names the image after the current
branch, which keeps parallel worktrees from overwriting each other's build;
delete it with `docker rmi` when you are done (root
[AGENTS.md § Docker images](../../../AGENTS.md#docker-images--one-tag-per-branch-and-remove-it-afterwards)).
`run.sh` instead uses this suite's own
[image-tag.sh](image-tag.sh), which hashes the Rust sources and both registry
files so the image rebuilds exactly when its inputs change.

On a Docker Desktop host (verified here on Windows), `--network host` does not
share the host's network stack, so a container cannot reach an Overcast
process on the host's `localhost` that way — address it as
`host.docker.internal` instead, as above.

### Via the compose harness

```bash
OVERCAST_COMPAT_SUITE=rust-sdk docker compose -f compat/docker-compose.yml run --rm compat
```

The runner container has the host Docker socket, so this suite's image runs as
a sibling container rather than a nested one.

### Where the unit tests run

The image's build stage runs `cargo test` before `cargo build` (see
[Dockerfile](Dockerfile)), so the registry, registration and scenario-runtime
tests run on every image build and a broken registration fails it. That is what
keeps a host Rust toolchain optional: the crate is an ordinary Cargo project, so
`cargo` works here if you have it, but nothing in the suite's own paths needs
it.

One case needs a checkout rather than the image. The shared error-matching
conformance fixtures live in `compat/model/testdata/errors`, outside the
`compat/suites` build context, so in the image build
`src/scenario/errorfixtures.rs` says so on stderr and returns — as do
`nowfixtures.rs` and `equalsjsonfixtures.rs` for the `$now` and `equalsJSON`
fixtures beside them in `compat/model/testdata`. `test.yml`'s
`compat-suite-unit-tests` job runs `cargo test` from a full checkout beside the
other suites' unit tests, which is where those fixtures are really answered.

---

## Environment variables

| Variable                         | Default                 | Description                                                                                            |
| -------------------------------- | ----------------------- | ------------------------------------------------------------------------------------------------------ |
| `OVERCAST_ENDPOINT`              | `http://localhost:4566` | Overcast base URL. `run.sh` rewrites a loopback host to `host.docker.internal` when it starts the container from outside one |
| `OVERCAST_DEFAULT_REGION`        | `us-east-1`             | AWS region advertised to the SDK                                                                       |
| `OVERCAST_REGISTRY_PATH`         | `../registry.json`\*    | Override the path to `registry.json`. The image sets it to `/registry.json`                            |
| `OVERCAST_COMPAT_RUN_ID`         | `local`                 | Prefix for resource names, so concurrent runs and the orphan sweep do not collide                      |
| `OVERCAST_COMPAT_SKIP_DOCKER`    | unset                   | Set to `1` to drop the `docker` capability, skipping every test the registry marks `requires: [docker]` |
| `OVERCAST_COMPAT_SERVICE`        | unset (all)             | Comma-separated AWS service names to run, e.g. `s3`                                                    |
| `OVERCAST_COMPAT_GROUPS`         | unset (all)             | Comma-separated group names to run                                                                     |
| `OVERCAST_COMPAT_TESTS`          | unset (all)             | Comma-separated test names to run within those groups                                                  |
| `OVERCAST_COMPAT_TEST_PAIRS`     | unset                   | Comma-separated `group:test` pairs — overrides the three filters above                                 |
| `OVERCAST_COMPAT_PARALLEL_SLOTS` | `8`                     | Max groups run concurrently                                                                            |
| `OVERCAST_COMPAT_INTERACTIVE`    | unset                   | Set to `1` to serve the interactive command protocol instead of one batch run                          |
| `OVERCAST_RUST_SKIP_PULL`        | unset                   | `run.sh` only: skip the GHCR pull and build locally instead                                            |
| `OVERCAST_RUST_REMOTE_IMAGE`     | `ghcr.io/overcast-sh/overcast/rust-sdk-compat` | `run.sh` only: registry image to pull the tagged build from                     |

\* Resolved relative to the process working directory when unset.
`registry.generated.json` is always read from that file's own directory.

---

## Architecture

```
rust-sdk/
  Cargo.toml       ← one pinned aws-sdk-* crate per service, plus tokio and serde
  Dockerfile       ← rust:slim-bookworm build stage (runs cargo test) →
                     debian:bookworm-slim runtime carrying both registry files
  run.sh           ← resolves/builds the image and runs it; what cmd/compat invokes
  image-tag.sh     ← content hash of the sources and both registry files
  BUILD_NOTES.md   ← the BuildKit cache-mount notes behind the Dockerfile
  README.md        ← you are here

  src/
    main.rs        ← entry point: builds clients, merges impls, loads the registry,
                     then runs once or serves the interactive command loop.
                     Also carries the registration test.
    clients.rs     ← AwsClients: one constructor per service over a shared SdkConfig
    harness.rs     ← TestContext, TestCase, TestGroup, run_suite, sdk_error,
                     the NDJSON emitters and the stdin command loop
    registry.rs    ← loads registry.json + registry.generated.json, merges and
                     validates impl keys, builds groups; unit tests live here
    groups/
      mod.rs       ← the ServiceGroup trait and the module list
      s3.rs  sqs.rs  dynamodb.rs  sns.rs  lambda.rs
      sts.rs  kms.rs  secretsmanager.rs  ssm.rs  eventbridge.rs
      scenarios_gen.rs  scenarios_<service>_gen.rs
                   ← GENERATED by cmd/compatgen; never hand-edited
    scenario/      ← the runtime those generated groups call into: the context
                     bag, the IR's value expressions, the closed check set,
                     error matching, `eventually` and the six-field failure
                     message, written once by hand
```

The group list is **not** defined here. It comes from the shared cross-suite
registry at [compat/suites/registry.json](../registry.json), the single source
of truth for which groups and tests exist across every suite. `main.rs` loads
it, collects this suite's implementations keyed `group:Test`, and builds the
groups from it.

### Key types (`src/harness.rs`)

| Type / function         | Purpose                                                                                     |
| ----------------------- | --------------------------------------------------------------------------------------------- |
| `TestFn`                | `Arc<dyn Fn(TestContext) -> TestFuture>` returning `Result<(), String>`                      |
| `TestContext`           | `endpoint`, `region`, `run_id`, `log()`, plus a `set`/`get` string bag shared across a group |
| `TestCase`/`TestGroup`  | What the registry builds: a named group of tests with optional setup and teardown            |
| `run_suite`             | Runs every group and emits NDJSON to stdout                                                  |
| `sdk_error(err)`        | Renders an `SdkError` with its whole `source()` chain — the code, message and request id     |

---

## CI

`rust-sdk` is one of the eight suites in the compat matrix
(`.github/workflows/compat.yml`). A separate `rust-sdk-image` job builds the
image from this directory's `Dockerfile` under the tag `image-tag.sh` computes
and, on non-pull-request events, pushes it to GHCR — which is what makes
`run.sh`'s pull step a fast path rather than a rebuild on every run.
