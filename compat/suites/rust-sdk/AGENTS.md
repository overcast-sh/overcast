# AGENTS.md — rust-sdk suite

> Conventions for AI agents and contributors working in
> `compat/suites/rust-sdk/`.
>
> **Read [compat/AGENTS.md](../../AGENTS.md) first** — it contains the
> canonical teardown rules, the separation boundary, and the `group:test`
> implementation-key rules that apply to every suite.
> This file covers only rust-sdk-specific details.
>
> For quick-start, prerequisites, env vars and architecture see
> [README.md](README.md).

---

## What this suite tests

Core AWS service operations reachable via the **AWS SDK for Rust**. It is the
Rust column of the compatibility matrix. Failures on unimplemented services
are correct and expected — they are the coverage gap metric, not bugs to
silence.

Unlike the suites that aim at full parity with `node-js-sdk`, this one covers
ten services on purpose. Every other registry test still runs and reports a
`skip`, so the narrower scope is visible in the results rather than hidden.

---

## Status

**Implemented.** `all_service_groups` (in `src/main.rs`) wires ten group
structs: S3, SQS, DynamoDB, SNS, Lambda, STS, KMS, Secrets Manager, SSM and
EventBridge. It runs in the compat CI matrix
(`.github/workflows/compat.yml`), where a dedicated `rust-sdk-image` job
builds and publishes its image.

