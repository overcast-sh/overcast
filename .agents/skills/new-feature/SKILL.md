---
name: new-feature
description: "Add new endpoints, services, or features to the Overcast codebase with full TDD, AWS wire fidelity, CloudFormation integration, docs, and web UI. Use when: adding a new API endpoint, implementing a stub, creating a new service, or adding a resource-creating operation. NOT for bug fixes — use the bug-fix skill."
compatibility: opencode
metadata:
  audience: contributors
  workflow: tdd
  languages: "go,typescript"
argument-hint: "Service name, endpoint name, or feature description"
license: MIT
---

# New Feature — Overcast

Add new endpoints or services with full TDD, AWS wire fidelity, CloudFormation integration, docs, and web UI. Every feature starts with a failing test and ends with `make test` passing clean.

The project's core contract is **AWS wire compatibility** — requests and responses are the compatibility contract. Internal implementation may differ freely, but wire-level inputs and outputs must be indistinguishable from real AWS. A `501` is always preferable to a `200` that silently diverges.

All coding standards are in [CONTRIBUTING.md](../../../CONTRIBUTING.md). Agent guardrails are in [AGENTS.md](../../../AGENTS.md). Test conventions are in [tests/AGENTS.md](../../../tests/AGENTS.md). Read all three before starting.

---

## Check Your Assumptions — Verify Before Acting

**Your knowledge may be stale.** AWS APIs evolve, dependencies update, and the codebase moves fast. Never assume you know the current state of anything without checking. Every assumption you don't verify is a potential implementation that silently diverges from real AWS.

### Mandatory verification before any code change

| Assumption | How to verify |
| ---------- | ------------- |
| "I know what AWS returns for this operation" | **Use the escalation strategy below.** Start with AWS docs, then existing Overcast tests, then other emulators. Real AWS is a last resort that **requires user permission.** Never guess. |
| "I know the AWS API version / protocol for this service" | Check `docs/services/<service>.md` and its `operations.md`, the real AWS API reference, and the service's existing codec/dispatch pattern. Protocol (Query, JSON 1.1, REST JSON, REST XML) determines the codec, dispatch, error helpers, and CF internal handler. |
| "This library/utility is available" | Search imports in sibling services. Check `go.mod` for Go, `package.json` for TypeScript. Never add a dependency without confirming it's not already available under a different import path. |
| "This is how existing services implement this pattern" | Read the actual code of the nearest siblings (ECR for JSON-target new services, SSM for REST-path, SQS for typed dispatch + legacy coexistence). Copy the pattern, don't recall it from memory. |
| "The state.Store interface supports what I need" | Read `internal/state/store.go`. Both `MemoryStore` and `SQLiteStore` must be updated for any interface change. |
| "The web UI already has an SDK client for this service" | Check `web/src/services/aws-clients.ts`. If it exists, use it. If not, add a factory. Never write ad-hoc `fetch` calls for AWS endpoints. |
| "CloudFormation handler is already registered" | Search `resourceHandlers` in `internal/services/cloudformation/provisioner.go` for the exact resource type string (`AWS::<Service>::<Resource>`). |
| "The doc has sentinel markers" | `make docs` appends them if a landing page has none, but check they sit above `## Related` — that is where `make docs-check` expects the generated block. |
| "This route won't fallthrough to S3" | For REST-path services: confirm the route is in `RegisterRoutes` AND the path prefix is in `detectService` (`internal/middleware/logger.go`). Test with a real request — don't assume routing works. |
| "I know the latest version of this dependency / tool" | Check `go.mod`, `package.json`, npm registry, or the official source. Don't recommend APIs, CLI flags, or patterns from outdated versions. |

### Recognising a fidelity decision — AWS is the tie-breaker

The table above catches assumptions you know you are making. The escalation strategy below tells
you how to check one. Neither fires if you never notice there was a question — and that is how
most divergence gets in.

While designing, watch for the moment **two or more options both look fine**: what to name a
derived resource or auto-created child, which default to use for a field the caller omitted,
whether an operation on a missing resource 404s or succeeds silently, whether an empty result is
an omitted field or an empty list, what order side effects fire in, which validation runs first.
These do not feel like AWS questions. Every one of them is observable by a client, which makes
every one of them part of the contract.

**Do not settle these on taste, on convenience, or on what the surrounding code happens to do.
Ask what AWS does — in this service, in this exact case — and follow it.** Sibling services are
evidence about house style, not about AWS: S3 and DynamoDB genuinely disagree, and copying the
wrong sibling produces confident, consistent, wrong behaviour. Route the question through the
escalation strategy below; tier 1 or 2 answers most of them in under a minute.

When the evidence runs out and you still have to pick, break the tie toward **failing locally**.
An emulator that accepts what AWS rejects — a missing required field, an out-of-range value, a
CloudFormation property we ignore — hands the user a stack that deploys here and rolls back in
their account, which is the one failure this project exists to prevent. Too strict is also wrong,
but the user sees it immediately and can tell us.

