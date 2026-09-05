# AGENTS.md — cli suite

> Conventions for AI agents and contributors working in `compat/suites/cli/`.
>
> **Read [compat/AGENTS.md](../../AGENTS.md) first** — it contains the
> canonical teardown rules, the separation boundary, and the `group:test`
> implementation-key rules that apply to every suite.
> This file covers only cli-specific details.
>
> For quick-start, prerequisites, env vars and architecture see
> [README.md](README.md).

---

## What this suite tests

Every AWS service operation reachable via the **AWS CLI v2**. It is the CLI
column of the compatibility matrix. Failures on unimplemented services are
correct and expected — they are the coverage gap metric, not bugs to silence.

---

## Status

**Implemented**, and the widest suite here: `groups.All` wires 35 service
groups, several of them (AppConfig, AppConfig Data, OpenSearch, Backup, MSK,
EKS, ECR) with no counterpart in any SDK suite yet. It runs in the compat CI
matrix (`.github/workflows/compat.yml`) alongside every other suite.

---

## Runtime

| Item        | Value                                                                                                     |
| ----------- | ----------------------------------------------------------------------------------------------------------- |
| Language    | Go, its own module (`go.mod`) — with no AWS SDK dependency at all                                          |
| AWS client  | The `aws` v2 binary on `PATH`, spawned only from `internal/awscli`                                          |
| Version pin | `AWS_CLI_VERSION` in `.devcontainer/Dockerfile`, mirrored in `requirements.txt`. It governs the container path; a local run and the GitHub-hosted runner use whatever `aws` is on `PATH` |
| CI image    | None of its own — GitHub Actions installs Go from the root `go.mod` and uses the runner's `aws`; the compose path uses `.devcontainer/Dockerfile`, which carries both |