It is also a **source-emitting** backend for the generated groups: `cmd/compatgen`
writes one Rust function per scenario test into `src/groups/scenarios_*_gen.rs`,
and `src/scenario/` is the hand-written runtime they call into. See
[Generated groups](#generated-groups-and-the-scenariobackend-hook).

Widening coverage means adding a group struct and its `aws-sdk-*` crate — not
building the suite from scratch. Start from the code that is here.

---

## Runtime

| Item       | Value                                                                                          |
| ---------- | ------------------------------------------------------------------------------------------------ |
| Language   | Rust, edition 2021, async on Tokio                                                              |
| AWS client | `aws-sdk-*` crates pinned exactly (`=1.x.y` in `Cargo.toml`), one per service                    |
| Errors     | `Result<(), String>` throughout — **no `anyhow`**; SDK errors go through `harness::sdk_error`     |
| CI image   | `rust:1.94.1-slim-bookworm` build stage → `debian:bookworm-slim` runtime (see `Dockerfile`)      |
| Profile    | Debug, not release. Test-harness build speed beats runtime speed here                            |

> SDK upgrade policy: [compat/AGENTS.md § SDK version pinning](../../AGENTS.md#sdk-version-pinning--upgrade-strategy).
> `Cargo.toml` is the source of truth for every pinned version; do not restate
> one here. `Cargo.lock` is not checked in — the exact `=` pins stand in for it.

**The service crates and the smithy runtime crates are pinned from different
releases, on purpose.** `aws-sdk-organizations` has to be new enough to declare
every operation the pinned AWS model does — the generated `organizations-gen-probe`
group calls five that a crate of the other services' vintage does not have — and
that crate's own requirements pull `aws-smithy-*`, `aws-runtime` and
`aws-credential-types` forward with it. Cargo unifies them to one version each,
which is what a user's build would do too. Moving the service crates is a
separate change with a separate blast radius (every hand-written group's
results); moving the runtime crates is what keeps the generated groups
compilable.

---

## File layout

```
compat/suites/rust-sdk/
  AGENTS.md        ← you are here
  README.md        ← quick-start, prerequisites, env vars, architecture
  Cargo.toml       ← one pinned aws-sdk-* crate per service, plus tokio and serde
  Dockerfile       ← build stage runs `cargo test` before `cargo build`
  run.sh           ← resolves/builds the image and runs it; cmd/compat invokes it
  image-tag.sh     ← content hash of the Rust sources and both registry files
  BUILD_NOTES.md   ← the BuildKit cache-mount notes behind the Dockerfile

  src/
    main.rs        ← entry point and the registration tests
    clients.rs     ← AwsClients: one constructor per service over a shared SdkConfig
    harness.rs     ← TestContext, TestCase, TestGroup, run_suite, sdk_error, NDJSON
    registry.rs    ← registry loading, impl-key merge/validate, group building,
                     and the loader unit tests
    groups/
      mod.rs       ← the ServiceGroup trait and the module list
      s3.rs  sqs.rs  dynamodb.rs  sns.rs  lambda.rs
      sts.rs  kms.rs  secretsmanager.rs  ssm.rs  eventbridge.rs
      scenarios_gen.rs               ← GENERATED: the index, and the module
                                       declarations for the files below
      scenarios_<service>_gen.rs     ← GENERATED: one per scenario service
    scenario/      ← the hand-written runtime the generated groups call into
      mod.rs       ← Call/Test/Clause/Check, the constructors, the backend
      value.rs     ← the IR's value expressions, the context bag, the Binder
      json.rs      ← paths, canonical rendering, JSON equality, equalsJSON's
                     percent-decoding and document comparison
      errors.rs    ← the error surfaces this SDK has, and the matcher
      capture.rs   ← the interceptor that keeps the raw response body
      xml.rs       ← the Query/REST XML body as a document, for the wires JSON
                     parsing cannot read
      exec.rs      ← running a group, and the closed assertion set
      failure.rs   ← the six-field failure message
      tests.rs errorfixtures.rs nowfixtures.rs equalsjsonfixtures.rs
                   ← its unit tests
```

**One module per AWS service.** Never split a service across modules, and
never merge two services into one.

---

## Group anatomy

A service module exposes one struct implementing `ServiceGroup`: three maps
(`impls`, `setups`, `teardowns`) that `main` merges into the suite's
registrations at startup. Each impl is an `Arc`'d closure that clones the
`Arc<AwsClients>` it captures and returns a boxed future. This is the real
`EventBridgeGroup` (`src/groups/eventbridge.rs`), trimmed:

```rust
pub struct EventBridgeGroup {
    clients: Arc<AwsClients>,
}

impl EventBridgeGroup {
    pub fn new(clients: Arc<AwsClients>) -> Self {
        Self { clients }
    }
}

impl ServiceGroup for EventBridgeGroup {
    fn name(&self) -> &'static str {
        "eventbridge"
    }

    fn impls(&self) -> HashMap<String, TestFn> {
        let mut impls: HashMap<String, TestFn> = HashMap::new();

        let clients = self.clients.clone();
        impls.insert(
            "eventbridge-patterns:TestEventPattern".to_string(),
            Arc::new(move |ctx: TestContext| {
                let clients = clients.clone();
                Box::pin(async move {
                    let response = clients
                        .eventbridge()
                        .test_event_pattern()
                        .event_pattern(pattern)
                        .event(sample_event(&ctx))
                        .send()
                        .await
                        .map_err(crate::harness::sdk_error)?;
                    if !response.result() {
                        return Err(format!(
                            "TestEventPattern: expected Result=true for pattern {pattern}"
                        ));
                    }
                    Ok(())
                })
            }),
        );

        impls
    }

    fn setups(&self) -> HashMap<String, TestFn> { HashMap::new() }
    fn teardowns(&self) -> HashMap<String, TestFn> { HashMap::new() }
}
```

Setup and teardown are `TestFn` values too, keyed by **group name** rather
than `group:test`. Clients come from `clients.s3()`, `clients.sqs()` and so
on — never build an SDK client inside a closure.

`name()` labels the module a duplicate-key error came from, so a collision
points at the two files rather than only the key they disagree about.

---

## Key types

```rust
// src/harness.rs
pub type TestFuture = Pin<Box<dyn Future<Output = Result<(), String>> + Send>>;
pub type TestFn = Arc<dyn Fn(TestContext) -> TestFuture + Send + Sync>;

#[derive(Clone)]
pub struct TestContext {
    pub endpoint: Arc<String>,
    pub region: Arc<String>,
    pub run_id: Arc<String>,
    // plus a Mutex'd HashMap<String, String> reached through set/get
}

impl TestContext {
    pub fn set(&self, key: &str, value: String);
    pub fn get(&self, key: &str) -> Option<String>;
    pub fn log(&self, msg: &str);   // stderr only — stdout is NDJSON
}

pub struct TestCase {
    pub name: String,
    pub op: Option<String>,      // AWS operation name for doc links
    pub skip: Option<String>,    // Some(reason) emits "skip" without running
    pub depends: Vec<String>,    // same-group tests that must pass first
    pub fn_: TestFn,
}

pub struct TestGroup {
    pub suite: String,
    pub service: String,
    pub name: String,
    pub tests: Vec<TestCase>,
    pub setup: Option<TestFn>,
    pub teardown: Option<TestFn>,
}

/// Renders an SdkError with its whole source() chain. `SdkError`'s own
/// Display is the single word "service error".
pub fn sdk_error(err: impl std::error::Error + Send + Sync + 'static) -> String;
```

**The state bag holds `String` values only.** Anything else — a number, a
struct — is serialised on the way in and parsed on the way out.

---

## Naming conventions

| Element         | Convention                                                                                                                                                                 |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Impl map key    | `<group>:<Test>` — **always group-qualified**, never bare (see [compat/AGENTS.md § Implementation keys](../../AGENTS.md#implementation-keys--grouptest-and-a-bad-key-aborts-the-run)) |
| Setup/teardown key | The bare group name, e.g. `"s3-crud"`                                                                                                                                     |
| Group name      | `<service>-<feature>` (kebab-case), e.g. `s3-crud`, `sqs-fifo`                                                                                                              |
| Test name       | PascalCase AWS operation name, e.g. `CreateBucket`, `SendMessage`                                                                                                          |
| Resource prefix | `{ctx.run_id}-<short>`, e.g. `{run_id}-s3crud`                                                                                                                             |
| Module          | Lowercase service name: `s3`, `sqs`, `dynamodb`                                                                                                                            |
| Struct          | `<Service>Group`, e.g. `S3Group`, `DynamoDbGroup`                                                                                                                          |
| Context key     | snake_case string, e.g. `"bucket"`, `"queue_url"`                                                                                                                          |

---

## Inter-test state

`TestContext` is cloned per group and its bag is shared through an `Arc<Mutex<…>>`,
so a value set by one test is visible to the next one in the same group:

```rust
// in setup, or an earlier test:
ctx.set("queue_url", url.to_string());

// in a later test, or in teardown:
let url = ctx
    .get("queue_url")
    .ok_or_else(|| "queue_url not set".to_string())?;
```

Never rely on inter-group state, and never stash an SDK client — it already
lives in `AwsClients`.

---

## Teardown rules (Rust-specific)

The canonical teardown rules are in [compat/AGENTS.md](../../AGENTS.md).
Additional Rust specifics:

- Ignore teardown errors with `let _ = …` or `.ok()` — never propagate them.
  One failed delete must not abort the deletes after it.
- `ctx.get("key")` returns `None` rather than panicking when setup failed
  before setting the value; return early instead of unwrapping.
- For S3, delete every object (and every version, when the bucket is
  versioned) before `delete_bucket`.
- Map every SDK error through `crate::harness::sdk_error` on the way to a
  `String`. `SdkError`'s own `Display` is the single word "service error", and
  reporting only that is what once made nine baseline failures unactionable.
- AWS SDK Rust errors implement `ProvideErrorMetadata`, so an unimplemented
  operation can be recognised by `err.code() == Some("NotImplemented")` or an
  HTTP 501 in the raw response.

---

## Error messages

A test fails by returning `Err(String)`. Lead with the AWS operation name and
include what was expected, so the NDJSON `error` field is readable in the
dashboard without opening the source:

```rust
return Err(format!(
    "ListObjectsV2: key {key} not found in bucket {bucket} (run_id={})",
    ctx.run_id
));
```

---

## Generated groups and the `ScenarioBackend` hook

`src/registry.rs` loads `registry.json` and its machine-written sibling
`registry.generated.json` (concatenated, hand-written groups first — see
[compat/AGENTS.md § registry.json](../../AGENTS.md#registryjson--canonical-test-matrix)),
validates this suite's impl keys against the result, and builds the groups the
harness runs.

A **generated** group is a registry group `cmd/compatgen` produced from the
scenario IR. This suite executes one as **emitted source**, not as an
interpreted document: the AWS SDK for Rust takes typed request builders, so
`cmd/compatgen`'s `emit_rust.go` writes one function per scenario test —
`src/groups/scenarios_<service>_gen.rs` — and `cargo build` compiles it. The
semantics those functions call into live once, by hand, in `src/scenario/`.

The wiring has two halves, because the loader's two extension points are
different shapes:

- **Setup and teardown** join the same `setups`/`teardowns` maps the
  hand-written groups use. A hook is keyed by group name and there is one
  namespace for that.
- **Tests** resolve through `ScenarioBackend`, which `main` passes as
  `scenario::Backend` over the generated impl map. That keeps the two impl
  namespaces apart: a generated group can neither shadow nor be shadowed by a
  hand-written registration, and `merge_impls` stays a statement about the
  hand-written files.

A generated test the backend cannot resolve still reports as a hard **failure**
naming the group, not a skip — which is what
`registration_tests::generated_groups_are_registered_for_every_test_the_registry_declares`
in `src/main.rs` exists to catch a build earlier.

### The generated files are output, not source

`scenarios_gen.rs` and every `scenarios_*_gen.rs` are rewritten wholly by
`make generate-compat-model`. Two rules follow:

- **Never hand-edit one**, and never reformat one. There is no formatter in the
  emitter's path — `cmd/compatgen` is a Go program and CI's docs job carries no
  Rust toolchain — so the layout it writes is the committed layout, and running
  `cargo fmt` over the crate would make `go run -tags dev ./cmd/compatgen -check`
  fail. Format `src/scenario/` and the hand-written groups file by file instead.
- **A crate older than the pinned AWS model is a compile failure here**, not a
  generation-time refusal: the emitter derives every spelling from the model and
  has no way to ask whether the vendored crate has the operation. The Dockerfile
  builds before it runs, so it is loud, and the fix is a pin in `Cargo.toml`.

### The response document is the wire, not the SDK's output struct

`src/scenario/capture.rs` keeps the raw response body with an interceptor and
the assertions walk that. `aws-sdk-*` output types carry no `serde` derive at
the pinned versions and Rust has no reflection, so the alternative would be a
generated converter per modeled output shape — written from accessor signatures
a Go program cannot read. The two AWS JSON protocols serialize modeled member
names verbatim, so a path resolves against exactly the names the scenario file
spells. The SDK still deserializes on its own path, so a response it cannot
parse still fails the call. A REST protocol, which binds members to headers and
to the status line, would need more than this.

**An AWS Query or REST XML body goes through `src/scenario/xml.rs` first**
(#1878). Parsing XML as JSON yields `null`, so before that every response
assertion against a Query service failed while the other seven suites passed —
invisible only because every such service was Tier 0 and answered 501. The
conversion drops the root element, unwraps the `<Op>Response`/`<Op>Result`
envelope and its `ResponseMetadata`, flattens `<member>` lists (a list of one
included — the rule is by name, not by count), folds `<entry><key>/<value>`
maps to objects, and makes a repeated element name a list in document order.
An element sent with no content is `[]`, because the wire does not say whether
it is an empty string or an empty list and only `[]` satisfies both `isList`
and `nonEmpty`'s emptiness. Which conversion runs is decided by the body's own
first non-space byte, not by a protocol the emitter would have to carry into
the runtime: no JSON body begins with `<`.

Three consequences to hold on to:

- **Scalars stay text, and `equals` reads the literal's spelling.** The wire has
  no types and this crate has no model at run time. A document therefore records
  which wire it came off (`capture::Wire`) and `json::equal` compares an
  XML-derived scalar against the literal — so `equals: 30` holds against
  `<Interval>30</Interval>`, as it does in every model-backed backend, while a
  JSON document keeps the IR's strict rule that `"30"` is not `30`. Do not
  "simplify" that into a global coercion; the two are different wires.
- **A context value exported from an XML response is a string.** Nothing in the
  corpus exports a numeric member from a Query service, and if one ever does,
  `Binder::i32` will refuse the string rather than send a wrong number. Fix it
  there, deliberately, rather than by typing the document from how its text
  happens to look — ELB's own `ProxyProtocol` policy attribute is a modeled
  string whose value is `"true"`.
- **The conventions implemented are `awsQuery`'s.** REST XML's wrapper lists and
  `ec2Query`'s `<item>` are not; `xml.rs`'s
  `a_rest_xml_wrapper_is_not_flattened` pins that, and the first scenario over
  either protocol is what should change it.

---

## Registration tests

`src/main.rs` carries `registration_tests`, which merges every service's real
impl map through the same `merge_impls` the binary uses and resolves the
result against the real `registry.json`:

- merging the real maps **is** the duplicate-key check;
- validating them against the real registry **is** the bare-key check.

The fixture-based loader tests in `src/registry.rs` cover merge, validate,
dependency ordering and `registry.generated.json` handling on synthetic
registries — which cannot see a collision introduced in an actual service
file, hence the pair.

`generated_groups_are_registered_for_every_test_the_registry_declares` is the
same check for the generated half: every test of every generated group scoped to
this suite must be registered under its group-qualified key, and every such
group must carry both hooks. The Dockerfile copies `registry.generated.json`
into the build stage for it — without that file the case would see no generated
groups and pass vacuously, which is the one thing a registration test must not
do.

The Dockerfile runs `cargo test` in its build stage, so all three sets run on
every image build and a broken registration fails it rather than quietly
changing what a run reports. Building the image is therefore the way to run them
without a host Rust toolchain:

```bash
# from the repo root; the branch-named tag keeps parallel worktrees apart
docker build -f compat/suites/rust-sdk/Dockerfile \
  -t "oc-rust-sdk-compat:$(sh scripts/image-tag.sh)" compat/suites
```

One case cannot run there. `src/scenario/errorfixtures.rs` reads the shared
error-matching fixtures under `compat/model/testdata/errors`, which sit outside
the `compat/suites` build context, so in the image build it says so on stderr
and returns. `nowfixtures.rs` and `equalsjsonfixtures.rs` do the same with
`compat/model/testdata/now` and `compat/model/testdata/equalsjson`. It runs for real in `test.yml`'s `compat-suite-unit-tests` job,
from a full checkout, beside the other suites' unit tests — which is also where
to run `cargo test` by hand when you have a host toolchain.

Where it does run, it asserts the exact set of skips it takes (`EXPECTED_SKIPS`)
and a floor on the number of expectations it answers (`MINIMUM_CHECKED`). Adding
a fixture whose carriers this suite cannot read therefore fails here until the
skip is named with its reason, rather than passing quietly.

---

## Adding a new group

1. Add the group and its tests to [compat/suites/registry.json](../registry.json)
   first (see [compat/AGENTS.md § Uniformity](../../AGENTS.md#2-uniformity--the-registry-is-the-contract)),
   or confirm another suite has already declared them.
2. Create or open `src/groups/<service>.rs` and implement `ServiceGroup`.
3. Register every test under a **group-qualified** key in `impls()`, and its
   setup/teardown under the bare group name.
4. For a new service, add the module to `src/groups/mod.rs`, the struct to
   `all_service_groups` in `src/main.rs`, a constructor to `src/clients.rs`,
   and the pinned `aws-sdk-*` crate to `Cargo.toml`.
5. Build the image (or run `cargo test`, with a host toolchain) — the
   registration test fails on a mis-keyed registration without needing a run.
6. Run the suite against a live instance (see
   [README.md § Running the suite](README.md#running-the-suite)) and check the
   NDJSON output.

---

## What agents must NOT do

- Never import from `internal/`, `router/`, or any Overcast server source tree
  — see [compat/AGENTS.md § Separation boundary](../../AGENTS.md#separation-boundary--non-negotiable).
- Never hard-code the endpoint — clients are configured once from
  `OVERCAST_ENDPOINT` in `AwsClients::new`.
- Never use `std::thread::sleep`, or `tokio::time::sleep` with a fixed
  duration, inside a test — poll with a maximum retry count instead.
- Never construct SDK clients inside a test closure — clone the
  `Arc<AwsClients>` the group holds.
- Never register an impl key without the `group:` qualifier — the registration
  test rejects it, and the Docker build fails with it.
- Never add a setup entry without a corresponding teardown entry.
- Never call `delete_bucket` without first emptying the bucket.
- Never schedule KMS key deletion without first deleting its aliases.
- Never write to stdout inside a test — the runner parses stdout as NDJSON;
  use `ctx.log()` (stderr) for diagnostics.
- Never add `anyhow` (or another error crate) to `Cargo.toml` for test code —
  the harness's error type is `String`, and `sdk_error` is how an SDK error
  becomes one.
- Never hand-edit `src/groups/scenarios_gen.rs` or `src/groups/scenarios_*_gen.rs`,
  and never run `cargo fmt` over the whole crate — see
  [The generated files are output](#the-generated-files-are-output-not-source).
- Never classify a generated failure with `harness::is_unimplemented`. A
  generated failure message embeds the exact params JSON sent, where a run id or
  a port number can put a "501" that means nothing; `crate::scenario` states the
  classification with `harness::UNIMPLEMENTED_TAG` instead, and `harness::classify`
  strips it before the message is emitted.
