# python-sdk suite

Runs the full Overcast AWS compatibility matrix using **boto3** (the AWS SDK
for Python).

> **Status: implemented.** See [AGENTS.md](AGENTS.md) for code conventions.

Tests cover all services — including ones not yet implemented in Overcast.
Failures on unimplemented services are expected and are the coverage metric,
not a problem to fix.

---

## What it covers

All AWS services tested by the `node-js-sdk` suite, cross-validated with
boto3. Also tests Python-specific edge cases (e.g. `ResourceNotFoundException`
vs SDK error shapes, paginator patterns).

---

## Prerequisites

- Python 3.11 or newer (CI runs 3.14; verified here against 3.14)
- `pip install -r requirements.txt` (boto3, botocore)
- Docker, for the tests the registry marks `requires: [docker]` (Lambda
  invocation, event-source-mapping delivery). Without a daemon, set
  `OVERCAST_COMPAT_SKIP_DOCKER=1` and they are skipped rather than failed.
- Overcast running somewhere reachable — see
  [compat/AGENTS.md § Running a session](../../AGENTS.md#running-a-session--ports-are-chosen-never-assumed)
  for why `4566`/`4567` are off-limits for a test instance you start yourself.

No AWS credentials are needed: the clients are built with fixed placeholder
values, which the emulator accepts without validating.

---

## Running the suite

### Locally (Python required)

```bash
cd compat/suites/python-sdk
pip install -r requirements.txt
python -m unittest discover -s tests   # registry unit tests; no emulator needed

# Start Overcast first (separate terminal):
#   go run ./cmd/overcast serve

python runner.py
```

PowerShell:

```powershell
cd compat/suites/python-sdk
pip install -r requirements.txt
python -m unittest discover -s tests

$env:OVERCAST_ENDPOINT = "http://localhost:4566"
python runner.py
```

### Via Docker (no local Python required)

This suite ships no image of its own. It runs as a subprocess of the compat
runner container, which already carries Python. From the repo root:

```bash
OVERCAST_COMPAT_SUITE=python-sdk docker compose -f compat/docker-compose.yml run --rm compat
```

Arguments after the compose service name reach the container entrypoint rather
than the runner, which is why the suite selection is an environment variable —
see [compat/AGENTS.md § Running suites](../../AGENTS.md#running-suites-docker--ci).

### Via the Go CLI (recommended — runs all suites)

```bash
# Starts its own Overcast instance on a free port and stops it afterwards:
go run ./cmd/compat
# or just this suite:
go run ./cmd/compat --suite python-sdk
# or against an instance you are already running:
go run ./cmd/compat --endpoint http://localhost:4566 --suite python-sdk
```

---

## Environment variables

| Variable                         | Default                 | Description                                                                                            |
| -------------------------------- | ----------------------- | ------------------------------------------------------------------------------------------------------ |
| `OVERCAST_ENDPOINT`              | `http://localhost:4566` | Overcast base URL                                                                                      |
| `OVERCAST_DEFAULT_REGION`        | `us-east-1`             | AWS region advertised to the SDK                                                                       |
| `OVERCAST_COMPAT_RUN_ID`         | auto-generated          | Prefix for resource names, so concurrent runs and the orphan sweep do not collide                      |
| `OVERCAST_COMPAT_SKIP_DOCKER`    | unset                   | Set to `1` to drop the `docker` capability, skipping every test the registry marks `requires: [docker]` |
| `OVERCAST_COMPAT_SERVICE`        | unset (all)             | Comma-separated AWS service names to run, e.g. `s3,sqs`                                                |
| `OVERCAST_COMPAT_GROUPS`         | unset (all)             | Comma-separated group names to run                                                                     |
| `OVERCAST_COMPAT_TESTS`          | unset (all)             | Comma-separated test names to run within those groups                                                  |
| `OVERCAST_COMPAT_PARALLEL_SLOTS` | `8`                     | Max groups run concurrently                                                                            |
| `OVERCAST_COMPAT_INTERACTIVE`    | unset                   | Set to `1` to serve the interactive command protocol instead of one batch run                          |

`registry.json` is located relative to `lib/registry.py`, not through an
environment variable — this suite has no `OVERCAST_REGISTRY_PATH` override.

---

## Architecture

```
python-sdk/
  requirements.txt    ← boto3, botocore
  runner.py           ← entry point; imports all group modules; applies the env
                        filters; runs once or serves the interactive command loop
  README.md           ← you are here

  lib/
    harness.py        ← TestContext, run_suite(), run_group(), is_unimplemented()
    clients.py        ← make_clients(endpoint, region) → named tuple of clients
    registry.py       ← loads registry.json + registry.generated.json, merges and
                        validates impl keys, builds the groups
    scenario/         ← the scenario interpreter: executes the generated IR

  groups/             ← one file per AWS service
    s3.py
    sqs.py
    dynamodb.py
    sns.py
    …

  tests/
    test_registry.py  ← impl-key resolution tests; run with
                        `python -m unittest discover -s tests`
    test_scenario.py  ← the interpreter, against an in-memory fake client
```

This suite ships no `Dockerfile`: the compat runner container already carries
Python, so it runs there as a plain subprocess (see `defaultSuites` in
[compat/runner.go](../../runner.go)).

### The scenario interpreter (`lib/scenario/`)

Groups in `registry.generated.json` have no Python source of their own. Each
carries a `scenario` field naming a file under `compat/model/scenarios/`, and
`lib/scenario` executes that IR with boto3 — `boto3.client(<endpointPrefix>)`
plus `getattr(client, botocore.xform_name(op))(**params)`, which is boto3's
ordinary public API and therefore the same serialization path a real
application takes. `runner.py` calls `scenario_hooks(registry)` once and passes
the result to `build_groups_from_registry` as the scenario backend plus the
generated groups' setup and teardown. Hand-written groups are untouched: the
backend is consulted only for a test with no registered impl, and it answers
"not mine" for anything outside a scenario file.

| Module | Responsibility |
| --- | --- |
| `loader.py` | reads the scenario files the registry names, lazily and once |
| `expressions.py` | `$lit`/`$ref`/`$name`/`$concat`/`$index`, the path syntax, JSON equality |
| `executor.py` | the boto3 calls, the group's context bag, exports, error names |
| `assertions.py` | the closed assertion set (`responseField`, `readback`, `listContains`, `absent`, `errorCode`, `eventually`) |
| `failures.py` | the one builder for the six-field failure message |

The normative specification is [compat/model/README.md](../../model/README.md);
where it and this interpreter disagree, that document is right and the
interpreter has a bug. `cmd/compatgen -explain <group>/<test> -lang python`
renders a generated test as pseudo-code, which is how a failure message is
turned back into something to run by hand.

### Key types (`lib/harness.py`)

| Type / function       | Purpose                                                                        |
| --------------------- | ------------------------------------------------------------------------------ |
| `TestContext`         | Dict-like; has `endpoint`, `region`, `run_id`, `log`; plus a `[str]` state bag |
| `run_suite(groups)`   | Runs all groups; emits NDJSON to stdout                                        |
| `is_unimplemented(e)` | Returns `True` if the exception wraps an HTTP 501 response                     |

### Group modules (`groups/`)

Each service file exports three module-level dicts:

```python
IMPLS: dict[str, dict[str, Callable[[TestContext], None]]]
SETUP: dict[str, Callable[[TestContext], None]]   # keyed by group name
TEARDOWN: dict[str, Callable[[TestContext], None]] # keyed by group name
```

Individual test functions raise `AssertionError` (or any exception) to fail;
return normally to pass. Teardown functions must never raise.

---

## Adding a new group

1. Open (or create) `groups/<service>.py` — one file per AWS service.
2. Add entries to `IMPLS`, `SETUP`, and `TEARDOWN` maps.
3. Register the module in `runner.py`.
4. Run `python runner.py` locally to verify NDJSON output is well-formed.

See [AGENTS.md](AGENTS.md) for detailed code conventions and teardown rules.