If a fork was genuinely load-bearing — you had to pick and the evidence was thin — record it in
the PR body so a reviewer sees the choice instead of reverse-engineering it.

### AWS behaviour verification — escalation strategy

When you need to confirm how AWS behaves, escalate through these sources in order. Each tier costs progressively more in time, money, or effort. Stop as soon as you have a definitive answer.

> **Hard rule: Tier 4 (real AWS) requires explicit user permission.** Never spin up real AWS resources without asking first — even in autopilot mode. The user must explicitly consent. This is a cost and safety boundary. If you reach tier 4 and the user hasn't pre-approved it, **stop and ask.**

| Tier | Source | Cost | When to use | What you get |
| ---- | ------ | ---- | ----------- | ------------ |
| **1. AWS docs** | Official AWS API Reference, service developer guides | Free, seconds | Always start here | Request/response schemas, error codes, field descriptions, validation rules. Many docs include example responses with real field names and casing. |
| **2. Existing Overcast code** | Handler implementations, integration tests, compat suites | Free, seconds | When the operation is already partially implemented | Production request/response shapes already tested against real AWS. `tests/integration/<service>/` and `compat/suites/` are validated wire-level expectations. **Trust but verify — cross-reference with AWS docs (tier 1). Existing code can be wrong too.** |
| **3. Other emulators** | LocalStack, Moto (Python), MinStack, Flick (Go) | Free, minutes | When docs are ambiguous or missing edge cases | These projects have already done the research. Their test fixtures and handler logic encode real AWS behaviour. Cross-reference, don't blindly copy. |
| **4. Real AWS** *(requires user permission)* | Spin up a real resource, test with `aws --debug` | ~$0.01–$1.00, 2–10 min | Last resort — when all cheaper tiers are exhausted and the user consents | Definitive wire-level truth. **Annotate the code with a comment linking to the evidence so future agents can skip re-researching (see below).** |

**Decision rule:** If tier N gives a clear, unambiguous answer from an authoritative source, stop — don't escalate further. Only escalate when:
- Docs are silent or contradictory on the specific edge case
- Existing Overcast code doesn't cover this scenario
- Emulators disagree with each other (or all implement it differently from what seems right)
- You're implementing a brand-new operation with no prior art anywhere
- **AND (for tier 4) the user has explicitly said yes**

### Annotate verified behaviour — shortcircuit future research

When you definitively verify how AWS behaves (especially via tier 4, but also when docs are unambiguous), **leave a comment in the code** recording the source. This means the next agent (or you, in 6 months) can skip straight to implementation without re-researching.

```go
// Verified against real AWS (2026-05-04, aws-cli 2.15.x):
//   aws ecr create-repository --repository-name test --debug
// Expected: {"repository": {"repositoryArn": "arn:aws:ecr:...", ...}}
// Note: repositoryUri is returned in create response, not just describe.
```

Or for doc-verified behaviour:

```go
// Per AWS docs (https://docs.aws.amazon.com/AmazonECR/latest/APIReference/API_CreateRepository.html):
// Returns repository object with repositoryArn, repositoryUri, registryId, and repositoryName.
// createdAt is an ISO 8601 timestamp string, not Unix epoch.
```

**When to annotate:**
- Any behaviour that was surprising or counterintuitive
- Edge cases where AWS docs are ambiguous but real AWS confirmed the answer
- Error codes and status codes that differ from what seems reasonable
- Response shapes with non-obvious field names or nesting
- Default values and validation rules not clearly stated in docs

**Format:** `// Verified against <source> (<date>): <concrete evidence>`. Include enough detail that a reader can trust the comment without repeating the verification.

### Other research protocols

1. **For third-party facts (versions, APIs, protocols):** The codebase is **not** the source of truth. Check the official source online — `go.mod` / `package.json` may be stale, and existing imports may reflect an outdated version. Look up the current stable release of any dependency before recommending it. For AWS API behaviour, use the escalation strategy above.
2. **For codebase conventions:** Read the most recently added service of the same protocol type (ECR = JSON-target, Scheduler = REST-path). New code should be indistinguishable in style from recently-merged code. The codebase *is* the source of truth for how this project does things.
3. **For dependencies:** `grep` the import in the codebase *and* check the current version online. Never add a dependency that duplicates an existing one.
4. **For store operations:** Both store implementations share the same test suite. If you add a store method, both implementations must pass.
5. **For tool output:** Treat tool results as the current truth, not cached knowledge. If a tool result conflicts with what you "know," the tool is right. Don't silently override tool output with memory.

---

## When to Use

- Adding a new AWS API endpoint to an existing service
- Implementing a stubbed handler (moving from `501` to real behaviour)
- Creating an entirely new service
- Adding a resource-creating operation that CloudFormation must provision
- Extending the web UI for a new resource type

Do NOT use this for:
- Bug fixes to existing behaviour (use the `bug-fix` skill)
- Refactoring without new functionality (use the `code-review` skill)
- Purely cosmetic or style changes

