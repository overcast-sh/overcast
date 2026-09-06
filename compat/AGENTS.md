# AGENTS.md — Overcast Compatibility Tests

> Conventions for AI agents and contributors working in `compat/`.
> For the project-wide conventions see the root [AGENTS.md](../AGENTS.md).
> For the wire format and architecture see [README.md](./README.md).
>
> Each suite has its own `AGENTS.md` for runtime/language/library specifics.
> **Read both this file and the suite's `AGENTS.md` before making any changes.**
> See the [Suite-specific conventions](#suite-specific-conventions) section for
> what every suite `AGENTS.md` must contain, and links to each one.

---

## Purpose of this directory

`compat/` is a **black-box external test harness** for Overcast. Each suite
uses its AWS tool (SDK, CLI, CDK, IaC) **without any modification** — the only
difference from talking to real AWS is the endpoint URL (`http://localhost:4566`
instead of `https://*.amazonaws.com`). The emulator has no knowledge that compat
exists, and compat has no knowledge of Overcast's internals.

Its job:

1. Measure overall AWS compatibility across services and clients.
2. Catch regressions when emulator internals change.
3. Provide a shared benchmark for “what’s left to implement”.
4. Drive the compat server and UI that visualise the results.

Failing tests are **normal and expected** for unimplemented services. The goal
of a compat test is not “the suite passes” but “the suite accurately reflects
reality”.

---

## Separation boundary — non-negotiable

The boundary between `compat/` and the Overcast emulator is absolute:

| Allowed                                      | Forbidden                                                 |
| -------------------------------------------- | --------------------------------------------------------- |
| HTTP calls to a running Overcast instance    | Importing anything from `internal/`                       |
| Reading `compat/result.go` types             | Importing `router/`, `middleware/`, `protocol/`, `state/` |
| Sharing nothing with `web/`                  | Adding compat routes to the Overcast server               |
| Building a standalone binary in `cmd/compat` | Adding compat config to `internal/config`                 |
| A separate `compat/ui/` Vite app             | Adding compat pages to `web/src/`                         |

If you find yourself touching anything outside `compat/` or `cmd/compat/`, stop
and reconsider the approach.

---

## Running a session — ports are chosen, never assumed

`cmd/compat` manages its own emulator. Unless `--endpoint` is pinned it starts
a throwaway Overcast instance on a **free port**, waits for `/_overcast/health`, and
stops it on exit; the dashboard and Vite ports are probed the same way. Ports
**4566 and 4567 are never bound** — they belong to the user's own instance, per
the root [AGENTS.md § Reserved ports](../AGENTS.md#reserved-ports--4566-and-4567-belong-to-the-user).

Consequences for agents:

- **Never hard-code a port in instructions, docs, or a URL you hand the user.**
  Read the banner the CLI prints (`Dashboard:` / `Overcast:`) and quote those
  exact URLs. Two sessions can run at once, so yesterday's port proves nothing.
- **Do not start your own emulator alongside compat.** `go run ./cmd/compat`
  already has one. Passing `--endpoint` opts out of management entirely — use
  it only when you mean to target an instance that already exists.
- The wrappers (`compat/dev.sh`, `compat/dev.ps1`, `compat/run.sh`,
  `compat/run.ps1`), the `task compat-*` / `make compat-*` targets, and the
  compose services are all thin shells over the same code path. Fix behaviour
  in [cmd/compat/launch.go](../cmd/compat/launch.go), never in a wrapper —
  otherwise the platforms drift apart.
- **A managed instance listens on `127.0.0.1`, never on every interface.**
  Both start-up paths default to every interface if left alone — a bare
  `-p <host>:<container>` publishes on all of them, and the emulator's own
  `OVERCAST_LISTEN` default is `0.0.0.0` — and either puts an unauthenticated
  emulator on whatever network the machine is attached to. So a container is
  published on loopback and a native binary is told `OVERCAST_LISTEN=127.0.0.1`
  (renamed from `OVERCAST_HOST`, which has been removed — see
  [#870](https://github.com/overcast-sh/overcast/issues/870)).
  On Linux both also cover the Docker bridge gateway, because that is what
  `host.docker.internal:host-gateway` resolves to there and it is the only way
  [suites/rust-sdk/run.sh](./suites/rust-sdk/run.sh) reaches the emulator from
  its own container. The gateway is dropped unless compat can bind it here,
  which is what tells a native Linux daemon apart from Docker Desktop's VM on
  WSL2 without reading `uname`. The two paths are `dockerRunArgs`/`publishArgs`
  and `overcastEnv`/`bindHosts` in
  [cmd/compat/launch.go](../cmd/compat/launch.go) — keep them in step, and
  remember the binary path is the one `findOvercastBinary` prefers, so it is
  the common case rather than the fallback.

---

## Core principles (compat-specific)

1. **Tests use the SDK/CLI/CDK exactly as production code would.** The only
   configuration change is the endpoint URL. No special Overcast-only headers,
   no internal client factories, no test-only SDK modes. If real application
   code wouldn't do it, neither should a compat test.

2. **Tests must never mock the AWS SDK or the emulator.** Every call goes over
   the wire to a real Overcast instance. No `jest.mock`, no HTTP intercept.

3. **Tests cover all services, not just implemented ones.** A 501 response that
   causes the SDK to throw is recorded as `"fail"` — that's the correct result.
   Never `skip` a test because a feature isn't implemented yet. Only `skip` when
   the test requires external infrastructure that isn't guaranteed (e.g. Docker
   for Lambda invocation).

4. **Each group is independently runnable.** A group must not depend on state
   left by another group. Use setup/teardown to create and destroy resources.

5. **Teardown must be fault-tolerant.** Wrap every delete in `try/catch` (or
   `//nolint:errcheck` in Go, `except Exception: pass` in Python) so partial
   failures don't block cleanup of other resources.

6. **Resource names must be unique per run.** Use the `ctx.runId` prefix (e.g.
   `oc-{runId}-s3-crud-bucket`) to avoid conflicts between concurrent runs.

7. **Tests assert meaningful state, not just "no error".** After creating a
   resource, verify it appears in the corresponding List call. After writing
   data, verify it can be read back. See the [Assertion contract](#assertion-contract) section below for the full
   requirement table — this is a hard rule, not a guideline.

8. **No sleep/polling unless strictly necessary.** If an operation is truly
   async (Lambda cold start, SQS visibility timeout), use a short poll loop
   with a maximum retry count rather than a fixed `sleep()`.

9. **Do not hard-code the emulator endpoint.** Always use `ctx.endpoint`
   (Node.js) or `cfg.Endpoint` (Go).

---

## Assertion contract

Every test function **must** verify the server's observable state — not just
that the call did not throw. A test that fires an API call and returns without
inspecting the response is incomplete. These are the hard requirements:

### Required roundtrips by operation pattern

| Operation type                                               | Required assertion                                                                                                               |
| ------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| `Create*` / `Put*`                                           | Call the matching `Describe*`, `Get*`, or `List*` and verify the resource appears with the correct field values                  |
| `Update*` / `Set*Attributes` / `TagResource`                 | Call `Describe*` / `Get*Attributes` / `List*Tags*` and verify the changed field now holds the new value                          |
| `Delete*` / `Untag*`                                         | Call `Describe*` / `List*` and verify the resource is absent; or call the direct `Get*` and assert the expected not-found error  |
| `Put*` on data plane (S3 object, SQS message, DynamoDB item) | Call `Get*` / `Receive*` / `GetItem` and verify the returned value matches what was stored                                       |
| `Publish` (SNS, EventBridge)                                 | If testing cross-service delivery (e.g. SNS→SQS), poll the destination with a retry loop and assert at least one message arrives |

### Checking response fields (minimum bar)

- A `Create*` response **must** assert at least: ARN/ID is non-empty, name
  matches what was requested.
- A `List*` response **must** check: result list is non-empty **and** contains
  the resource created in setup or earlier in the group.
- A `Describe*` / `Get*` response **must** assert the key identifying field
  (name, ARN, value) plus any field that was just mutated by an `Update*`.
- A `Scan` / `Query` response **must** assert `Count >= expected_seed_count`.

### Exceptions

The following are the **only** acceptable cases for a test with no roundtrip:

1. The test exercises an operation that has no observable side-effect visible via
   any other API call (e.g. `GenerateDataKey`, `GetRandomPassword` — verify the
   returned value's shape/length instead).
2. The operation is specifically testing a negative path (e.g. expecting a
   specific error code on a bad request), in which case asserting the error
   code and message is the assertion.
3. The operation is on a service that is entirely stubbed (returns 501) — in
   that case the test is expected to fail and there is nothing to assert.
4. The test is a **generated probe** (`<service>-gen-probe` in
   `registry.generated.json`): it calls an operation the emulator does not
   implement, with deliberately nonexistent identifiers, so it created nothing
   to read back. It asserts one clause on its own response — the output's
   identity member non-empty, or, for a `List*` whose only assertable output is
   a page, `{"isList": true}` on that page. The "list is non-empty" bar above
   applies to a list the test itself populated; a probe populated nothing, and
   `nonEmpty` on a pagination token or an unpopulated page would be false by
   construction. See [compat/model/README.md](model/README.md) § What a probe
   asserts.

### Anti-patterns to reject in code review

- `_, err := callAPI(...)` followed by `return err` — response discarded.
- `await client.send(new SomeCommand(...))` with no assignment and no further
  assertion — silently passes regardless of what the server returned.
- `_ = output` / `_ = resp` after a call — result discarded.
- Checking only `if resp.SomeId == nil` when the test is about a mutation — the
  ID was already set before the mutation; asserting its non-nil-ness doesn't
  verify the mutation happened.
- Relying on test ordering to provide coverage: `UpdateX` has no assertion
  because "the next test `GetX` will catch it". If `UpdateX` is broken, it must
  fail at `UpdateX` — not pass through and cause `GetX` to report the failure.

---

## Teardown rules (apply to ALL suites)

These rules are canonical and apply to every suite — cli, go-sdk, python-sdk,
node-js-sdk. Each suite's own `AGENTS.md` may add language-specific detail but
must never contradict or weaken these requirements.

1. **Every group that creates at least one durable resource must have a
   teardown.** The only acceptable exception is a group that is entirely
   read-only (e.g. `GetCallerIdentity`, `DescribeKey`). Tests that delete a
   resource inline as the last step of a happy-path sequence are **not** a
   substitute for teardown — teardown exists precisely to handle the cases
   where that last step is skipped (test failure, early return, etc.).

2. **Clean up ALL resources, including incidental ones.** If a test creates a
   resource as a side effect of testing something else — an access key created
   to test `CreateAccessKey`, a subscription created when subscribing a queue
   to a topic, an inline policy attached to a user, a role added to an instance
   profile — that resource **must** also be cleaned up in teardown. Do not rely
   on the parent resource's delete to cascade unless AWS documents the cascade
   explicitly (deleting a DynamoDB table removes all items; deleting a log
   group removes all streams). When the cascade is not documented, add an
   explicit delete call.

3. **Tear down in reverse creation order.** Dependencies must be removed before
   the resource that owns them. Examples: detach role from instance profile
   before deleting the profile; delete objects before deleting the bucket;
   delete subnet before deleting the VPC; remove targets before deleting an
   EventBridge rule; deregister task definitions before deleting an ECS cluster.

4. **Resources that require pre-conditions before deletion must handle them.**
   Examples: disable a CloudFront distribution before deleting it; fetch a
   fresh `LockToken` before deleting a WAF Web ACL (the token changes after
   each mutating call); cancel a pending KMS key deletion before rescheduling;
   detach or delete a managed policy before deleting the IAM role.

5. **Incomplete multipart uploads are invisible to `ListObjectsV2`.** If a
   group creates or might leave an in-progress multipart upload, teardown must
   call `ListMultipartUploads` (or equivalent) and abort each one before
   attempting to delete the bucket.

6. **KMS aliases are NOT deleted when the key is scheduled for deletion.** Any
   group that creates a KMS alias must explicitly call `DeleteAlias` before (or
   as well as) scheduling the key for deletion.

7. **SQS FIFO queues use a different suffix (`.fifo`)** — make sure teardown
   references the correct queue name / URL stored in context, not a hardcoded
   standard name.

8. **Teardown must not throw.** Wrap individual deletes so that one failure does
   not prevent subsequent deletes from running.

---

## Wire format contract

Every suite runner **must** emit valid NDJSON to stdout matching the schema in
[README.md](./README.md). The Go runner (`compat/runner.go`) parses this output
line-by-line — malformed lines are silently skipped and a warning is logged.

Invariants:

- Exactly one `run_start` event, as the first line.
- Exactly one `run_end` event, as the last line.
- One `test_result` per test case, emitted immediately after the test completes.
- `duration_ms` is always present and non-negative.
- `error` is only present (and non-empty) when `status` is `"fail"` or `"unimplemented"`.
- `"unimplemented"` means the emulator returned HTTP 501 — it is never used for
  assertion failures or unexpected errors. `"fail"` means something that should
  work didn't.

---

## Compat server contract

The compat server (`compat/server.go`) exposes the NDJSON event stream over
HTTP using **Server-Sent Events (SSE)**, not polling and not WebSockets.

### `GET /events`

- `Content-Type: text/event-stream`
- Each SSE message is a single JSON object on a `data:` line, using the same
  event shapes as the internal NDJSON wire format.
- The server buffers all events from the current (or last completed) run in
  memory. A client that connects after the run has started receives all buffered
  events immediately, then live events as they arrive.
- The stream stays open after `run_end` so the UI can reconnect when a new run
  starts without a page reload.

### `GET /results`

- Returns the latest completed `RunReport` as a single JSON object.
- Returns `204 No Content` if no run has completed yet.
- Intended for CI badge generation and one-shot queries.

### `GET /`

- Serves the embedded `compat/ui/` static bundle.

### Rules for the compat server

- Must never import anything from `internal/`, `router/`, `middleware/`, or
  `state/`.
- Must not connect to the Overcast emulator itself — it only receives events
  from the runner and serves them to the UI.
- SSE connections must respect `r.Context()` cancellation (client disconnect)
  to avoid goroutine leaks.

### `POST /mcp/` — MCP server (agent interface)

The compat server embeds an **MCP (Model Context Protocol)** server at `/mcp/`
that AI agents can use to trigger test runs and query results without parsing
raw SSE streams or JSON files.

**Transport:** Streamable HTTP — JSON-RPC 2.0 over `POST /mcp/`, and that is
the only endpoint. MCP revision `2026-07-28` is stateless: there is no
handshake to perform first, no session, and no GET stream. Every request
declares its own protocol version in `_meta` and mirrors its method into the
`Mcp-Method` header, or it is refused. For live event notifications use the
dashboard's own `GET /events`, which carries the same feed.

**When the compat dev server is running** (`go run ./cmd/compat --dev`, or the
`compat/dev.sh` / `compat\dev.ps1` wrappers), the MCP endpoint is live under
the dashboard's base URL. The port is **chosen at runtime** — `:7777` when it
is free, the next free port otherwise — so read it from the banner the CLI
prints rather than assuming. The examples below use `:7777`; substitute the
port you were given. Agents with an MCP client configured can call tools
directly. Agents without an MCP client can call the endpoint via `curl`:

Every request needs the `_meta` block and the `Mcp-Method` header; `tools/call`
also needs `Mcp-Name` naming the tool, because the server checks the headers
against the body and refuses a disagreement with `-32020`.

```bash
# List all available tools
curl -s -X POST http://localhost:7777/mcp/ \
  -H "Content-Type: application/json" \
  -H "Mcp-Method: tools/list" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientCapabilities":{}}}}' | jq '.result.tools[].name'

# Run all node-js-sdk tests
curl -s -X POST http://localhost:7777/mcp/ \
  -H "Content-Type: application/json" \
  -H "Mcp-Method: tools/call" -H "Mcp-Name: compat_run_tests" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientCapabilities":{}},
        "name":"compat_run_tests","arguments":{"suite":"node-js-sdk","all":true}}}'
```

**Available tools:**

| Tool                   | Description                                                                                   | Key parameters                                |
| ---------------------- | --------------------------------------------------------------------------------------------- | --------------------------------------------- |
| `compat_list_suites`   | List all suite runners and their state (`building`, `ready`, `busy`, `error`, `stopped`).     | —                                             |
| `compat_list_services` | List all AWS services from the registry with group and test counts.                           | —                                             |
| `compat_list_tests`    | List tests from the registry with last result status, filterable.                             | `service`, `group`, `suite`                   |
| `compat_get_results`   | Get test results, filterable by suite/service/group/test/status.                              | `suite`, `service`, `group`, `test`, `status` |
| `compat_get_queue`     | Show tests currently queued or running across all suites.                                     | —                                             |
| `compat_run_tests`     | Queue tests for execution. Returns `batch_id`, `queued`, `skipped_duplicates`.                | `all`, `suite`, `service`, `group`, `test`    |
| `compat_run_failing`   | Re-run all tests that failed in the last run.                                                 | `suite`, `service`                            |
| `compat_cancel`        | Cancel queued or running tests.                                                               | `batch_id`, `suite`, `group`, `test`, `all`   |
| `compat_reload_suite`  | Hot-reload a suite runner (rebuilds the suite, restores queued tests). Requires `suite` name. | `suite` (required)                            |

**Preferred workflow for agents:** use `compat_run_tests` to trigger a run,
then poll `compat_get_queue` until the queue empties, then call
`compat_get_results` (filtered by `status: "fail"`) to see what to fix. This
is preferable to reading `compat-results.json` directly because it reflects
real-time state rather than the last completed run.

**Rules for the MCP server:**

- The `/mcp/` handler lives in `compat/mcp.go` — do not add MCP logic to any
  other file.
- `POST /mcp/` responds synchronously (JSON-RPC result or error). Long-running
  operations (`compat_run_tests`) return immediately with a `batch_id`; the
  actual test output arrives on `GET /events`.
- There is no MCP event stream. `GET /mcp/sse` used to copy the orchestrator's
  events onto MCP, and went with the GET stream in revision `2026-07-28` —
  `subscriptions/listen` only carries notification types a client named, and
  raw suite events are not one of them. `GET /events` was always the primary
  feed and is now the only one; do not add a second pump.

---

## Running suites (Docker / CI)

All suites are designed to run cross-platform via Docker. No local toolchain
(Node.js, Go, etc.) is required.

```bash
# From repo root — works on any machine with Docker
docker compose -f compat/docker-compose.yml run --rm compat

# Or via the Makefile shorthand
make -C compat ci
```

The `docker-compose.yml` in `compat/` wires up:

1. **overcast** — the emulator, health-checked before compat tests start.
2. **compat** — the Go CLI runner (`cmd/compat`) that spawns suite subprocesses.

That second container is what makes the run toolchain-free. It is built from
`.devcontainer/Dockerfile`, which already carries Go, Node.js and Python, so
`node-js-sdk`, `cdk`, `python-sdk`, `go-sdk` and `cli` run as plain subprocesses
inside it (see `defaultSuites` in `runner.go`). Those suites have no image of
their own and nothing injects one.

The three whose toolchain that image does *not* carry — `java-sdk`,
`dotnet-sdk` and `rust-sdk` — each ship a `Dockerfile` beside a `run.sh`. The
runner spawns `sh run.sh` like any other suite; the script builds the image and
`docker run`s it, directing stdout to the runner's NDJSON parser. The image is
tagged with a content hash of the suite's sources so it rebuilds when they
change, and the name is the script's own constant — the runner neither knows
nor supplies it.

Those images are independently buildable, and **the build context is
`compat/suites/`, not the suite directory, for all three — no exceptions.** The
shared `registry.json` lives at the top of that tree, and each image copies it
in as `/registry.json` (`OVERCAST_REGISTRY_PATH`); a suite-directory context
cannot reach it:

```bash
# Build just the java-sdk image
docker build -f compat/suites/java-sdk/Dockerfile -t oc-java-sdk-compat compat/suites

# Build just the dotnet-sdk image
docker build -f compat/suites/dotnet-sdk/Dockerfile -t oc-dotnet-sdk-compat compat/suites
```

Each of the three also runs its unit tests inside the image build (`mvn
package`, `cargo test`, `dotnet test`) so a registration regression fails the
build rather than surfacing as a wrong result in a compat run. None of that
needs `compat/model/`, so none of it needs a wider context — see
[Where the shared error corpus runs](#where-the-shared-error-corpus-runs)
below for the one test in each of those three that does need it, and where it
actually runs instead.

### Where the shared error corpus runs

[compat/model/testdata/errors](./model/testdata/errors) is the shared
error-matching conformance corpus every suite's matcher must answer
identically (see [Errors](./model/README.md#errors)). **Every suite's fixture
test runs from a full checkout, in `test.yml`'s `compat-suite-unit-tests` job
— never inside a Docker image build.** That job sets
`OVERCAST_COMPAT_FIXTURES_REQUIRED=1` for all of its steps, and every fixture
test honours it the same way:

- **Corpus found:** run for real, exactly as before.
- **Corpus not found, and the env var is set:** fail loudly, naming the
  reason — this is the one place the corpus must always be reachable, so
  silence here would mean nothing in CI ever checked it.
- **Corpus not found, and the env var is unset:** skip by name, with a
  reason. This is the branch a suite's own Docker image build takes: `java-sdk`,
  `dotnet-sdk` and `rust-sdk` all build from the `compat/suites/` context (see
  above), which does not contain `compat/model/`, so their fixture test would
  otherwise either fail the image build for a corpus the build was never meant
  to reach, or — worse — silently report nothing while looking exactly like a
  passing conformance check.

This is a single convention applied uniformly, not four suites each making
their own call: three different shapes reached this corpus one PR at a time
during G3 (#1820) — go-sdk from the root checkout always, java-sdk and rust-sdk
via extra steps in `compat-suite-unit-tests` because their build context could
not reach it, and dotnet-sdk by widening its build context to `compat/` with a
`Dockerfile.dockerignore` instead. The third shape cost a 2.6 GiB classic-build
trap and a BuildKit-only ignore file nothing else in `compat/` needed; #1865
replaced all three with the one rule above.

### Flags that read a results file instead of producing one

`--max-failures`, `--compare-baseline`, `--update-baseline`, `--report`,
`--check-parity` and `--compare-shadow` are **gate modes**. Each reads an existing `--results-file`
and exits without running a single test. So this does not do what it looks
like:

```bash
# WRONG — runs nothing, fails with "read compat-results.json: no such file"
go run ./cmd/compat --results-file out.json --max-failures 0
```

Run the suites first, gate second:

```bash
go run ./cmd/compat --format json --results-file out.json
go run ./cmd/compat --results-file out.json --max-failures 0
```

Two more shapes that look reasonable and are not:

- **Under compose, arguments after the service name go to the entrypoint**, not
  to the runner — `docker compose ... run --rm compat --suite go-sdk` dies with
  `exec: "--suite": executable file not found`. Use the environment variable:
  `OVERCAST_COMPAT_SUITE=go-sdk,cli docker compose -f compat/docker-compose.yml run --rm compat`.
- **`scripts/docker-go.sh run ./cmd/compat` cannot work at all.** That container
  has no Docker socket, so the runner reports "no way to start Overcast".
  Use the compose path, or `--endpoint` against an instance you started.

### Testing a published image rather than a source build

Through `cmd/compat`, name it and it is what runs:

```bash
go run ./cmd/compat --overcast-image ghcr.io/overcast-sh/overcast:<version>-rc.<n>
```

Naming an image **selects the container**, so a `bin/overcast` sitting in the
tree is passed over rather than silently preferred, and the run says which
artifact it chose:

```
compat: using the container image ghcr.io/overcast-sh/overcast:0.0.1-alpha.33-rc.1 — --overcast-image names it, …
compat: NOT using the local binary /repo/bin/overcast: --overcast-image was given, …
compat: Overcast ready at http://localhost:4570 (container image ghcr.io/overcast-sh/overcast:0.0.1-alpha.33-rc.1, managed by compat)
```

It did not always: binary discovery used to run first unconditionally and the
image was a fallback, so an RC was once "compat-tested" against a day-old local
build (issue #801). If a run does not print the image you named, you are not
testing it — check that line before believing any result. The same applies when
compat is not managing the instance at all (`--endpoint`, or
`--start-overcast=never`): the flag cannot apply, and the run says so.

**Under compose it is different.** The `overcast` service declares `build:` with no `image:`, so there is no
override that points it at a registry tag — useful when the thing under test is
a release candidate and you want the bits CI published, not a local rebuild.
Build the harness, then retag:

```bash
docker compose -f compat/docker-compose.yml build
docker tag ghcr.io/overcast-sh/overcast:<version>-rc.<n> compat-overcast
docker compose -f compat/docker-compose.yml run --rm compat
```

`compose run` reuses an existing image, so the run then exercises the published
bits. Confirm you are testing what you think you are:

```bash
docker image inspect compat-overcast --format '{{index .RepoDigests 0}}'
```

against the digest in the release PR's RC comment.

### When every Lambda test fails at once

A run where the *only* failures are Lambda-backed — and each one says
`the function is in a failed state` or `Failed to pull container image` — is
almost never a Lambda regression. It is the emulator container being unable to
reach the Docker socket, which surfaces about nineteen tests away from its
cause. Check membership before believing the result:

```bash
docker exec compat-overcast-1 sh -c \
  'stat -c %g /var/run/docker.sock; getent group | grep ":$(stat -c %g /var/run/docker.sock):"'
```

No group for the socket's gid means the emulator's user is not in it. The
entrypoint (`docker/entrypoint.sh`) derives that group **once**, at container
start, so anything that changes the socket's ownership afterwards locks it out
until it restarts. `docker compose restart overcast` is the cure.

The compose file used to cause this itself, by `chgrp`-ing the shared socket
from the runner after the emulator had already started; it now joins the
socket's group with `group_add` instead. On native Linux pass the gid, since
the default only covers Docker Desktop's root-owned socket:

```bash
COMPAT_DOCKER_GID=$(stat -c %g /var/run/docker.sock) \
  docker compose -f compat/docker-compose.yml run --rm compat
```

### Cross-platform rules for suite authors

- Do **not** use shell scripts as entry points (`sh -c "..."`) — use a proper
  language runtime command in `CMD` to avoid platform-specific shell differences.
- Do **not** use `#!/bin/sh` shebangs in TypeScript. TypeScript suites are
  launched as `node run.js`, a plain-JavaScript entry point that checks the
  Node version and then imports the `.ts` runner — Node strips the types
  itself, so there is no build step and no loader (no `tsx`). Relative
  imports name the `.ts` file, and the suite's `tsconfig.json` sets
  `allowImportingTsExtensions`, `erasableSyntaxOnly` and
  `verbatimModuleSyntax` so `tsc --noEmit` means "Node can run this".
- Do **not** hard-code `/tmp` paths; use `os.tmpdir()` / Node's `tmp` utilities.
- Do **not** hard-code an interpreter name in a suite's `Argv`. `python3` is
  right on Linux and macOS, and on Windows it is usually a Microsoft Store
  *alias stub* — on PATH, executable, and good only for printing "Python was
  not found". So `exec.LookPath` proves nothing: probe `--version` and require
  the answer to look right (`compat/python.go`). If nothing usable is found,
  set `SuiteConfig.ArgvErr` — the runner and the orchestrator both report it
  as a suite failure naming what was tried, rather than spawning something
  that dies further from its cause.
- Do **not** spawn an npm-installed CLI by its bare name (`npx`, `npm`). On
  Windows those are `.cmd` shims: `spawn` will not find them without an
  extension, and since the CVE-2024-27980 fix refuses to run them at all
  without `shell: true` (`spawn EINVAL`). Resolve the package's own entry
  point and run it with `process.execPath` — see `runCdk` in the cdk suite.
- All suite images derive from official multi-arch base images (`node:24-alpine`,
  `golang:1.24-alpine`) and build cleanly on `amd64` and `arm64`. Node type
  stripping needs 22.18+ on the 22.x line or 23.6+, so a TypeScript suite on
  an older Node base image cannot start at all.

---

## Suite-specific conventions

Every suite **must** have both an `AGENTS.md` and a `README.md` at its root.
These are two distinct documents with different audiences and purposes — never
merge them.

### README.md vs AGENTS.md — what goes where

| Document    | Audience         | Purpose                                                                                                                                                                                                                                  |
| ----------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `README.md` | Human developers | Human-facing project documentation: what the suite is, what it covers, current status, prerequisites, how to run it locally and via Docker, environment variable reference, architecture diagram.                                        |
| `AGENTS.md` | AI agents        | Machine-targeted implementation instructions: how to add a test group, exact code conventions, file layout, key types, group anatomy with working code examples, teardown rules specific to the suite's language, explicit prohibitions. |

**README.md is the entry point for humans.** It answers "what is this?" and
"how do I run it?". It should be readable in isolation. It should **not**
contain code conventions or agent-facing checklists.

**AGENTS.md is the entry point for agents.** It answers "how do I implement
this?" and "what must I never do?". It should **not** repeat information
already in `README.md` (prerequisites, env vars, quick-start). Link to
`README.md` for those. For suites that are not yet implemented, `AGENTS.md`
must also include an **implementation checklist** that an agent can follow to
build the suite from scratch.

### README.md required sections

Every suite `README.md` must contain at minimum:

| Section                   | Content                                                                             |
| ------------------------- | ----------------------------------------------------------------------------------- |
| **Title + status**        | Suite name, technology, and current status (implemented / planned).                 |
| **What it covers**        | Which AWS services / operations / lifecycle phases the suite verifies.              |
| **Prerequisites**         | Required tools, language runtimes, and CLIs — with install instructions or links.   |
| **Running the suite**     | Three paths: locally (native toolchain), via Docker, via the Go CLI (`cmd/compat`). |
| **Environment variables** | Table of all `OVERCAST_*` variables plus any suite-specific env vars.               |
| **Architecture**          | Annotated directory tree and brief description of key modules.                      |

### What a suite AGENTS.md must cover

Use [suites/node-js-sdk/AGENTS.md](suites/node-js-sdk/AGENTS.md) as the
canonical reference. At minimum, every suite `AGENTS.md` must document:

| Section                     | Content                                                                                                                                                                                         |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **What this suite tests**   | Which AWS client/tool (SDK version, CLI version, etc.) and which column of the compat matrix it represents.                                                                                     |
| **Status**                  | `Implemented` or `Planned`. For planned suites, include an implementation checklist at the end.                                                                                                 |
| **Runtime**                 | Language version, AWS library name and version, CI base image.                                                                                                                                  |
| **File layout**             | Annotated directory tree so contributors know where to put new files.                                                                                                                           |
| **Group anatomy**           | A worked example showing the exact structure of a service group — setup, teardown, and one or two test functions — in that suite's language.                                                    |
| **Key types / interfaces**  | The `TestContext` fields (endpoint, region, runId, log, state bag), `TestGroup`/`ServiceGroup` shape, and how test functions signal pass/fail.                                                  |
| **Naming conventions**      | Group name format (`<service>-<feature>`), resource name prefix pattern, context key style, file name rules, export/function name style.                                                        |
| **Inter-test state**        | How to pass data between sequential tests within a group (context bag); rules about what must/must not be stashed.                                                                              |
| **Teardown rules**          | Suite-specific additions and gotchas on top of the canonical rules in this file (e.g. which helpers to use for S3 bucket emptying, how to suppress errors in Go, paginator patterns in Python). |
| **Error messages**          | How assertion errors should be formatted so failures are actionable.                                                                                                                            |
| **Adding a new group**      | Step-by-step checklist: create file → implement group → register in runner → verify wire output.                                                                                                |
| **What agents must NOT do** | Hard prohibitions specific to this suite (e.g. "never construct SDK clients inside test functions", "never call sys.exit", "no require() in Node.js").                                          |

Current suite AGENTS.md files (implemented suites — the eight in
`defaultSuites` and in the CI matrix):

- [suites/node-js-sdk/AGENTS.md](suites/node-js-sdk/AGENTS.md)
- [suites/cli/AGENTS.md](suites/cli/AGENTS.md)
- [suites/go-sdk/AGENTS.md](suites/go-sdk/AGENTS.md)
- [suites/python-sdk/AGENTS.md](suites/python-sdk/AGENTS.md)
- [suites/java-sdk/AGENTS.md](suites/java-sdk/AGENTS.md)
- [suites/dotnet-sdk/AGENTS.md](suites/dotnet-sdk/AGENTS.md)
- [suites/cdk/AGENTS.md](suites/cdk/AGENTS.md)
- [suites/rust-sdk/AGENTS.md](suites/rust-sdk/AGENTS.md)

Planned suite AGENTS.md files (implementation guide for agents building each
suite — none of these appears in `defaultSuites`, the CI matrix or
`registry.json`, and `--suite <name>` rejects all three as unknown):

- [suites/pulumi/AGENTS.md](suites/pulumi/AGENTS.md)
- [suites/terraform/AGENTS.md](suites/terraform/AGENTS.md)
- [suites/tofu/AGENTS.md](suites/tofu/AGENTS.md)

The two lists restate `defaultSuites` in [runner.go](./runner.go) and
`all_suites` in [.github/workflows/compat.yml](../.github/workflows/compat.yml),
so move a suite between them in the same change that registers it there.

When adding a new suite, create **both** `AGENTS.md` and `README.md` before writing any group code.

## Baseline & uniformity policy

**This section is enforced by CI. Read it before changing anything under
`compat/`.**

Two invariants hold the compat suites together. Both are checked on every push
and pull request by `.github/workflows/compat.yml`, and both fail the build.

### 1. No new failures — the baseline is a ratchet

[compat/baseline/](./baseline) records the expected status of every
(suite, group, test) triple, as one JSON shard per suite —
`compat/baseline/node-js-sdk.json` and so on. Every tool reads the directory and
aggregates it; `--baseline-file` also still accepts a single baseline file, so a
branch cut before the split stays comparable. `go run ./cmd/compat
--compare-baseline` fails when:

- a result is **worse** than the baseline records, ranked
  `pass` > `skip`/`na` > `unimplemented` > `fail`; or
- a test is **failing and absent** from the baseline — a brand-new failure
  cannot slip in just because nothing recorded it before.

Adding tests is always welcome: a new test that passes, is `unimplemented`,
`skip`, or `na` never blocks a PR.

**The burn-down is over: any failure is a regression.** The grandfathered set
reached zero in #462, so `go run ./cmd/compat --max-failures 0` runs as a second
gate and fails on *any* failing test, whatever the baseline says. The two gates
ask different questions — `--compare-baseline` asks whether a result got worse
than recorded, `--max-failures` asks whether anything failed at all — and only
the second one is immune to the baseline file being stale or hand-edited. That
is not a theoretical worry: promotion could not publish for weeks (issue #440)
and the recorded fail set sat 26 entries behind reality the whole time. Both
gates skip tests quarantined in [flaky.json](./flaky.json).

**Legitimate non-passing states.** A test may sit at:

| Status | Means | Example |
| --- | --- | --- |
| `unimplemented` | The **emulator** does not implement the operation (HTTP 501). A real, tracked gap in Overcast. | A service endpoint nobody has built yet |
| `skip` | The test exists but could not run **here** — an environmental gate — or the **suite** has not implemented the registry group yet | `requires: docker`; `not yet implemented in rust-sdk test suite` |
| `na` | The SDK or tool **has no API** for the operation. Not a gap in Overcast or in the suite. | An operation the AWS CLI does not expose |
| `fail` | The emulator answered, and answered **wrongly**. Always a bug — in the emulator or in the test. | Wrong field, wrong error, assertion failure |

`fail` is never an acceptable resting state, and as of #462 no entry is at one.
The baseline records no `fail` and CI asserts it stays that way, so a failing
test is a bug to fix or a change to revert — there is no longer a "record it and
move on" path.

**Cascades are not gaps.** A skip reading `setup failed: …` or
`dependency failed: X` is a symptom of another failure in the same group. Fix
the root cause; never grandfather a cascade on its own.

**Flaky tests are quarantined, not tolerated.** A test that gives different
answers on identical input cannot be ratcheted: baseline it as passing and the
next bad run reds an innocent PR; baseline it as failing and a promotion run
lifts it back. Both teach people to re-run the gate instead of reading it.

List such a test in [compat/flaky.json](./flaky.json) with a reason. It is then
exempt in **both** directions — never a regression, never promoted — while its
recorded status stays at the worst outcome observed. The exemption is per test,
not per group. Do not reach for this to silence a test that fails consistently;
that is a `fail` entry in the baseline and belongs in the burn-down.

**Adding an entry is a reviewer's decision, and CI enforces that.** Quarantine
takes a test out of the gate entirely, so an unguarded list is an amnesty file:
any inconvenient failure could be silenced by adding a line, and nothing would
notice the coverage had gone. `--lint-flaky-from/--lint-flaky-to` runs on PRs
and **fails on any new entry until a reviewer applies the `quarantine-approved`
label** to the pull request, and on any entry without a reason. The label is
read live from the API, so applying it and re-running the failed `Aggregate
Compatibility Results` job is enough — no new push needed — and it waives the
growth check only: entries missing a reason, issue, or date, and overdue
entries, still fail. Approved additions are named in the job log and as a
`::notice` annotation so the decision stays visible. Removing an entry — the
fix — is always allowed. If you genuinely need a new quarantine, say so in the
PR description with the evidence: two runs of an unchanged tree disagreeing is
the bar.

### Stabilising a flaky test

Quarantine buys time; it does not fix anything. The list is a work queue, it is
meant to empty, and the process makes that happen rather than hoping:

| Stage | What happens | Enforced by |
| --- | --- | --- |
| **Detected** | Nightly runs each suite 3× against unchanged `main`; any test that answers inconsistently fails that job | [compat-flake-detection.yml](../.github/workflows/compat-flake-detection.yml) |
| **Tracked** | An issue is raised automatically — one per (suite, group) cluster, since related tests are almost always one root cause. A recurrence reopens and comments rather than duplicating | [scripts/compat-flake-issue.py](../scripts/compat-flake-issue.py) |
| **Quarantined** | Only with a `reason`, a tracking `issue`, and a `since` date. Adding an entry fails the PR lint until a reviewer applies the `quarantine-approved` label, so it takes a reviewer's agreement | `--lint-flaky-to` |
| **Nagged (14 days)** | The nightly job reports the entry as overdue and annotates it | `--report-flaky-overdue` |
| **Blocking (30 days)** | The PR lint fails on the entry. Continuing means fixing it, or re-dating it with a fresh argument in review | `--lint-flaky-to` |
| **Closed** | The fix deletes the entry; removals are always allowed | — |

The deadlines escalate deliberately: nagging happens where it cannot block
anyone, and blocking only starts once a month has passed with nobody acting.
Both are visible in every compat run's job summary, so an entry cannot quietly
rot.

**Detection is scheduled, not accidental.**
[compat-flake-detection.yml](../.github/workflows/compat-flake-detection.yml)
runs every suite three times against an unchanged `main` each night and reports
any test that did not answer the same way every time. Already-quarantined tests
are reported separately — they are not news. Anything else is a **new** flake and
fails that job, because the moment to catch one is before it starts failing
someone else's pull request. Run it on demand from the Actions tab with
`workflow_dispatch` (optionally narrowing to one suite) when a result looks
suspicious.

**Reproducing one locally:**

```sh
for i in 1 2 3; do go run ./cmd/compat --suite cli --results-file "run-$i.json"; done
```

```sh
python3 scripts/compat-flake-detect.py run-1.json run-2.json run-3.json
```

**What to look for.** Every flake found so far has been an emulator state race
rather than a test bug, and they rhyme: a resource is created successfully, the
next call says it does not exist. `dotnet-sdk/sns-subscriptions` loses a topic
between `SubscribeSQS` and `Publish`; `cli/eventbridge-buses` loses a bus
between create and `ListEventBuses`. Suspect write visibility in
[internal/state](../internal/state), or a handler reading through a snapshot
taken before the write landed — and check whether the fix clears more than one
entry at once.

**Cascades deserve extra weight.** A failing test does not fail its dependants
the same way twice: `dependency failed: X` on one run, an outright `fail` on the
next. Both quarantines added so far were cascades of a *stable* failure, not
flaky tests in their own right. So a root cause with dependants is worth more
than its raw failure count suggests — fixing it removes the quarantine and the
failure together.

**Improvements are promoted for you.** On push to `main`, the aggregate job runs
`--update-baseline` and commits the changed shards under `compat/baseline/` when
a result improved. Do not hand-edit the baseline to record a fix — merge the fix
and the ratchet tightens itself. `make -C compat baseline-update` still exists
for making an improvement visible in a PR diff, and it rewrites every shard
canonically, so a hand-edited one shows up as churn in the next promotion.

> This automated commit is the **only** exception to the "never push to `main`"
> rule in the root [AGENTS.md](../AGENTS.md). It is granted to the workflow, not
> to agents or contributors: it touches `compat/baseline/` and nothing else.

**Each shard has a size budget.** `go run ./cmd/compat --lint-baseline-size`
(`make -C compat baseline-size`, and a step in the compat workflow) fails when a
shard exceeds 512 KiB — a little over 4x the largest one today. Sharding only
buys a reviewable diff while a shard stays small, so tripping the budget means
sharding further, by service say, not raising the number.

**Changing what CI measures means re-seeding, not comparing.** The baseline
records what a particular configuration produces. Change the configuration —
turn a class of test on, add a suite, change the emulator's defaults — and the
next run legitimately differs from the baseline in both directions, so comparing
is meaningless and the gate fires on work that is not a regression. Re-seed
instead: delete the shards under `compat/baseline/`, run `--update-baseline`
against an artifact **from the new configuration**, and say in the PR why the
seed moved.

This is not hypothetical. Enabling the Docker-dependent tests in CI turned five
results over: the CDK ESM assertions started passing, and two `cli` Lambda and
EventBridge tests started failing because they had only ever been measured
against a stub. A baseline seeded before that change described a configuration
that no longer existed.

`--lint-baseline-from/--lint-baseline-to` runs on PRs and rejects a baseline
edit that downgrades an expectation, adds a new `fail`, or removes an
expectation **the registry still asks that suite to produce**. A removal is
allowed exactly when the pull request's own registry — hand-written plus
generated — no longer asks for it: the group is gone, the test is gone from the
group, or the group's `suites` no longer names the suite. Those are reported as
`compat baseline: <key> dropped — no longer in scope for <suite>` and pass. Any
other configuration change still takes the re-seed route above, because the row
is one a run would still produce.

### Generated groups soak in before they gate — never hand-edit them in

A group in [suites/registry.generated.json](./suites/registry.generated.json)
lands in `state: "candidate"`: it runs and reports everywhere but is excluded
from `--compare-baseline` and `--max-failures` in both directions. This is the
inverse of `flaky.json` — a quarantined test escaped a gate it was already
under, with a reviewer's approval; a candidate has not entered it yet.

Promotion is mechanical, and nobody types it. Each night, after the 3×
flake-detection runs, `go run ./cmd/compat --promote-generated` reads them and
promotes every candidate whose every `(suite, test)` answered **identically in
all three runs, with no `fail` and no `skip`**, with every suite the group is
scoped to reporting in every run. It writes exactly one file,
[model/promotions.json](./model/promotions.json) — the soak ledger, which
`cmd/compatgen` reads to emit each group's `state`. `cmd/compatgen` still owns
`registry.generated.json` and rewrites it wholly; the bot PR on
`automation/promote-generated` carries the ledger and the regenerated registry
in one commit, so `make compat-model-check` stays byte-identical. Two tools
writing one generated file is the thing this shape exists to prevent.

`skip` blocks for a different reason from `fail`, and the difference matters:
a skip is not an answer about the operation, it is the suite saying it never
asked. A group whose every test skips in all three runs — a setup failing the
same way three nights running is the realistic case — is perfectly consistent
and has been exercised exactly zero times, so consistency alone would gate it
on evidence that nothing ran. It is reported like a flip, naming the
`(suite, test)`.

**`unimplemented` is a promotable agreement, and deliberately so.** A Tier 0
probe group calls an operation the emulator does not implement, with
identifiers that do not exist; a stable 501 from every suite in every run is
that operation answering exactly as modelled, and gating on it is what makes
the group catch the day it stops being true. Holding `unimplemented` back
would leave the largest class of generated group outside the gate forever.

A candidate that has not promoted 30 days after its recorded `firstSeen` is
reported overdue by the same command, naming the `(suite, test)`s that could not
agree. That is a bug in the recipe, the values table or the emulator — fix it
there. **Never** gate a group by hand-editing `promotions.json`, and never
quarantine a generated test in `flaky.json` to make a run green: the soak's
whole purpose is that a mass-generated test enters the gate on evidence rather
than on somebody's patience.

### 2. Uniformity — the registry is the contract

Every SDK and CLI suite tests **the same operations**. When you add an
operation, group, or case to one suite, it goes into all of them.

The workflow is registry-first:

1. Add the group/test to [suites/registry.json](./suites/registry.json).
2. Implement it in **every** suite — `node-js-sdk`, `python-sdk`, `go-sdk`,
   `cli`, `java-sdk`, `dotnet-sdk`, `rust-sdk`.
3. Where an SDK genuinely lacks the API, register the test as `na` with a
   reason rather than leaving it unimplemented.

`go run ./cmd/compat --check-parity` enforces this. It classifies every
(suite, registry test) pair from a real run and fails when a gap is not
declared in [compat/parity-debt.json](./parity-debt.json).

**Divergence is allowed only when the tool differs, and must be explicit:**

- **`na`** — the SDK has no API for the operation. Recorded per test, in the
  suite.
- **`"suites": [...]`** on a registry group — the group only makes sense for
  specific suites. Used by `cdk-lifecycle`: CDK deploys whole stacks rather than
  calling operations one at a time, so registry-wide uniformity does not apply
  to it and it runs only groups scoped to it.

  This has three cases, and they are not symmetric:

  - **On a hand-written group** (an entry in `compat/suites/registry.json`),
    `suites` scoping remains reserved for `cdk-lifecycle`. Reach for it rarely;
    an SDK suite is never a legitimate `suites` scope on a hand-written group.
  - **On a generated group** (an entry in `compat/suites/registry.generated.json`,
    carrying `"generated": true`), `suites` is not merely allowed but
    **required** — it lists the backends a scenario-driven test can actually
    execute, mechanically derived from which suites have a generated backend
    for that recipe. It is written by the generator and widens automatically as
    typed backends land (see the model-driven coverage plan); it is never
    hand-edited.
  - **On a ported group** — a hand-written group carrying `scenario`, whose
    tests an authored IR scenario resolves — `suites` is neither written nor
    left to default: it is **derived**, from the same backend availability a
    generated group's comes from, and recorded by `cmd/compatgen` in the
    **`ported` index** of `registry.generated.json`. `cmd/compat` applies it on
    load, so the parity checker asks of a ported group exactly what it asks of
    a generated one, and a suite with no backend for it neither implements it
    nor owes debt for it. The hand-written entry carries no `suites` of its
    own, and the `cdk-lifecycle` allowlist is untouched by any of this.

  `scripts/validate-compat-registry.py` enforces all three: a hand-written
  group with `suites` outside the allowed set fails, a generated group without
  `suites` fails, and a ported group missing from the `ported` index — or an
  index entry naming a group that is not ported — fails.
- **`compat/parity-debt.json`** — a group a suite has not implemented *yet*.
  Temporary, and it only shrinks. The check fails if debt grows, if new debt is
  undeclared, or if declared debt is stale (the group is now implemented, so the
  entry must be deleted in the same PR).

Regenerate the debt file with:

```sh
go run ./cmd/compat --update-parity-debt --results-file compat-results.json
```

**Every suite loader emits the same not-implemented sentinel** —
`not yet implemented in <suite> test suite`. The parity checker tells a registry
gap apart from an environmental skip by that exact phrasing, so all eight
loaders must keep producing it. A skip reason matching no known category is
reported as `unclassified`, which is how wording drift surfaces instead of
silently hiding a gap.

### 3. Docker-dependent tests are first-class

Lambda invocation, ESM delivery, and the CDK stream tests execute real
containers. They run **locally and in CI** — GitHub's `ubuntu-latest` runners
have a Docker daemon, and `compat/docker-compose.yml` mounts the host socket.

`OVERCAST_COMPAT_SKIP_DOCKER=1` is a local opt-out for a machine without a
daemon. **Never set it in CI**: skipping those tests hid the emulator's Lambda
stub behind a green check, which is exactly the blindspot this policy exists to
prevent.

A managed instance — one compat starts itself, not an endpoint you pinned —
gets the host Docker socket bind-mounted (`--mount-docker-socket`, on by
default). Compat then reads the instance's own `/_overcast/health` and, when the
*machine* is why there is no daemon (nothing to mount, or none running here),
sets `OVERCAST_COMPAT_SKIP_DOCKER=1` itself and says so once at startup. That
automatic skip is deliberately confined: it never fires for a pinned endpoint,
so CI and the compose file are untouched, it never overwrites a value you set,
and it never fires when compat mounted the socket and the instance *still*
reports no daemon — that answer is the emulator's, and its tests are left to
fail. Widening any of those three would rebuild the blindspot above.

### How failures reach you

The aggregate job renders one report three ways, all from
[scripts/compat-report.py](../scripts/compat-report.py):

| Surface | What it carries |
| --- | --- |
| **Job summary** | Gate failures first, then the suite matrix, new vs known failures, and parity debt |
| **PR annotations** | One `::error` per regression and per parity issue, on the checks tab |
| **`Compat Report` check run** | JUnit — regressions are failures, expected gaps are skips, with per-test history |
| **Sticky PR comment** | Digest: counts, pass rate, top issues, link to the run |
| **Artifacts** | `compat-results.json` + `compat-junit.xml`, 90-day retention; attached to releases |

---

### registry.json — canonical test matrix

`compat/suites/registry.json` is the **single source of truth** for every
group and test that any suite should implement. It lists all services, group
names, and individual test names across the entire compat matrix.

```
compat/suites/
  registry.json         ← canonical list of all groups + tests
  registry.schema.json  ← JSON Schema for the registry
```

**The generated sibling.** `registry.generated.json` is the other half of the
matrix: `cmd/compatgen` rewrites it wholly from the scenario IR under
`compat/model/scenarios/`, and every loader concatenates it with
`registry.json`. Never hand-edit it, and never add a generated group to
`registry.json` — see [compat/model/README.md](./model/README.md) for the IR,
the recipes that produce it and the refusal report, and
[cmd/compatgen/README.md](../cmd/compatgen/README.md) for the workflow
(`make generate-compat-model`, `make compat-model-check`).

**Rules for every suite:**

- A suite must implement **every** group in `registry.json`, except groups a
  registry entry scopes elsewhere via `"suites"`. "The tool only covers a few
  services" is parity debt, not a design — it belongs in
  [parity-debt.json](./parity-debt.json) and it shrinks. See
  [§ Baseline & uniformity policy](#baseline--uniformity-policy).
- When a suite has not yet implemented a group, it must emit a `test_result`
  with `status: "skip"` for every test in that group — never simply omit the
  group from the output. Use the shared sentinel reason
  `not yet implemented in <suite> test suite`; the parity checker classifies
  gaps by that exact wording.
- When the SDK or tool genuinely has no API for an operation, emit `"na"` with a
  reason instead — that is a permanent, accepted divergence, unlike a skip.
- Group names and test names in suite implementations **must exactly match**
  the registry (`name` fields are case-sensitive). The dashboard joins results
  across suites using these names.
- The `op` field on a test entry is the AWS API operation being exercised
  (absent when it matches `name`). Suites may use it for display or filtering
  but must still use the `name` field as the test identifier.

A generated group may also carry **`"parallel": true`** — `cmd/compatgen` sets
it on a probe group and on nothing else, and it lets a loader run that group's
tests concurrently (bounded by `OVERCAST_COMPAT_PARALLEL_SLOTS`) while still
reporting them in registry order; a loader that ignores it runs them in order,
which is always correct.

A generated group may also carry **`"shadowOf": "<group>"`**. That is a
hand-written group being ported to an authored IR scenario
([compat/model/README.md § Authored scenarios](./model/README.md#authored-scenarios)):
while the port soaks it runs beside the natives under `<group>-shadow`, so both
are live at once and no suite registers two implementations for one
`group:test` key. `go run ./cmd/compat --compare-shadow --results-file <run>`
joins the two on (suite, test) and reports every pair that answered
differently — the evidence the flip PR cites when it deletes the native code.
A shadow group is always in state `candidate` and never promotes;
`--promote-generated` skips it. Like every field above it is derived by
`cmd/compatgen`, from the group's own name, and is never hand-written.

#### `scenario` on a hand-written group — a ported group

`scenario` is the one field above that is legal in **both** registries.
`generated`, `state`, `shadowOf` and `parallel` each state a fact about
generator output, and a hand-written copy of one could only contradict it;
`scenario` states something a human decides. On a hand-written group it means
the group has been **ported** (#1903, and
[compat/model/README.md § Authored scenarios](./model/README.md#authored-scenarios)):
an authored IR scenario under `compat/model/authored/` resolves its tests
through each suite's scenario backend, and the per-language implementations are
gone.

Three rules follow, and each is enforced rather than trusted:

- **A ported group has no per-suite implementations.** The port replaces them;
  an impl left behind for one of its tests is a duplicate, not a fallback, and
  in `java-sdk` and `dotnet-sdk` a leftover *setup* hook aborts the run. Delete
  them in the same change that adds the field.
- **The two halves of a port are one change.** `cmd/compatgen` refuses a
  registry group carrying `scenario` while its authored scenario is still a
  `-shadow`, refuses a live authored group whose registry entry carries no
  `scenario`, and refuses a `scenario` no authored file backs. Either half
  alone fails silently: the interpreting suites resolve nothing while the typed
  suites resolve by group name and run the port anyway.
- **Its `suites` is derived, never written** — see the `ported` index in
  [§ Uniformity](#2-uniformity--the-registry-is-the-contract) above.

`cmd/compat`'s `lintGeneratedRegistry` and
`scripts/validate-compat-registry.py` both hold the hand-written registry to
the shortened generated-only list and both check the `ported` join in each
direction.

**Every loader concatenates `registry.generated.json`** onto `registry.json`
(hand-written groups first) and honours `"suites"` on **every** group, generated
or hand-written (#1393, #1737) — one general rule, which is what replaced each
loader's `service == "cdk"` carve-out. A group whose `suites` does not name the
running suite is not loaded there at all: no tests, no skips, no results.

Interim rule, until the G2 interpreters land:
a generated group with no static impl **and** no scenario backend must never
report `skip` or `na` — it is a **`fail`**, with message exactly `generated
group "<group>" is scoped to <suite> but <suite> has no scenario backend`.
`"suites"` on a generated group is derived from backend availability by
`cmd/compatgen`, so a suite named in it that cannot run the group is a
generator/loader bug, and `candidate`-state groups keep this out of the gates
until promotion — but it must never look like a pass.

### Implementation keys — `group:test`, and a bad key aborts the run

Every suite maps a key to each test implementation. **The key is always the
group-qualified `group:test`**, and the separator is a **colon** in all seven
loaders:

```
"lambda-crud:CreateFunction"      ← group-qualified: the only form to write
"CreateFunction"                  ← bare: legacy, and refused by the suite's own tests
"lambda-crud/CreateFunction"      ← WRONG: not a separator any loader accepts
```

The loaders still *resolve* a bare key while exactly one group declares that
test name, and that is the trap #1700 closes. A bare key is not wrong when it is
written; it becomes wrong when someone else adds a second group declaring the
same name — a one-line registry diff, in another PR, that turns every bare key
for that name into an abort. So the rule is not "qualify the ambiguous ones", it
is **qualify every key**, unconditionally, and each suite's registration test
enforces it by refusing any key without a `:` (named per suite below).

That is what makes a shared test name a non-event. Twenty-odd registry test
names are already declared by two or more groups — `ListUsers`, `TagResource`,
`CreateFunction` — and `registry.generated.json` adds far more by construction:
a generated test's name is the PascalCase operation name
([docs/plans/compat-coverage-modelgen.md](../docs/plans/compat-coverage-modelgen.md)
§3.3), so every generated SQS group declares `CreateQueue`, `SendMessage` and
the rest beside `sqs-queues` and `sqs-messages`, and a model refresh may add
more with zero human actions (§3.11). **A test name declared by several groups —
hand-written or generated — is normal and safe**, precisely because no key that
resolves it is ever bare. Nothing has to be acknowledged, listed or waived when
it happens.

Three rules are enforced by every loader, and breaking any of them **aborts the
run**:

1. **A key must resolve.** A key matching neither `test` nor `group:test` in
   `registry.json` is refused, naming the key. It used to be a stderr warning
   nobody read.
2. **A bare key must be unambiguous.** When more than one group declares a test
   name — `ListUsers` is in both `iam-users` and `cognito-userpools`;
   `CreateFunction` is in both `lambda-crud` and `appsync-functions` — a bare
   key cannot say which group it implements, so it is refused. Qualify it.
3. **A key may be registered once.** Two service files registering the same key
   are refused, naming the key and both files. Rules 1 and 2 are checked by
   `ValidateImpls`; this one cannot be, and is checked during the merge instead
   — see below.

All three exist because the failure they prevent is silent. A key that did not
resolve fell through to the bare name, which for a shared name is *another
group's implementation*: the run then reported that group's result under this
test's name, and nothing failed. Two suites were doing exactly this — `go-sdk`
and `python-sdk` recorded `iam-users/ListUsers` as a pass while running
Cognito's `ListUsers`, which returned early because no user pool was in scope,
so no IAM request was ever made.

**Rule 3 is checked where the maps are merged, not where they are validated.**
Every suite flattens its per-service impl maps into one before resolving
anything (`allImpls[k] = v`, `all_impls.update(...)`, `impls.putAll(...)`), and
that merge is last-writer-wins. Validation cannot see a collision: by the time
it runs, the losing implementation has already been dropped and the surviving
key resolves perfectly well. So each loader has a **`MergeImpls`** —
`merge_impls` / `mergeImpls` — that takes the per-service maps as *labelled
sources* and refuses a key two of them claim. Runners must build their impl map
through it rather than assigning into one map, and a duplicate is reported as:

```
[go-sdk] 1 duplicate impl registration(s):
  - impl "lambda-crud:CreateFunction" is registered by both "lambda" and "appsync" — one of the two would be silently discarded; remove or re-key one
```

Naming both sources matters: the key alone does not say where to look, and the
whole point is that one of the two files is in the wrong. Each suite labels its
sources from what it already has — the service name in `groups.All()` (`go-sdk`,
`cli`), `mod.__name__` (`python-sdk`), the class name (`java-sdk`,
`dotnet-sdk`), a `name()` on the group trait (`rust-sdk`), or `service/group`
for `node-js-sdk`, which derives its keys from built groups rather than from
per-service maps.

This is the harness-side application of the principle in
[docs/plans/full-emulation-priority.md](../docs/plans/full-emulation-priority.md)
§2.1: fail loudly rather than silently do the wrong thing.

`BuildGroups` (and each suite's equivalent) also refuses the bare fallback for
an ambiguous name, so a mis-bind cannot occur even if validation is bypassed —
such a test is reported as `not yet implemented in <suite> test suite` rather
than bound to the wrong implementation. **That refusal is the second line of
defence and it stays** — but with every key qualified it no longer has anything
to stand between: there is no bare key left for it to disambiguate.

What there deliberately **is not** is a registry-side lint against shared test
names. Ambiguity is not the fault to catch; an unqualified key is, and that is
caught in the suite that wrote it. `cmd/compat` cannot read a suite's impl map
anyway — those are Go, Python, Java, C#, Rust and TypeScript source, not data —
and a lint that failed on new ambiguity would fail on the generator's own naming
convention, on every model refresh, against the plan's zero-human-actions rule.
`lintGeneratedRegistry` in
[cmd/compat/generatedregistry.go](../cmd/compat/generatedregistry.go) therefore
checks only what really is a conflict: a duplicate group name across the two
registries, and an exact `(group, test)` duplicate — both of which would merge
rather than clash in `compat/baseline/`, `compat/flaky.json` and
`compat/parity-debt.json`.

Each loader has unit tests pinning all three rules, plus — where the suite's
group list can be imported without starting a run — a test that merges the
suite's real registrations and resolves them against the real `registry.json`.
That test is the one that catches a collision introduced in a service file,
because merging the real maps is itself the duplicate check. It is also where
the no-bare-keys rule is enforced: the test asserts that every key the suite
registers contains a `:`, and names the offenders when one does not. It is
`TestRegisteredImplsHaveNoBareKeys` in `go-sdk` and `cli`,
`test_registered_impls_have_no_bare_keys` in `python-sdk`,
`registeredImplKeysAreAllQualified` in `java-sdk`,
`RegisteredImplKeysAreAllQualified` in `dotnet-sdk`, and
`registration_tests::real_impls_resolve_against_the_real_registry_and_are_all_qualified`
in `rust-sdk`. `node-js-sdk` needs none: `makeImplMap` (`src/groups/index.ts`)
qualifies every key by construction.

| Suite | Tests | Run with |
| --- | --- | --- |
| `go-sdk`, `cli` | loader + real registrations | `scripts/docker-go.sh -C compat/suites/<suite> test ./...` |
| `java-sdk` | loader + real registrations | `mvn test` (also runs in the image build) |
| `python-sdk` | loader + real registrations | `python -m unittest discover -s tests` (after `pip install -r requirements.txt`) |
| `node-js-sdk` | loader + real registrations | `npm run test:unit` (after `npm ci`) |
| `rust-sdk` | loader + real registrations (`registration_tests`, #1714) | `cargo test` (also runs in the image build) |
| `dotnet-sdk` | loader + real registrations (`Tests/`, #1697) | `dotnet test Tests/OvercastCompat.Tests.csproj` (also runs in the image build) |

`dotnet-sdk` had no test project at all until #1697, which adds `Tests/` — the
loader cases and the registration cases together — and runs `dotnet test` in the
image build. Until that lands its only guard is the abort at startup, so a
duplicate or bare key there surfaces on the first run rather than in CI.

In CI they run in `test.yml`'s **Compat suite unit tests** job: `go-sdk`, `cli`,
`python-sdk` and `node-js-sdk`, each from its own directory, with no emulator
and no Docker. The two interpreted suites install their dependencies there
first, and both need to: their real-registration cases import the suite's own
group files, which import the SDK clients those files drive. For `python-sdk`
it reaches further than that import — `lib/harness.py` imports `botocore`
lazily, inside the path that classifies a raised exception, so the cases that
*deliberately* raise need it too.

`java-sdk` and `rust-sdk` stay out of that job because their
image builds already run them — `mvn package` runs surefire, and `cargo test`
sits beside `cargo build` — on a dependency layer that is already resolved and
compiled there. Repeating either in the job would mean fetching or building the
whole AWS SDK a second time for a verdict CI already has.

Until that job existed nothing ran any of them. Every suite is its own module,
so the root `go test ./...` matches no package under `compat/suites/`, and
`compat.yml` installs each suite's dependencies only to run it against a live
emulator — the unit tests fell between the two. `scripts/verify-changed.sh` had
the same blind spot from the other side: it now runs `go test ./...` inside each
module a branch touched, so editing a suite runs that suite's tests instead of
failing the pre-push gate with "no packages to test".

**Rules for modifying the registry:**

- Adding a new group or test to `registry.json` is the first step when
  implementing a new service or operation — do it before writing suite code.
- Test names must be PascalCase (`^[A-Z][A-Za-z0-9]+$`) and group names
  kebab-case (`^[a-z0-9]+(-[a-z0-9]+)*$`). When a test exercises a variant of an
  operation, fold the distinction into the name (`CreateUserPoolClientWithTokenValidity`)
  and put the bare operation in `op` — never use a descriptive name with spaces.
- Avoid removing or renaming an existing group or test entry: the name is the
  join key across every suite, `compat/baseline/`, and dashboard history, so
  a rename must update all of them in the same commit. A rename is only
  justified when an entry violates the schema. If an operation is simply no
  longer relevant, mark it `"deprecated": true` rather than deleting it.
- **A rename you cannot avoid must carry the state files with it.**
  `compat/baseline/` is keyed by `suite/group/test`, so renaming one test
  orphans one entry *per suite* — seven or eight of them, one per shard — and
  each orphan fails the gate as `compat baseline missing result`.
  `compat/flaky.json` and `compat/parity-debt.json` are keyed the same way.
  Rename in the registry and every suite, re-key all three files, and land it
  in one PR.
- **Reusing a test name another group already declares is fine and needs no
  ceremony.** The join key is `suite/group/test` and every impl key is
  `group:test`, so a shared name binds nothing wrongly — see § Implementation
  keys. Prefer a distinct name where the two tests really are different things,
  but a generated group declaring the same operation name as a hand-written one
  is the expected shape, not a clash to resolve.
- Bump the `version` field only for breaking schema changes; adding new groups
  is non-breaking and does not require a version bump.
- CI validates the registry against `registry.schema.json` (the `Compat registry
  schema` job). Run it locally before pushing:

```bash
make compat-registry-check
```

### When a new Overcast service is implemented

Every new service **must** have a corresponding compat group added at the same
time as the implementation:

1. Add the new service's groups and tests to `compat/suites/registry.json`.
2. Implement the group in **every** SDK/CLI suite, matching the registry
   group/test names exactly and registering it in that suite's runner:

   | Suite | Group file | Registered in |
   | --- | --- | --- |
   | `node-js-sdk` | `src/groups/<service>.ts` | `src/index.ts` |
   | `python-sdk` | `groups/<service>.py` | `runner.py` |
   | `go-sdk` | `internal/groups/<service>.go` | `internal/groups/groups.go` |
   | `cli` | `internal/groups/<service>.go` | `internal/groups/groups.go` |
   | `java-sdk` | `src/main/java/io/overcast/compat/groups/<Service>Group.java` | `Main.java` |
   | `dotnet-sdk` | `src/Groups/<Service>Group.cs` | the group registry |
   | `rust-sdk` | `src/groups/<service>.rs` | `src/groups/mod.rs` |

   Where an SDK has no API for an operation, register it as `na` with a reason —
   do not leave it unimplemented. See the suite's own `AGENTS.md` for its
   "Adding a new group" checklist.
3. Run `go run ./cmd/compat --check-parity` and confirm it passes. If a suite
   genuinely cannot be completed in the same PR, record the gap in
   `compat/parity-debt.json` and say why in the PR description — debt is a
   deliberate, reviewed decision, not a default.

Do not open a PR that adds a new service without also updating the registry and
adding its compat group. Compat tests are the external contract check —
integration tests alone are not sufficient.

---

## Go runner conventions

- The Go runner in `compat/runner.go` starts each suite as a subprocess.
- It reads NDJSON from the subprocess stdout line by line.
- It surfaces `stderr` from subprocesses as `WARN`-level log lines.
- Suite processes are run sequentially by default; `--parallel` flag is planned.

### Adding a new Go suite

Implement `Suite` interface in a new file under `compat/`:

```go
type Suite interface {
    Name() string
    // Command returns the argv to run (first element is the executable).
    Command(cfg RunConfig) []string
    // Env returns additional environment variables for the subprocess.
    Env(cfg RunConfig) []string
}
```

---

## Reading compat results — how agents should use the report

After a compat run the full results are written to **`/workspace/compat-results.json`**
(path is configurable via `OVERCAST_COMPAT_RESULTS_FILE`). This file is the
canonical source of truth for the current state of compatibility. It is **not**
the raw NDJSON event stream — it is the aggregated `RunReport` built by
`compat/runner.go`.

### Getting an actionable summary

The fastest way to turn the report into something actionable is:

```bash
# From the workspace root — no Docker, no running server needed:
make compat-report

# Or equivalently:
go run ./cmd/compat --report

# Point at a non-default file:
go run ./cmd/compat --report --results-file /path/to/results.json
```

This prints three sections:

| Section                    | What to do with it                                                                                                                          |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| **UNIMPLEMENTED SERVICES** | Lists every service where the emulator returned HTTP 501, grouped by service directory. These are prioritised implementation targets.       |
| **GENUINE FAILURES**       | Tests that should work but returned the wrong response or a non-501 error. Investigate `internal/services/<service>/`.                      |
| **CASCADE FAILURES**       | Tests that failed only because an earlier step in the same group failed. Fix the genuine failure above first; cascades should self-resolve. |

### JSON schema for direct parsing

If you need to parse the file directly (e.g. to filter by suite or service):

```python
import json
data = json.load(open("compat-results.json"))
# Top-level keys: Endpoint, StartedAt, FinishedAt, Suites
for suite in data["Suites"]:          # suite["Suite"] = "go-sdk", "cli", …
    for group in suite["Groups"]:     # group["Name"] = "s3-crud", "sqs-queues", …
        for test in group["Tests"]:   # test["status"] = "pass"|"fail"|"unimplemented"|"skip"
            if test["status"] in ("fail", "unimplemented"):
                print(f"{suite['Suite']}/{group['Name']}/{test['test']}: {test.get('error','')}")
```

Full field reference for each `test` entry:

| Field         | Type   | Notes                                                               |
| ------------- | ------ | ------------------------------------------------------------------- |
| `event`       | string | Always `"test_result"`                                              |
| `suite`       | string | e.g. `"go-sdk"`                                                     |
| `service`     | string | e.g. `"s3"`, `"sqs"`                                                |
| `group`       | string | Group name e.g. `"s3-crud"`                                         |
| `test`        | string | Test name e.g. `"CreateBucket"`                                     |
| `op`          | string | AWS operation name (may differ from `test`)                         |
| `status`      | string | `"pass"`, `"fail"`, `"unimplemented"`, `"skip"`                     |
| `duration_ms` | int    | Elapsed time in milliseconds                                        |
| `error`       | string | Only present (non-empty) when `status` is `fail` or `unimplemented` |

### Interpreting `fail` vs `unimplemented`

- **`unimplemented`** — the emulator returned HTTP 501. The error message identifies
  the exact AWS target, e.g. `"Unknown target: Kinesis_20131202.CreateStream"`. The
  fix is to implement or stub the endpoint in `internal/services/<service>/`. These
  are always expected gaps — never treat them as urgent bugs.

- **`fail`** — the emulator returned a non-501 response that caused the test to fail.
  Sub-categories:

  | Error pattern                                                                    | Likely cause                                | Fix                                                                                    |
  | -------------------------------------------------------------------------------- | ------------------------------------------- | -------------------------------------------------------------------------------------- |
  | `"ResourceAlreadyExists"`, `"BucketAlreadyOwnedByYou"`, `"Table already exists"` | Orphan from a previous run; teardown failed | Fix teardown in the suite group; run `make -C compat ci` (which rebuilds from scratch) |
  | `"no <resource> from <PreviousOp>"`                                              | Cascade — earlier step failed               | Fix the root cause (the operation listed in `GENUINE FAILURES`)                        |
  | `"Error parsing parameter '--body'"`                                             | CLI group passes a file path incorrectly    | Fix in `compat/suites/cli/internal/groups/<service>.go`                                |
  | AWS error on a supposedly implemented op                                         | Emulator bug                                | Investigate `internal/services/<service>/handler*.go`                                  |

### Mapping a failing test to emulator code

1. From the test's `service` field (e.g. `"sqs"`), find `internal/services/sqs/`.
2. Check `internal/services/sqs/handler.go` for the handler method (search for the operation name).
3. If the method is in `handler_stubs.go`, the operation is not yet implemented — add it to `handler.go`.
4. For cross-suite failures (same operation fails in `go-sdk` AND `cli`), the bug is certainly in the emulator, not the suite.

### Cross-suite signal

When the same group/operation fails in **multiple suites**, that strongly
indicates an emulator bug rather than a suite authoring error. A single-suite
failure is more likely a suite bug (wrong resource name, missing teardown,
wrong parameter). Use `make compat-report` to quickly see which suites hit each failure.

---

## What agents must NOT do in compat/

### Separation boundary

- **Never import from `internal/`** — compat has zero dependency on the emulator source tree.
- **Never add routes, middleware, or handlers to the Overcast server** for compat purposes.
- **Never add compat pages, components, or API calls to `web/`** (the Overcast UI) — the compat UI lives in `compat/ui/` and is served by the compat server only.
- **Never add compat configuration to `internal/config/`**.
- **Never reference `cmd/overcast/` or `internal/` from any compat file**.

### Runner and suite behaviour

- Never start Overcast inside a test group — the runner manages the emulator lifecycle.
- Never use `process.exit()` inside a test function — throw instead.
- Never write to stdout inside a test function — use `ctx.log()` which writes to stderr.
- Never add dependencies that require native binaries (e.g. `node-gyp`) to any suite.
- Never skip a test to hide a gap — let it run, let it fail, record the result.

### Compat server and UI

- The compat server (`compat/server.go`, served by `cmd/compat`) must never import Overcast internals.
- The compat UI (`compat/ui/`) must only fetch from the compat server, never from the Overcast emulator directly.
- Do not embed compat UI assets into the Overcast binary (`cmd/overcast`).

---

## SDK version pinning & upgrade strategy

Every suite **must pin** its AWS SDK version to a specific, reproducible
version — never use floating tags (`latest`, `^x.y.z` in npm, `>=x.y.z` in
pip). Pinning ensures that CI results are identical across every machine and
every day. A compat test that passes with SDK v3.1020.0 must still pass with
the same SDK a month later; the only variable is the emulator.

### Pinned versions by suite

| Suite        | File                      | Pinned version                                        |
| ------------ | ------------------------- | ----------------------------------------------------- |
| node-js-sdk  | `package.json`            | `@aws-sdk/client-*` `^3.1020.0`                       |
| python-sdk   | `requirements.txt`        | `boto3>=1.34.0`, `botocore>=1.34.0`                   |
| go-sdk       | `go.mod`                  | `github.com/aws/aws-sdk-go-v2 v1.41.5` (+ `config/*`, `service/*`) |
| dotnet-sdk   | `OvercastCompat.csproj`   | `AWSSDK.*` `4.0.0`, except `AWSSDK.Core` `4.0.102.3` and `AWSSDK.Organizations` `4.0.101.4` (added) |
| java-sdk     | `pom.xml`                 | `software.amazon.awssdk` (BOM-managed)                 |
| rust-sdk     | `Cargo.toml`              | `aws-sdk-*` `=1.x` (exact, e.g. `=1.65.0`)            |
| cli          | `Dockerfile`              | AWS CLI v2 (pinned via base image tag)                 |
| cdk          | `package.json`            | `aws-cdk-lib` / `aws-cdk` (v2, pinned in package.json) |

### Upgrade procedure

1. **Do not upgrade unprompted.** SDK upgrades are rare — only trigger one when
   a suite hits an SDK bug (fixed in a newer point release), when a new major
   version brings material API surface that the suite should exercise, or when
   **the generated corpus needs operations the pin predates**: a typed backend
   compiles the operations `cmd/compatgen` emits, so an operation newer than the
   pin is a build failure rather than a test result. Split that bump into its
   own PR too, unless the feature will not compile without it — in which case it
   belongs in the feature's PR, with the before/after table of step 4 in the PR
   body saying so.

2. **Upgrade one suite at a time.** Open a separate PR for each suite's SDK
   bump so CI can isolate regressions.

3. **Update the pin file and the AGENTS.md simultaneously.** Every version
   change in `package.json` / `Cargo.toml` / `.csproj` / `go.mod` /
   `requirements.txt` must be paired with an update to the pinned version
   table in that suite's own `AGENTS.md`.

4. **Full re-run against latest emulator `main`.** After bumping, trigger a
   complete run of the affected suite and verify zero new failures:
   - Zero new `fail` results (regressions)
   - Zero new `unimplemented` results (API shape changes may rename operations)
   - Any status change from the previous run must be explainable and documented
     in the PR body.

5. **Dockerfile compatibility.** If the SDK bump requires a newer runtime
   (e.g. .NET 9, Node.js 22), update the base image tag in the Dockerfile in
   the same PR. Do not leave a suite with a runtime that cannot execute the
   SDK it declares.

6. **Notification list.** When upgrading, mention the suite maintainers in
   the PR description so they can spot-check the API diffs. A breaking change
   in the SDK's public API may require test code changes that only a human
   familiar with the service can validate.
