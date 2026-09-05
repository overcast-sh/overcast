# AGENTS.md — python-sdk suite

> Conventions for AI agents and contributors working in `compat/suites/python-sdk/`.
>
> **Read [compat/AGENTS.md](../../AGENTS.md) first** — it contains the
> canonical teardown rules and separation boundary that apply to every suite.
> This file covers only python-sdk-specific details.

---

## What this suite tests

Every AWS service operation reachable via **boto3** (AWS SDK for Python v3).
It is the Python SDK column of the compatibility matrix. Failures on
unimplemented services are correct and expected.

---

## Status

**Implemented.** `runner.py` imports 28 service modules covering S3, SQS,
DynamoDB, SNS, Lambda, CloudWatch Logs, SES, IAM, STS, Secrets Manager, KMS,
SSM, Kinesis, EventBridge, CloudFormation, EC2, ECS, Cognito, AppSync, API
Gateway, CloudFront, ElastiCache, RDS, Step Functions, EventBridge Pipes,
WAFv2, Shield and EFS. It runs in the compat CI matrix
(`.github/workflows/compat.yml`) alongside every other suite.

---

## Runtime

| Item       | Value                                                                                                                                             |
| ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Language   | Python 3.11+ (CI runs 3.14)                                                                                                                       |
| AWS client | `boto3` / `botocore`, floors in `requirements.txt`                                                                                                |
| CI image   | None of its own — GitHub Actions installs Python and runs `pip install -r requirements.txt`; the compose path uses `.devcontainer/Dockerfile`, which already carries Python |

> SDK upgrade policy: [compat/AGENTS.md § SDK version pinning](../../AGENTS.md#sdk-version-pinning--upgrade-strategy).

---

## File layout

```
compat/suites/python-sdk/
  AGENTS.md          ← you are here
  README.md          ← quick-start, prerequisites, env vars, architecture
  runner.py          ← entry point; imports all group modules; NDJSON output
  requirements.txt
  lib/
    harness.py       ← TestContext, run_suite(), run_group(), is_unimplemented()
    clients.py       ← make_clients(endpoint, region) → named tuple of clients
    registry.py      ← loads registry.json + registry.generated.json, merges and
                       validates impl keys, builds the groups
    scenario/        ← the scenario interpreter for generated groups (see below)
  groups/            ← one file per AWS service
    s3.py
    sqs.py
    ...
  tests/
    test_registry.py ← impl-key resolution tests, run with
                       `python -m unittest discover -s tests`
    test_scenario.py ← the interpreter, against an in-memory fake client
```

**One file per AWS service.** Never split a service across files.

---

## Generated groups have no file here

A group in `registry.generated.json` is executed from the scenario IR
(`compat/model/scenarios/<service>.json`) by `lib/scenario`, not from a file
under `groups/`. **Never hand-write an impl for a generated test.** If a
generated group is wrong, the fix is in `compat/model/recipes/<service>.json`
plus `make generate-compat-model` — a hand-written impl would shadow the
scenario (the loader prefers a registered impl and only then consults the
backend) and would silently stop tracking the model.

`lib/scenario` is written against
[compat/model/README.md](../../model/README.md), which is normative for the IR:
the step kinds, the assertion set, the value expressions, the path syntax and
the six fields every failure message carries. Change the interpreter to match
that document, never the other way round, and take an IR ambiguity to #1113
rather than settling it in one suite — `node-js-sdk` and `cli` have to make the
same decisions.

---

## Group anatomy

Each service file must export these module-level dicts:

```python
IMPLS: dict[str, Callable[[TestContext], None]]     # keyed "group:Test"
SETUP: dict[str, Callable[[TestContext], None]]     # keyed by group name
TEARDOWN: dict[str, Callable[[TestContext], None]]  # keyed by group name
```

`runner.py` imports each module and merges these three dicts, so a new service
file has to be added to that import list as well.

```python
# groups/sts.py
IMPLS = {
    "sts-identity:GetCallerIdentity": GetCallerIdentity,
    "sts-assume:AssumeRole": AssumeRole,
}
SETUP = {}
TEARDOWN = {}
```

Individual test functions raise `AssertionError` to fail; return normally to
pass. They must not call `sys.exit`.

Context state is stored and read via `ctx["key"]` / `ctx.get("key")`.

---

## Naming conventions

| Element         | Convention                                                                                                                                                                 |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Impl dict key   | `<group>:<Test>` — **always group-qualified**, never bare (see [compat/AGENTS.md § Implementation keys](../../AGENTS.md#implementation-keys--grouptest-and-a-bad-key-aborts-the-run)) |
| Group name      | `<service>-<feature>` (kebab-case), e.g. `s3-crud`, `iam-roles`                                                                                                              |
| Test name       | PascalCase AWS operation name, e.g. `CreateBucket`, `AssumeRole`                                                                                                            |
| Resource prefix | `{ctx.run_id}-<short>` (e.g. `{ctx.run_id}-s3-crud`)                                                                                                                        |
| Context key     | snake_case string, e.g. `"s3_bucket"`, `"kms_key_id"`                                                                                                                       |
| Setup function  | `setup_<group_name>` (underscores), e.g. `setup_s3_crud`                                                                                                                    |
| Teardown fn     | `teardown_<group_name>`, e.g. `teardown_s3_crud`                                                                                                                            |
| Service file    | Lowercase service name: `s3.py`, `cloudwatch_logs.py`                                                                                                                       |

---

## Registration tests

`tests/test_registry.py` covers the loader on synthetic registries **and**
resolves this suite's own real registrations against the real `registry.json`.
Together they pin the two rules that stop a run from reporting a result for a
test that never executed: a key resolving to nothing aborts rather than
warning, and a bare key for a name several groups declare is refused rather
than binding whichever group registered last.

```bash
python -m unittest discover -s tests
```

No emulator, and no boto3 call, is involved.

---

## Teardown rules (python-sdk-specific additions)

The canonical teardown rules are in [compat/AGENTS.md](../../AGENTS.md).
Additional Python specifics:

- Wrap each individual delete call in `try: ... except Exception: pass` — never
  let one failure abort the rest of teardown.
- When deleting many objects from S3, use paginator `list_objects_v2` and batch
  them via `delete_objects`. For versioned buckets use `list_object_versions`
  and include both `Versions` and `DeleteMarkers`.
- Abort incomplete multipart uploads via `list_multipart_uploads` paginator
  before deleting a bucket that may have had in-progress uploads.
- Store resource IDs in `ctx["key"]` during setup so teardown can read them.
- Delete KMS aliases explicitly via `delete_alias` before scheduling key
  deletion — aliases are NOT removed automatically.
- Use `ctx.get("key")` (not `ctx["key"]`) in teardown to avoid `KeyError` when
  setup failed before storing the value.

---

## What agents must NOT do

- Never use `time.sleep` with a fixed duration — use a poll loop with a max
  retry count.
- Never hard-code the endpoint — always use `ctx.endpoint`.
- Never write to stdout inside a test function — the runner parses stdout as
  NDJSON.
- Never add a setup entry without a corresponding teardown entry in `TEARDOWN`.
- Never call `delete_bucket` without first emptying the bucket (objects,
  versions, delete markers, and incomplete multipart uploads).
- Never schedule KMS key deletion without first deleting any aliases pointing
  to that key.
