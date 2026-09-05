# tests/AGENTS.md

> Test conventions for this project. Read this before writing any test.
> For general project conventions see the root [AGENTS.md](../AGENTS.md).

---

## Test philosophy

- **Every feature has a failing test before implementation.**
- **Every bug has a reproducing test before the fix.**
- Tests are the specification. If the behaviour isn't tested, it isn't guaranteed.
- Tests must be deterministic — no time dependencies, no random ordering, no
  shared mutable state between tests.
- Tests must be fast. Integration tests should complete in under 5 seconds total.

---

## Test structure: Given / When / Then

All tests are written in GWT (Given/When/Then) form. This makes the intent
unambiguous and makes failures easy to diagnose.

```go
func TestGetObject_success(t *testing.T) {
    // Given: a bucket and an object exist
    srv := helpers.NewTestServer(t)
    createBucket(t, srv, "my-bucket")
    putObject(t, srv, "my-bucket", "hello.txt", []byte("hello world"), "text/plain")

    // When: we GET the object
    resp, err := http.DefaultClient.Do(get(srv, "/my-bucket/hello.txt"))
    require.NoError(t, err)
    defer resp.Body.Close()

    // Then: we receive the correct body and headers
    helpers.AssertStatus(t, resp, http.StatusOK)
    helpers.AssertHeader(t, resp, "Content-Type", "text/plain")
    body := helpers.ReadBody(t, resp)
    assert.Equal(t, "hello world", body)
}
```

`http.DefaultClient` is the right client here, and closing without reading the
body is fine: importing `tests/helpers` retunes the default client for test
binaries (drain-on-close plus a wide connection pool — see
`tests/helpers/httpclient.go`). Don't hand-roll a `&http.Client{...}` per test
or per request; on Windows the connection churn exhausts the 16k-port dynamic
range and fails whole packages with `connectex` dial errors.

### Naming convention

Test function names follow this pattern:

```
Test<Subject>_<scenario>
```

Examples:

- `TestCreateBucket_success`
- `TestCreateBucket_nameTooShort`
- `TestReceiveMessage_visibilityTimeout`
- `TestDeleteObject_nonExistentIsIdempotent`

The scenario name should describe the **state or condition**, not the expected
outcome. The outcome is in the assertion, not the name.

---

## Two types of tests

### Unit tests — `internal/*/`

Test a single function or type in isolation. No HTTP, no server startup.
Mock or stub any external dependency.

```go
// internal/state/memory_test.go
func TestMemoryStore_GetSetDelete(t *testing.T) {
    // No server, no HTTP — just the Store interface
    s := state.NewMemoryStore()
    ctx := context.Background()
    ...
}
```

Unit tests live **alongside the code they test** in `internal/`.
Run with: `make test-unit`

### Integration tests — `tests/integration/*/`

Test the full HTTP request/response cycle through the real middleware stack.
Use `helpers.NewTestServer(t)` which spins up a real `httptest.Server`.

```go
// tests/integration/s3/s3_test.go
func TestPutObject_success(t *testing.T) {
    srv := helpers.NewTestServer(t)
    // real HTTP requests through the real router
    ...
}
```

Integration tests live in `tests/integration/<service>/`.
Run with: `make test-integration`

#### Lambda is two packages, split on Docker

The Lambda suite is two sibling packages so `go test ./...` runs them at the
same time instead of serialising ~250 metadata tests behind the container ones.
Which half a new test belongs in:

- **`tests/integration/lambda`** — the control plane over HTTP. Starts no
  container and talks to no daemon. This is the default home; put a test here
  unless it needs a running Lambda.
- **`tests/integration/lambdadocker`** — everything that starts a container:
  anything that would call `helpers.SkipWithoutDocker`,
  `helpers.WithLambdaDocker()` or `requireLambdaInit`. **Never add
  `t.Parallel()` to a test here** — they share named Docker networks, fixed
  registry ports and the daemon's address pool.

The two halves cannot see each other's unexported declarations, so anything
both need — the wire types, `doJSON`, `lambdaURL`, the in-container init
bootstrap — lives once in `tests/helpers/lambdafixture`, and each package's
`fixtures_test.go` binds it back to the short local name. Extend that package
and add a binding; never write a second copy of a helper body.

