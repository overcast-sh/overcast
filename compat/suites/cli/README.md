# cli suite

Runs the Overcast AWS compatibility matrix through the **AWS CLI v2** rather
than an SDK. The test bodies are Go (its own module,
`compat/suites/cli/go.mod`); every assertion is made against the JSON the
`aws` binary printed.

> **Status: implemented.** See [AGENTS.md](AGENTS.md) for code conventions.

Tests cover every AWS service the registry declares — including ones not yet
implemented in Overcast. Failures on unimplemented services are expected and
are the coverage metric, not a problem to fix.

---

## What it covers

The widest service list of any suite, because the CLI exposes services no SDK
group here has caught up with yet: S3, SQS, DynamoDB, SNS, Lambda, CloudWatch
Logs, SES, IAM, STS, Secrets Manager, KMS, SSM, Kinesis, EventBridge,
CloudFormation, EC2, ECS, ECR, Cognito, AppSync, API Gateway, CloudFront, RDS,
Step Functions, EventBridge Pipes, WAFv2, Shield, ElastiCache, EFS, AppConfig,
AppConfig Data, OpenSearch, Backup, MSK and EKS — `groups.All` in
[internal/groups/groups.go](internal/groups/groups.go) is the authoritative,
registration-order list.

Beyond service coverage, this suite is where CLI-specific behaviour is
checked: the CLI's own request shaping, its JSON output, and the handful of
tests that need a signed request (`awscli.RunSigned`) rather than the
`--no-sign-request` every other call uses.

---

## Prerequisites

- **AWS CLI v2** on `PATH`. Without it the suite emits every test as `na`
  ("aws CLI not found in PATH") and exits cleanly, rather than failing the
  run — so a machine without it reports an honest gap instead of a red wall.
- Go to build the runner — this module declares `go 1.22`, and CI installs
  the version the root `go.mod` names (verified here against Go 1.27).
- Docker, for the tests the registry marks `requires: [docker]`. The runner
  probes for it (`docker info`) and skips those tests when the daemon does not
  answer.