---

## Decision Tree — What Kind of Feature?

Before starting, determine the scope:

1. **Single endpoint on an existing service** → Follow [Phase 1 — Adding an Endpoint](#phase-1--adding-an-endpoint)
2. **Multiple related endpoints (e.g., CRUD for a new resource type)** → Follow [Phase 1](#phase-1--adding-an-endpoint) for each, iteratively
3. **An entirely new service** → Follow [Phase 2 — Adding a Service](#phase-2--adding-a-service), then use the endpoint flow for each operation

Both flows share the same verification, docs, and web UI phases.

---

## Phase 1 — Adding an Endpoint

### Step 1.0 — Decide the path, and get it right first

Every path Overcast serves is either a binding the pinned AWS manifest models
or lives under `/_overcast/`. There is no third category, and CI enforces it
(`TestNoRouteIsRegisteredOutsideTheNamespace`).

- **Emulating an AWS operation:** copy the `URI` from
  `internal/awsapi/manifest.gen.go`. Do not infer the path from AWS docs and do
  not invent one — an operation served on an invented binding passes every
  status check while being unreachable from any SDK, which is what #793, #815
  and #854–#860 all were.
- **Emulator-only endpoint** (admin, debug, console-facing, or the data plane
  of an emulated workload): `/_overcast/<service>/<resource>/…`.

**Never nest an invented sub-resource inside a modeled prefix.** It reads as an
AWS binding, is not one, and a `grep` for `/_` will not find it. Full rule and
rationale: [CONTRIBUTING.md § How to add an endpoint](../../../CONTRIBUTING.md#how-to-add-an-endpoint).

### Step 1.1 — Write a Failing Test (TDD)

Tests follow the **Given/When/Then** pattern. See [tests/AGENTS.md](../../../tests/AGENTS.md).

1. **Location:** `tests/integration/<service>/<service>_test.go`
2. **Naming:** `Test<Operation>_<scenario>` — scenario describes the condition, not the outcome.
3. **Form:**

   ```go
   func TestCreateThing_success(t *testing.T) {
       // Given: a running server
       srv := helpers.NewTestServer(t)

       // When: we create the resource
       resp := callService(t, srv, "CreateThing", map[string]any{
           "Name": "my-thing",
       })
       defer resp.Body.Close()

       // Then: the resource is created with correct response shape
       helpers.AssertStatus(t, resp, http.StatusOK)
       helpers.AssertRequestID(t, resp)

       var out CreateThingResponse
       helpers.DecodeJSON(t, resp, &out)
       assert.Equal(t, "my-thing", out.Name)
       // ... assert every response field matches real AWS
   }
   ```

4. **Run and confirm it fails** for the expected reason (e.g., `501 Not Implemented`).
5. **Assert the right things:** HTTP status, error code (not message), request ID header, response field names/casing/nesting, default values.

### Step 1.2 — Identify the Service Pattern

New endpoints on existing services use the **typed dispatch pattern** if available, or the **legacy pattern** for older services.

#### Typed pattern (required for new services; used in most existing services)

| File | What goes there |
| ---- | --------------- |
| `typed_logic.go` | Request/response types + codec-agnostic handler function |
| `typed_ops.go` | Register via `op.NewTyped[In, Out]("OperationName", s.handlerTyped)` |
| `service.go` | `Dispatch`/`DispatchQuery` already routes through typed dispatcher — no change needed |

Handler signature:

```go
func (s *Service) myHandler(ctx context.Context, in *MyInput) (*MyOutput, *protocol.AWSError) {
    // Validation
    if in.Name == "" {
        return nil, protocol.NewAWSError("ValidationError", "Name is required", http.StatusBadRequest)
    }
    // Store operation
    resource, err := s.store.Create(ctx, in.Name)
    if err != nil {
        return nil, protocol.Wrap(protocol.ErrInternalError, err)
    }
    // Response
    return &MyOutput{Name: resource.Name, Arn: resource.ARN}, nil
}
```

Then register in `typedOps()`:

```go
func (s *Service) typedOps() map[string]op.Operation {
    return map[string]op.Operation{
        // ...
        "CreateThing": op.NewTyped(s.createThing),
    }
}
```

#### Legacy pattern (existing pre-codec services only)

| File | What goes there |
| ---- | --------------- |
| `handler.go` | **Implemented** handler method — `func (s *Service) handleCreateThing(w http.ResponseWriter, r *http.Request)` |
| `handler_stubs.go` | Remove the `NotImplemented*` stub for this operation |
| `service.go` | Add dispatch case if needed |

**Rule:** `handler.go` must never contain a stub. When implementing, _move_ the method body from `handler_stubs.go` into `handler.go`.

### Step 1.3 — Implement the Handler

**Before writing code, verify your assumptions:**

1. Read the actual handler/stub/dispatch code you're extending — don't rely on memory.
2. Search for similar operations in sibling services — copy the established approach.
3. Check `go.mod` / `package.json` — never add a dependency without confirming it's not already available.
4. Confirm the expected wire format using the [escalation strategy](#aws-behaviour-verification--escalation-strategy) — every field name, casing, and default value must match the authoritative source. Don't assume.
5. Read both `MemoryStore` and `SQLiteStore` if adding store methods — confirm they're in sync before adding to them.

Then follow all code standards:

- **Error format by service:** S3 → XML (`protocol.WriteXMLError`). SQS, SNS, DynamoDB, Lambda → JSON (`protocol.WriteJSONError`). For async services, use `protocol.NotImplementedXML`/`protocol.NotImplementedJSON` — never bare `404`.
- **HTTP success:** Use protocol writers (`protocol.WriteXML`, `protocol.WriteQueryXML`, `protocol.WriteJSON`, `protocol.WriteAWSJSON`) — never ad-hoc `json.Marshal` + header writing.
- **Clock:** `clock.Clock` — never `time.Now()`.
- **State:** All through `state.Store`. Update both `MemoryStore` and `SQLiteStore` if adding store methods.
- **Config:** `*config.Config` — never `os.Getenv`.
- **Logging:** Structured `zap`. DEBUG for per-request detail. Never log credentials.
- **Shared utilities:** Use `serviceutil`. Check it before writing a helper.
- **Performance:** Pre-size slices, stream large data, use `strings.Builder` and `strconv`.
- **No subfolders in service packages** — all files in one flat package.

### Step 1.4 — Update Store (if needed)

If the endpoint creates, reads, or mutates data, add methods to `store.go`:

```go
func (s *Store) CreateThing(ctx context.Context, name string) (*Thing, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    // ... serialise with encoding/json ...
}
```

- JSON serialisation only in `store.go`
- Update **both** `MemoryStore` and `SQLiteStore` if the state interface changes
- Use `clock.Clock` for timestamps

### Step 1.5 — Verify Scoped

```bash
# Run the new test
go test -count=1 -run TestCreateThing ./tests/integration/<service>/

# Run full service test suite
go test -count=1 ./internal/services/<service>/... ./tests/integration/<service>/...

# Format and vet
gofmt -w ./internal/services/<service>/
go vet ./internal/services/<service>/...
```

---

## Phase 2 — Adding a Service

### Step 2.1 — Create the Package Structure

**Before creating files, verify:**

1. Read another recently-added service of the same protocol type (ECR for JSON-target, Scheduler for REST-path). Copy the pattern exactly.
2. Check `go.mod` for available dependencies — use what's already in the module graph.
3. Confirm the service name doesn't collide with an existing package in `internal/services/`.
4. Check the real AWS API reference — confirm the service's protocol, target prefix format, and API version string.

Create `internal/services/<n>/` with:

| File | Purpose |
| ---- | ------- |
| `service.go` | `Service` struct, `New`, route registration, `Dispatch`/`DispatchQuery` with codec check |
| `typed_ops.go` | `typedOps()` → `map[string]op.Operation`, `Operations()`, `SupportedProtocols()` |
| `typed_logic.go` | Codec-agnostic handlers + request/response types |
| `typed_ops_test.go` | Handler unit tests |
| `store.go` | State access, JSON serialisation |
| `capabilities_dev.go` | `//go:build dev` — operation inventory |

**Do NOT create `handler.go` or `handler_stubs.go`** — those are pre-codec artifacts. New services use the typed pattern exclusively.

### Step 2.2 — Implement `service.go`

```go
package myservice

import (
    "net/http"
    "github.com/go-chi/chi/v5"
    // ... project imports
)

type Service struct {
    cfg   *config.Config
    store *Store
    clk   clock.Clock
    log   *zap.Logger
    // typedOps lazy-initialised
}

func New(cfg *config.Config, store state.Store, clk clock.Clock, log *zap.Logger) *Service {
    return &Service{
        cfg:   cfg,
        store: newStore(store),
        clk:   clk,
        log:   log,
    }
}

func (s *Service) Name() string    { return "myservice" }
func (s *Service) Enabled() bool   { return true }

func (s *Service) RegisterRoutes(r chi.Router) {
    // Only if the service uses REST paths (e.g., Lambda, API Gateway)
    // Most JSON-target services don't need this
}
```

For **JSON-target services** (like ECS, ECR, KMS), implement `router.TargetDispatcher`:

```go
func (s *Service) TargetPrefix() string { return "MyService_20200101." }

func (s *Service) Dispatch(w http.ResponseWriter, r *http.Request, target string, body []byte) {
    if codec, ok := codec.FromContext(r.Context()); ok {
        s.dispatchTyped(w, r, target, body, codec)
        return
    }
    protocol.NotImplementedJSON(w, r)
}
```

For **Query services** (like EC2), implement `router.QueryDispatcher` with `DispatchQuery`.

For **REST-path services** (like Lambda, Scheduler), implement `router.PathPrefixService`.

### Step 2.3 — Implement `typed_ops.go`

```go
func (s *Service) typedOps() map[string]op.Operation {
    return map[string]op.Operation{
        "CreateThing": op.NewTyped(s.createThing),
        "GetThing":    op.NewTyped(s.getThing),
        "DeleteThing": op.NewTyped(s.deleteThing),
        "ListThings":  op.NewTyped(s.listThings),
    }
}
```

### Step 2.4 — Implement `typed_logic.go`

Add request/response types and handler functions. Each handler is codec-agnostic — it receives typed input and returns typed output or an `*protocol.AWSError`.

### Step 2.5 — Implement `store.go`

Domain-level data access with JSON serialisation. Follow existing store patterns.

### Step 2.6 — Register the Service

In `internal/router/router.go`, append to `allServices`:

```go
allServices := []router.Service{
    // ... existing services ...
    myservice.New(cfg, store, clk, log),
}
```

**Respect the startup budget.** `New()` must be pure field assignment — no store reads, no network I/O, no DDL. Use `sync.Once`-guarded lazy-init for anything that needs the store on first use.

### Step 2.7 — Routing Fallthrough Check

If the service uses **versioned REST paths** (e.g., `/2018-10-31/...`), add the prefix to `detectService` in `internal/middleware/logger.go`. Otherwise requests will appear as `service=s3` in logs and bypass IAM/region/SigV4 middleware.

### Step 2.8 — Write Integration Tests

Create `tests/integration/<n>/<n>_test.go`:

```go
package myservice_test

func TestCreateThing_success(t *testing.T) {
    // Given: a running server
    srv := helpers.NewTestServer(t)

    // When: we create a thing
    resp := serviceCall(t, srv, "CreateThing", map[string]any{
        "Name": "my-thing",
    })
    defer resp.Body.Close()

    // Then: it succeeds
    helpers.AssertStatus(t, resp, http.StatusOK)
    helpers.AssertRequestID(t, resp)
}
```

Write P1 tests first (create, read, list, delete). Follow GWT form.

---

## Phase 3 — CloudFormation Integration (Required)

Every resource-creating endpoint must have a CloudFormation handler. This is **not optional** — CDK users need it.

### Register in `resourceHandlers`

In `internal/services/cloudformation/provisioner.go`, add an entry:

```go
"AWS::MyService::Thing": &thingResourceHandler{},
```

### Implement the handler

Create the handler file in `internal/services/cloudformation/`. Group handlers by service: `provisioner_myservice.go`.

```go
type thingResourceHandler struct{}

func (h *thingResourceHandler) Create(ctx context.Context, cfnRouter chi.Router, cfg *config.Config,
    props map[string]interface{}, rCtx resourceContext) (physicalID string, attrs map[string]string, err error) {

    name := props["Name"].(string)
    resp := internalJSON(ctx, cfnRouter, cfg, "MyService_20200101.CreateThing", map[string]any{
        "Name": name,
    })
    // ... parse response, return physicalID (ARN or ID matching AWS format) and GetAtt attributes
}

func (h *thingResourceHandler) Delete(ctx context.Context, cfnRouter chi.Router, cfg *config.Config,
    physicalID string, rCtx resourceContext) error {
    return internalJSON(ctx, cfnRouter, cfg, "MyService_20200101.DeleteThing", map[string]any{
        "Arn": physicalID,
    })
}
```

### Dispatch helpers

| Helper | Protocol | Used by |
| ------ | -------- | ------- |
| `internalQuery` | Query/XML | EC2, IAM |
| `internalJSON` | JSON target | ECS, EventBridge, KMS, Step Functions |
| `internalRequest` | REST path/JSON | API Gateway, Lambda, S3 |

### Rules

1. **Physical IDs must match AWS format** (ARN, ID with correct prefix, URL)
2. **Return `GetAtt` attributes** so cross-resource references resolve
3. **Implement `Delete`** — stack deletion must clean up resources
4. **Stub what you can't implement yet** — use `&stubResourceHandler{}` for recognised but unsupported types
5. **Handler files live in the cloudformation package** — `provisioner_myservice.go`

---

## Phase 4 — Capabilities & Docs

### Step 4.1 — Create/Update `capabilities_dev.go`

Build tag: `//go:build dev`

```go
//go:build dev

package myservice

import "github.com/overcast-sh/overcast/internal/capabilities"

var Capabilities = []*capabilities.Capability{
    {Operation: "CreateThing", Status: capabilities.StatusSupported, Tier: "core"},
    {Operation: "GetThing", Status: capabilities.StatusSupported, Tier: "core"},
    {Operation: "DeleteThing", Status: capabilities.StatusSupported, Tier: "core"},
    {Operation: "ListThings", Status: capabilities.StatusSupported, Tier: "core"},
    // ... all other operations
}
```

### Step 4.2 — Regenerate Metadata

```bash
make generate-caps   # regenerates internal/capabilities/all.gen.go
make docs            # writes docs/services/<service>/operations.md + the landing stub
make check-caps      # verifies dispatcher entries have matching capabilities
```

### Step 4.3 — Create Service Doc (new services only)

Create `docs/services/<n>.md` following `docs/dev/service-doc-template.md`:

```markdown
# <Service Name> — <AWS product name>

<One sentence.>

**Status:** ✅ Supported

## Quick start

<The smallest working command.>

<!-- BEGIN overcast:capabilities -->
<!-- END overcast:capabilities -->

## Related

- <Links out.>
```

Run `make docs`: it fills the `## Operations` stub between the markers and
writes the per-operation tables to `docs/services/<n>/operations.md`. **Never
edit either by hand.** `make docs-check` fails on a landing page that is
missing `## Quick start`, `## Operations` or `## Related`, or that puts them
out of order.

### Step 4.4 — Add a changelog fragment

Add `.changelog/YYYYMMDD-<slug>.md` (never edit `CHANGELOG.md`'s `[Unreleased]` — see `.changelog/README.md`):

```markdown
---
section: Added
area: myservice
---

- **MyService** — `CreateThing`, `GetThing`, `DeleteThing`, `ListThings` endpoints (#123)
```

---

## Phase 5 — Web UI

Web UI must not be an afterthought. Most CRUD-style services benefit from a management console page. Internal plumbing services (STS, IAM enforcement) usually don't.

### What to add

| Resource type | Action |
| ------------- | ------ |
| **New service (CRUD)** | All items below |
| **New resource type in existing service** | Pages, topology, SSE, search contributor |

### Step 5.1 — Service Registry Entry

In `web/src/lib/service-registry.ts`, add to `SERVICES`:

```ts
{
  label: "My Service",
  to: "/myservice",
  category: "storage",  // choose from existing categories
  description: "Manage MyService resources",
  dashboardDescription: "Create, list, and delete MyService things",
  docKey: "myservice",
}
```

Fields:
- `to`, `category`, `description` — required for sidebar navigation
- `dashboardDescription` — longer card description (falls back to `description`)
- `dashboardLabel` — alternate dashboard label (falls back to `label`)
- `docKey` — enables the docs button on dashboard cards
- `nav: false` — omit from sidebar but show dashboard card
- `dashboardCard: false` — omit from dashboard but show in sidebar

**`nav-services.ts` and `dashboard.tsx` derive from this automatically** — no separate registrations needed.

### Step 5.2 — Create Pages

Follow an existing service (SSM or KMS) as a template:

- `web/src/routes/<n>/` — route files (auto-generates `routeTree.gen.ts` — never edit by hand)
- `web/src/features/<n>/components/` — list, detail, create/edit components
- `web/src/features/<n>/data.ts` — TanStack Query options for API calls

Every service list page **must** include `ServiceDocsButton` in `PageHeader` actions:

```tsx
const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

<ServiceDocsButton
  service="myservice"
  label="My Service"
  open={docsOpen}
  onOpen={openDocs}
  onClose={closeDocs}
/>
```

API access: Use AWS SDK clients from `web/src/services/aws-clients.ts`. Use `fetch` only for Overcast-specific `/_*` internal endpoints.

### Step 5.3 — Topology Map (Backend + Frontend)

The system map is Overcast's most important developer surface. It is not just a status dashboard — it is a **graph-based workspace** for building, testing, and iterating on stacks. Developers use it to visualize what's connected, observe events in real time, and interact with resources without leaving the graph.

A new resource type needs three things to be useful on the map:

#### 5.3.1 — Backend topology nodes and edges

Implement `topology.Contributor` on the service: a `ContributeTopology(ctx, g *topology.Graph) error` method in the service's own `topology.go`. The router collects every service that implements it. In it, read only your own state (`serviceutil.ScanRegions` over your namespace) and add:

1. **A node** per resource (`g.AddNode`), with ID `topology.NodeID(region, kind, name)`, registered under `topology.CFN(region, resourceType, physicalID)` so CloudFormation stacks own it
2. **State-driven visual counts** — e.g., a queue node shows message counts. A per-service field on `topology.Node` needs `make generate-ts`
3. **Links** (`g.AddLink`) for the relationships your state records, naming the other end by `topology.ID(...)` (or `topology.Image(...)` for a container image). Never read another service's state to draw an edge; `topology.Build` resolves the refs

Follow `internal/services/sqs/topology.go` and `internal/services/lambda/topology.go` as templates. Services not yet migrated still live in `internal/router/topology.go`'s legacy contributor (#2090).

#### 5.3.2 — SSE event emission (backend)

In your service handler, emit SSE events on resource lifecycle changes. Events flow through `internal/events/` to the SSE stream consumed by the frontend.

```go
// Emit an event when a resource is created/deleted/mutated
s.events.Emit(ctx, "myservice.ThingCreated", map[string]interface{}{
    "thingName": thing.Name,
    "region":    cfg.Region,
})
```

Event naming convention: `<service>.<Resource><Action>` (e.g., `sqs.MessageSent`, `lambda.FunctionCreated`, `dynamodb.ItemMutated`).

#### 5.3.3 — Frontend event types

Register new event types in `web/src/services/event-types.ts`:

```ts
export const EventType = {
  // ... existing services ...
  myservice: {
    ThingCreated: "myservice.ThingCreated" as const,
    ThingDeleted: "myservice.ThingDeleted" as const,
    ThingUpdated: "myservice.ThingUpdated" as const,
  },
}
```

#### 5.3.4 — Frontend SSE cache invalidation

In `web/src/hooks/use-event-stream.ts`, inside the `sseInvalidationMap`, map each event type to the TanStack Query key prefixes it should invalidate:

```ts
[EventType.myservice.ThingCreated]: [thingsKeys.all(), topologyKey],
[EventType.myservice.ThingDeleted]: [thingsKeys.all(), topologyKey],
[EventType.myservice.ThingUpdated]: [thingsKeys.all()],
```

Include `topologyKey` for any event that adds/removes a node or edge from the map.

#### 5.3.5 — Frontend event animations (map)

In `web/src/features/map/use-event-animations.ts`, register animation behaviour for each event type:

```ts
[EventType.myservice.ThingCreated]: {
  sourceNode: thingSourceNode,
  isWrite: true,
},
```

This enables edge glows, write flashes, and node pulses when events fire. Look at `snsTopicSource`, `sqsQueueSource`, `fieldSourceNode` for the pattern.

#### 5.3.6 — Topology map node design (QOL)

> The map is a developer workspace, not a read-only status board. Each node should help a developer explore, test, and iterate.

When designing nodes, follow these principles (from [CONTRIBUTING.md § Topology map methodology](../../../CONTRIBUTING.md#topology-map-methodology)):

**Observability over literal timing:**
- Dilate fast transitions so they're perceivable — ghost rows for recently deleted items, short dwell windows for in-flight states, client-side decay for draining states
- Never slow states that are already human-visible
- Preserve sequencing even when dilated — right order, readable timing

**One visual-state model per node type:**
- Counts, badges, row states, pulses, and ghost rows for the same resource must derive from the same visual logic
- The header, embedded list, and animation must tell the same story

**QOL action decision framework.** For each action, ask:
- What's the first thing a developer wants to do with this resource on the map?
- Can it be done safely without leaving the graph?
- Is the action common enough to justify persistent node space?
- Does each surfaced detail earn its place in the tight node UI?

**Prefer direct, contextual actions over navigation** for frequent, fast-feedback operations. Examples:
- SQS nodes: send-message and queue-peek on-node
- SNS nodes: publish-on-node (testing fan-out)
- Lambda nodes: test invoke and recent-log access
- Log group nodes: stream activity dots with a short recent-activity window

**Keep the AWS API truthful.** Time dilation and QOL actions live only in the map UI and emulator-specific observability surfaces. Never alter AWS API behaviour or backend state timing to satisfy the map.

#### 5.3.7 — Topology node rendering

In `web/src/features/map/topology-nodes.tsx`, add node rendering for the new resource type. Follow the patterns in existing node handlers (SQS `message_events`, Lambda `lambda_instances`, etc.):

- Subscribe to SSE events via `useEventStream`
- Maintain transient visual state (ghost rows, in-flight messages, recent activity)
- Derive node badges, counters, and row states from a single visual-state model
- Expose on-node QOL actions where they pass the decision framework above

Also add the node type to `web/src/features/map/topology-nodes.test.tsx` with tests.

### Step 5.4 — Global Search Contributor

Create `web/src/lib/search-contributors/<service>.ts`:

```ts
import { myService } from "@/services/api";
import { createSearchContributor } from "./create-contributor";
import type { MyResource } from "@aws-sdk/client-my-service";

createSearchContributor<MyResource>({
  id: "myservice",
  cacheKey: (ep) => [ep.baseUrl, ep.region, "myservice", "resources"] as const,
  fetchAll: () => myService.listResources(),
  matchFields: (r) => [r.name, r.arn],
  toResult: (r) => ({
    id: `myservice:${r.name}`,
    label: r.name ?? "",
    sublabel: r.arn,
    service: "My Service",
    serviceKey: "/myservice",
    type: "Resource",
    href: `/myservice/${encodeURIComponent(r.name ?? "")}`,
  }),
});
```

Import it in `web/src/lib/search-contributors/index.ts`.

The `cacheKey` **must** include `ep.baseUrl` and `ep.region` as the first two elements.

### Step 5.5 — TypeScript Check

```bash
(cd web && pnpm run typecheck)
```

---

## Phase 6 — Compat Tests

**Ask first: are there missing compat tests related to this feature?** Check
`compat/suites/registry.json` for the operations the feature adds or changes.
Three cases:

1. **The operations are not in the registry** (a new service, or new
   operations on an existing one): add the group/tests to the registry FIRST
   — it is the single source of truth in both directions, and the parity gate
   fails any suite result the registry does not know. Then implement the
   tests in **every SDK/CLI suite**, or record the gap per suite in
   `compat/parity-debt.json` (the debt file only shrinks; the checker fails
   on undeclared or stale entries). Not just node-js-sdk — the uniformity
   policy in [compat/AGENTS.md](../../../compat/AGENTS.md#baseline--uniformity-policy)
   applies from the first test.
2. **The operations are registered but recorded `unimplemented` in
   `compat/baseline/<suite>.json`**: your feature should flip them. Run the affected
   suites locally (`go run ./cmd/compat --suite <suite>`) and confirm
   unimplemented → pass; baseline auto-promotion records the improvement on
   merge. If they still fail after the feature, the feature is not done — a
   `fail` is a wire-compatibility defect, not a coverage note.
3. **The operations are registered and passing**: confirm your change did not
   regress them (`make -C compat baseline-check` against a local run, and
   `make -C compat failures-check` — the baseline is at zero failures, so any
   failing test fails CI whatever the baseline records).

Isolation rules for any new compat test: resource names embed the group token
(`{runId}-<group>-…`), never another group's resources, exact-name teardowns —
groups run in 8 parallel slots and violations surface as intermittent
created-then-not-found failures (#388).

---

## Phase 7 — Final Verification

```bash
# Widen build
go build ./...
go vet ./...

# Full test suite with race detector
make test

# Lint
make lint

# Cross-platform build (if platform-specific code added)
GOOS=linux   GOARCH=amd64 go build ./...
GOOS=darwin  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...

# Docs check
make check-caps
make docs-check

# Web UI build
(cd web && pnpm run typecheck)
(cd web && pnpm run build)
```

---

---

## What Agents Must NOT Do During Feature Work

- Never implement a feature without a failing test first — TDD is mandatory
- Never spin up real AWS resources without explicit user permission — even in autopilot mode. Stop and ask at tier 4 of the escalation strategy. The user must consent.
- Never create non-AWS endpoints or custom response fields — the AWS SDK must work unmodified
- Never change wire formats without tests — request/response shapes are the compatibility contract
- Never return bare `404` — unimplemented operations must return `501`
- Never update only one store implementation — `MemoryStore` and `SQLiteStore` must stay in sync
- Never call `os.Getenv` in service code — use `*config.Config`
- Never use `time.Now()` — use `clock.Clock`
- Never bypass `serviceutil` / duplicate helper logic — DRY across services
- Never manually edit auto-generated doc tables — use `make docs`
- Never edit `web/src/routeTree.gen.ts` — it is auto-generated
- Never do blocking work in `New()` or `Init*` — use `sync.Once` lazy-init
- Never skip CloudFormation handlers — every resource-creating endpoint needs a `resourceHandlers` entry
- Never leave the workspace in a broken state — run `go build ./...` and `go vet ./...` before finishing

## Common Pitfalls

### Forgetting `detectService` for REST-path services
New services with versioned REST paths need a prefix entry in `internal/middleware/logger.go`. Without it, logs show `service=s3` and IAM/region middleware is skipped.

### Routing bugs cause S3 fallthrough
A typo in a route path, missing `RegisterRoutes` entry, or misnamed `chi.URLParam` causes requests to miss their handler and land in S3 with 404/501. Confirm the route matches before adding to `detectService`.

### Updating only one store implementation
`MemoryStore` and `SQLiteStore` must stay in sync. Every new store method needs both implementations. Tests run against both.

### Skipping CloudFormation handlers
Every resource-creating endpoint needs an entry in `resourceHandlers`. CDK stacks will fail without it. At minimum, use `&stubResourceHandler{}`.

### Manual doc table edits
Everything between `<!-- BEGIN overcast:capabilities -->` and `<!-- END overcast:capabilities -->` is auto-generated. Never edit by hand. Use `make docs`.

### Blocking work in `New()`
No store reads, network I/O, DDL, or file reads in service constructors. Use `sync.Once` lazy-init pattern. This is enforced — slow startup breaks CI pipelines.

### Creating non-AWS endpoints
Never invent custom fields in AWS responses. The AWS SDK must work unmodified. Emulator-only features go on `/_` prefixed internal endpoints.

### Using `time.Now()` instead of `clock.Clock`
`clock.Clock` is injectable and testable. `time.Now()` is neither. Always use the injected clock.

### Duplicating utility logic
Check `serviceutil` before writing any parsing, validation, pagination, or logging helper. If a pattern appears in two services, extract it.

---

## Quick Reference

```bash
# New endpoint — TDD cycle
go test -count=1 -run TestNewOp ./tests/integration/<service>/   # fail
# ... implement ...
go test -count=1 ./internal/services/<service>/... ./tests/integration/<service>/...  # pass
make generate-caps && make docs && make check-caps                 # update capabilities
(cd web && pnpm run typecheck)                                     # UI check

# Final gate
go build ./... && go vet ./...
make test && make lint
make docs-check
```