> SDK upgrade policy: [compat/AGENTS.md § SDK version pinning](../../AGENTS.md#sdk-version-pinning--upgrade-strategy).

**No `aws` on `PATH` is not a failure.** `cmd/runner` emits every registry
test as `na` with the reason "aws CLI not found in PATH" and exits cleanly, so
a machine without the CLI reports an honest gap rather than a wall of red.
The `docker` capability is likewise probed (`docker info`) rather than
configured: tests the registry marks `requires: [docker]` are skipped when no
daemon answers, and there is no `OVERCAST_COMPAT_SKIP_DOCKER` here.

---

## File layout

```
compat/suites/cli/
  AGENTS.md          ← you are here
  README.md          ← quick-start, prerequisites, env vars, architecture
  go.mod
  requirements.txt   ← the pinned AWS CLI version and its install command
  cmd/runner/        ← binary entry point
  internal/
    awscli/          ← the only place the `aws` binary is spawned
    harness/         ← TestContext, TestFn, TestGroup, RunSuite, Namer
    registry/        ← registry loading, impl-key merge/validate, group building
    scenario/        ← the scenario backend: executes the generated IR
    groups/          ← one file per AWS service
      groups.go      ← the ServiceGroup type and All(), the registration point
      groups_test.go ← this suite's registrations vs. the real registry.json
      s3.go
      sqs.go
      ...
```

**One file per AWS service.** Never split a service across files.

---

## Group anatomy

A service file exports one constructor returning a `ServiceGroup`: three maps
that `cmd/runner` merges into the suite's registrations at startup. This is the
real `STS` constructor (`internal/groups/sts.go`), trimmed:

```go
func STS() ServiceGroup {
    g := &stsGroup{}
    return ServiceGroup{
        Impls: map[string]harness.TestFn{
            "sts-identity:GetCallerIdentity": g.GetCallerIdentity,
            "sts-assume:AssumeRole":          g.AssumeRole,
            // … one entry per test, keyed "group:Test"
        },
        Setup: map[string]func(context.Context, *harness.TestContext) error{
            "sts-identity": g.setupIdentity,
        },
        Teardown: map[string]func(context.Context, *harness.TestContext) error{
            "sts-identity": g.teardownNoop,
        },
    }
}

type stsGroup struct{}

func (g *stsGroup) GetCallerIdentity(_ context.Context, t *harness.TestContext) error {
    out, err := awscli.RunOutput(t.Endpoint, t.Region, "sts", "get-caller-identity")
    if err != nil {
        return err
    }
    if out["Account"] == nil {
        return fmt.Errorf("sts GetCallerIdentity: missing Account")
    }
    return nil
}
```

The `awscli` helpers, and when to reach for each:

| Helper                     | Returns                                  | Use for                                          |
| -------------------------- | ---------------------------------------- | ------------------------------------------------ |
| `Run`                      | error only                               | a call whose output you do not read              |
| `RunOutput`                | parsed JSON object                       | anything you assert on                           |
| `RunWithStdin`             | error only                               | a command taking a blob on stdin                 |
| `RunOutputWithStdin`       | parsed JSON object                       | as above, with output                            |
| `RunStatus`                | the HTTP status from the CLI's debug log | asserting an error's status code                 |
| `RunSigned` / `RunOutputSigned` / `RunStatusSigned` | as their unsigned twins | the few calls that must carry SigV4     |

Every one of them injects `--endpoint-url`, `--region` and `--output json`.
The unsigned majority also pass `--no-sign-request`; the signed variants run
with placeholder credentials in a **scrubbed** environment (`signingEnv`
drops the ambient `AWS_*` variables rather than trusting the shell's).

---

## Naming conventions

| Element         | Convention                                                                                                                                                                 |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Impl map key    | `<group>:<Test>` — **always group-qualified**, never bare (see [compat/AGENTS.md § Implementation keys](../../AGENTS.md#implementation-keys--grouptest-and-a-bad-key-aborts-the-run)) |
| Group name      | `<service>-<feature>` (kebab-case), e.g. `s3-crud`, `iam-roles`                                                                                                              |
| Test name       | PascalCase AWS operation name, e.g. `CreateBucket`, `AssumeRole`                                                                                                            |
| Resource names  | A package-level `harness.NewNamer("<short tag>")` per group; `Name(t)` returns `"{runID}-<tag>"`, and `Suffixed(t, ".fifo")` covers a mandatory suffix                       |
| Service file    | Lowercase service name: `s3.go`, `cloudwatch.go`                                                                                                                            |
| Struct          | `type <service>Group struct{}`                                                                                                                                              |
| Context key     | snake_case string, e.g. `"s3_bucket"`, `"kms_key_id"`                                                                                                                       |

---

## Teardown rules (cli-specific additions)

The canonical teardown rules are in [compat/AGENTS.md](../../AGENTS.md).
Additional Go/CLI specifics:

- Ignore errors on teardown calls with `//nolint:errcheck`.
- Use `awscli.RunOutput` when you need the JSON response to iterate over
  resources (e.g. listing uploads before aborting them).
- `teardownBucket` uses `list-objects-v2` — it does **not** handle versioned
  objects. Use `teardownVersioning` (or equivalent) for versioned buckets,
  which calls `list-object-versions` and deletes all versions and delete
  markers explicitly.
- `teardownMultipart` aborts incomplete uploads via `list-multipart-uploads`
  before deleting the bucket. Use it (not `teardownBucket`) for the
  `s3-multipart` group.

---

## Inter-test state

Use `t.Set` / `t.GetString` to pass data between sequential tests in a group:

```go
// in setup:
t.Set("s3_bucket", bucket)

// in a later test or in teardown:
bucket := t.GetString("s3_bucket")
```

A fresh `TestContext` is created per group, so never rely on inter-group
state. Store every created resource's name or ID during setup, so teardown can
read it back rather than recomputing it.

---

## Error messages

Return an error naming the operation, what was expected, and enough context to
diagnose the failure without re-running it:

```go
return fmt.Errorf("s3 ListObjectsV2: key %q not found in bucket %s (runID=%s)", key, bucket, t.RunID)
```

`awscli` already wraps a non-zero exit with the command and the CLI's own
stderr, so pass that error through rather than replacing it with a summary.

---

## Generated groups

`internal/registry` loads `registry.json` and its machine-written sibling
`registry.generated.json` (concatenated, hand-written groups first — see
[compat/AGENTS.md § registry.json](../../AGENTS.md#registryjson--canonical-test-matrix)).
A generated group carries a `scenario` path instead of a registered impl, and
**`internal/scenario` executes it** — the suite's scenario backend (G2, #1768).
`cmd/runner` installs it as `BuildGroupsOptions.Scenario` and takes each
generated group's setup and teardown from the same file.

Rules for that package:

- The IR it executes is specified by [compat/model/README.md](../../model/README.md).
  That file is normative; where it and this interpreter disagree, one of them is
  a bug, and the IR is settled across three suites — take a change to #1113
  rather than making it here.
- **Never hand-edit `compat/model/scenarios/*.json` or
  `registry.generated.json`.** `cmd/compatgen` rewrites both wholly, and
  `make compat-model-check` fails on a hand edit. A missing generated test is
  a recipe change, not a scenario-file change.
- Every call still goes through `internal/awscli`, as
  `aws <endpointPrefix> <kebab-op> --cli-input-json '<json>'`. There is no
  second process-spawning path, and the operation → subcommand derivation is
  botocore's `xform_name`, pinned against the real CLI by
  `TestKebabOpMatchesTheCLI`.
- Every failure carries the six fields in
  [compat/model/README.md § Failure messages](../../model/README.md#failure-messages),
  built by the single helper in `failure.go`. Its wording deliberately avoids
  every phrase `harness.IsUnimplemented` matches, so an assertion failure can
  never be mis-reported as `unimplemented` — and the CLI's own error text is
  quoted verbatim so a real 501 still is.
- One process is spawned per call, so `eventually` is expensive: honour the
  IR's `maxAttempts`/`delayMs` exactly and add no sleeps of your own.
- Unit tests use the in-memory fake runner in `executor_test.go`; the package's
  tests never spawn `aws` and never need an emulator.

---

## Registration tests

`internal/groups/groups_test.go` resolves this suite's real registrations
against the real `registry.json` without starting a run:

- `TestRegisteredImplsResolveAgainstRegistry` — every impl key resolves to a
  real `group:test` pair.
- `TestRegisteredImplsHaveNoDuplicateKeys` — no two service files register the
  same key.
- `TestRegisteredImplsHaveNoBareKeys` — every key is group-qualified.

`internal/registry/registry_test.go` covers the loader itself, and
`internal/awscli/awscli_test.go` pins the two pieces of CLI output parsing
that a version bump could break: reading the HTTP status out of the debug log,
and stripping that log back to the CLI's own output.

Run them all with `go test ./...` from this directory. None needs a live
Overcast instance — or the `aws` binary.

---

## Adding a new group

1. Add the group and its tests to [compat/suites/registry.json](../registry.json)
   first (see [compat/AGENTS.md § Uniformity](../../AGENTS.md#2-uniformity--the-registry-is-the-contract)),
   or confirm another suite has already declared them.
2. Create or open `internal/groups/<service>.go`.
3. Register every test under a **group-qualified** key in `Impls`, and its
   setup/teardown in `Setup`/`Teardown`.
4. Declare a package-level `harness.NewNamer` per group and name every
   resource through it.
5. For a new service, add the constructor to `All()` in `groups.go`.
6. Run `go test ./...`, then the suite against a live instance.

---

## What agents must NOT do

- Never import from `internal/`, `router/`, or any Overcast server source tree
  — see [compat/AGENTS.md § Separation boundary](../../AGENTS.md#separation-boundary--non-negotiable).
- Never call `aws` yourself with `exec.Command` — go through `internal/awscli`,
  which is what keeps the endpoint, region, output format and credential
  handling identical across every test.
- Never call `time.Sleep` inside a test — use a poll loop with a max count.
- Never hard-code the endpoint — always use `t.Endpoint`.
- Never write to stdout inside a test — the runner parses stdout as NDJSON;
  use `t.Log()` (stderr) for diagnostics.
- Never register an impl key without the `group:` qualifier — the registration
  tests reject it.
- Never add a setup function without a corresponding teardown.
- Never reuse `teardownBucket` for versioned or multipart groups — use the
  dedicated helpers.
