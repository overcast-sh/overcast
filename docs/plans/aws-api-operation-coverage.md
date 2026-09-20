# AWS API operation coverage and S3 fallthrough prevention

> Status: **implemented — all phases (A0–A5) merged** (A4 gates in #342, the A5
> refresh workflow in #352, the offline staleness gate in #358; the workflow has
> run weekly since). Kept as the living reference for the manifest, registry and
> refresh machinery. Last verified against `main` 2026-08-21. Owner: TBD.
> Follow-on enforcement built on this foundation:
> [manifest-enforcement.md](./manifest-enforcement.md) (route-vs-model gates,
> merged in #876/#921).
> Related: [Level 2 codegen](./level2-codegen.md) Track 3, [Smithy wire protocols](../dev/smithy.md), and [wire-byte goldens](./wire-byte-goldens.md).
>
> **`OVERCAST_SERVICES` was removed after this plan was written.** Every service
> is now always registered, so there is no disabled-service state and no
> `ServiceDisabled` response. The audit findings in §2 and the note in §8
> describe the routing situation as it stood then, and are kept as the record of
> what motivated the work; where they say a `PathPrefixService` prefix protects a
> *disabled* service, that protection now only applies under the test-only
> `config.TestOnlyServiceSubset`. Nothing else in the plan is affected: S3's
> broad fallback and the evidence-based registry are what actually prevent
> fallthrough, and both are unchanged.

## 1. Objective

For every public AWS API operation known to the emulator's pinned AWS model snapshot, an SDK, CLI, or CDK request must either reach an implementation (including inert/metadata-only implementations) or receive a protocol-correct `501 NotImplemented` response. It must never receive an S3 response unless it is an actual S3 request.

This is a routing and compatibility guarantee, not a claim that Overcast implements every AWS operation.

## 2. Audit findings

S3 deliberately registers broad `/{bucket}` and `/{bucket}/*` routes after all other services. That is correct for S3 because its REST-XML API has no reliable distinguishing header or fixed root prefix. It also means any AWS request not positively claimed by another service can be read as a bucket or object request.

Current protections are useful but incomplete:

- `TargetDispatcher` claims only currently registered AWS JSON target prefixes.
- `QueryDispatcher`, `QueryVersionOwner`, and `QueryActionOwner` claim only some Query traffic.
- `PathPrefixService` protects only manually declared disabled REST roots.
- Existing service-local stubs provide correct 501s only after the request reaches the correct service.

The ownership gaps are:

- Valid non-S3 REST paths without explicit chi handlers can match S3's wildcard.
- Lambda owns several versioned API roots but declares only `/2015-03-31` for disabled-service protection.
- `GET /?Action=...` with no claiming Query dispatcher falls through to S3's root route.
- Target prefixes, Query versions, operation lists, and REST bindings are hand-maintained and only cover currently implemented services.
- `cmd/stub-report` inventories typed Overcast operations; it is not an AWS operation inventory. Capability tables describe Overcast status, not AWS ownership.

## 3. Source of truth and scope

Use AWS's public [`aws/api-models-aws`](https://github.com/aws/api-models-aws) repository as the source of truth. AWS publishes its public service models daily in Smithy JSON AST form and describes them as definitive public API definitions. The model supplies service identity, operations, protocol traits, and HTTP bindings; it does not dictate Overcast's implementation status.

The complete raw Smithy snapshot is deliberately not vendored in A1: it is large, while the checked-in generated manifest is the only artifact required at runtime. `models/aws/VERSION` records the upstream commit, model date, source URL, and license provenance. Regeneration requires a local `api-models-aws` checkout whose `HEAD` matches that recorded commit; the generator validates the match before writing output. Runtime code must never parse model files or contact AWS/GitHub.

Normal PRs validate the committed corpus without network access. When a verified
model checkout is available, `make aws-models-check AWS_MODELS_DIR=...` also
regenerates the manifest and compares it byte-for-byte. The A5 refresh workflow
keeps a GitHub Actions cache of the upstream Git mirror and supplies that
checkout; the raw Smithy snapshot is still not committed or loaded at runtime.

**Amendment (2026-08-23): the pruned shape snapshot.** One artifact now sits
beside the manifest that this policy would otherwise appear to forbid:
`models/aws/shapes/<service>.json`, added by
[inert-tier-rollout.md §4.6](./inert-tier-rollout.md) Phase I1 and shared with
[compat-coverage-modelgen.md §3.7](./compat-coverage-modelgen.md) — one pruner in
`cmd/awsmodelgen`, one `shapes-sha256`, two consumers. It is **not** the raw
corpus and weakens nothing above. It is a generated, byte-deterministic
derivative holding, for a reviewed in-scope service list only, the shapes
reachable from those services' operations and resources, with traits filtered to
what the generators consume — 14.7% of the upstream bytes for the four services
committed so far, with documentation, examples, waiters, smoke tests, endpoint
rule sets and every out-of-scope service dropped. It is **generator input only**,
the same status as the upstream checkout, enforced by a test that no non-test
package outside `cmd/` may name it, so "runtime code must never parse model
files" stands unchanged. And it is **offline-checkable**, which is why it exists
rather than the external checkout: `shapes-sha256` in `models/aws/VERSION` gives
per-service codegen the same no-network staleness gate `manifest-sha256` gives
the manifest, and the A5 refresh workflow regenerates it in the same bot PR that
bumps the revision, so the two cannot drift. Its growth is capped by a
CI-enforced size budget (§4.6 there). See
[cmd/awsmodelgen/README.md](../../cmd/awsmodelgen/README.md) for the layout, the
trait allowlist and the digest definition.

This covers public AWS management-plane/service API operations used by SDKs, CLI, and CDK. It excludes emulator-only `/_*` endpoints, service data/runtime endpoints with intentionally arbitrary user paths (such as API Gateway execution), and CLI conveniences such as waiters. Waiters are covered indirectly through their API operations.

## 4. Design

### 4.1 Generated operation manifest

`cmd/awsmodelgen` reads the pinned JSON AST and generates checked-in `internal/awsapi/manifest.gen.go`. Regenerate it with `make generate-aws-operations AWS_MODELS_DIR=/path/to/api-models-aws/models`; it reads the expected revision from `models/aws/VERSION` and validates the local checkout. Generate routing metadata only:

- canonical service name, Smithy service shape name, SDK ID, API version, and source provenance;
- operation name and protocol traits;
- AWS JSON target-prefix data;
- AWS Query API-version/action ownership;
- REST method and URI template;
- protocol/error-envelope family; and
- S3 designation or an ambiguous root requiring stronger service evidence.

Where a legacy AWS JSON `X-Amz-Target` prefix is not encoded in Smithy (CloudTrail is the initial case), the generator uses a small, tested override table. Each entry must cite existing AWS-compatible service evidence and be removed if upstream models become sufficient.

Do not generate clients, every request/response type, or a handler per operation. Types remain an allowlisted, implementation-only concern in Level 2 Track 3.

The A1 corpus is private and is not a runtime lookup structure. A2/A3 generate immutable target, Query, and REST-trie indexes from it; neither phase may linearly scan it per request or construct model-sized maps during router startup. The `Service` field is a normalized Smithy SDK identity, not an Overcast router key. A2 introduces an explicit alias table for the non-identical names (for example, Smithy `cloudwatch-logs` to Overcast `logs`).

The alias audit now has sixteen intentional mappings (the count in
`internal/awsapi/registry_data.go`'s `serviceAliases` is authoritative). The initial nine: Overcast `apigateway`, `appregistry`, `autoscaling`, `dynamodbstreams`, `elbv2`, `msk`, `route53`, `secretsmanager`, and `stepfunctions` map respectively to manifest `api-gateway`, `service-catalog-appregistry`, `auto-scaling`, `dynamodb-streams`, `elastic-load-balancing-v2`, `kafka`, `route-53`, `secrets-manager`, and `sfn`. Later additions fold separately modeled families into the implementing key: `apigatewayv2` → `apigateway`, `bedrock-runtime` → `bedrock`, `cognito-identity-provider` → `cognito`, `sesv2` → `ses`, `wafv2` → `waf`, and `cloudwatch-events` → `eventbridge` (EventBridge under its former modeled name — the same statement the A2 collision resolution below relies on). One divert runs the other direction: manifest `waf` is WAF *Classic* (unimplemented, with operation names overlapping v2), so it maps to the deliberately unregistered key `waf-classic` to keep classic operations from validating capability rows declared against the v2-implementing `waf` key. `cognito-identity` (Federated Identities, identity pools) is deliberately *not* aliased — Overcast's cognito implements only user pools. `lambda-core` is a separate modeled AWS service (`Lambda Core`, version `2026-04-30`), not an alias of Lambda; preserve it as distinct. `capgen --check-model` enforces the audit mechanically: every service key (directory or capability declaration) must resolve to at least one manifest identity via key-or-alias (`SERVICE_KEY_NOT_IN_MODEL`). The same gate holds the compat registry to the capability vocabulary — every `compat/suites/registry.json` group's `service` must be a capability service key or carry an explicit non-service exemption (`COMPAT_REGISTRY_UNKNOWN_SERVICE`) — so a hand-written group cannot invent a key, and composing the two checks means every compat group's service resolves to a modeled identity.

### 4.2 Registry

`internal/awsapi.Registry` consumes generated tables and only determines ownership:

```go
type Claim struct {
    Service   string
    Operation string
    Protocol  Protocol
}

func (r *Registry) Claim(req *http.Request) (Claim, bool)
```

### A2 implementation boundary

A2 emits sorted, immutable AWS JSON target and fully-qualified Query
`(Version, Action)` indexes from the manifest. `Registry` binary-searches those
indexes and the router invokes one shared error-profile writer only after
service dispatchers decline ownership. This is deliberately
not a broad service-prefix interceptor: it preserves existing implementations,
explicit service stubs, and S3 controls. REST URI-template and RPC v2 matching
remain A3 work, where the generated trie can provide sufficient evidence rather
than guessing from a path prefix. The service-key aliases live beside the
registry and are tested as explicit data, never inferred from a naming rule.
When multiple modeled services share one target or `(Version, Action)` key, the
generator emits a collision record and marks the registry claim ambiguous rather
than silently selecting the alphabetically first service. The shared protocol
still selects the fallback envelope; A3/A4 ownership and coverage reporting use
the collision records to require an explicit resolution or exclusion.

The collision arrays are no longer write-only: `resolveCollision`
(`internal/awsapi/ambiguity.go`) reads them so a claim can be attributed where
Overcast — not the models — already knows the owner. Two declarations do that
work, and neither is new data. `serviceAliases` collapses a collision whose
members are one service under two modeled identities, which is all 51
`cloudwatch-events`/`eventbridge` target collisions. `queryVersionOwners`
(`versions.go`) names the service that answers an API version, which is the
same statement each service's `OwnsVersion` is built from, and settles all 71
query collisions — DocumentDB, Neptune and RDS sharing `2014-10-31`, of which
Overcast implements only RDS. A declared owner is honoured only when it is one
of the services that actually collided, so it tie-breaks rather than overrides,
and a collision neither step resolves stays ambiguous: the four Timestream
target collisions are two real services Overcast does not implement.

This changes attribution only. The shared error-profile writer switches on
`ErrorProfile`, which is derived from the protocol and never from the service,
so the 501 responses are unchanged; the classifier in `internal/middleware` is
the only consumer of a resolved `Claim.Service`. The resolution runs solely on
the already-ambiguous branch, so an ordinary claim still binary-searches one
sorted index and allocates nothing. Coverage reporting remains A3/A4 work, and
now reads arrays whose ambiguity has already been narrowed to the genuinely
unattributable.

The generated Query index otherwise preserves existing observable behavior: the
generic Query fallback already uses its common XML envelope, and EC2 owns its
only modeled version before the registry can choose `ErrorProfileEC2QueryXML`.

Measured locally after A2 with `go test -run=^$ -bench=RegistryClaim -benchmem
-count=3 ./internal/awsapi` in a Linux `golang:1.24` container on an AMD Ryzen
9 5900X: AWS JSON target lookup was 53–56 ns/op and Query lookup 93–105 ns/op;
both reported `0 B/op` and `0 allocs/op`. This is a focused lookup measurement,
not a router construction or end-to-end request benchmark; A4 retains the full
startup and request-path guardrail.

### A3/A4 implementation boundary

A3 compiles REST JSON/XML bindings into immutable generated trie tables and
consults them only after every explicit non-S3 route declines the request. A4
completes the generated ownership surface:

- Smithy literal query components are stored separately from the REST path and
  matched without allocation; trailing-slash templates normalize to the same
  trie leaf as the request path;
- REST collisions use normalized, human-readable method/template/query keys
  and ambiguous bindings remain explicit S3-preserving exclusions;
- RPC v2 CBOR and RPC v2 JSON get a sorted immutable
  `(protocol, service shape, operation)` index. It includes additive protocol
  traits, so the 575 operations that advertise CBOR alongside another
  canonical protocol are not omitted (593 total CBOR-capable operations,
  including 18 whose canonical protocol is CBOR);
- the explicit `/service/{service}/operation/{operation}` route consults that
  index after implemented service operations. A modeled gap gets its native
  501 envelope, and a truly unknown service preserves the existing
  unsupported/unknown behavior. (There is no longer a disabled-service case:
  `OVERCAST_SERVICES` is gone and every service is always wired, so the
  `ServiceDisabled` branch this route used to carry was removed with it.)
  The Smithy protocol header is required evidence: a headerless S3 multipart
  request with the same legal path grammar delegates to S3; and
- a sorted `modelServices` index lists every modeled service identity, so
  `IsServiceKey` can answer whether a name is a service at all. RPC v2 is the
  one protocol that names its service in a URI label rather than attaching it
  to something resolvable, and the router dispatches that label against its own
  registered services before the registry is consulted — so `/service/ecs/…`
  answers CBOR for a service the models bind no RPC v2 operation on. The
  classifier reads the label the same way, and a label naming no service is not
  believed; and
- generated corpus tests require every non-S3 operation and every additive RPC
  trait to resolve through a target, Query, REST, or RPC ownership index.

Runtime request paths never read or scan `manifest`. Target, Query, and RPC
lookups binary-search static generated slices, while REST walks static trie
tables. The RPC dispatcher builds only a service's small implemented-operation
set, lazily on its first RPC request, using `sync.Once`; router construction
does not allocate a model-sized map.

It does not decode bodies, persist data, implement business logic, or replace service routers. It follows Smithy protocol precision:

1. RPC v2 CBOR marker and request path;
2. AWS JSON content type plus `X-Amz-Target`;
3. AWS Query `Version`/`Action`, with SigV4 credential service as a tiebreaker; and
4. REST method plus generated URI-template match, using SigV4 service for genuinely ambiguous roots.

The registry never claims `s3` or `/_*`. REST paths overlap S3's legal bucket
and object namespace, including generic Smithy labels. A REST fallback therefore
requires both an unambiguous modeled method/path binding and the modeled
`aws.auth#sigv4` service name from the request credential scope. AWS SDK, CLI, and CDK calls
provide that scope; unsigned or S3-scoped traffic preserves S3 behavior. This
also safely disambiguates the rare modeled REST root binding from ListBuckets.

### 4.3 Router fallback

Existing exact chi routes and target/query dispatchers retain ownership of implemented operations. Add one fallback before S3's wildcard registration:

1. existing implementation or service stub handles the request;
2. otherwise, the generated registry claims a known non-S3 AWS operation;
3. one shared fallback writes a protocol-correct 501;
4. only an unclaimed request proceeds to S3.

This avoids a generated chi route for every operation, keeps startup and registration small, and makes adding an implementation a normal handler-registration change. Existing route/dispatcher precedence automatically supersedes the fallback.

The shared fallback selects `protocol.NotImplementedJSON`, `protocol.NotImplementedQueryXML`, `protocol.NotImplementedEC2QueryXML`, or `protocol.NotImplementedXML` from generated metadata. Keep a tiny explicit error-profile override table only where a Smithy protocol trait cannot select the real AWS envelope.

Until that metadata exists, an unclaimed Query action with no `Version` cannot
safely select the EC2-family envelope: for example, `DescribeInstances` falls
back to the common Query `ErrorResponse` envelope. A2 must use generated
service/protocol ownership to select `NotImplementedEC2QueryXML` only when the
request has sufficient EC2 evidence.

### 4.4 Status stays separate

The manifest declares **what AWS exposes**. `capabilities_dev.go` declares **what Overcast does**: unsupported, inert, partial, WIP, or supported.

Make `capgen` reject every non-`DocOnly` capability that does not map to a manifest operation, while retaining an explicit allowance for documented synthetic rows. Make `stub-report` consume the manifest rather than scrape typed source; it becomes a coverage report for model operations, Overcast status, implementation registration, and fallback ownership.

## 5. Performance requirements

Models are fetched, parsed, and generated at build/update time only. The binary contains compact generated maps and a precompiled REST-template trie.

- AWS JSON and Query: bounded header/form read plus map lookup.
- REST: path-segment trie walk, independent of total operation count.
- No per-request/startup disk I/O, model parsing, network I/O, or allocations proportional to model size.
- Implemented calls retain existing handlers. Unsigned and S3-scoped requests
  skip the REST registry entirely, then delegate once to S3's private chi
  router; signed non-S3 fallback requests perform the bounded trie lookup.

Before landing, benchmark router construction and representative S3, AWS JSON, Query, and REST-fallback requests. Do not materially move the startup budget or <=1 ms handler-overhead target. Record command, environment, operation, and before/after figures for every published claim.

### A4 measured guardrail

Measurements below used Linux/amd64 Docker containers on an AMD Ryzen 9 5900X,
the `golang:1.25.6` image, Go's default benchmark duration, `-benchmem`, and
three runs per benchmark on 2026-07-28. The exact commands were:

```sh
go test -run '^$' -bench BenchmarkRegistryClaim -benchmem -count=3 ./internal/awsapi
go test -run '^$' -bench 'Benchmark(RouterConstruction|OperationCoverageRoutes)' -benchmem -count=3 ./internal/router
```

The exact A3 parent (`290f484b`) measured target lookup at 83–93 ns/op, Query
at 112–120 ns/op, and REST at 90–103 ns/op. A4 measured target at 79–82 ns/op,
Query at 101–114 ns/op, REST at 106–109 ns/op, and RPC at 160–217 ns/op; every
registry lookup remained `0 B/op` and `0 allocs/op`. The small REST increase is
the literal-query discriminator and remains independent of corpus size.

With the final lazy RPC implementation index, router construction measured
2.75–3.28 ms/op and 1.68 MB/op. Representative in-process requests measured
13–61 µs/op across two runs for S3 ListBuckets, modeled AWS JSON, Query, and signed REST
fallbacks, including request construction, middleware, response recording, and
request-ID generation. These are development-machine microbenchmarks, not
network latency claims; the request paths remain well below the plan's 1 ms
handler-overhead guardrail. CI enforces zero-allocation generated lookups, while
the committed router benchmarks preserve the broader startup/request baseline
without relying on a noisy wall-clock threshold.

A review A/B after rebasing onto `main` used Linux/amd64, Go 1.24.13 in
`golang:1.24-bookworm`, the same Ryzen 9 5900X, `-benchtime=2s -count=5`, and
the committed `S3ListBuckets` benchmark. Main measured a median 11.7 µs/op
(11.3–25.2 µs/op, 125–132 allocs/op); the initial private-router fallback
measured a median 16.4 µs/op (16.2–16.7 µs/op, 121 allocs/op), a 40% median
CPU trade-off from the second chi dispatch rather than allocation or registry
size. The credential fast path now skips trie matching for unsigned and
S3-scoped requests. Under the same Go version, CPU, `-benchtime=2s -count=5`,
and repository Docker harness it measured 16.4–17.7 µs/op at 121 allocs/op.
The remaining dispatch cost is accepted for A4 because it preserves the
positive-evidence S3 safety boundary and remains roughly 60x below the 1 ms
handler-overhead budget; the benchmark remains committed so a future
single-dispatch design can improve it with evidence.

## 6. Delivery phases

| Phase | Work | Acceptance gate |
| --- | --- | --- |
| A0 | Write failing router integration tests for Lambda alternate versions, unknown API Gateway REST paths, Query `GET /?Action=`, AWS JSON unknown services, and legitimate S3 controls. | Every non-S3 fixture returns the correct 501 envelope, not an S3 response; S3 behavior remains unchanged. |
| A1 | Record a pinned model provenance; add the generator and reproducible regeneration target; generate the complete operation-metadata baseline. | Manifest is deterministic, records source provenance, and retains every recognized protocol trait currently present in the model source. |
| A2 | Implement `awsapi.Registry`, shared fallback, and error-profile selection. | Pilot operations route correctly without changing implementation behavior. |
| A3 | Compile the generated metadata into the REST trie; migrate `stub-report`; validate capabilities against manifest, including explicit aliases and the ten legacy JSON-target dispatchers modeled as REST (`backup`, `appconfig`, `appconfigdata`, `appregistry`, `appsync`, `bedrock`, `eks`, `msk`, `opensearch`, `scheduler`). | Every generated operation has ownership or an explicit ambiguity/exclusion, and every Overcast service resolves through an alias to at least one manifest operation or an explicit exemption. |
| A4 | Add generated route-coverage tests, collision reports, performance guardrails, and CI generation checks. | No modeled non-S3 request falls through to S3. |
| A5 | Add upstream refresh automation and contributor documentation. | One bot-owned PR is updated for model changes with an actionable diff report. |

Each phase begins with failing tests. Any correction to modeled protocol, error format, or REST binding uses AWS API docs/Smithy evidence and updates the compatibility tracker.

Until A2 provides the generated evidence-based registry, do not install an
enabled-service REST catch-all for a path that can also be a legal S3 bucket or
object path. The disabled-service prefixes remain safe because the configured
service state is unambiguous; enabled Lambda REST fallback therefore belongs in
the registry phase, not in a service-local chi wildcard.

### Shipping and branch strategy

Ship this work as small, independently reviewable PRs; do not keep it on one
long-lived feature branch. Each phase should leave main internally consistent,
fully tested, and ready for the next phase:

| PR | Shippable scope | Required condition |
| --- | --- | --- |
| A0 | Regression tests that demonstrate current fallthrough cases. | Tests describe the desired 501 contract without weakening existing S3 coverage. |
| A1 | Pinned-model tooling and a small generated-manifest pilot. | No runtime routing behavior changes. |
| A2 | Registry and fallback for the pilot services/protocols. | Include positive S3 controls and route-precedence tests; do not merge a generic fallback that could classify a genuine S3 request incorrectly. |
| A3 | Full-model manifest and capability/stub-report convergence. | Model and implementation inventories agree, with explicit exclusions. |
| A4 | Generated corpus, collision detection, and performance gates. | Corpus proves no modeled non-S3 operation reaches S3. |
| A5 | Scheduled refresh workflow and bot PR lifecycle. | The workflow updates only its dedicated automation branch and cannot silently merge. |

A2 is the first user-visible milestone and should be intentionally narrow: it
can eliminate the highest-impact REST and Query fallthroughs without waiting
for every AWS model/service migration. Every later PR widens coverage without
changing this safety rule.

No phase is shippable with a known regression, a failing or skipped required
check, an introduced compiler/vet/problem-list error, stale generated output,
or reduced S3 positive coverage. A failed acceptance gate blocks the PR; it is
not deferred to the next phase. Regression fixes use a reproducing test first
and are merged with the affected phase or as a separately green corrective PR.

## 7. Testing strategy

### Focused contracts

Test through `router.New`, not direct handlers. Assert status, content type, request ID, `x-emulator-unsupported`, and JSON/XML/Query error envelope. Maintain positive S3 coverage for bucket, object, virtual-host, root list, and subresources.

### Generated corpus

Generate a safe discriminator request for every manifest operation: target header, Query version/action, or REST method/template placeholders. Assert it reaches an owner or returns a non-S3 501; it need not have valid business input.

Generator validation must prove deterministic REST-route ownership or require a checked-in resolver declaration. Route collisions must never silently choose a service.

### Regression and performance

Run router/protocol integration tests, generator unit tests, manifest determinism tests, and generated corpus tests. Keep wire-byte goldens for implementation migrations; fallback tests cover ownership and error envelopes, not successful operation semantics.

## 8. Model refresh automation

The `AWS API model refresh` GitHub Actions workflow runs at 03:17 UTC every
Monday and is also manually dispatchable. AWS publishes daily, but weekly keeps
review diffs manageable. It:

1. compares upstream `aws/api-models-aws` with `models/aws/VERSION`;
2. exits when current;
3. fetches the new revision and runs generation and all model/route checks;
4. summarizes service, operation, trait, binding, collision, and fallback-coverage changes; and
5. creates or updates one bot-owned PR.

It uses the stable `automation/aws-api-models` branch. Each run resets that
branch to current `main`, commits only the generated manifest and provenance
pin, and pushes with an exact force-with-lease against the remote revision it
observed. If an open PR exists from that branch, the workflow updates its title
and body; otherwise it creates one. The `aws-api-model-refresh` concurrency
group serializes runs. It never updates contributor branches and never merges.

Besides the weekly schedule and manual dispatch it runs on a push to `main` that
touches the refresh's inputs (`models/aws/`, `internal/awsapi/`,
`internal/awsshapes/`, `cmd/awsmodelgen/`, `cmd/compatgen/`, `compat/model/`,
`scripts/aws-models.go`). Such a push
only refreshes a PR that is already open, regenerating it on the new `main` and
at the latest upstream revision; with no PR open it stops, so opening a refresh
stays the weekly run's job. The changelog waiver is posted once per PR rather
than on every run.

The compat model (`compat/model/`, and the suites `cmd/compatgen` emits) is
derived from the manifest and shape snapshot, and its CI gate fails on any
drift, so the workflow runs `cmd/compatgen` after the models and commits its
output with them. A refresh that adds an operation adds a row to
`compat/model/gaps.json`; without regenerating it the PR could not pass CI.

The upstream repository is cached as a Git mirror keyed by the observed commit.
Every run still fetches from the configured official source, verifies that both
the old and new revisions are real commits, and verifies that the mirror's
`HEAD` is the revision returned by `ls-remote`. The cache is a performance
optimization, not a trust decision.

The workflow authenticates as the repository's release GitHub App
(`RELEASE_APP_CLIENT_ID` / `RELEASE_APP_PRIVATE_KEY`) and fails immediately if
those secrets are absent. `GITHUB_TOKEN` is not a fallback: GitHub refuses
`createPullRequest` from Actions unless repository Actions settings permit it,
and it suppresses ordinary workflow runs caused by that token, so a PR opened
with it would wait on required checks that never start. Branch protection and
human review remain the merge gate.

Starting in A4, every normal PR runs a no-network `aws-models-check` target
that validates the committed generator fixtures, immutable indexes, collision
metadata, capability alignment, protocol envelopes, router regressions, and
generated no-S3-ownership-gap corpus. It deliberately does not fetch models.
It also verifies the committed manifest against the `manifest-sha256` recorded
in `models/aws/VERSION`, which closes the remaining offline gap: proving the
manifest matches *upstream* needs the corpus, but proving it is still the
generator's own output does not, so a hand-edit or a partial merge fails on an
ordinary pull request.

A5 supplies the verified upstream checkout from its cache and invokes that same
target with `AWS_MODELS_DIR` and `AWS_MODELS_REVISION`, enabling full
regeneration-and-diff validation. Contributors can invoke the identical path
with any local checkout at the pinned revision.

Note what each check can and cannot prove. Inside the refresh workflow the
regeneration diff runs against the revision the manifest was just generated
from, so it asserts determinism only. The staleness assertion is a separate,
earlier step that regenerates at the *pinned* revision and compares against the
committed manifest, so a refresh cannot carry existing drift forward.

## 9. Relationship to Level 2 codegen

This plan is the routing-completeness foundation of Level 2 Track 3; it does not replace it.

- Track 1 identifies protocol and operation.
- Track 2 provides one implementation per implemented operation.
- This plan supplies Track 3's complete AWS operation/protocol/binding manifest and makes an operation without an implementation safely owned and rejected.
- Track 3 keeps generated input/output types allowlisted to implemented operations; unsupported ones require only the manifest and generic fallback.
- `capgen` and `stub-report` converge on the manifest, removing parallel hand-maintained operation inventories.

Deliver A1-A4 before or alongside Track 3's SQS/Scheduler generator pilot. A5 keeps Track 3 current as AWS adds services, operations, or protocol traits.

## 10. Done definition

Done means: a pinned model snapshot defines the operation universe; a generated runtime registry claims every non-S3 operation with sufficient evidence; unsupported calls receive the correct 501 envelope; S3 remains fallback only for actual/indistinguishable S3 traffic; and CI plus the scheduled update PR keep that guarantee current.

## 11. What next: converging on operation management

Yes: eventually, all existing AWS-facing routes should be represented in the
generated manifest. That does **not** mean replacing every service handler with
a generated router, nor moving business logic into a central registry. The
target architecture has three deliberately separate layers:

| Layer | Owner | Responsibility |
| --- | --- | --- |
| AWS model manifest | Generated from pinned Smithy models | What operations exist and how requests identify them. |
| Operation registration | Small hand-written service declaration | Whether Overcast implements an operation and which handler owns it. |
| Service implementation | Existing focused service packages | Parsing/validation, business behavior, state, and AWS-shaped responses. |

This separation keeps the design DRY: model metadata is defined once and shared
by router fallback, capabilities, stub reporting, tests, and automation. It
keeps it SOLID: the registry classifies; service packages implement; code
generation translates model data; none needs to know another layer's internals.
It remains idiomatic Go: static generated data, small interfaces, explicit
registrations, and ordinary handlers rather than reflection or a runtime
framework.

### Migration order

1. **Adopt the manifest for ownership first.** Complete A1-A4 so every
   operation is classified and unsupported requests cannot reach S3.
2. **Reconcile existing services.** For each service, compare manifest
   operations with capability declarations and actual handlers. Add an explicit
   operation registration for every implementation, inert stub, and deliberate
   501. Do this service-by-service; do not perform a risky fleet rewrite.
3. **Make registrations the implementation index.** Replace source scraping in
   `capgen` and `stub-report` with generated manifest plus service
   registration. CI rejects an implementation that is undocumented or an
   implemented capability without a handler owner.
4. **Converge protocols onto one operation implementation.** Follow Level 2
   Track 2: legacy HTTP paths become thin decode/delegate/encode shims, while
   one operation function owns behavior. REST services may retain their chi
   bindings because HTTP path matching is their natural protocol boundary.
5. **Generate only repetitive plumbing.** Generate operation constants,
   protocol declarations, ownership tables, and optional registration
   scaffolding. Keep validation, lifecycle, state, errors, and all meaningful
   business behavior hand-written and test-first.
6. **Manage the long tail cheaply.** A model-refresh PR automatically adds new
   operations to the fallback universe. An unimplemented operation therefore
   has zero service boilerplate yet returns a correct 501; implementing it is
   an intentional small registration plus handler change.

### Permanent guardrails

- No AWS operation may be added outside the manifest; model refresh is the
  entry point for new public API surface.
- No manifest operation may silently route to S3; generated corpus tests enforce
  this.
- Every operation has one status and one implementation owner, even when that
  owner is the generic unsupported fallback.
- Generated tables are immutable at runtime and built once, so operation
  management has no unbounded memory growth, no model parsing at startup, and
  no per-request linear scans.
- A new service implementation begins by registering selected manifest
  operations, rather than introducing another hand-maintained prefix/action
  list.

The resulting steady state is easy to manage: AWS publishes the inventory,
the generator turns it into reviewed static data, the registry guarantees
ownership, and service packages remain small, explicit, and independently
testable.
