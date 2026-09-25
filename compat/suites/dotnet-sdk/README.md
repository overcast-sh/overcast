# dotnet-sdk suite

Runs the Overcast AWS compatibility matrix using the **AWS SDK for .NET v4**
(.NET 8, C# 12, published as a self-contained console app).

> **Status: implemented.** See [AGENTS.md](AGENTS.md) for code conventions.

Explicitly listed in the Overcast project goals: *"Works with all official
AWS SDK clients (Go, JS/TS, Python, Java, **.NET**) without changes."*

Tests cover every AWS service the registry declares — including ones not yet
implemented in Overcast. Failures on unimplemented services are expected and
are the coverage metric, not a problem to fix.

---

## What it covers

Every operation the suite's service groups register, cross-validated with the
AWS SDK for .NET v4's async clients: S3, SQS, DynamoDB, SNS, Lambda, STS, KMS,
Secrets Manager, SSM, IAM and EventBridge — see `ServiceGroups.All()` for the
authoritative, registration-order list. This is a smaller service set than
`java-sdk` or `go-sdk` cover today; a group this suite has not implemented
yet is recorded as parity debt (see
[compat/AGENTS.md § Uniformity](../../AGENTS.md#2-uniformity--the-registry-is-the-contract)),
not silently skipped.

The suite also loads [compat/suites/registry.generated.json](../registry.generated.json):
a group marked there for `dotnet-sdk` is executed through a `ScenarioBackend`
rather than a hand-written method. That backend is the generated C# under
`Groups/Scenarios<Service>Gen.cs`, which `cmd/compatgen` writes from the
scenario IR and this project compiles like any other source — the AWS SDK for
.NET has no dynamic-dispatch API, so the calls are emitted rather than
interpreted. The semantics they call into are hand-written once in
`Scenario/`. See [AGENTS.md § Generated groups](AGENTS.md#generated-groups-and-the-scenariobackend-hook).

## Prerequisites

- .NET 8 SDK (`dotnet`) — verified here against SDK `8.0.424`
- Docker, only for the "via Docker" and "via the Go CLI" paths
- Overcast running somewhere reachable — see [compat/AGENTS.md § Running a session](../../AGENTS.md#running-a-session--ports-are-chosen-never-assumed)
  for why `4566`/`4567` are off-limits for a test instance you start yourself

---

## Running the suite

### Locally (.NET 8 SDK required)

```bash
cd compat/suites/dotnet-sdk
dotnet build OvercastCompat.csproj -c Release

# Start Overcast first (separate terminal), e.g.:
#   go run ./cmd/overcast -- serve

OVERCAST_ENDPOINT=http://localhost:4566 dotnet bin/Release/net8.0/OvercastCompat.dll
```

PowerShell:

```powershell
cd compat/suites/dotnet-sdk
dotnet build OvercastCompat.csproj -c Release

$env:OVERCAST_ENDPOINT = "http://localhost:4566"
dotnet bin/Release/net8.0/OvercastCompat.dll
```

`Tests/OvercastCompat.Tests.csproj` is a separate xunit project (added in
[#1697](https://github.com/overcast-sh/overcast/pull/1697)) holding the
registry-loader registration tests — it never talks to a live Overcast
instance:

```bash
dotnet test Tests/OvercastCompat.Tests.csproj -c Release
```

### Via Docker (no local .NET SDK required)

This suite ships its own image. The **build context is `compat/suites/`**, not
this directory, because the image copies in the shared
`compat/suites/registry.json` (see [compat/AGENTS.md § Running suites](../../AGENTS.md#running-suites-docker--ci)).
The build stage also runs the `Tests/` project — the registration tests,
never against a live Overcast instance — but not the shared error-fixture
conformance set: `compat/model/testdata/errors` is not reachable from this
context, and `ScenarioErrorFixtureTests` detects that and skips itself rather
than failing the build. That fixture set runs for real from a full checkout,
in `test.yml`'s `compat-suite-unit-tests` job — see
[compat/AGENTS.md § Where the shared error corpus runs](../../AGENTS.md#where-the-shared-error-corpus-runs).

```bash
docker build -f compat/suites/dotnet-sdk/Dockerfile -t oc-dotnet-sdk-compat compat/suites
docker run --rm --network host \
  -e OVERCAST_ENDPOINT=http://localhost:4566 \
  oc-dotnet-sdk-compat
```

On Docker Desktop (verified here on Windows; macOS runs the same
Linux-VM architecture and is expected to behave the same way),
`--network host` does not share the host's network stack the way it does on
native Linux, so a container cannot reach an Overcast process listening on
the host's `localhost` this way — point it at `host.docker.internal` instead:

```bash
docker run --rm \
  -e OVERCAST_ENDPOINT=http://host.docker.internal:4566 \
  oc-dotnet-sdk-compat
```

`run.sh` (used by the paths below) already picks the right network mode when
it runs *inside* a container itself; this distinction only matters when you
invoke Docker by hand from a Windows or macOS shell.

### The SDK type table

`sdk-types/AWSSDK.<Service>.txt` holds the member types of every `AWSSDK.*`
package `OvercastCompat.csproj` pins — one file per package, every model class
and each property's type — reflected from the assemblies by this suite
(`Scenario/SdkTypeTable.cs`). `cmd/compatgen` spells the generated groups'
C# from it, because it runs where there is no .NET SDK to load the assemblies
with. Refresh it whenever you change an `AWSSDK.*` pin, then regenerate:

```bash
docker build -f compat/suites/dotnet-sdk/Dockerfile --target sdk-types \
  --output type=local,dest=compat/suites/dotnet-sdk/sdk-types compat/suites
make generate-compat-model
```

A stale table fails loudly in two places: `cmd/compatgen` refuses one whose
versions differ from the csproj's pins, and `Tests/SdkTypeTableTests` fails on
one that differs from the pinned assemblies. The `sdk-types` target stops
before the image's test stage, so it works on a table that test would reject.
If you add a package, delete the file of any package you removed: the export
writes files and never deletes one.

### Via the Go CLI (recommended — runs all suites, or just this one)

```bash
# Starts its own Overcast instance on a free port and stops it afterwards:
go run ./cmd/compat
# or just this suite:
go run ./cmd/compat --suite dotnet-sdk
# or against an instance you are already running:
go run ./cmd/compat --endpoint http://localhost:4566 --suite dotnet-sdk
```

This is what CI runs. `cmd/compat` invokes `sh run.sh` for this suite (see
[compat/AGENTS.md § Running suites](../../AGENTS.md#running-suites-docker--ci)),
which builds the image above — tagged by a content hash of the sources so it
rebuilds only when they change — and runs it, choosing the network mode for
you.

---

## Environment variables

| Variable                        | Default                 | Description                                                                     |
| -------------------------------- | ------------------------ | -------------------------------------------------------------------------------- |
| `OVERCAST_ENDPOINT`              | `http://localhost:4566` | Overcast base URL                                                                |
| `OVERCAST_DEFAULT_REGION`        | `us-east-1`              | AWS region advertised to the SDK                                                 |
| `OVERCAST_REGISTRY_PATH`         | `../registry.json`\*    | Override path to `registry.json`. The Docker image sets this to `/registry.json` |
| `OVERCAST_COMPAT_RUN_ID`         | `local`                  | Prefix for resource names, so concurrent runs and the orphan sweep don't collide |
| `OVERCAST_COMPAT_SKIP_DOCKER`    | unset                    | Set to `1` to drop the `docker` capability, skipping every test the registry marks `requires: [docker]` |
| `OVERCAST_COMPAT_SERVICE`        | unset (all)              | Single AWS service name to run, e.g. `s3`                                       |
| `OVERCAST_COMPAT_GROUPS`         | unset (all)              | Comma-separated group names to run                                              |
| `OVERCAST_COMPAT_TESTS`          | unset (all)              | Comma-separated test names to run within those groups                           |
| `OVERCAST_COMPAT_TEST_PAIRS`     | unset (all)              | Comma-separated `group:test` pairs to run — the exact-pair filter `java-sdk` does not have |
| `OVERCAST_COMPAT_PARALLEL_SLOTS` | `8`                      | Max groups run concurrently, and max tests of one parallel probe group          |
| `OVERCAST_COMPAT_INTERACTIVE`    | unset                    | Set to `1` to run the interactive command protocol instead of one batch run      |

\* `Program.cs` resolves `registry.json` relative to the process working
directory unless `OVERCAST_REGISTRY_PATH` is set; running from
`compat/suites/dotnet-sdk/` finds it at `../registry.json`.

---

## Architecture

```
dotnet-sdk/
  Dockerfile               ← .NET SDK build stage (also runs the Tests/ project) + runtime stage
  run.sh                   ← builds/runs the Dockerfile; what cmd/compat invokes in CI
  OvercastCompat.csproj    ← AWSSDK.* NuGet references; OutputType=Exe
  Program.cs               ← top-level statement entry point: wires clients, merges impls, runs
                             (`--sdk-types <dir>` writes the SDK type table instead)
  README.md                ← you are here
  sdk-types/               ← AWSSDK.<Service>.txt: the pinned SDK's member types, for cmd/compatgen

  Harness/
    TestContext.cs         ← per-group state bag (endpoint, region, runId, log)
    TestTypes.cs            ← TestFn/SetupFn delegates, TestCase/TestGroup records
    Runner.cs               ← runs groups (bounded concurrency), emits NDJSON to stdout
    InteractiveRunner.cs    ← the OVERCAST_COMPAT_INTERACTIVE command protocol
    Assertions.cs           ← assertion helpers that throw InvalidOperationException with context

  Clients/
    AwsClients.cs           ← lazily-initialised, per-service AWS SDK client factory

  Registry/
    RegistryLoader.cs       ← loads registry.json + registry.generated.json, builds TestGroups;
                               also declares the ScenarioBackend delegate

  Groups/
    IServiceGroup.cs        ← the interface every group class implements
    ServiceGroups.cs        ← ServiceGroups.All(clients) — every group, in registration order
    S3Group.cs, SqsGroup.cs, DynamoDbGroup.cs, …  ← one class per AWS service
    ScenariosGen.cs, Scenarios<Service>Gen.cs     ← generated by cmd/compatgen; never hand-edited

  Scenario/               ← hand-written runtime the generated groups call into
    Model.cs                ← the IR's calls, tests, clauses and checks as C# types
    Values.cs               ← $lit/$ref/$name/$concat/$index/$base64/$now, the context bag and the Binder
    ScenarioGroup.cs        ← setup → tests → teardown, and the group's own identity
    Execution.cs            ← the calls a clause makes and the closed assertion set
    EqualsJsonCheck.cs      ← equalsJSON: percent-decode, strict parse, compare as documents
    Documents.cs, Paths.cs  ← an SDK response as one of the IR's documents, and paths over it
    Errors.cs, Failure.cs   ← error matching, 501 classification, the six-field message
    SdkTypeTable.cs         ← renders sdk-types/ from the AWSSDK assemblies

  Tests/
    OvercastCompat.Tests.csproj ← separate xunit project (added in #1697)
    RegistrationTests.cs        ← this suite's own registrations vs. real registry.json
    GeneratedRegistryTests.cs   ← registry.generated.json loading + ScenarioBackend resolution
    ScenarioRegistrationTests.cs ← every generated test resolves to an emitted method, and back
    ScenarioTests.cs             ← the Scenario/ runtime against real SDK request/response objects
    ScenarioDocumentTests.cs     ← an SDK response as a document, and paths over it
    ScenarioErrorFixtureTests.cs ← the shared error-matching conformance fixtures
    ScenarioEqualsJsonFixtureTests.cs ← the shared equalsJSON fixture (compat/model/testdata/equalsjson)
    SdkTypeTableTests.cs         ← the committed sdk-types/ is what the pinned assemblies declare
    SdkWireFormTests.cs          ← CloudWatch Logs' DateTime-over-long, measured on the wire
```

**One file per AWS service.** Never split a service across multiple group
files or merge two services into one file. See [AGENTS.md](AGENTS.md) for the
exact shape and how to add one.