`lambdafixture.EnsureInit` is why the container half passes on a fresh
checkout: the init artefacts are build output and the embed is baked at compile
time, so that half's `TestMain` builds them once per test binary and points
`initbin.EnvDistDir` at the result. Do that from `TestMain`, never from
whichever test happens to sort first — that is what the old single package did,
and it made `go test -run TestInvoke_nodeRuntime_success` fail on a fresh
checkout with an "Unhandled" function error naming nothing.

##### What the container half requires, per platform

`lambdadocker` is expected to run everywhere, and a skip here has to name a
capability rather than a platform. There is exactly one Docker gate,
`helpers.SkipWithoutDocker`: it resolves the endpoint with
`helpers.TestDockerSocket` — `LAMBDA_DOCKER_SOCKET`, else the platform default
— and pings that daemon. **Never gate on a socket path.** A gate that stat-ed
`/var/run/docker.sock` skipped all 49 tests on every Windows machine, and one
that dialled the empty string skipped everywhere including Linux CI; both
reported green ([#1785](https://github.com/overcast-sh/overcast/issues/1785)).
`TestMain`'s image pre-pull uses the same resolution, through
`helpers.DockerAvailable`, so the daemon the suite warms is the daemon it tests.

| Platform | What runs | What does not, and why |
| --- | --- | --- |
| Linux, daemon on this kernel | everything | — |
| Windows / macOS, Docker Desktop | everything but one | the Telemetry API delivery test: its destination is a listener inside the sandbox that Overcast posts to at the container's bridge IP, and Desktop's engine is in a VM the host cannot route into. `skipIfHostCannotReachContainerIPs` |
| The suite itself in a container | everything, if the daemon's socket is mounted | a hot-reload bind mount needs a path this process and the daemon both see; `hotReloadSourceDir` uses `OVERCAST_TEST_LAMBDA_HOT_RELOAD_DIR`, else `/workspace`, else `skipIfContainerizedHotReloadBindMount` |

Two notes measured on Windows 11 with Docker Desktop 29.7.2, since both are
easy to assume the other way round. All twelve hot-reload cases **pass** there:
Desktop bind-mounts host paths, and `skipIfContainerizedHotReloadBindMount`
correctly does not fire because that condition is about *this process* being
containerised, not about the daemon being in a VM. And the image-build helper
reaches the daemon through `docker.Transport`, the emulator's own dialer — a
hand-rolled one that assumed a Unix socket failed six tests there with `dial
unix npipe://…`. Anything needing an Engine API call `docker.Client` does not
implement takes that transport; never write a second dialer in test code.

---

## Build-tag-sensitive tests — guard the test like its subject

Some surfaces only exist in some builds. The runtime MCP endpoint is the current
example: `internal/router/mcp_routes.go` is `//go:build !slim` and its slim twin
makes `registerMCPRoutes` a no-op, so under `-tags slim` there is no `/_overcast/mcp`
route and requests to it correctly fall through to S3's catch-all and get a 501.

**A test must carry the same build constraint as the surface it exercises.** An
unguarded test asserting on a non-slim-only route passes untagged and fails under
`-tags slim` — a false positive that costs whoever hits it the time to re-derive
that it isn't a real bug. AGENTS.md points agents at `-tags slim` for backend-only
verification, so that is a well-travelled path.

The same applies to `-tags nosqlite`, where `state.NewSQLiteStore` and
`state.NewHybridStore` are stubs that always error
(`internal/state/sqlite_hybrid_nosqlite.go`). The shipped `overcastd` binary and
slim Docker image are built `slim,nosqlite`, so both tags matter.

Guard the **whole file**, not the individual assertion — put the affected tests in
their own file with the constraint at the top. Precedents:
`internal/router/mcp_routes_test.go` and `internal/bff/docs_search_test.go`
(`!slim`); `internal/router/debug_hybrid_test.go`,
`internal/services/cloudwatch/metric_retention_sqlite_test.go`,
`internal/services/cloudwatch/logs/event_backend_sqlite_test.go`,
`internal/services/sqs/message_backend_sqlite_test.go` and
`internal/services/dynamodb/index_store_sqlite_test.go` (`!nosqlite`);
`tests/integration/router/mcp_test.go` (`!slim`) and
`tests/integration/router/sqlite_test.go` (`!nosqlite`). Leave a one-line pointer
in the file the tests moved out of.

The one exception is a test whose subject only *partly* disappears — see the next
section.

### Parity tests — drop the missing backend, don't tag out the test

A file-level tag is wrong when only *part* of a test's subject disappears.
Memory-vs-SQL parity tests build both backends inside one test function and run
identical assertions against each; tagging the file out under `-tags nosqlite`
would silently drop the memory-side coverage too, even though it still works
there.

Instead, have the fixture return **only the backends this build has** and let
the test iterate over whatever it gets. Use `config.SQLiteSupported()` — it
exists for exactly this and needs no build tag on the test file:

```go
func newTestBackends(t *testing.T) map[string]eventBackend {
	backends := map[string]eventBackend{"memory": newMemEventBackend()}
	if !config.SQLiteSupported() {
		return backends
	}
	// ... build the SQLite-backed one ...
	backends["sql"] = sqlBackend
	return backends
}

for name, b := range newTestBackends(t) {
	t.Run(name, func(t *testing.T) { /* same assertions for every backend */ })
}
```

Under `nosqlite` the suite then runs the `memory` subtest and skips `sql`, rather
than failing or vanishing. The worked examples are
`internal/services/cloudwatch/logs/event_backend_test.go` (`newTestBackends`),
`internal/services/sqs/message_backend_test.go` (`newTestMessageBackends`) and
`internal/services/dynamodb/item_store_test.go` (`newTestItemBackends`, shared
with `index_store_test.go`) — plus two fixtures that do the same one layer up,
returning whole stores rather than bare backends:
`internal/services/cloudwatch/logs/retention_test.go` (`newTestLogsStores`) and
`internal/services/dynamodb/handler_transact_index_test.go`
(`newTestDynamoStores`).

Tests that are *wholly* about SQLite — "does this select `sqlEventBackend`?",
"does the hybrid backend physically delete the row?", "does `*sql.Tx` roll the
write back?" — still belong in their own `!nosqlite` file; there is no memory
half to preserve.

When you add a build-tagged file or a backend-map fixture, verify all three ways
before finishing:

```sh
go test -count=1 ./tests/integration/foo/
go test -count=1 -tags slim ./tests/integration/foo/
go test -count=1 -tags slim,nosqlite ./tests/integration/foo/
```

CI runs the whole suite under both tag sets — the `build-tags` matrix job in
`.github/workflows/test.yml` covers `slim` and `slim,nosqlite`, the latter being
what the released `overcastd` binary and slim Docker image are actually built
with — so an unguarded tag-sensitive test fails there.

---

## Coverage requirements

| Layer                            | Target coverage                                          |
| -------------------------------- | -------------------------------------------------------- |
| `internal/protocol/`             | 100% — these are the AWS wire format contracts           |
| `internal/state/`                | 100% — both MemoryStore and SQLiteStore, same test suite |
| `internal/config/`               | 100% — all env var parsing paths                         |
| `internal/services/*/store.go`   | 100% — all domain model operations                       |
| `internal/services/*/handler.go` | ≥ 90% — all happy paths + key error paths                |
| `internal/middleware/`           | ≥ 90%                                                    |

Check coverage: `make test-coverage` → opens `coverage.html`

---

## Shared helpers — always extract, never duplicate

If the same setup pattern appears in more than one test, extract it to a helper.
Helpers live in one of two places:

1. **`tests/helpers/`** — shared across all service tests (TestServer, assertions)
2. **Local to the test file** — unexported helpers used only within one `_test.go` file

### When to use `tests/helpers/`

- `helpers.NewTestServer(t, opts...)` — always use this, never construct manually
- `helpers.AssertStatus(t, resp, code)` — always use this, never `t.Error` inline
- `helpers.AssertRequestID(t, resp)` — verify AWS request ID header is present
- `helpers.AssertJSONError(t, resp, code)` — decode and check JSON error code
- `helpers.AssertXMLError(t, resp, code)` — decode and check XML error code
- `helpers.DecodeJSON(t, resp, &v)` — decode response body, fail on error
- `helpers.DecodeXML(t, resp, &v)` — decode XML response body, fail on error
- `helpers.ReadBody(t, resp)` — read response body as string

### Local helpers (file-scoped)

Setup helpers specific to one service's tests are defined in the same file,
unexported, and named after what they create:

```go
// Good — named after what it creates, takes testing.T + server + params
func createBucket(t *testing.T, srv *helpers.TestServer, name string) { ... }
func putObject(t *testing.T, srv *helpers.TestServer, bucket, key string, body []byte, ct string) { ... }
func createQueue(t *testing.T, srv *helpers.TestServer, name string) string { ... }

// Bad — too generic, hides intent
func setup(t *testing.T) { ... }
func doRequest(t *testing.T, ...) { ... }
```

---

## Mocks

Use mocks to test components in isolation when the real dependency is:

- An external service (not our emulated services — those we test for real)
- Slow to set up
- Non-deterministic

We use the standard Go mock pattern — interfaces with hand-written test doubles.
Do **not** use code-generation mock libraries (mockery, gomock) in this project
to keep the dependency footprint small. Hand-written fakes are simpler and easier
to read.

### Mock pattern example

```go
// In the test file or a _test.go helper:

// mockStore is a fake state.Store for unit tests.
// Only implement the methods your test actually calls.
type mockStore struct {
    data map[string]string
    // Record calls for assertion:
    setCalls []setCall
}

type setCall struct{ namespace, key, value string }

func (m *mockStore) Get(_ context.Context, ns, key string) (string, bool, error) {
    v, ok := m.data[ns+"\x00"+key]
    return v, ok, nil
}

func (m *mockStore) Set(_ context.Context, ns, key, value string) error {
    m.data[ns+"\x00"+key] = value
    m.setCalls = append(m.setCalls, setCall{ns, key, value})
    return nil
}

// Implement remaining interface methods as no-ops if not needed:
func (m *mockStore) Delete(_ context.Context, _, _ string) error  { return nil }
func (m *mockStore) List(_ context.Context, _, _ string) ([]string, error) { return nil, nil }
func (m *mockStore) Close() error                                  { return nil }
```

### When NOT to mock

- Do not mock `state.Store` in integration tests — use `helpers.NewTestServer(t)`
  which provides a real `MemoryStore`. Integration tests must exercise the real stack.
- Do not mock HTTP handlers — test them through the real router.

---

## Table-driven tests

Use table-driven tests when the same logic needs many input/output pairs:

```go
func TestValidateBucketName(t *testing.T) {
    // Given: various bucket name inputs
    cases := []struct {
        name        string
        input       string
        expectError bool
    }{
        // When + Then combined in the table:
        {name: "valid name",    input: "my-bucket",    expectError: false},
        {name: "too short",     input: "ab",           expectError: true},
        {name: "too long",      input: strings.Repeat("a", 64), expectError: true},
        {name: "with numbers",  input: "bucket123",    expectError: false},
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            // When: we validate the name
            err := validateBucketName(tc.input)

            // Then: error presence matches expectation
            if tc.expectError && err == nil {
                t.Errorf("expected error for input %q, got nil", tc.input)
            }
            if !tc.expectError && err != nil {
                t.Errorf("expected no error for input %q, got: %v", tc.input, err)
            }
        })
    }
}
```

---

## Test isolation

Every test gets a fresh server with empty state:

```go
srv := helpers.NewTestServer(t)   // fresh MemoryStore, no state
```

`t.Cleanup(srv.Close)` is registered automatically. You do not need to close
the server manually.

**Never share state between tests.** Never use `TestMain` to pre-populate state
for all tests in a package. Each test must arrange its own state in the Given section.

---

## Race condition tests

Run all tests with `-race`: `make test` (not `make test-unit`).

Tests that specifically verify concurrent behaviour use goroutines explicitly:

```go
func TestMemoryStore_concurrentAccess(t *testing.T) {
    // Given: a store
    s := state.NewMemoryStore()
    ctx := context.Background()

    // When: 50 goroutines read and write concurrently
    done := make(chan struct{}, 50)
    for i := 0; i < 50; i++ {
        go func() {
            s.Set(ctx, "ns", "key", "value")
            s.Get(ctx, "ns", "key")
            done <- struct{}{}
        }()
    }
    for i := 0; i < 50; i++ {
        <-done
    }
    // Then: no data race (enforced by -race flag, no explicit assertion needed)
}
```

---

## Mock clocks — advancing time is not the same as waiting for it

Tests drive `clock.Mock` rather than sleeping. But `mock.Add(d)` does **not**
wait for the `AfterFunc` callbacks it fires: the mock runs each on a goroutine
of its own and sleeps a single millisecond before returning. So this is a race,
not a sequence:

```go
clk.Add(time.Second)              // fires the PROVISIONING → RUNNING transition
got, _ := h.store.getTask(ctx, …) // may run before the callback does
```

It passes on an idle machine and fails on a loaded one — a flake that only ever
reproduces in CI, and one that looks like the callback's work being broken
rather than not having happened yet. `TestTaskTransition_nonDefaultRegion` lost
several PRs' CI runs to exactly that, failing in 0.01 s with the task still
`PROVISIONING`, which reads convincingly as the region-scoping bug the test was
written for.

For anything scheduled through `lifecycle.Scheduler`, advance with
`scheduler.AdvanceAndSettle(clk, d)` — it waits for every transition that came
due. `scheduler.Settle()` is the real-clock counterpart: it waits for what is
pending without cancelling it. Elsewhere, wait on something the callback itself
signals. Never on a sleep, and never by polling until the state shows up.

**A mock clock also serialises callbacks**, firing due timers one at a time with
that millisecond between them. That makes it the wrong tool for reproducing a
race *between* two transitions — on an idle machine each callback finishes
before the next starts. `internal/services/ecs/service_steady_state_race_test.go`
is the worked example of the exception: it runs on a real clock, and says why at
the top of the file.

### Wind the clock before the tickers exist

That millisecond is also why **how far you move a mock clock matters as much as
when**. `Set`/`Add` replays every interval it passes over — each due timer *and
every ticker tick* between the old time and the new one, a millisecond apiece.
Against a subject that has already started a ticker-driven loop, a long jump is
effectively unbounded: winding a fresh `clock.NewMock()` from its 1970 epoch to
a date in 2027 with a 30-second sweep ticker registered is tens of millions of
ticks, and the package times out rather than failing.

```go
h, _ := lifecycleTestHandler(t)          // starts the InstancePool and instanceTracker sweep loops
h.clk.(*clock.Mock).Set(someFutureDate)  // never returns
```

Build the clock, wind it, *then* construct the subject —
`lifecycleTestHandlerAt` in `internal/services/lambda/instance_lifecycle_test.go`
is the worked example, and a helper that takes the time is the right shape when
several tests need different dates. Do **not** reach for a cut-down subject with
no tickers instead: the pool and tracker are usually on the path under test, so
that trades a hang for lost coverage.

Recognising it matters, because it presents as anything but its cause: a package
timeout rather than a test failure, a goroutine dump full of idle `select`s, and
local runs that pass because they happen never to reach the test. **If a package
times out with a goroutine parked in `clock.(*Mock).Set` → `runNextTimer` →
`Tick` → `gosched`, this is it** — not the load on the machine. A failure that
passes on re-run has not been shown to be a flake; it has only been shown not to
be deterministic *in the runs you did*.

---

## Test for error responses specifically

Always verify:

1. The HTTP status code
2. The error code in the response body (not just the message — messages can change)
3. The request ID header is present

```go
// Then: we get a well-formed AWS error response
helpers.AssertStatus(t, resp, http.StatusNotFound)
helpers.AssertXMLError(t, resp, "NoSuchBucket")   // checks the Code field
helpers.AssertRequestID(t, resp)                   // checks x-amz-request-id header
```

---

## Adding tests for a new service

1. Create `tests/integration/<service>/<service>_test.go`
2. Package name: `package <service>_test` (black-box — tests via HTTP only)
3. Write P1 tests in GWT form, failing first
4. Extract setup helpers at the bottom of the file (unexported, file-scoped)
5. Run `make test-integration` to confirm they fail before implementing

Template for the first test in a new service file:

```go
package myservice_test

import (
    "net/http"
    "testing"

    "github.com/overcast-sh/overcast/tests/helpers"
)

// ---- CreateThing -----------------------------------------------------------

func TestCreateThing_success(t *testing.T) {
    // Given: a running server
    srv := helpers.NewTestServer(t)

    // When: we create a thing
    resp := serviceCall(t, srv, "CreateThing", map[string]any{
        "Name": "my-thing",
    })
    defer resp.Body.Close()

    // Then: it succeeds and returns the thing's identifier
    helpers.AssertStatus(t, resp, http.StatusOK)
    helpers.AssertRequestID(t, resp)
    // ... decode and assert response body
}

// ---- Test helpers ----------------------------------------------------------

func serviceCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
    t.Helper()
    // ... build and send the request
}
```