- Overcast running somewhere reachable — see
  [compat/AGENTS.md § Running a session](../../AGENTS.md#running-a-session--ports-are-chosen-never-assumed)
  for why `4566`/`4567` are off-limits for a test instance you start yourself.

No real AWS credentials are needed. Every call gets `--no-sign-request`, and
the few that must be signed run with placeholder credentials in a scrubbed
environment — `signingEnv` in `internal/awscli/awscli.go` drops the ambient
`AWS_*` variables rather than trusting the shell's.

### The pinned CLI version

The devcontainer installs AWS CLI v2 automatically, pinned by
`AWS_CLI_VERSION` in `.devcontainer/Dockerfile`; the same version is recorded
in [requirements.txt](requirements.txt) alongside the manual install command.
A local run and the GitHub-hosted CI runner both use whatever `aws` is already
on `PATH`, so the pin governs the container path rather than every path.

To upgrade, bump `AWS_CLI_VERSION` in `.devcontainer/Dockerfile`, update
`requirements.txt` to match, and rebuild the container.

---

## Running the suite

### Locally (Go and the AWS CLI required)

```bash
cd compat/suites/cli
go build ./...   # compiles the runner and the unit-test packages
go test ./...    # registry, registration and awscli unit tests; no emulator needed

# Start Overcast first (separate terminal), e.g.:
#   go run ./cmd/overcast serve

OVERCAST_ENDPOINT=http://localhost:4566 go run ./cmd/runner
```

PowerShell:

```powershell
cd compat/suites/cli
go build ./...
go test ./...

$env:OVERCAST_ENDPOINT = "http://localhost:4566"
go run ./cmd/runner
```

### Via Docker (no local toolchain required)

This suite ships no image of its own. It runs as a subprocess of the compat
runner container, which carries Go and the pinned AWS CLI. From the repo root:

```bash
OVERCAST_COMPAT_SUITE=cli docker compose -f compat/docker-compose.yml run --rm compat
```

Arguments after the compose service name reach the container entrypoint rather
than the runner, which is why the suite selection is an environment variable —
see [compat/AGENTS.md § Running suites](../../AGENTS.md#running-suites-docker--ci).

### Via the Go CLI (recommended — runs all suites, or just this one)

```bash
# Starts its own Overcast instance on a free port and stops it afterwards:
go run ./cmd/compat
# or just this suite:
go run ./cmd/compat --suite cli
# or against an instance you are already running:
go run ./cmd/compat --endpoint http://localhost:4566 --suite cli
# with the dashboard, hot-reloading:
go run ./cmd/compat --dev --suite cli
```

This is what CI runs. `cmd/compat` spawns `go run ./cmd/runner` in this
directory — see `defaultSuites` in [compat/runner.go](../../runner.go).

---

## Environment variables

| Variable                         | Default                 | Description                                                                        |
| -------------------------------- | ----------------------- | ------------------------------------------------------------------------------------ |
| `OVERCAST_ENDPOINT`              | `http://localhost:4566` | Overcast base URL, passed to every call as `--endpoint-url`                        |
| `OVERCAST_DEFAULT_REGION`        | `us-east-1`             | Passed to every call as `--region`                                                 |
| `OVERCAST_REGISTRY_PATH`         | `../registry.json`\*    | Override the path to `registry.json`                                               |
| `OVERCAST_COMPAT_RUN_ID`         | `local`                 | Prefix for resource names, so concurrent runs and the orphan sweep do not collide  |
| `OVERCAST_COMPAT_SERVICE`        | unset (all)             | Single AWS service name to run, e.g. `s3`                                          |
| `OVERCAST_COMPAT_GROUPS`         | unset (all)             | Comma-separated group names to run                                                 |
| `OVERCAST_COMPAT_TESTS`          | unset (all)             | Comma-separated test names to run within those groups                              |
| `OVERCAST_COMPAT_TEST_PAIRS`     | unset                   | Comma-separated `group:test` pairs — overrides the three filters above             |
| `OVERCAST_COMPAT_PARALLEL_SLOTS` | `8`                     | Max groups run concurrently in interactive mode                                    |
| `OVERCAST_COMPAT_INTERACTIVE`    | unset                   | Set to `1` to serve the interactive command protocol instead of one batch run      |

\* Resolved relative to the process working directory when unset, so the suite
finds it at `../registry.json` when run from `compat/suites/cli/`.

The `docker` capability is **probed, not configured**: the runner looks for a
`docker` binary and a daemon that answers `docker info`. There is no
`OVERCAST_COMPAT_SKIP_DOCKER` here.

---

## Architecture

```
cli/
  go.mod             ← its own module (no AWS SDK dependency at all)
  requirements.txt   ← the pinned AWS CLI version and its install command
  README.md          ← you are here

  cmd/runner/main.go ← entry point: merges impls, loads the registry, probes for
                       docker, then runs once or serves the interactive loop

  internal/
    awscli/awscli.go ← the only place the `aws` binary is spawned: Run,
                       RunOutput, RunWithStdin, RunStatus and their signed
                       variants, plus the environment scrubbing
    harness/
      harness.go     ← TestContext, TestCase, TestGroup, RunGroup, RunSuite,
                       IsUnimplemented, the NDJSON emitters
      namer.go       ← Namer: per-group resource names scoped to the run
    registry/registry.go ← loads registry.json + registry.generated.json, merges
                           and validates impl keys, builds TestGroups
    scenario/        ← the scenario backend: runs a generated group straight
                       from compat/model/scenarios/<service>.json (loader,
                       expr/path, executor, assert, failure)
    groups/          ← one file per AWS service
      groups.go      ← the ServiceGroup type and All(), the registration point
      s3.go  sqs.go  dynamodb.go  …
```

The group list is **not** defined here. It comes from the shared cross-suite
registry at [compat/suites/registry.json](../registry.json), the single source
of truth for which groups and tests exist across every suite. `cmd/runner`
loads it, collects this suite's implementations keyed `group:Test`, and builds
the groups from it.

### Generated groups

Alongside `registry.json` the loader reads its machine-written sibling
`registry.generated.json`. A group there has no Go implementation: it names a
scenario file under [compat/model/scenarios/](../../model/scenarios), and
`internal/scenario` executes it — the calls, the assertions, the setup and the
teardown all come out of that file. `aws <service> <kebab-op> --cli-input-json`
is still the only way a call is made, through the same `internal/awscli`
helpers every hand-written group uses.

Nothing about a generated group is edited here: the scenario files and the
generated registry are both written by `cmd/compatgen` from
[compat/model/recipes/](../../model/recipes), and `make compat-model-check`
fails on a hand edit. `compat/model/README.md` is the normative description of
what the interpreter executes.

### Key types

| Type / function          | Purpose                                                                                  |
| ------------------------ | ------------------------------------------------------------------------------------------ |
| `harness.TestFn`         | `func(context.Context, *TestContext) error` — return `nil` to pass, an error to fail     |
| `harness.TestContext`    | `Endpoint`, `Region`, `RunID`, `Log()`, plus a `Set`/`Get`/`GetString` bag for group state |
| `harness.Namer`          | `NewNamer("sqs-q").Name(t)` → `"{runID}-sqs-q"`, so parallel groups cannot collide       |
| `awscli.Run`             | Runs a command, discarding output; error on a non-zero exit                              |
| `awscli.RunOutput`       | Runs a command and returns the parsed JSON object                                        |
| `awscli.RunStatus`       | Returns the HTTP status the CLI's debug log reported, for error-shape assertions          |

---

## Adding a new group

1. Add the group and its tests to [compat/suites/registry.json](../registry.json).
   Nothing runs until it is declared there, and every other suite immediately
   shows the new tests as skips — which is the point.
2. Open (or create) `internal/groups/<service>.go` — one file per AWS service.
3. Register `group:Test` keys in `Impls`, plus the group's `Setup`/`Teardown`.
4. For a new file, add the constructor to `All()` in `internal/groups/groups.go`.
5. Run `go test ./...` — the registration tests resolve this suite's real impl
   keys against the real `registry.json`, so a mis-keyed registration fails
   without a run.

See [AGENTS.md](AGENTS.md) for the exact shape and the teardown rules.
