# Model-driven compat coverage — scenario generation across every suite

> Status: **in progress** — G0 and G1 are done, **G2 is code complete**, and
> **G3 is done**: all four typed backends — `go-sdk`, `java-sdk`, `dotnet-sdk`,
> `rust-sdk` — landed 2026-09-06 (#1820). See the §2 note dated 2026-09-06 for
> what each measured.
> All three interpreters are on `main` (`python-sdk` #1787, `node-js-sdk` #1788,
> `cli` #1790), and every §4.1 criterion and §4.2's criteria 1–3 are met in all
> three suites. §4.2 criterion 5, the regeneration demonstration, is met as of
> 2026-09-06 (#1818 then #1813), and so is criterion 4's second half: the first
> candidate → gated promotion happened by itself on 2026-09-06 (#1871, three
> agreeing nightly runs) — **G2 is done, #1768 closed.** G4 has started,
> tracked as #1883. See the § 2 notes dated 2026-09-06.
> Proposed 2026-08-03. Owner: TBD.
> Siblings written concurrently, and part of the same tier programme:
> [inert-tier-rollout.md](./inert-tier-rollout.md) (Tier 1 implementation — the
> thing generated tests will mostly exercise),
> [services-never-emulated.md](./services-never-emulated.md) (services that stay
> Tier 0 forever), [full-emulation-priority.md](./full-emulation-priority.md)
> (which services earn Tier 2).
> Prior art this plan must slot alongside, not contradict:
> [aws-api-operation-coverage.md](./aws-api-operation-coverage.md) (the pinned
> model snapshot, the generated manifest, the model-refresh workflow) and
> [level2-codegen.md](./level2-codegen.md) Track 3 (server-side model-driven
> generation). Policy this plan is bound by:
> [compat/AGENTS.md](../../compat/AGENTS.md) and
> [compat-baseline-and-uniformity.md](./compat-baseline-and-uniformity.md).

**Tier vocabulary** (shared across the four plans, used throughout):

| Tier | Meaning |
| --- | --- |
| **Tier 0** | No implementation. A protocol-correct `501 NotImplemented` in the right error envelope, guaranteed by [aws-api-operation-coverage.md](./aws-api-operation-coverage.md). |
| **Tier 1 "inert"** | Metadata CRUD only. Correct shapes, status codes, identifiers and error codes; resources are created, described, listed and deleted; CDK/CFN provisioning works; **no behaviour** (a Route 53 zone serves no DNS, a Shield protection protects nothing). |
| **Tier 2** | Full emulation: the resource actually does its job. |

---

## 1. Objective

Every AWS operation known to the emulator's pinned Smithy snapshot that Overcast
either implements or intends to route should have a compat test **in every
suite**, without any of those tests being hand-written eight times.

Concretely: replace linear per-suite authoring with a single declarative
**scenario IR** generated from the pinned AWS models plus a small hand-curated
layer of per-service *recipes*, executed by thin per-suite interpreters (dynamic
SDKs) or compiled from generated source (statically typed SDKs).

Two constraints are non-negotiable and shape every decision below:

1. **Purposeful.** Generated tests satisfy the
   [assertion contract](../../compat/AGENTS.md#assertion-contract) — observable
   state is verified. "The call didn't throw" must be *structurally impossible*
   to emit, not merely discouraged.
2. **Reliable.** The CI gate is zero failures
   ([compat/AGENTS.md § Baseline & uniformity](../../compat/AGENTS.md#baseline--uniformity-policy)),
   and quarantine needs a reviewer's approval. Mass-generating flaky tests would
   turn a working gate into an amnesty queue. Generation must therefore arrive
   behind a mechanical soak, not straight into the gate.

Non-goals: generating *emulator* code (that is
[level2-codegen.md](./level2-codegen.md) Track 3); *synthesizing behavioural
tests from the model* (behaviour cannot be derived from shapes — it is ported to
**hand-authored IR scenarios**, written once and executed by every backend,
§3.11); generating IaC stacks (§3.8); testing services Overcast will never route
at full operation depth (§3.9).

> **Owner decision, 2026-08-03:** the endgame is **IR-first for all compat
> tests**, not "generation as a floor beneath hand-written groups". The existing
> hand-written groups document *what* to test; how it is executed is not
> precious, provided the result is reliable and genuinely exercises the SUT
> through each suite's real SDK path. Per-language test code becomes the
> audited exception (§3.11), because at this volume, manual maintenance that
> scales with coverage will simply be ignored. Sections below that describe
> generated coverage as "additive" describe the **rollout posture**, not the
> steady state.

---

## 2. Current state (verified 2026-08-03)

> **Re-verified 2026-08-23: phase G0 is complete and G1 is partly done.**
> What landed, all under #1113:
>
> | Deliverable | PR |
> | --- | --- |
> | `--shard i/n` over the existing `OVERCAST_COMPAT_GROUPS` plumbing | #1356 |
> | `suites`-scoping amendment (§3.6, §7.2) + its lint, and the `service`-key lint (§7.7) | #1357 |
> | `registry.generated.json` + `registry.generated.schema.json`, `--generated-registry-file`, `candidate`/`gated` gate semantics | #1367 |
> | `internal/awsmodel` — the shared Smithy AST reader (G1) | #1359 |
> | `compat/baseline.json` → `compat/baseline/<suite>.json` + a per-shard size budget | #1370 |
>
> **G1's pruned shape snapshot is also done**, but via
> [inert-tier-rollout.md](./inert-tier-rollout.md) Phase I1 rather than this
> plan — §3.7 said "build once, whichever plan gets there first", and that plan
> got there first. `models/aws/shapes/` and the `shapes-sha256` in
> [models/aws/VERSION](../../models/aws/VERSION) exist today; the pruner is
> `cmd/awsmodelgen/shapes.go`, and it is already a consumer of
> `internal/awsmodel`. **Do not build a second distillation.**
>
> **Still unimplemented: `cmd/compatgen` and `compat/model/`** — the IR and
> recipe schemas, `--scaffold`/`--review-report`/`--explain`, and `gaps.json`.
> That is the whole of what stands between here and the G2 pilot. G0's tail is
> also outstanding: the seven per-suite loaders and `compat/mcp.go` do not yet
> read the generated sibling, which is harmless only while it stays empty.
>
> **This section's counts are a 2026-08-03 snapshot and have drifted.**
> Recomputed 2026-08-23: `compat/suites/registry.json` is **140 groups / 790
> tests / 36 services**; the baseline is **5,404 entries** across eight shards
> (largest `dotnet-sdk.json` at 127,377 B of a 512 KiB per-shard ceiling) —
> 3,230 `pass`, 2,137 `skip`, 36 `unimplemented`, 1 `na`, **0 `fail`**;
> `compat/parity-debt.json` holds **325** entries. Capabilities totalled
> **1,434 rows — 1,240 Supported / 154 Unsupported / 28 Inert / 12 Partial**
> at the 2026-08-21 check (Auto Scaling promoted out of inert by #474, Backup
> made a real REST implementation via #815/#904, `StatusPartial` in live use).
> The alias table has 16 entries and has moved within `registry_data.go`, so
> the line-number citations below are approximate. Treat the generated
> artifacts named in this section as authoritative and recompute before
> acting; do not trust the prose numbers.

> **Re-verified 2026-09-05: G0 is complete and G1 is landing.** Wave 1 of #1113
> closed G0's loader tail, built `cmd/compatgen`, and turned up one new
> prerequisite for G2 — so the paragraph above naming `cmd/compatgen`,
> `compat/model/` and the loader tail as outstanding is superseded. State of the
> wave at this commit:
>
> | Deliverable | Merged | Open |
> | --- | --- | --- |
> | **G0 tail** (#1393, closed) — the seven suite loaders and `compat/mcp.go` read `registry.generated.json` | java-sdk #1680, node-js-sdk #1683, python-sdk #1682, go-sdk #1685 (carries `compat/mcp.go`), cli #1687, rust-sdk #1694, dotnet-sdk #1697 | — |
> | **G1** (#1394) — `sqs` added to the pruned shape snapshot; `cmd/compatgen` and `compat/model/` | #1684 | #1709 |
> | **G2 prerequisite** (#1700, closed) — every impl key qualified `group:test` | dotnet-sdk #1725, go-sdk #1711, python-sdk #1712, java-sdk #1713, rust-sdk #1714, cli #1715, docs #1716 | — |
>
> **The loader contract, decided once so all eight loaders agree.** A
> missing generated file loads as empty. A present but malformed one — bad JSON,
> an unsupported `version`, a group missing `generated`/`state`/`suites`, or a
> group name a hand-written group already owns — is a load error. Concatenation
> is hand-written first, generated after. Every loader gained an optional
> **scenario backend** hook, consulted for any test with no static impl,
> hand-written or generated, so the G6 port of a hand-written group to an
> authored scenario needs no loader change. Until the G2 interpreters exist, a
> generated test scoped to a suite with neither an impl nor a backend is a
> **`fail`** carrying exactly `generated group "<group>" is scoped to <suite>
> but <suite> has no scenario backend` — never `skip` and never `na`, because
> `suites` is derived from backend availability (§3.6), so a suite that cannot
> run a group it is named in is a generator or loader bug. `candidate` state
> keeps that out of both gates until promotion.
>
> **`suites` scoping is uniform across all eight loaders (#1737).** `go-sdk`,
> `cli`, `python-sdk` and `node-js-sdk` honoured `suites` for *every* group from
> the start of G0 — it replaced their `service == "cdk"` carve-out.
> `java-sdk`, `dotnet-sdk` and `rust-sdk` honoured it for **generated groups
> only** until #1737, because all three loaded `cdk-lifecycle` and recorded its
> 35 tests as `skip` in `compat/baseline/<suite>.json`, and the PR-time baseline
> lint rejected every removed expectation outright. Aligning them therefore meant
> re-seeding those three shards — "changing what CI measures means re-seeding,
> not comparing"
> ([compat/AGENTS.md § Baseline & uniformity](../../compat/AGENTS.md#baseline--uniformity-policy))
> — which #1737 did in one PR alongside the loader change. The same PR made that
> lint registry-aware: a removal is now an issue only while the pull request's
> own registry still asks that suite to run the test, so scoping a group away
> reports an informational line instead of 105 phantom downgrades. Re-seeding
> remains the route for a configuration change, where the row is one a run would
> still produce.
>
> **The new G2 prerequisite: #1700, qualify every impl key — done.** Six suites
> registered bare `"<test>"` keys — `go-sdk` 487, `cli` 513, `python-sdk` 487,
> `java-sdk` 487, `dotnet-sdk` 208, `rust-sdk` 170; `node-js-sdk` already
> qualified everything by construction. Every loader refuses a bare key the
> moment two groups declare that name, so the first generated SQS group —
> `CreateQueue`, `SendMessage` and the rest beside `sqs-queues` and
> `sqs-messages`, because a generated test's name is the PascalCase operation
> name (§3.3) — would have aborted six suites at startup. Each rewrite was
> mechanical and proved binding-identical, and each suite gained a registration
> test that refuses a bare key. There is deliberately **no** registry-side
> ambiguity lint: a shared test name is normal, and a lint against it would fail
> on the generator's own naming convention at every model refresh, against
> §3.11's zero-human-actions rule — a first revision of #1716 built one and
> removed it for exactly that reason. The mechanical pass also found two latent
> registration faults, neither of which changed a binding: `python-sdk`'s
> `GetSendQuota` sat under the `ses-identities` section comment while the
> registry's only owner is `ses-send`, and `cli`'s `lambda.go` carried two dead
> bare keys duplicating qualified entries for the same two tests.
>
> **Recomputed at this commit**, from the checked-in artifacts:
> `compat/suites/registry.json` is **141 groups / 796 tests / 36 services**;
> `compat/baseline/` holds **5,467 entries** — 3,281 `pass`, 2,149 `skip`, 36
> `unimplemented`, 1 `na`, **0 `fail`** — with `dotnet-sdk.json` the largest
> shard at 128,996 B of the 512 KiB ceiling; `compat/parity-debt.json` holds
> **327** entries; and `compat/suites/registry.generated.json` is still
> `groups: []`, which is what keeps G0's empty-file gate meaningful.

> **Model-utilisation audit, 2026-09-06 (#1795).** Counted from the files at
> the revision `models/aws/VERSION` pins: **12 of the 46 traits** the pruner
> kept were read by any shipped consumer, and 26 were held for an inert
> generator that does not exist yet. (#1804 has since made it 13 of 47, by
> adding `aws.protocols#awsQueryCompatible` and reading it in the same PR.)
> **Zero** Smithy `resource` shapes appear in any committed snapshot (121 of
> 426 upstream model files carry any), so §3.4's and §3.7's expectation that
> resource bindings would supply lifecycles
> was false for every service in scope and both pilots use name clustering —
> those two sections now say so. Three derivations the pilots had typed by
> hand reproduce from the model exactly: `notFound.error` from the read's
> modeled errors, `list.itemsPath` from `@paginated.items` or the sole list
> member (6 of 6 pilot values), and 31 of the organizations recipe's 83
> `neverProbe` lines from a default-deny verb rule — zero misses, zero
> effective over-refusals. #1795 moves all three into the generator, the
> recipe value staying as an override, and makes `-scaffold` name the rule
> behind every value it proposes.

> **G2 is code complete — 2026-09-06.** The three interpreters are on `main`,
> the generated registry is no longer empty, and the pilot has run in all three
> suites. This supersedes the 2026-09-05 note's account of what is outstanding.
> What landed, all under #1113:
>
> | Deliverable | PR |
> | --- | --- |
> | **G2 interpreters** (#1768) — one PR per suite, each flipping its own entry in `scenarioBackends` | `python-sdk` #1787, `node-js-sdk` #1788 (review fixes #1796), `cli` #1790 |
> | Teardown runs after a failed setup, in the harnesses that skipped it | `cli` #1790, `go-sdk` #1808, `node-js-sdk` #1812 — `python-sdk` never had the fault |
> | **Candidate → gated soak** (#1789, closed) — `--promote-generated`, the `promotions.json` ledger, the nightly `promote` job | #1792, review fixes #1798 |
> | **The interpreter rules pinned** in [compat/model/README.md](../../compat/model/README.md), with a shared error-matching fixture set every suite runs | #1817 |
> | **Model utilisation** (#1795, closed) — shared snapshot vocabulary, `awsQueryCompatible` in the snapshot and header, derived `notFound.error` and `list.itemsPath`, default-deny probes, scaffold provenance, the §3.4/§3.7 corrections | #1797, #1804, #1800, #1809, #1802, #1803 |
> | **Loader uniformity** (#1737, closed) — `suites` scoping on every group in all eight loaders, three baseline shards re-seeded, the baseline-change lint made registry-aware | #1794 |
> | Compat endpoint defaults to `127.0.0.1`; `python-sdk`'s scenario client cache shared across groups | #1807 |
> | **Fidelity bugs the programme found in the emulator** | #1750 (organization ID shape, closing #1736), #1816 (SQS `x-amzn-query-error` header, closing #1810) |
>
> `scenarioBackends` in
> [cmd/compatgen/registry.go](../../cmd/compatgen/registry.go) reads `cli`,
> `node-js-sdk`, `python-sdk`, so `compat/suites/registry.generated.json` now
> carries the **seven pilot groups, 56 tests**, every group `candidate` and
> scoped to those three suites.
>
> **The pilot run.** Each interpreter PR ran the seven groups three times
> against a local Overcast started by `scripts/run-test-instance.sh`, with a
> distinct run id each time: **30 `pass`, 27 `unimplemented`, 0 `fail`, 0
> `skip`**, identical test for test across all three runs in every suite. Zero
> trace — `ListQueues` and `ListPolicies` both answer `[]` afterwards — and the
> four hand-written `sqs` groups are untouched and still pass, 21 of 21. That
> tally was taken before #1809 refused `CancelMessageMoveTask` as a write and
> took it out of `sqs-gen-probe`; the corpus at this commit is 56 tests, so the
> same run today reads **30 / 26 / 0 / 0**. That last figure is arithmetic on
> the artifacts, not a fourth measurement.
>
> **Wall clock, measured 2026-09-06** over the seven groups against a slim image
> on loopback, and recorded in #1801: `node-js-sdk` **1.2 s**, `python-sdk`
> **1.6 s**, `cli` **23 s**. §4.3's ≤ 90 s local budget is met — the slowest
> suite takes a quarter of it, and all three together 26 s. The earlier 77 s
> for `cli` (#1790) was not spawn cost: on a dual-stack host `localhost`
> resolves to `::1` first while the container publishes IPv4 only, so every new
> connection paid a ~2 s IPv6-then-IPv4 fallback. #1807 made `127.0.0.1` the
> default endpoint everywhere and the fallback went with it. What is left is
> genuine process-spawn cost, and §3.10 says where it binds.
>
> **Recomputed at this commit**, from the checked-in artifacts:
> `compat/suites/registry.json` is **141 groups / 797 tests / 36 services**;
> `compat/suites/registry.generated.json` is **7 groups / 56 tests**, all
> `candidate`; `compat/baseline/` holds **5,369 entries** — 3,286 `pass`, 2,046
> `skip`, 36 `unimplemented`, 1 `na`, **0 `fail`** — with `node-js-sdk.json` now
> the largest shard at 129,021 B of the 512 KiB ceiling;
> `compat/parity-debt.json` holds **327** entries; `compat/model/gaps.json`
> holds **32** refusals, every one `never-probe` (29 `organizations`, 3 `sqs`);
> and `compat/model/promotions.json` is `groups: {}`, no group having soaked
> yet. Candidate groups gate nothing, which is why 56 generated tests × 3 suites
> do not appear in the baseline.
>
> **Both of G2's remaining items are done (2026-09-06).**
>
> - **The first candidate → gated promotion** (§4.2 criterion 4) happened by
>   itself: the nightly `promote` job opened #1871 from three agreeing runs and
>   it merged, gating all nine generated groups. The one thing it found was a
>   test written to break at exactly this moment —
>   `TestCheckedInGeneratedRegistryLeavesGatesUnchanged` pinned "every generated
>   group is `candidate`" — and #1879 rewrote it to pin candidate-vs-gated
>   behaviour, non-vacuously in either file state.
> - **#1801, running a probe group's tests in parallel within the group**, landed
>   as #1823 (cli's probe group 21 s → 5 s; per-group results identical).
>
> **#1813, the §4.2 criterion 5 regeneration demonstration, is done** — the OU
> lifecycle recipe (#1818) and then the inert emulator half. Regeneration after
> the implementation changed no byte of the corpus, and the same generated
> tests went `skip`/`unimplemented` → `pass` in all three interpreters; the
> §4.2 note dated 2026-09-06 carries the before/after table.
>
> #1768 closed 2026-09-06.

> **G3 is done — 2026-09-06.** All four typed backends landed, one PR each
> (plus two go-sdk follow-ups), all under #1820: `go-sdk` (#1830, then #1836
> for emit-time SDK type resolution and #1833 for the precedent notes),
> `java-sdk` (#1851), `dotnet-sdk` (#1848), `rust-sdk` (#1853). Every one
> produces results identical, test for test, to the three interpreters and to
> each other: **39 `pass` / 23 `unimplemented` / 0 `fail` / 0 `skip`**, three
> runs each. `compat/suites/registry.generated.json` is still the nine
> pilot groups / 62 tests (all `gated` since #1871, later the same day), and
> every group's `suites` now lists all seven backends: `cli, dotnet-sdk, go-sdk, java-sdk, node-js-sdk,
> python-sdk, rust-sdk`. `compat/model/gaps.json` is unchanged at 29 entries,
> all `never-probe` — no emitter refused anything in the pilot corpus.
>
> **The binding decision (§3.2) resolved differently than the plan predicted
> for `dotnet-sdk`.** §3.2 said .NET would need the same emit-time SDK lookup
> as Go. Measured against the pinned AWSSDK v4 major, it does not: every
> value-typed member is `Nullable<T>`, so an explicit zero is sent rather than
> dropped — `ScenarioRequestNullabilityTests` enforces this by reflecting over
> every emitted `<Op>Request`, rather than leaving it observed once. `java-sdk`
> and `rust-sdk` confirmed the half of the prediction that was already right:
> `JavaSdkWireFactsTest` captures `ReceiveMessage`'s `VisibilityTimeout: 0` on
> the wire (every AWS SDK for Java v2 scalar is boxed), and Rust's fluent
> setters take the value itself, never an `Option`. Of the four typed
> backends, only `go-sdk` reads the vendored SDK at emit time; the other three
> derive everything from the pinned model. §3.2 below is corrected to say so.
>
> **A second axis the model cannot answer: SDK *existence*.** The pinned
> snapshot can outpace a vendored SDK's own release history, and five
> Organizations operations in `organizations-gen-probe`
> (`DescribeResponsibilityTransfer`, `ListInboundResponsibilityTransfers`,
> `ListOutboundResponsibilityTransfers`, `ListAccountsWithInvalidEffectivePolicy`,
> `ListEffectivePolicyValidationErrors`) did not exist in java's, dotnet's or
> rust's *original* pinned SDK (go-sdk's pin already carried them). Had
> go-sdk's pin been behind too, it would have turned the gap into a scoped
> generation-time refusal, because it already asks the SDK; the other three
> have no such lookup, so a missing operation surfaces as a suite-wide compile
> error instead, which each answered by pinning the SDK at least as new as the
> snapshot — java-sdk's BOM `2.31.7` → `2.40.0`, dotnet-sdk's
> `AWSSDK.Organizations` to `4.0.101.4` (with `AWSSDK.Core` to `4.0.102.3`),
> rust-sdk's `aws-sdk-organizations` to `1.102.0`. `compat/AGENTS.md` § SDK
> version pinning now states the trigger: a generated corpus calling an
> operation newer than the pin is grounds to bump inside the feature PR,
> rather than splitting the bump out as the section otherwise asks.
>
> **501 classification stopped sniffing composed messages.** The six-field
> failure message embeds the exact params JSON sent, where a run id or a port
> number can itself spell "501" — the same fault #1790 fixed for `cli`.
> `java-sdk` (`ComposedFailure`/`Unimplemented`), `dotnet-sdk`
> (`IComposedFailure`) and `rust-sdk` (a stripped classification tag) each
> replaced message-sniffing with a typed classification decided where the raw
> SDK error is still in hand.
>
> **Where the shared error-fixture corpus runs is not yet one convention
> across the four typed backends.** `go-sdk` answers `compat/model/testdata/errors`
> from a root checkout, in `compat-suite-unit-tests` plus a host `go test`;
> `java-sdk` and `rust-sdk` answer it in the same job (`mvn -B test` / `cargo
> test`, from the checkout) while their own Docker image builds — context
> `compat/suites/` — cannot reach the corpus and abort there instead of
> failing; `dotnet-sdk` widened its image's build context to `compat/` and
> answers the corpus inside the image build. Not a G3 gap — filed as a
> follow-up, **#1865** (§7).
>
> **Shared across the three PRs after go-sdk's**: `cmd/compatgen/emit_shared.go`
> (`callsOf`, `valueKind`, `numberOf`, `uniqueNames`, `refusals`, `camel`,
> `sourceWriter`, `sourceEmission`, the `sourceBackends` table), `markUnable`
> and `checkStaleEmitted` in `main.go`, and `renderEnv.model` — every
> `-explain -lang <x>` renders through its emitter's own naming table,
> asserted per language.
>
> **G4 fleet rollout is unblocked.** §5's G4 row names no first service to
> recompute against; do not invent one here.

> **G4 wave 1 is done at Tier 0, and wave 2 is chosen by measurement —
> 2026-09-07.** Wave 1 named the three services with a committed shape
> snapshot: `batch` (REST-JSON, #1881), `elastic-load-balancing` (AWS Query,
> #1882, plus the classification fix #1889 closing #1884), `servicediscovery`
> (AWS JSON 1.1, #1887) — the same trio as `inert-tier-rollout.md` Phase I4.
> All three PRs are **merged**, stacked bottom-up onto `main`. Each service is
> Tier 0 today, so every result is exactly what §3.1's second consequence
> predicts — no `pass`, no `fail`, every probe `unimplemented`, every
> lifecycle test `skip` behind a failed setup — measured once per suite
> against an Overcast built from each branch (Tier 0 needs no soak):
>
> | Service | pass | unimplemented | skip | fail | wall clock |
> | --- | ---: | ---: | ---: | ---: | ---: |
> | `batch` | 0 | 11 | 34 | 0 | go-sdk 4 s / cli 3 s |
> | `servicediscovery` | 0 | 3 | 25 | 0 | go-sdk 3 s / cli 2 s |
> | `elastic-load-balancing` | 0 | 5 | 12 | 0 | go-sdk 2 s / cli 3 s |
>
> The `elastic-load-balancing` row is the result **after** #1889; before it,
> #1884's fault (ELB Classic and ELBv2 share the `elasticloadbalancing`
> signing name, and the emulator answered every Classic request from ELBv2)
> gave 1 `pass` / 2 `fail` instead of two of those five `unimplemented`s.
>
> **Recomputed at this commit, from the checked-in artifacts:**
> `compat/suites/registry.generated.json` is **22 groups / 152 tests** — the
> nine pilot groups (`organizations`, `sqs`) are `gated`; the thirteen wave-1
> groups (`batch` 6, `elastic-load-balancing` 3, `servicediscovery` 4) are all
> still `candidate`, because the wave's first nightly promotion has not run
> yet (tracked open in #1883). `compat/model/gaps.json` holds **65** entries —
> 59 `never-probe`, 3 `unsupported-tag-shape` (ELB's `RemoveTags`, whose
> `TagKeyOnly` list of structures `detectTagShape` does not accept), 3
> `rust-emit-unsupported`. Those three (`batch-gen-jobdefinition`,
> `elastic-load-balancing-gen-loadbalancer`, `servicediscovery-gen-service`)
> were scoped to six backends with `rust-sdk` excluded while #1885 (the rust
> emitter could not spell a composite nested inside a structure literal) was
> open; #1890 fixed it the same day and regained the three rows, so every
> wave-1 and pilot group now lists all seven.
>
> **Four generator faults, found and fixed inside the wave** — `batch` and
> `elastic-load-balancing` are the corpus's first REST-JSON and first AWS
> Query services, so each exercised `cmd/compatgen` in a way no JSON-1.1
> service had: `@clientOptional` had been read by `RequiredMembers` as "not
> required," a fault no other service's snapshot ever exposed because Batch
> alone marks all 182 of its `@required` members `@clientOptional` too — five
> probes reported `fail` (an SDK validation error) instead of the honest
> `unimplemented` a 501 gives, until `RequiredMembers` was made to honour
> `@required` outright; the java emitter's camel-case boundary splitter
> consumed a letter at the join (`ListJobsByConsumableResource` →
> the invalid `ListJobsByconsumableResourceRequest`); the rust emitter passed
> a bare shape name through where smithy-rs pascal-cases it (`CEState` → the
> crate's own `CeState`); and the `cli` backend ran its generated calls
> unsigned, which Overcast's REST fallback answers as S3 rather than the
> operation's own 501 — 11 of batch's 45 tests read `fail` unsigned and
> `unimplemented` once signed. `scripts/validate-compat-registry.py` was also
> widened to accept, for the generated registry only, a service the pruned
> shape snapshot covers but the emulator has no capability row for at all —
> G4 breaks §7 item 7's "generated groups will use the capability key by
> construction" assumption on purpose. `elastic-load-balancing` landed the
> cli/python-sdk name-override tables (§7 item 3) for the four services whose
> per-backend name is not the endpoint prefix (`elasticloadbalancing`,
> `monitoring`, `email`, `states`).
>
> **Follow-ups filed from the wave, none blocking:** #1885 (rust nested
> composites, above; fixed by #1890, merged 2026-09-07), #1886 (`$name`
> exceeds an undeclared AWS length limit — ELB Classic's load balancer name
> caps at 32 characters and `$name` is 54; §7 records it as an open
> question), #1878 (`rust-sdk` needs an XML→document conversion before a
> Query/REST-XML service is implemented — blocks
> `elastic-load-balancing`'s inert-tier implementation from reaching
> `rust-sdk` parity, not the recipe merged here), #1865 (one convention for
> where the shared error-fixture corpus runs, carried over from G3). #1884 is
> closed, fixed by #1889 above.
>
> **Wave 2 chosen by measurement, 2026-09-07 (#1883).** The pruner was run
> read-only against the pinned models (revision `06544fdc`, no tracked file
> touched) over nine candidate services, scored by **implemented operations
> per KB of snapshot** — how much immediate pass/fail signal a recipe buys —
> rather than by smallest operation count, the selector §7 item 1 had assumed
> would still apply at this scale:
>
> | Service | Bytes | Ops | B/op | Implemented ops | impl/KB |
> | --- | ---: | ---: | ---: | ---: | ---: |
> | `secretsmanager` | 32,256 | 23 | 1,402 | 19 | 0.603 |
> | `sns` | 43,009 | 42 | 1,024 | 22 | 0.524 |
> | `iam` | 181,071 | 180 | 1,006 | 74 | 0.418 |
> | `kms` | 86,962 | 54 | 1,610 | 34 | 0.400 |
> | `cognito-idp` | 197,617 | 264 | 749 | 70 | 0.363 |
> | `lambda` | 188,486 | 176 | 1,071 | 61 | 0.331 |
> | `dynamodb` | 122,247 | 116 | 1,054 | 21 | 0.176 |
> | `s3` | 246,352 | 112 | 2,200 — over the 1,608 gate | 45 | 0.187 |
> | `ec2` | 1,899,684 | 1,602 | 1,186 | 79 | 0.043 |
>
> **Correction, 2026-09-08:** the `Ops` and `B/op` values for the four wave-2
> rows above were double-counted when this note was first written; `iam` is
> **180** modeled operations, not 360, and the same halving applies to
> `secretsmanager` (23), `sns` (42) and `kms` (54) — each service's own recipe
> PR states the true count independently (#1897, #1900, #1899, #1919). `B/op`
> is recomputed from the corrected count, roughly doubling each of the four.
> `impl/KB`, the column that actually chose the wave, divides implemented
> operations by snapshot bytes and never read the wrong count, so the ranking
> and the wave it produced stand unchanged. The other five rows were produced
> by the same measurement run and are plausibly halved the same way, but no
> merged PR has independently re-stated their true operation counts, so they
> are left as measured rather than corrected on an assumption.
>
> **Wave 2 = `secretsmanager`, `sns`, `kms`, `iam`**: 343,298 B / 299 ops /
> 1,148 B/op / 149 implemented operations combined. The committed snapshot —
> five services, 307,535 B today, inside the 336 KiB budget
> `internal/awsapi/shapes_provenance_test.go` enforces — would grow to
> 650,833 B, so `maxShapeSnapshotBytes` needs raising once, deliberately, to
> 800 KiB (650,833 B × ~1.26 headroom, the same factor `inert-tier-rollout.md`
> §4.6 used to set 336 KiB): 2.6% of the 24 MiB fleet ceiling. Landing order:
> one PR adds the four snapshots and the budget raise, then one recipe PR per
> service — `secretsmanager`, `sns`, `kms` in parallel, `iam` after (it alone
> is 180 operations). **The snapshot/budget PR merged as #1891** (2026-09-07).
> All four recipe PRs are merged too, as of the dated note below — wave 2 is
> done.
>
> Two findings for `inert-tier-rollout.md`, not blockers here: `s3` is the
> only measured service over the per-op gate, and needs structural pruning or
> a more compact encoding before any wave takes it on; and the ten smallest
> Tier-0 JSON-family services (4-19 operations each) **all** exceed the 1,608
> B/op gate, several by 30-90%, because fixed per-service shape overhead
> dominates at a tiny operation count (seven of the ten are query/verb APIs
> §3.6 keeps at Tier 0 regardless) — so smallest-operation-count is the wrong
> selector for an inert-tier wave, and implemented-operations-per-byte is the
> one that produced a workable wave 2. The measurement also caught
> `inert-tier-rollout.md` §4.6 citing a stale model revision (`66e973ca`);
> `models/aws/VERSION` now pins `06544fdc`, though the five committed
> snapshots still total exactly 307,535 B either way.

> **G4 wave 2 is done, 2026-09-07.** All four recipe PRs are merged:
> `secretsmanager` (#1897), `sns` (#1900), `kms` (#1899), `iam` (#1919, the
> wave's largest at 180 modeled operations). Each is the first generated
> corpus entry for a service Overcast actually implements, so results are
> real `pass`/`fail` rather than wave 1's uniform `unimplemented`/`skip`:
>
> | Service | Groups / tests | pass | fail | unimplemented |
> | --- | --- | ---: | ---: | ---: |
> | `secretsmanager` | 2 / 18 | 18 (17 in `rust-sdk`) | 0 (1 in `rust-sdk`, below) | 0 |
> | `sns` | 3 / 29 | 16 | 2 | 11 |
> | `kms` | 2 / 37 | 32 | 1 | 4 |
> | `iam` | 6 / 113 | 72 | 2 | 39 |
>
> `secretsmanager` held 18/18 in `go-sdk`, `cli`, `python-sdk` and
> `node-js-sdk`; `rust-sdk` read 17/18 (one fidelity defect, below); `java-sdk`
> and `dotnet-sdk` compiled and ran but could not reach the emulator from the
> Windows host the PR was verified on (`--network host` does not reach
> loopback there), so both are unconfirmed rather than failing — a
> platform-specific gap, not a defect. Wall clock was seconds per run
> throughout except `iam`'s `cli` suite, which spawns a process per call and
> averaged ~45 s across three runs; `iam`'s `go-sdk` runs stayed under 3 s.
>
> Every failure and every unconfirmed-by-design gap is a filed emulator
> defect, never a recipe fault: `secretsmanager` — #1901 (`DescribeSecret`
> serialises unset `Tags`/`Description` as `null`/`""` instead of omitting
> them) and #1902 (`DeleteSecret` ignores `RecoveryWindowInDays`, and a forced
> delete of an already-deleted secret answers `ResourceNotFoundException`
> where AWS does not); `sns` — #1911 (`ConfirmSubscription` accepts any token
> and answers with an ARN for a subscription that never existed) and #1912
> (`CreateTopic` ignores its `Attributes` map); `kms` — #1906 (`KeyMetadata`
> omits `AWSAccountId` on `CreateKey`/`DescribeKey`) and #1907 (six further
> wire divergences found while authoring the recipe); `iam` — #1920 (both
> failures: `PermissionsBoundaryType` is emitted as the enum member name
> `Policy` instead of AWS's wire value `PermissionsBoundaryPolicy`).
> Generator/IR findings, filed rather than worked around in a recipe: #1908
> (go-sdk's failure message renders the SDK input struct, not the evaluated
> params), #1909 — **fixed by #1917**, `detectTagShape` now accepts KMS's
> `{TagKey, TagValue}` tags and ELB's key-only untag lists, #1910 (a `$ref`
> bound to a blob member is refused only by the four source emitters, so the
> refusal silently narrows a group's `suites` rather than surfacing in
> `gaps.json`), #1921 and #1922 (added to §7 below), #1923 — **fixed by
> #1926**, `binds` now wraps a scalar export into a list member, #1924 —
> **fixed by #1928**, hand-written harnesses now classify `unimplemented` by
> HTTP status rather than by "501" appearing in composed failure text.

> **Promotion, 2026-09-07.** The hand-triggered nightly (run 34066538988)
> gated ten clean wave-1/wave-2 groups via #1925 — `batch-gen-probe`,
> `elastic-load-balancing-gen-probe`, `iam-gen-group`,
> `iam-gen-instanceprofile`, `iam-gen-policy`, `iam-gen-probe`,
> `kms-gen-probe`, `secretsmanager-gen-version`, `servicediscovery-gen-probe`,
> `sns-gen-probe` — bringing the gated total to 19 (nine already gated from
> the pilot: the five `organizations-*` groups and four `sqs-*` groups). The
> sixteen groups still `candidate` in `compat/model/promotions.json` either
> carry a known defect failure (`iam-gen-role`, `iam-gen-user` — #1920;
> `kms-gen-key` — #1906; `secretsmanager-gen-secret` — #1901; `sns-gen-topic`,
> `sns-gen-subscription` — #1911, #1912) or are still Tier-0 `skip` groups
> awaiting their service's inert-tier implementation: `batch-gen-*` (five
> groups), `elastic-load-balancing-gen-loadbalancer` and
> `-loadbalancerpolicy`, `servicediscovery-gen-instance`, `-namespace` and
> `-service`.

> **G3 follow-ups landed, 2026-09-07.** #1917 fixed #1909 (above). #1918
> fixed #1896 — every backend now reads a nested `Error.Code`, both a Query
> `<ErrorResponse><Error><Code>` and a REST-XML bare `<Error><Code>` body,
> proved by a shared fixture — closing the gap the §5 G4 row named as
> blocking before the first Query/REST-XML service reached Tier 1 (`sns` and
> `iam` are both Query-protocol, and are the proof). #1926 fixed #1923
> (above); the ELB and KMS tag trios move to the derived `binds` path and
> regenerate byte-identically. #1928 fixed #1924 (above). #1894 landed the
> `rust-sdk` XML→document conversion, closing #1878. #1895 closed #1865: one
> convention for where the shared error-fixture corpus runs, across all four
> typed backends (§7). #1930 is filed, not landed: the suite modules have no
> lint/format gate of their own.

Counts below were computed from the checked-in generated artifacts, not from
`STATUS.md` — **`STATUS.md` prose is stale** (it describes Shield as "Stub — all
ops return 501" while `internal/capabilities/all.gen.go:*` declares five Shield
operations `StatusSupported`). Treat `internal/capabilities/all.gen.go`,
`internal/awsapi/manifest.gen.go`, `compat/suites/registry.json` and
`compat/baseline/` as the sources of truth.

### 2.1 The model universe

- [internal/awsapi/manifest.gen.go:1](../../internal/awsapi/manifest.gen.go) —
  generated by `cmd/awsmodelgen`, `SourceRevision =
  "66e973cadf6b6e909b200217d0d6065e49445a9a"`. **18,850 operations across 426
  modeled service identities** — 424 distinct keys after the alias table at
  [internal/awsapi/registry_data.go:71-84](../../internal/awsapi/registry_data.go)
  merges 52 identities onto Overcast's 50 registered service keys (the sibling
  plans count in identity space; both figures describe the same corpus).
- Of those, **4,440 operations belong to the 52 identities (50 Overcast
  services) that are registered**; 14,410 belong to 374 identities that are not.
- The per-operation metadata is deliberately routing-only —
  [internal/awsapi/manifest.go:37-50](../../internal/awsapi/manifest.go):
  `Service, ServiceShape, SDKID, APIVersion, Name, Protocol, Protocols,
  TargetPrefix, HTTPMethod, URI`. **There are no input or output shapes.**
- **The raw Smithy AST is not vendored.**
  [models/aws/VERSION](../../models/aws/VERSION) records
  `source/revision/model-date/manifest-sha256/license/format` and states that
  only the compact generated manifest is committed;
  [aws-api-operation-coverage.md §3](./aws-api-operation-coverage.md) explains
  why (size, and nothing at runtime needs it). Regeneration requires a local
  `api-models-aws` checkout at the pinned revision, supplied via
  `AWS_MODELS_DIR`, and the generator validates the match.

So: operation *names* and *protocols* are available offline today; input
members, required-ness, enums, constraints, output members, error shapes and
Smithy `resource` lifecycle bindings are **not**. §3.7 resolves that.

### 2.2 What the emulator claims

[internal/capabilities/all.gen.go](../../internal/capabilities/all.gen.go)
(capgen-generated) declares **1,318 operations across 50 services**:

| Status | Count |
| --- | --- |
| `StatusSupported` | 1,116 |
| `StatusUnsupported` (always 501) | 154 |
| `StatusInert` | 48 |
| `StatusWIP` / `StatusPartial` | 0 declared today |

The five statuses are defined at
[internal/capabilities/capabilities.go:17-27](../../internal/capabilities/capabilities.go)
— `StatusInert` already exists and is exactly Tier 1. Capgen already refuses a
non-`DocOnly` capability that no manifest operation backs
([cmd/capgen/main.go:244-258](../../cmd/capgen/main.go)), so the capability
table and the model are kept in agreement.

### 2.3 What compat actually measures

- [compat/suites/registry.json](../../compat/suites/registry.json) —
  **94 groups, 496 tests, 27 services, 466 distinct `(service, op)` pairs.**
  (The "~39 groups" figure in circulation is out of date.)
- Cross-referenced against the capability table:
  - **418 of 1,116 `StatusSupported` operations (37%) have a compat test.**
  - **900 declared capabilities have no compat test at all.**
  - **0 of the 48 `StatusInert` operations have a compat test.** The tier that
    exists specifically so CDK works is completely unmeasured by compat.
- [compat/baseline/](../../compat/baseline) — **3,367 entries, 532 KB** at the
  time of writing: 2,690 `pass`, 676 `skip`, 1 `na`, **0 `fail`** (the ratchet
  reached zero in #462 and `--max-failures 0` now asserts it absolutely). Since
  #1370 this is a directory of per-suite shards rather than a single
  `compat/baseline.json`; see the §2 note for current figures.
- [compat/parity-debt.json](../../compat/parity-debt.json) — 558 registry tests
  of debt, all in `rust-sdk` (297) and `dotnet-sdk` (261).

### 2.4 The harness is already registry-driven — this is the key enabler

Every suite loads `registry.json` and resolves a `TestName → impl` map, emitting
the shared sentinel for anything unimplemented:

| Suite | Loader |
| --- | --- |
| node-js-sdk | [src/lib/registry.ts:141-234](../../compat/suites/node-js-sdk/src/lib/registry.ts) |
| python-sdk | [lib/registry.py:35-125](../../compat/suites/python-sdk/lib/registry.py) |
| go-sdk | `internal/registry/registry.go` |
| cli | `internal/registry/registry.go` |
| java-sdk | `src/main/java/io/overcast/compat/registry/Registry.java` |
| dotnet-sdk | `Registry/RegistryLoader.cs` |
| rust-sdk | `src/registry.rs` |
| cdk | `src/runner.ts` (scoped groups only) |

The resolution rule is identical everywhere: try `"<group>:<test>"`, then the
bare test name, else emit `skip: not yet implemented in <suite> test suite`
([registry.ts](../../compat/suites/node-js-sdk/src/lib/registry.ts),
[registry.py](../../compat/suites/python-sdk/lib/registry.py)). **The qualified
form is now the only one anyone writes**: #1700 rewrote the six suites that
still registered bare keys and gave each a registration test that refuses one
(all seven of its PRs have merged and the issue is closed). The
bare fallback survives as a second line of defence, and is itself refused for a
name more than one group declares — which a generated group produces routinely,
since a generated test's name is the PascalCase operation name (§3.3). **A
generic scenario interpreter is one extra fallback
in that same lookup** — after the hand-written impl, before the
not-implemented sentinel. That hook landed with the G0 tail (#1393), so no
suite architecture changes.

Other facts the design leans on:

- `"suites": [...]` group scoping exists and is enforced by the parity checker
  ([registry.schema.json:39-43](../../compat/suites/registry.schema.json));
  out-of-scope suites carry neither an implementation nor parity debt.
- Group filtering already exists end to end: `compat/runner.go:336` passes
  `OVERCAST_COMPAT_GROUPS` to every suite subprocess, and all eight runners read
  it.
- Groups run 8-way parallel inside a suite
  (`OVERCAST_COMPAT_PARALLEL_SLOTS`); suite processes run **sequentially** in
  the local runner ([compat/AGENTS.md:780](../../compat/AGENTS.md)) but CI runs
  one job per suite in a matrix
  ([.github/workflows/compat.yml:200-216](../../.github/workflows/compat.yml)).
- `cmd/compat` has no group/test/shard filter flags today — only `--suite`
  ([cmd/compat/main.go:53](../../cmd/compat/main.go)). Sharding needs one new
  flag over the existing env var.
- Resource-name hygiene is a review rule, not tooling: every resource name must
  embed its group token, because sibling groups run concurrently and prefix
  collisions caused the whole #388 flake cluster
  ([compat-baseline-and-uniformity.md](./compat-baseline-and-uniformity.md)).
  Generation must obey it *by construction*.

---

## 3. Design

### 3.1 What generation is actually for

Framing that resolves most of the design tension: **the scenario generator is
the Tier 1 conformance test generator.**

A Tier 1 inert service is defined precisely by a Create → Describe/List
round-trip with field verification, an Update read-back, and a Delete absence
check — which is exactly the assertion contract's required-roundtrip table, and
exactly what a model plus a small recipe can produce. Behaviour (Tier 2) is what
generation *cannot* express, and that is what hand-written groups keep doing.

Three consequences:

- During rollout, generated coverage is **additive and clearly marked**: it does
  not displace a hand-written group until that group is deliberately ported and
  proven equivalent (§3.11); where both cover an operation meanwhile, the
  hand-written one is the richer test and the generated one is the
  shape/lifecycle floor.
- A generated group written against a Tier 0 service records `unimplemented`
  today and **starts passing the day the service reaches Tier 1, with no test
  edit**. That is the payoff: `inert-tier-rollout.md` gets its acceptance gate
  for free, per service, in eight clients.
- [services-never-emulated.md](./services-never-emulated.md) services get **no**
  generated groups (§3.9): their honesty mechanism is the server-side 501 corpus
  plus the `NeverEmulated` policy marker on the dashboard, not permanent
  `unimplemented` rows.

### 3.2 D1 — Generation target: a scenario IR with two backend families (hybrid)

**Recommendation: one declarative scenario IR, executed by an interpreter in the
dynamically-invokable tools and compiled to source for the statically typed
ones.**

| Suite | Backend | Why |
| --- | --- | --- |
| `python-sdk` | **interpreter** | boto3 is dynamic *in production*: `boto3.client(svc)` + `getattr(client, xform_name(op))(**params)` is the ordinary public API, so the interpreter exercises the identical serialization path a real app does. botocore also ships the full service model locally, which the interpreter can use for input coercion. |
| `node-js-sdk` | **interpreter** | `new mod[`${Op}Command`](params)` against a generated service→module import map. Command classes are the public API; no private surface touched. |
| `cli` | **interpreter** | `aws <cli-service> <kebab-op> --cli-input-json '<json>'`. `--cli-input-json` accepts exactly the modeled input JSON, so one code path covers every operation with zero per-op flag mapping. |
| `go-sdk`, `java-sdk`, `dotnet-sdk`, `rust-sdk` | **generated source** | No public dynamic-dispatch API exists. Emit one test function per scenario step into `*_gen.go` / `*Gen.java` / `*Gen.cs` / `*_gen.rs`, compiled by the normal build. |

**Rejected: a generic typed-SDK invoker built on each SDK's protocol/marshaller
layer** (smithy-go middleware stacks, the Java SDK's internal marshallers). It
would be less code, but those APIs are internal and unstable, and using them
breaks the first core principle — *tests use the SDK exactly as production code
would* ([compat/AGENTS.md:78-81](../../compat/AGENTS.md)). The entire value of
running eight suites is that each exercises its own real typed serialization
path; a shortcut there deletes the reason the suite exists.

**Rejected: per-language source codegen everywhere** (no IR). It fixes nothing —
seven emitters instead of one IR plus three interpreters, and every recipe change
regenerates megabytes of source in seven languages.

**Rejected: an IR-only approach with no typed backends** — it would leave four
suites permanently behind, which the uniformity policy correctly treats as debt.

Debuggability is the interpreter approach's real cost, and it is paid explicitly:

- Every interpreter failure message must carry `group/test`, the operation, the
  **exact params JSON sent**, the assertion kind, expected vs actual, and the
  scenario file + step index.
- `go run ./cmd/compatgen --explain <group>/<test> --lang python` renders a step
  as idiomatic pseudo-code in any target language, so a failing generated test
  can be reproduced by hand in seconds.
- The typed backends generate readable source, so four of eight suites are
  greppable by construction — which also serves as a cross-check that the IR
  means what the interpreters think it means.

**Typed-backend binding decision (2026-09, #1830/#1831): a typed backend
resolves its SDK's field types at emit time, from the vendored SDK — never from
the model's nullability.** #1830 shipped `go-sdk` binding every input member
through a runtime helper (`b.Set("Member", &in.Member, v)`) for a real reason:
the pinned model cannot say whether the vendored Go SDK models a member as a
value or a pointer, and for the pilot service the two already disagree —
`ReceiveMessage`'s `MaxNumberOfMessages`, `VisibilityTimeout` and
`WaitTimeSeconds` target `NullableInteger` in `models/aws/shapes/sqs.json`,
which says pointer, and are plain `int32` fields in
`aws-sdk-go-v2/service/sqs`. An emitter deriving `aws.Int32` from the model
would not have compiled in the first service it was pointed at.

#1831 settled it the other way: **ask the SDK.** `cmd/compatgen` loads
`aws-sdk-go-v2/service/<pkg>` from the `go-sdk` suite's own module — the module
the emitted source is compiled in — with `golang.org/x/tools/go/packages`, and
writes each member as that field's declared type. What that buys is the
property a typed backend exists for: the emitted call is checked by a compiler,
which the three interpreters cannot offer. It also turns three run-time
failures into generation-time refusals (`go-emit-unsupported:<Member>`) — a
member smithy-go renamed or dropped, a field of a type no literal builds, and a
value-typed member set to its zero value, which the SDK omits from the request
entirely.

**Measured for the other three (2026-09, #1848/#1851/#1853): only `go-sdk`
needs the lookup.** This section originally predicted `dotnet-sdk` would need
the same emit-time lookup as Go, because the .NET SDK decides per member
whether a value-typed property is nullable and the model does not say. Measured
against the AWSSDK v4 major the suite pins, that is false: reflection over
`Amazon.SQS.Model.ReceiveMessageRequest` shows every value-typed member is
`Nullable<T>`, and a wire capture through an in-process `HttpListener` confirms
an explicit zero is sent rather than dropped —
`Tests/ScenarioRequestNullabilityTests.cs` reflects over every emitted
`<Op>Request` and fails the build if a value-typed property is ever not
`Nullable<T>`, so `dotnet-sdk` needs no SDK lookup and no zero-value refusal.
`java-sdk` and `rust-sdk` confirmed the half of the original prediction that
was already right: `JavaSdkWireFactsTest` sends `ReceiveMessage`'s
`VisibilityTimeout` as `0` through a real client and asserts it on the wire
(every AWS SDK for Java v2 scalar is boxed, so a builder setter takes the value
whatever the member's optionality), and Rust's fluent setters
(`.max_number_of_messages(i32)`) take the value itself, never an `Option`, so a
member's optionality never reaches the call site — `Option<T>` in the
generated Rust comes from the model's own required-ness, not from asking the
crate. Of the four typed backends, `go-sdk` alone resolves a field's type by
reading the vendored SDK at emit time; `java-sdk`, `dotnet-sdk` and `rust-sdk`
derive everything from the pinned model, each with its own measurement rather
than an assumption.

### 3.3 D2 — Passing constructs between tests: recipes, exports, bindings

This is the part the model cannot solve alone, so the design is explicit about
where the machine stops and a human starts.

**What Smithy gives us:** operation names, input/output *members* with types,
required-ness, enums, length/pattern/range constraints, error shapes, and — in
the raw AST — `resource` shapes with lifecycle bindings (`create`, `read`,
`update`, `delete`, `list`) and identifier members.
**What Smithy never gives us:** legal *values*, cross-service semantics (a
Lambda function needs an IAM role ARN that must look like a role ARN), ordering
constraints, or which fields are server-assigned.

So: **structure is generated, semantics are curated.**

#### Recipes (hand-curated, one file per service, model-scaffolded)

`compat/model/recipes/<service>.json`, schema-validated:

```jsonc
{
  "service": "sqs",
  "resources": [{
    "id": "queue",
    "create":  { "op": "CreateQueue", "params": { "QueueName": { "$name": "q" } } },
    "exports": { "url": "$.QueueUrl" },
    "derived": [{ "export": "arn",
                  "op": "GetQueueAttributes",
                  "params": { "QueueUrl": { "$ref": "queue.url" },
                              "AttributeNames": { "$lit": ["QueueArn"] } },
                  "path": "$.Attributes.QueueArn" }],
    "binds":   { "QueueUrl": "queue.url", "QueueArn": "queue.arn" },
    "read":    { "op": "GetQueueAttributes", "identityPath": "$.Attributes.QueueArn" },
    "list":    { "op": "ListQueues", "itemsPath": "$.QueueUrls", "identityPath": "$" },
    "delete":  { "op": "DeleteQueue" },
    "notFound": { "errorCode": "AWS.SimpleQueueService.NonExistentQueue" },
    "mutable": [{ "member": "Attributes.VisibilityTimeout", "from": "30", "to": "60" }],
    "requires": []
  }]
}
```

- `binds` is the reference model: an input member name → a context-bag path.
  This is what makes `create-bucket` before `put-object`, `role ARN` before
  `create-function`, `subnet` before `instance` work — `requires: ["iam.role"]`
  on `lambda.function`, and `binds: {"Role": "iam.role.arn"}`.
- The generator **scaffolds** a recipe skeleton (`--scaffold <service>`) from
  the model: proposed resources from Smithy `resource` shapes where present,
  else from Create/Describe/List/Delete name clustering; required members
  pre-listed; identifier members guessed. A human fills in values and reviews.
  Scaffolding is a time-saver, never an authority.
- `mutable` is required before any Update-family operation is generated — see
  §3.4.
- Setup instantiates a group's recipes in topological `requires` order;
  teardown reverses it, wrapping each delete individually, per the canonical
  teardown rules.

> **The shipped vocabulary is wider than this sketch (#1709, 2026-09-05).**
> `compat/model/recipe.schema.json` is the authority; the sketch above is the
> shape, not the field list. What the pilot needed and the schema now carries:
> `operations` (authored coverage in the IR's own assertion vocabulary, for
> operations the lifecycle roles do not reach — `PurgeQueue`, the batch calls);
> `read.consuming` for a read that changes state (`ReceiveMessage`);
> `read.exports`; a plural `reads`; `setupOnly` for a resource that exists only
> to be required (the DLQ); a resource with no `create` at all, for one that
> pre-exists (`DescribeOrganization`); `async`, which wraps in `eventually`
> every clause that verifies by calling the service again — the derived
> read-back, list-membership and absence clauses, and authored clauses too,
> leaving alone a clause that only re-reads the test's own response and an
> authored `eventually` whose budget its author already chose; `tags`;
> `mutable.op` and `mutable.readPath`; `create.assert`; and **`neverProbe`**
> (§3.5). Timestamp, blob and document literals are refused by design — there is
> no portable literal for them — though no operation in either pilot service
> reaches that refusal today. The emitter never produces an `errorCode` clause
> of its own; a recipe may author one, and neither pilot service does.
>
> **Authored coverage is held to the guards, not exempted from them.** Only a
> clause that makes a call of its own — a `readback`, a `listContains` or
> `absent` carrying its own `call`, or an `eventually` around one — counts as
> verifying anything. So an authored `create.assert` built only of
> `responseField` clauses does not satisfy the create's read-back requirement
> (`no-readback-path`), and an authored update-family operation (`Update*`,
> `Set*`, `Put*`, `Tag*`, `Untag*` — one classifier, shared with the derived
> path) whose clauses all read its own response is refused
> (`update-without-readback`), which is guard 3 applied to `operations`.
>
> **One §3.5 lint is still unwritten.** "`$name` used for every user-supplied
> identifier" is enforced by recipe review today, not by the generator. It has
> the material to check it — `namesIn` already collects every `$name` suffix,
> and refuses two resources in a group claiming the same one — but nothing
> objects to a bare string literal where a `$name` belonged. Add it before G4
> puts recipe authoring on a per-service cadence.

#### Value expressions (closed, tiny, total)

`$lit`, `$ref` (context path), `$name` (unique name, always
`{runId}-{groupToken}-{suffix}` so name hygiene holds by construction),
`$concat`, `$index`. No conditionals, no scripting, no arithmetic — eight
implementations must agree exactly. Path syntax is dot-plus-numeric-index only
(`$.Attributes.QueueArn`, `$.Queues[0].Name`), not full JSONPath.

#### Parameter binding algorithm (generator, offline, deterministic)

For each modeled required input member, in order:

1. an explicit `binds` entry in an in-scope recipe → bind;
2. an exact name match against an in-scope recipe export → bind, and **record
   the automatic binding in the diff** so review sees it;
3. a curated literal in `compat/model/values.json`, keyed
   `(service, op, member)` then `(shapeName)` then `(memberName)`;
4. a constraint-derived synthetic value (first enum member; range minimum;
   `false` for a required boolean) — only for scalars;
5. otherwise **refuse**. The operation is not generated and appears in
   `compat/model/gaps.json` with a machine-readable reason
   (`unbound-required-member:RoleArn`).

Optional members are left unset except the single `mutable` member on Update
operations. Refusal is the default and is cheap to fix (one line in a recipe);
guessing is never allowed.

> **What rule 4 shipped as (#1709).** "First enum member" is the order the
> shape snapshot carries — which is the model's own order for a
> `smithy.api#enum` trait, a JSON array, but *not* for a `type: enum` shape,
> whose members are a JSON object that `cmd/awsmodelgen` writes through
> `encoding/json` and therefore sorted. Every enum in the committed snapshots
> is of that second form today, so the pick is in fact the alphabetically first
> value; recovering declaration order means teaching `cmd/awsmodelgen` to emit
> an ordered member list. Either way the pick is deterministic, which is what
> byte-identical regeneration needs. A required boolean is synthesised as
> **`false`** — the shape has exactly two legal values and `false` is the one
> asking the service to do less (no dry run, no force, no cascade), so the
> choice is exhaustive rather than a guess. The fourth candidate above,
> "shortest legal string for a pattern", is deliberately **not** implemented: a
> pattern constrains a string's *syntax*, never its *reference*, so the
> shortest match for `^arn:aws:.*` is a well-formed ARN of something that does
> not exist. The emulator accepts far more of those than AWS does, which is
> exactly the class of value §3.10 sends to the gap report — so the member is
> refused and a human writes the literal.

#### Grouping and naming

- Lifecycle groups: `<service>-gen-<resource>` (`sqs-gen-queue`) — kebab-case,
  matching the existing schema pattern.
- Probe groups: `<service>-gen-probe` (§3.5).
- Test names: the PascalCase operation name; variants fold into the name with
  the bare operation in `op` (`CreateQueueWithTagsAtCreate`, `op:
  "CreateQueue"`) exactly as the registry rules require.
- Each group is independently runnable: it creates its own recipes and destroys
  them; nothing crosses group boundaries.

### 3.4 D3 — Assertion generation

Derivable from the model: which output member carries identity, which members
echo input, and the error shapes an operation can raise. Which operation reads
a resource back is **proposed by operation-name clustering and confirmed by a
human**, not read off the AST: the 2026-09-06 audit found **zero** Smithy
`resource` shapes in every committed snapshot (121 of 426 upstream model files
carry any), so the resource bindings this plan expected to lean on do not
exist for a service in scope. Where a service does declare them, `-scaffold`
uses them. Not derivable either way: which fields are *semantically*
comparable versus server-assigned — so the recipe declares `read`, `list`,
`identityPath`, `notFound` and `mutable`, and the generator does the rest.

The IR has a closed set of assertion kinds:

| Kind | Emitted for | Contract clause satisfied |
| --- | --- | --- |
| `responseField` | any op — path exists / non-empty / equals / matches | "Create must assert ARN/ID non-empty, name matches" |
| `readback` | `Create*`/`Put*`/`Update*`/`Set*`/`Tag*` | Create→Describe, Update→read-back |
| `listContains` | `Create*` | "List must be non-empty and contain the created resource" |
| `absent` | `Delete*`/`Untag*` | Delete→absence, or the declared not-found error |
| `errorCode` | negative-path variants | assertion-contract exception 2 |
| `eventually` | wraps any of the above, bounded `maxAttempts`/`delayMs` | "no sleep/polling unless strictly necessary" — only when the recipe declares the resource async |
| `isList` (#1709) | a `checks` entry rather than a clause of its own: a `List*` whose only assertable output is its page — the path resolves to a list, **empty or not**, and a member omitted rather than serialized as `[]` counts too | "observable state is verified", where the state is that the service answered with a page: an empty page is a legal single-page answer, so `nonEmpty` on a list the test did not populate is false by construction |

Emission rules:

- `Create*`/`Put*` → `responseField` (identity non-empty) **+** `readback` via
  the recipe's `read` **+** `listContains` via the recipe's `list`.
- `Update*`/`Set*`/`Tag*` → the mutated member is set from `mutable.to`, then
  `readback` asserts the read path now equals `to`. Without a `mutable`
  declaration the operation is **refused**, which is what prevents the
  "asserting the ID is still non-nil" anti-pattern the contract calls out.
- `Delete*`/`Untag*` → `absent`, using `notFound.errorCode` if declared, else
  `list` non-membership.
- `Get*`/`Describe*`/`List*` standing alone → generated only inside a lifecycle
  group where the resource was created in the same group, asserting identity.
  A bare read of nothing is refused.
- An operation with no read-back path and no declared `shapeAssert` (the
  contract's exception 1 — `GenerateDataKey`-style) is **refused**.

**Invariant, enforced in the generator and by a CI lint: every emitted test
carries at least one assertion clause.** A vacuous generated test cannot be
represented in the IR.

### 3.5 D4 — Purposefulness guard

Structural guards (the generator physically cannot emit the bad cases):

1. No assertion clause → no test (§3.4).
2. Unbound required member → no test (§3.3).
3. Update without a declared mutation → no test.
4. **Probe groups may only contain operations the emulator does not implement**
   — i.e. modeled operations that are absent from
   `internal/capabilities/all.gen.go` or declared `StatusUnsupported`. A probe
   test calls the operation once with model-valid literals and asserts the
   modeled output identity member; against a Tier 0 service the SDK raises the
   501 and the harness records `unimplemented`, which is the correct and
   informative result. An implemented operation is never allowed in a probe
   group; regeneration moves it into a lifecycle group or refuses it. This is
   what stops "10,000 tests that assert nothing".
5. **A probe may not touch anything a run — or a real account — owns** (#1709).
   A probe is the one generated call no create/delete pair contains, so it has
   two guards of its own. (a) Binding rules 1 and 2 (§3.3) are switched off
   inside a probe group: it binds only curated `values.json` literals and
   constraint-derived ones, syntactically valid and deliberately nonexistent, so
   the call misses rather than lands. A member only a live export could supply
   refuses the operation (`probe-binds-live-resource:<Member>`). (b) Probe
   membership is **default-deny by verb** (#1795): only a `Describe*`,
   `List*` or `Get*` is probed at all, and every other operation is refused
   (`never-probe`) before it is bound, with a generated sentence saying so. A
   recipe's **`neverProbe`** map denies one the verb rule would have allowed —
   a read verb that is not a read — or restates a denial with a curated
   sentence where the prose says more than "not a read", and that sentence is
   what `gaps.json` then reports; **`allowProbe`** is the exception in the
   other direction, for an operation AWS spells with another verb that a human
   has judged safe to call.
6. **No assertion a probe cannot honestly make** (#1709). A pagination token is
   never chosen as the identity — the member `@paginated` names as its
   `outputToken`, or any member named `NextToken`/`Marker`/`NextMarker`/
   `ContinuationToken`/`NextContinuationToken`/`PaginationToken` or ending in
   `Token` or `Marker` — because that is precisely the field AWS omits on a
   single-page answer, so asserting it non-empty asserts the opposite of a
   correct response. A `List*` left with only its page gets `isList` on that
   page instead (§3.4); an operation with neither an identity member nor a
   single list is refused (`no-output-to-assert`).

Review guards (humans review the curated layer, not 5,000 JSON blobs):

- **One service per PR**, mirroring the parity-backfill cadence that worked.
- The reviewable artifacts are the **recipe file**, the **values entries**, and
  the **gap report** — a few hundred lines. The generated IR and registry are
  derived and diff-visible but reviewed by exception.
- `cmd/compatgen --review-report` prints, for the PR body: operations covered
  vs modeled, every refusal with its reason, every *automatic* name-match
  binding (rule 2 in §3.3 — the riskiest inference), and N randomly sampled
  scenarios rendered as pseudo-code.
- CI lint `compat-gen-check`: regen-and-diff; assertion-clause invariant; probe
  membership; naming rules; no generated group duplicates a hand-written
  group's `(group, test)` key; `$name` used for every user-supplied identifier.

Quality bar for accepting a service's generation, stated so a reviewer can apply
it: *would a competent human, writing this test by hand against real AWS, assert
the same thing?* If the generated assertion is weaker than the recipe could
express, fix the recipe. If it is weaker than the model allows, that is a
generator bug.

### 3.6 D5 — Registry mechanics, coexistence, and the CI gates

#### Where generated groups live

**A generated sibling file**, `compat/suites/registry.generated.json`, validated
by `registry.generated.schema.json` (which `$ref`s the shared `TestGroup`
definition and adds `generated`, `scenario`, `state`). Every loader
concatenates the two files; `cmd/compat` gains `--generated-registry-file`.

Rationale: the hand-written registry stays reviewable (a 5,000-entry diff would
drown the file that humans edit); the generator can rewrite its own file wholly
without merge conflicts; and "generated vs hand-written" is unambiguous for the
dashboard, the report and the lint. Baseline, flaky and parity-debt files key on
`suite/group/test` and are indifferent to which file a group came from.

#### Suite scoping

Generated groups carry an explicit `"suites": [...]` listing the backends that
can execute them — the three interpreters first, widening as each typed backend
lands. This reuses the tested scoping mechanism and keeps the parity checker
honest with zero new concepts: a suite without a backend neither implements the
group nor carries debt for it.

**This deviates from [compat/AGENTS.md:631-636](../../compat/AGENTS.md)** ("an
SDK suite is never a legitimate `suites` scope"). The amendment must land with
Phase G0 and must be narrow: *`suites` scoping on a **generated** group is
mechanically derived from backend availability and is never hand-edited; on a
hand-written group it remains reserved for `cdk-lifecycle`.* A lint enforces
both halves.

#### Candidate → gated soak (the flake defence)

New generated groups land with `"state": "candidate"`. Candidate groups are
**excluded from both `--compare-baseline` and `--max-failures`** — the inverse
of `flaky.json`: quarantined by default until they prove themselves, rather than
gated by default until someone gets a label.

| Stage | Rule | Enforced by |
| --- | --- | --- |
| Candidate | Runs everywhere, reports everywhere, gates nothing | `state` in the generated registry |
| Soak | The nightly 3× flake-detection job runs candidates, and a `promote` job downstream of it reads all three runs | the `detect` and `promote` jobs in [compat-flake-detection.yml](../../.github/workflows/compat-flake-detection.yml) |
| Promotion | A group flips to `gated` when **every suite it is scoped to reported every one of its tests in all 3 runs**, every `(suite, test)` carried the **same** status in all 3, and no status was `fail` **or `skip`** — via a bot PR on `automation/promote-generated`. A suite missing from one run is not two runs of evidence and an absence; it is one run in which the group was never exercised. A group that skips consistently is perfectly consistent and has been exercised zero times — consistency is not evidence. `unimplemented` promotes: a Tier 0 probe group is exactly that case, and a stable 501 is the operation answering as modelled | `--promote-generated` in `cmd/compat` (#1792, #1798), writing `compat/model/promotions.json`, which `cmd/compatgen` reads to emit each group's `state` |
| Stuck | Inconsistent groups stay candidate; `--promote-generated` names the offending `(suite, test)`s and reports a candidate older than 30 days as overdue (a `::warning` on the nightly). The flipping tests themselves raise the usual per-(suite, group) issue, since candidate groups run in the same nightly the flake detector reads | `--promote-generated`; [scripts/compat-flake-issue.py](../../scripts/compat-flake-issue.py) |

Promotion is mechanical, so it needs no reviewer label — the reviewer decision
already happened at recipe review. A generated test that fails because the
*test* is wrong is fixed in the recipe or values table, **never** by weakening
an assertion and never by adding it to `flaky.json`.

**Built 2026-09-05 (#1789):** the state is an *input* — `cmd/compat
--promote-generated` writes only `compat/model/promotions.json` (group →
`state`, `firstSeen`, `promotedAt`, the run ids that agreed) and `cmd/compatgen`
reads it when emitting each group's `state`, so no second tool ever writes
`registry.generated.json` and `-check` stays byte-identical across a promotion.
The ledger's Go shape, its version and its strict reader are shared by both
commands from `compat/model` (package `compatmodel`), because two copies of one
schema in two `main` packages is how the writer came to decode leniently and
then rewrite the file from what it had understood.

#### Volume: baseline, parity, CI runtime, dashboard

- **Baseline size.** ~~3,367 entries / 532 KB today.~~ **Done in #1370.** Tier-1
  fleet coverage plausibly reaches 3,000–4,000 generated tests × the suites in
  scope, i.e. a five-to-tenfold increase, so the file was sharded to
  `compat/baseline/<suite>.json` in **G0, while it was still small**, keeping
  the format and the lint semantics. The per-shard budget is **512 KiB**, about
  4x the largest shard today — tripping it means sharding further, by service,
  not raising the number. `--baseline-file` still accepts a single file, so a
  base commit older than the split remains lintable against. (Considered and
  deferred: dropping `pass` entries to shrink the file — it would weaken the
  "failing and absent from the baseline" check that #462 relies on.)
- **Parity debt.** Generated groups must not inflate debt: `suites` scoping means
  a suite without a backend is out of scope, not indebted. Open question §7.5:
  whether a suite with outstanding *hand-written* debt (rust-sdk 297,
  dotnet-sdk 261) should be blocked from receiving a generated backend until it
  clears — the debt file only shrinks, and mixing the two flows would obscure
  that.
- **CI runtime.** Add `--shard i/n` to `cmd/compat`, implemented over the
  existing `OVERCAST_COMPAT_GROUPS` plumbing (`compat/runner.go:336`) so no
  suite changes are needed. PR runs execute all hand-written groups plus one
  rotating shard of gated generated groups; the nightly runs everything. The
  `cli` suite spawns one process per API call and is already the slowest matrix
  job, so its shard count is tuned independently.
- **Dashboard.** Add a `generated` facet; default the matrix to hand-written
  groups; make **model coverage** (`operations with a test / operations in the
  service's tier`) the headline metric, per service and per tier. A matrix of
  5,000 rows is not a UI — a coverage meter with drill-down is.

### 3.7 D6 — Where the generator lives, and the boundary

**Rule, stated once and enforced: nothing under `compat/` imports a Go package
from the emulator tree.** That is the boundary
([compat/AGENTS.md:35-48](../../compat/AGENTS.md)) and it is untouched.

- The generator is **`cmd/compatgen`**, a build-time command in the main module
  beside `cmd/awsmodelgen` and `cmd/capgen`. It reads the pinned Smithy AST from
  a local `api-models-aws` checkout via `AWS_MODELS_DIR`, validating against
  [models/aws/VERSION](../../models/aws/VERSION) exactly as
  `make generate-aws-operations` does. It shares the AST reader with
  `cmd/awsmodelgen` (extract it to `internal/awsmodel` in G1) and may read
  `internal/capabilities` for tier classification. It is a tool, not a suite.
- Its outputs are **committed data files** read by suites through ordinary file
  I/O in each language — the same relationship suites already have with
  `registry.json`:

  | Artifact | Purpose |
  | --- | --- |
  | `models/aws/shapes/<service>.json` | the pruned shape snapshot: distilled input/output members, required-ness, enums, constraints, error shapes, and resource lifecycle bindings — **for the allowlisted services only**. **Shared with [inert-tier-rollout.md](./inert-tier-rollout.md) §4.6** — one pruner (in `cmd/awsmodelgen`), one `shapes-sha256` in `models/aws/VERSION`, two consumers (`cmd/awsmodelgen -inert-*` and `cmd/compatgen`); do not build a second compat-local distillation |
  | `compat/model/recipes/<service>.json` | hand-curated (input, not output) |
  | `compat/model/values.json` | hand-curated literal table (input) |
  | `compat/model/scenarios/<service>.json` | generated scenario IR |
  | `compat/model/gaps.json` | refusal report |
  | `compat/suites/registry.generated.json` | generated registry sibling |
  | `compat/suites/*/src/**/scenarios_gen.*` | generated source, typed backends only |

- **Do we extend the vendored snapshot?** Yes, but as a *distillation*, not the
  raw AST — and it is the **same** pruned snapshot
  [inert-tier-rollout.md](./inert-tier-rollout.md) §4.6 specifies
  (`models/aws/shapes/`, built by the `cmd/awsmodelgen` pruner in that plan's
  Phase I1); this plan adds a consumer, not a second corpus. The snapshot is
  scoped to the union of both plans' allowlists (§3.9 here) and carries
  only the fields §3.3/§3.4 consume. This preserves the snapshot policy's intent
  (the raw corpus stays out of the tree, nothing at runtime parses models) while
  giving the generator the shape data the routing manifest deliberately omits.
  A size budget is part of the acceptance gate. Smithy `resource` shapes *are*
  vendored into `shapes.json`, and they cost nothing to keep — but they are not
  the lifecycle source this plan expected. The 2026-09-06 audit counted **zero**
  resource shapes across every committed snapshot (121 of 426 upstream model
  files carry any at all), so `-scaffold` proposes a lifecycle by
  operation-name clustering and a human confirms it; resource bindings are used
  where a service has them, which so far is nowhere in scope.
- **Regeneration workflow.** Hook into the existing weekly model-refresh
  automation ([aws-api-operation-coverage.md §8](./aws-api-operation-coverage.md)):
  when the pinned revision moves, the same bot PR regenerates `shapes.json`,
  the scenarios and the generated registry, and reports operations added/removed
  per service. New operations arrive as **candidate** generated tests, so they
  can only ever be `unimplemented` or `pass` — a model refresh can never break
  the gate. Offline PRs verify a `shapes-sha256` recorded in
  `models/aws/VERSION`, mirroring the existing `manifest-sha256` check.

### 3.8 D7 — IaC suites: out of scope, deliberately

CDK, Terraform/OpenTofu and Pulumi deploy whole stacks; their unit of
observation is a stack lifecycle, not an operation, which is exactly why
`cdk-lifecycle` already uses `suites` scoping. **Model-driven per-operation
generation does not apply to them, and generated groups always exclude them.**

The IaC analogue is a different generator against a different model — CDK L1
constructs and Terraform resources map to CloudFormation resource types, whose
schemas live in the CloudFormation resource-schema registry, not in Smithy. A
"minimal stack per resource type" generator is plausible and valuable (it is the
most direct Tier 1 acceptance test there is), but it is a separate plan, needs a
second pinned model, and must not be smuggled into this one. Named here so
nobody re-derives the question; explicitly deferred.

What the IaC suites *do* get from this plan: the Tier 1 operations that CDK
depends on become measured in eight clients, so a CDK deploy failure can be
attributed to a specific operation's shape rather than bisected by hand.

### 3.9 Scope: which operations get generated

Generating all 18,850 modeled operations would be absurd. The allowlist is
tier-driven:

| Population | Treatment |
| --- | --- |
| Operations of the 50 registered services (**4,440**) | Full generation: lifecycle groups for implemented/inert resources, probe groups for the rest |
| Services promoted to Tier 1 by [inert-tier-rollout.md](./inert-tier-rollout.md) | Added to the allowlist as they are promoted |
| Services never-listed or deferred by [services-never-emulated.md](./services-never-emulated.md) | **Not generated — no probe groups.** (This supersedes an earlier draft's "capped probe group" idea, reconciled with that plan's §5.2: permanent `unimplemented` rows would read as roadmap gaps and never change.) Ownership and 501-envelope correctness are guaranteed and tested server-side by [aws-api-operation-coverage.md](./aws-api-operation-coverage.md)'s corpus; SDK-side 501-envelope parsing is already exercised by the probe groups of registered-but-unimplemented operations in every protocol family. Dashboard visibility comes from the `NeverEmulated` policy marker (that plan's §5.3), which the coverage report renders as "N/A by policy" rather than as a gap |
| Everything else (374 unregistered identities, 14,410 operations) | Not generated until [inert-tier-rollout.md](./inert-tier-rollout.md) promotes them onto the allowlist. Same server-side coverage argument as above |

### 3.10 Performance and fidelity constraints

Both are explicit repo values and both bite here.

**Performance.**

- Generation is entirely build-time. Nothing parses a model at test time: the
  interpreters read a per-service scenario file and execute steps. Per-step
  overhead must stay negligible against SDK + network time; the `cli` suite's
  process-spawn cost dominates and is the reason for independent shard tuning.
- CI wall-clock is a first-class acceptance gate on every phase, not an
  afterthought. A phase that adds more than its budgeted minutes to the matrix
  does not land until it is sharded.
- Generated runs are a fine **latency observatory** — `duration_ms` is already
  in the wire format, so a `--slowest N` report gives a fleet-wide per-operation
  latency census for free. They are **not** a benchmark gate: CI runners are too
  noisy, and performance claims still require the paced local methodology in
  [storage-test-plan.md](./storage-test-plan.md).

**What the G2 pilot measured — 2026-09-06, the seven pilot groups against a
slim image on loopback** (recorded in #1801; per-suite wall clock is in the §2
note). Three findings, because each changes a different decision.

- **`cli`'s wall clock is its largest group, not its slot count.** The suite
  takes 23 s and `organizations-gen-probe` accounts for 23.1 s of it — 25
  process spawns at roughly 850 ms each, against a per-test cost 29× that of
  `node-js-sdk`. Raising `OVERCAST_COMPAT_PARALLEL_SLOTS` from 8 to 16 changes
  nothing, because slots distribute *groups* and this is one group. Running a
  probe group's own tests concurrently is the measured lever: 16.9 s serial,
  5.2 s at 4-way, 3.3 s at 8-way, tracked as **#1801**. §3.6's rotating shard
  will not substitute for it at fleet scale, for the same reason — a shard
  distributes whole groups too — so at the 2,000–5,000 generated tests a Tier-1
  fleet implies, probe-group parallelism is what decides `cli`'s CI wall clock.
- **The `WaitTimeSeconds` absence polls are the only unconditional waits in the
  corpus, and they must not be shortened.** Each is the `ReceiveMessage` inside
  an `absent` clause — `sqs-gen-message/DeleteMessage` and
  `sqs-gen-batch/DeleteMessageBatch` — and the clause holds on its first
  attempt, so each costs its long poll and no more. Dropping to
  `WaitTimeSeconds: 0` makes it a short poll, which samples a subset of servers
  and can answer empty while the message is still there: a **false pass**, and
  the one failure mode an absence check may not have. The three other
  `ReceiveMessage` calls carrying a wait expect a message and return as soon as
  one arrives. Both polls were one second when this was measured; the batch one
  is five since #1820, because the go-sdk emitter cannot send
  `VisibilityTimeout: 0` — smithy-go omits a value-typed member equal to its
  modelled default — and the one-second hide the recipe asks for instead needs
  a poll longer than itself.
- **`eventually` budgets cost nothing on the happy path.** The corpus carries 15
  of them, several deliberately generous — eight at 30 attempts 2 s apart for
  the queue resource, one at 12 attempts 5 s apart for `PurgeQueue`'s lagging
  counters. A budget bounds a retry loop that exits on the first attempt that
  holds, so a passing run never spends it. Reading a wide budget as slow is
  reading the worst case as the cost, and shrinking one to save time buys
  nothing while making a slow-but-correct emulator response a failure.

**Fidelity.**

- No suite may gain an Overcast-specific code path to make generated tests work.
  The endpoint override remains the only deviation from production
  configuration. If a generated test cannot be expressed through the public SDK
  API, it is refused.
- Generated values must be AWS-legal, not merely emulator-accepted. When a
  generated test passes against Overcast the natural next question is whether it
  would pass against AWS; the values table should be reviewed with that question
  in mind, and any operation where the honest answer is "no" belongs in the gap
  report.
- **A scenario file has to be safe to point at AWS.** That is the same
  requirement read one step further: a probe "calls the operation once with
  model-valid literals" (§3.5), and against a real account that single call is
  `CloseAccount`, `DeleteOrganization` or `LeaveOrganization` — irreversible,
  and irreversible for the whole organization. So probe safety is not review
  advice but a structural guard, and it lands with the data rather than with the
  interpreters: §3.5's guard 5, in its two halves — a probe may never bind a
  value exported from a live resource, and a recipe's `neverProbe` names what
  must not be probed at all. `organizations` exercises both, and the result is a
  probe group that is entirely reads (§4.2).
- Refusals are a feature. `gaps.json` is a public, reviewed statement of what
  the model cannot mechanically express — it is far more valuable than a test
  that passes for the wrong reason.

### 3.11 Endgame — IR-first; native test code is the audited exception

Steady state has **three layers**, in strictly decreasing volume and strictly
increasing human involvement:

| Layer | Authored by | Volume | Human touch |
| --- | --- | --- | --- |
| **Model-generated scenarios** | `cmd/compatgen` from shapes + recipes | thousands | recipe/values review once per service; regeneration is free |
| **Authored scenarios** | a human, **in the IR**, once | hundreds | the scenario file itself is the review artifact — one spec replaces eight per-language implementations |
| **Native per-suite tests** | a human, per language | tens, capped | each entry requires a reason in a checked-in exceptions file |

Behavioural intent that the model cannot know (send a message, receive it,
assert the body; publish to a topic subscribed to a queue and poll; FIFO
ordering; DLQ redrive) is not lost and not machine-guessed — it is written **by
hand in the IR**, where the existing step/assertion vocabulary (`eventually`,
`readback`, `errorCode`, cross-resource `$ref`s across recipes) already
expresses most of today's hand-written groups. The economics change from
"behavioural test = 8 implementations to keep in sync" to "behavioural test =
1 spec, executed by every backend".

**The native exception list** is for what the IR structurally cannot express,
and it must stay short. Expected categories, each requiring a listed reason:
streaming/chunked request bodies; presigned-URL flows exercised outside the SDK
client; deliberately malformed wire traffic below the SDK's public surface; and
a small deliberate **idiom suite** — paginators, waiters, high-level layers
like boto3 resources or the DynamoDB DocumentClient — kept native *because*
those exercise SDK client code paths the interpreter/generated-source path does
not touch. An entry without a reason, or a reason the IR has since learned to
express, fails the lint.

**Migration of the existing hand-written groups**, group by group, any time
after the relevant backends exist (G3) — 94 groups / 496 tests when this
section was proposed (2026-08-03); **141 groups / 803 tests as of 2026-09-08**
(`compat/suites/registry.json`), since hand-written coverage kept growing
independently of this migration:

1. Author the IR scenario under the **same registry group/test names** — the
   names are the join keys, so baseline history, dashboard history, and
   flaky/debt bookkeeping survive untouched.
2. Run both implementations in parallel through one nightly soak cycle; every
   (suite, test) result must match its native predecessor exactly.
3. Delete the per-language implementations in the same PR that flips the group's
   resolution to the scenario. A divergence blocks the deletion — never the
   gate — and is triaged as either an IR expressiveness gap (extend the IR or
   add a native exception) or a latent bug in one of the eight copies (fix it;
   this migration is precisely how such divergences get found).
4. A ported group implemented by scenario counts as implemented in **every**
   suite with a backend — which is how `rust-sdk`'s 297 and `dotnet-sdk`'s 261
   parity-debt entries are burned down without anyone hand-porting them
   (resolving §7.5 in favour of "generation lands first").

**The human-input budget is a design constraint, not a hope.** Humans author
recipes, values, authored scenarios, and the exceptions file; review
concentrates on those four artifacts and on `gaps.json` as the exception queue.
Everything else — regeneration, soak, promotion, coverage accounting — is
mechanical. Two tripwires keep the budget honest: a per-service ceiling on
`gaps.json` entries (a service whose refusal list keeps growing means the
generator is missing a capability — fix the generator, don't grind the queue),
and the exceptions-file lint above. When a new operation appears in a model
refresh, the intended cost is zero human actions for shape coverage and one
reviewed scenario only if it warrants behavioural coverage.

#### 2026-09-07 — the G6 pilot, and the mechanism every later port reuses

`sqs-queues` is ported (#1116). The machinery, which nothing after this has to
invent again:

- **An authored scenario is an input with no recipe**, at
  `compat/model/authored/<group>.json` — beside `scenarios/`, never inside it,
  because everything in that directory is rewritten wholly on every run.
  `cmd/compatgen` validates it against `scenario.schema.json` and the IR's own
  rules, then against the model, and feeds it to the four typed emitters
  through exactly the `generation` a recipe produces. Its emit key is
  `authored-<group>`, so a port's source is `scenarios_authored_<group>_gen.*`
  and the diff of a migration is readable next to the service's generated one.
  `-check` covers everything produced from it.
- **The names are checked against the registry**, which is step 1 stated as a
  gate rather than as an instruction: the file's base name is the group it
  ports, and the test names, their `op` and their `depends` are that group's,
  in its order. A scenario that quietly renamed a test would soak green and
  orphan one baseline entry per suite on the flip.
- **Step 2 is a shadow group.** Both implementations have to be live at once,
  and no suite may register two implementations for one `group:test` key, so
  the port runs under `<group>-shadow`. Its generated registry entry carries
  `shadowOf`, always in state `candidate`; `--promote-generated` skips it (that
  soak asks whether a group agrees with itself, which is not the question);
  `go run ./cmd/compat --compare-shadow --results-file <run>` joins the two on
  (suite, test), and the nightly runs it beside the promotion soak.
- **The comparison classifies rather than diffs.** Only a *divergence* blocks
  the flip. A native `skip` carrying the not-implemented sentinel against a
  shadow that ran is **parity debt closed**, which is step 4 happening and not
  a fault; two skips are **not exercised**, which is agreement in status and
  evidence of nothing — promote.go's all-skip reasoning applied to this soak.
  Collapsing the four would make the tool unusable for the migration it exists
  to serve.
- **The `$comment` key is now legal** on a scenario, a group, a test and an
  assertion, and the generator still writes none. §3.11 calls an authored
  scenario "the review artifact"; one that could not say why a clause is what
  it is would be worse than the eight implementations it replaces.

Two findings from the pilot itself, both fixed here:

- **The seven native `sqs-queues` implementations were not one test.** They
  disagreed on the visibility timeout (dotnet 120, the rest 60), on six
  different tag sets, and on how much they verified at all: `java-sdk` asserted
  nothing for `SetQueueAttributes`, `TagQueue`, `UntagQueue` or `DeleteQueue`,
  its `CreateQueue` asserted a context value without calling the service, and
  `rust-sdk`'s `SetQueueAttributes` had no read-back either. The authored
  scenario is the union, so the port is a coverage *increase* for two suites —
  which is the class of divergence §3.11 predicted the migration would surface.
- **Two suites inferred "generated" from the group name.** `java-sdk`'s and
  `rust-sdk`'s registration tests skipped any group whose name lacked `-gen-`,
  which is exactly the inference compat/AGENTS.md forbids and which a ported
  group — named for the hand-written group it replaces — defeats. Both now ask
  whether the emitter registered anything for the group.

One thing the IR still cannot express: **"the response contains the resource
name"**. `matches` takes a fixed pattern and cannot embed a `$name`, so
`rust-sdk`'s "the queue URL contains the queue name" has no direct spelling.
Here the list-membership clause is strictly stronger and the loss is nil, but a
later port may not be so lucky.

#### 2026-09-07 — the first flip, and the two-PR shape of a port

`sqs-queues` is the first flip (#1932, `ee7a2d35`, closing the prerequisites
tracker #1903): the seven native implementations are deleted, the
hand-written `sqs-queues` entry in `registry.json` now names
`compat/model/authored/sqs-queues.json`, and `registry.generated.json`'s
`ported` index carries the group with all seven `suites`. 1,161 native lines
went with it (rust 328, cli 184, node 180, go 178, dotnet 121, python 100,
java 70). Results: 8/8 `pass` in every suite, matching the shadow's own
soak — three nightly runs of `--compare-shadow` (run 34066538988), 56
(suite, test) pairs agreeing each time — that the flip PR cited rather than
re-proved.

**A port is two PRs with a soak between them**, and neither PR alone is the
migration (`compat/model/README.md` § Authored scenarios): the shadow PR
authors the scenario under `<group>-shadow`, `candidate`, running beside the
natives it will replace and gating nothing; one nightly cycle of
`--compare-shadow` either reports zero divergences or blocks the flip; the
flip PR then makes the group's registry entry and the generated artifacts
agree — rename `<group>-shadow` → `<group>`, point the hand-written entry at
the scenario, delete the native implementations with their setup/teardown
hooks, regenerate so the group moves from `registry.generated.json`'s
`groups` into its `ported` index, and leave `compat/baseline/` untouched
because the names never moved. #1932 found a sixth thing the flip needs that
the README's five-edit list did not name: inverting every corpus guard test
that still asserted no port had happened yet
(`cmd/compat/ported_test.go`, `cmd/compatgen/ported_test.go`,
`scripts/validate_compat_registry_test.py`, and the python-sdk/node-js-sdk
registry tests) — now recorded there.

**The prediction on #1903 needs one correction.** `sqs-queues` carried no
`dotnet-sdk`/`rust-sdk` parity debt going in — all seven baseline rows were
already `pass` — so the flip closes no debt row by itself; the measurable
gain is strictly the union of assertions the pilot found missing above
(`java-sdk` asserting nothing for four of eight tests, `rust-sdk`'s
`SetQueueAttributes` with no read-back). Debt closes with the groups G6 wave 1
chose next.

**G6 wave 1 chosen, 2026-09-08** (#1116 comment). Ranked hand-written groups
by `dotnet-sdk`+`rust-sdk` baseline rows the port would close, divided by
estimated effort, restricted to groups the current IR can already express —
six groups, 94 rows total: `kinesis-streams`, `logs-groups`,
`eventbridge-rules`, `ecs-clusters`, `cognito-userpools`, `rds-instances`.
Deferred: the five-resource `ec2-vpc`/`ec2-instances` chain (the highest raw
debt, 30/20 rows, but the biggest port — next wave); `pipes-wiring` and
`eventbridge-target-fanout`, which need a scenario to set up a resource of
*another* service, an IR gap the current single-service authored format
cannot express, filed as **#1931**; the `iam-*` family, which needs #1922's
`equalsJSON` and whose operations the generated `iam` recipe (#1919) already
covers; and the cli-only groups (`msk`, `eks`, `backup`, `opensearch`,
`appconfig`, `ecr` — the largest raw debt, up to 32 rows each), whose
expressibility has to be read from the cli implementation rather than assumed
from go-sdk's. Each port follows the two-PR shape above; ranks 1–3 start
first.

---

## 4. First milestone — pilot (Phase G2)

Two services, chosen to exercise both ends of the tier ladder.

### 4.1 `sqs` — validate against real, known-good behaviour (Tier 2)

- 23 modeled operations
  ([internal/awsapi/manifest.gen.go](../../internal/awsapi/manifest.gen.go)),
  21 declared capabilities (19 `StatusSupported`, `AddPermission` and
  `RemovePermission` `StatusUnsupported`).
- Four hand-written groups exist today — `sqs-queues`, `sqs-messages`,
  `sqs-dlq`, `sqs-fifo`, 21 tests — implemented in every suite and passing.
  **They are not touched.** The pilot proves generated coverage is additive and
  that the generator's assertions agree with hand-written ones where they
  overlap.

Acceptance criteria:

1. `sqs-gen-queue` and `sqs-gen-message` are generated from one reviewed
   `recipes/sqs.json` plus at most 15 lines of `values.json`.
2. **≥ 20 of the 23 modeled operations** are covered by generated tests; every
   refusal appears in `gaps.json` with a specific reason.
3. Every generated test has ≥ 1 assertion clause; the two `StatusUnsupported`
   operations land in `sqs-gen-probe` and record `unimplemented`.
4. Passes in `python-sdk`, `node-js-sdk` and `cli`, with **identical results
   across three consecutive runs** (`scripts/compat-flake-detect.py`).
5. Zero trace: a `ListQueues` sweep after the run finds no `{runId}` resource.
6. No generated `(group, test)` key collides with a hand-written one, and the
   four hand-written groups' results are byte-identical to the previous
   baseline.

> **Generated 2026-09-05 (#1709), ahead of the interpreters.** `compat/model/scenarios/sqs.json`
> covers **21 of the 23 modeled operations** in **23 tests** across four groups:
> `sqs-gen-queue` (13), `sqs-gen-message` (4), **`sqs-gen-batch`** (4) and
> `sqs-gen-probe` (2 — `CancelMessageMoveTask`, `ListMessageMoveTasks`). Inputs
> are one reviewed `recipes/sqs.json` and six curated `values.json` literals in
> eleven lines, inside criterion 1's fifteen-line budget, and `gaps.json`
> records **two** refusals for the service.
>
> Criteria 1 and 2 are met on the artifacts — 21 clears criterion 2's "≥ 20 of
> 23" — as is criterion 3's structural half: the IR cannot express a test
> without an assertion clause. Everything that needs a run — 4, 5 and 6 — waits
> for a backend and stays open for G2.
>
> **Criterion 3's second half no longer holds as written, and should be
> restated.** The two `StatusUnsupported` operations do *not* land in
> `sqs-gen-probe` recording `unimplemented`: `AddPermission` and
> `RemovePermission` both return an empty output, and reading back the queue
> they name would assert something that was already true before the call, so
> both are refused `no-output-to-assert` into `gaps.json`. That is the §3.4
> invariant — no assertion, no test — applied honestly rather than worked
> around, and it is a better result than a probe that asserts nothing. Read the
> criterion as: *every generated test has ≥ 1 assertion clause, and every
> `StatusUnsupported` operation either lands in `sqs-gen-probe` and records
> `unimplemented` or appears in `gaps.json` with a specific reason.* The probe
> group's remaining two operations — `CancelMessageMoveTask` and
> `ListMessageMoveTasks`, modeled but undeclared — are the ones that carry the
> `unimplemented` demonstration.
>
> The fourth group is the one departure from the criteria as written: batch
> operations fit none of the lifecycle roles, so they are authored as a
> `sqs-gen-batch` resource of their own rather than refused. The plan asked for
> two groups; four is what the service's shape produces.

> **Recounted and run — 2026-09-06.** `compat/model/scenarios/sqs.json` now
> covers **20 of the 23 modeled operations** in **22 tests**: `sqs-gen-queue`
> (13), `sqs-gen-message` (4), `sqs-gen-batch` (4) and `sqs-gen-probe` (**1** —
> `ListMessageMoveTasks`). The probe group lost `CancelMessageMoveTask` to
> #1809, which made probe membership default-deny by verb, and `gaps.json`
> accordingly records **three** `sqs` refusals rather than two, all
> `never-probe`: `AddPermission`, `RemovePermission` and
> `CancelMessageMoveTask`. That **supersedes the `no-output-to-assert` reading**
> of the first two above — the verb rule refuses them earlier, before anything
> is bound, so the restatement of criterion 3 still holds but for a different
> reason.
>
> `CancelMessageMoveTask` is the judgement call in that set, and it is the
> owner's to reverse: a cancel carrying a curated, deliberately nonexistent task
> handle lands nowhere, so one `allowProbe` line in `recipes/sqs.json` would put
> it back in front of the remaining guards. Whether coverage then reaches 21 of
> 23 is a second question those guards answer, not this one — its whole output
> is `ApproximateNumberOfMessagesMoved`, a `Long` defaulting to `0`, which is
> neither an identity member nor a list, so `no-output-to-assert` is the likely
> next refusal. Default-deny is what makes the first call a decision rather than
> an accident. Criterion 2 is met either way, at exactly its floor.
>
> | Criterion | Status | Evidence |
> | --- | --- | --- |
> | 1 — one reviewed recipe plus ≤ 15 lines of `values.json` | **Met** | `recipes/sqs.json` and six curated literals in eleven lines (#1709) |
> | 2 — ≥ 20 of 23 operations covered, every refusal in `gaps.json` with a reason | **Met, at the floor** | 20 covered; three refusals, each carrying its sentence |
> | 3 — every test has ≥ 1 assertion clause; unsupported operations probed or explained | **Met** | the first half is structural in the IR; the second reads as restated above |
> | 4 — passes in `python-sdk`, `node-js-sdk` and `cli`, identical across three runs | **Met** | #1787, #1788, #1790 — identical test for test in all three suites |
> | 5 — zero trace | **Met** | `ListQueues` returns `[]` after every run |
> | 6 — no key collides with a hand-written group; hand-written results unchanged | **Met** | `sqs-queues`, `sqs-messages`, `sqs-dlq` and `sqs-fifo` still pass 21 of 21 |

### 4.2 `organizations` — prove the unimplemented path (Tier 0 → Tier 1)

Chosen because it is the cleanest instance of the problem: **63 modeled
operations, exactly one declared capability** — `DescribeOrganization`,
`StatusInert`
([internal/capabilities/all.gen.go:1046](../../internal/capabilities/all.gen.go))
— and **zero compat coverage today**. It is a Tier 1 candidate in
`inert-tier-rollout.md`, so the pilot doubles as that plan's acceptance rig.

> **Moved since this was written (checked 2026-08-23).**
> [inert-tier-rollout.md](./inert-tier-rollout.md) Phase I2 (#1376) landed the
> shared Tier 1 runtime and proved it on `organizations` policies, so the
> service now declares **nine** `StatusInert` operations, not one:
> `CreatePolicy`, `DeletePolicy`, `DescribeOrganization`, `DescribePolicy`,
> `ListPolicies`, `ListTagsForResource`, `TagResource`, `UntagResource`,
> `UpdatePolicy`.
>
> This does not invalidate the pilot — it *improves* it, and the criteria below
> need recounting rather than rewriting. The probe group covers 54 undeclared
> operations, not 62. More usefully, criterion 5 — the regeneration
> demonstration that justifies the whole plan — no longer needs a hypothetical
> future operation to move: eight operations already crossed from undeclared to
> `StatusInert` since this plan was written, so the demonstration can be run
> against a move that has actually happened. The policy resources also give the
> recipe a real Create → Describe/List → Update → Delete lifecycle to express,
> where before there was only a single read. Recount against
> `internal/capabilities/all.gen.go` when starting G2; do not trust the figures
> in this section.

> **Generated 2026-09-05 (#1709), and here is the recount.** Of the 63 modeled
> operations, all nine `StatusInert` ones are covered by lifecycle groups —
> `organizations-gen-policy` (8 tests: the full
> Create → Describe → Update → Tag/ListTags/Untag → List → Delete lifecycle) and
> `organizations-gen-organization` (1: `DescribeOrganization`, asserting
> `$.Organization.Arn` against the model's own pattern). That leaves **54**
> undeclared operations, of which `organizations-gen-probe` covers **25** — so
> **34 of 63** operations covered, by 34 tests in three groups. Criterion 1's
> "62" reads "25" today.
>
> The other **29** are refusals, every one of them `never-probe`, and every one
> carrying a curated sentence in `gaps.json` saying what the call does that
> cannot be undone. They are the whole of §3.5's guard 5 landing with the data:
> `recipes/organizations.json` gained a `neverProbe` map listing every modeled
> operation that writes — the account-mutating ones (`CloseAccount`,
> `CreateAccount`, `CreateGovCloudAccount`, `RemoveAccountFromOrganization`,
> `MoveAccount`), the organization-lifecycle ones (`CreateOrganization`,
> `DeleteOrganization`, `LeaveOrganization`, `EnableAllFeatures`), the
> policy-attachment and policy-type toggles (`AttachPolicy`, `DetachPolicy`,
> `EnablePolicyType`, `DisablePolicyType`), the service-access toggles
> (`EnableAWSServiceAccess`, `DisableAWSServiceAccess`), the handshake and
> invitation calls (`AcceptHandshake`, `CancelHandshake`, `DeclineHandshake`,
> `InviteAccountToOrganization`), the delegated-administrator pair
> (`RegisterDelegatedAdministrator`, `DeregisterDelegatedAdministrator`), the
> resource-policy writes (`PutResourcePolicy`, `DeleteResourcePolicy`), the
> responsibility-transfer calls
> (`InviteOrganizationToTransferResponsibility`, `UpdateResponsibilityTransfer`,
> `TerminateResponsibilityTransfer`) and the organizational-unit writes
> (`CreateOrganizationalUnit`, `UpdateOrganizationalUnit`,
> `DeleteOrganizationalUnit`). What that buys is a probe group which is entirely
> reads — nothing in it could damage a real account, which is the condition
> §3.10 puts on pointing a scenario file at AWS.
>
> This **subsumes the earlier refusals** an interim revision of #1709 reported:
> the six `no-output-to-assert` ones and the
> `unbound-required-member:StartTimestamp` on
> `InviteOrganizationToTransferResponsibility` are all in the `neverProbe` list
> now, and are refused earlier, before anything is bound. `gaps.json` for
> `organizations` is 29 `never-probe` entries and nothing else.
>
> Criterion 3 no longer holds for the service as a whole: the policy lifecycle
> creates and deletes a real resource, so `organizations-gen-policy` has a
> teardown. It still holds for `organizations-gen-organization`, which is the
> group it was written about — and, now, for `organizations-gen-probe`, which
> carries neither setup nor teardown because a probe has nothing to set up.

> **Criterion 5, first half (2026-09-06, #1813).** The organizational-unit
> lifecycle is now in the recipe, and the corpus was regenerated over it. The
> five OU operations and `ListRoots` are still undeclared in
> `internal/capabilities/all.gen.go`, so this records the *before* state that
> the emulator half of the demonstration will move:
>
> | | before | after |
> | --- | --- | --- |
> | `organizations` groups | 3 | 5 (`-ou`, `-root` added) |
> | `organizations` tests | 34 | 40 |
> | operations covered | 34 of 63 | 37 of 63 |
> | `organizations-gen-probe` tests | 25 | 22 |
> | `organizations` refusals | 29 `never-probe` | 26 `never-probe` |
>
> The three the probe group lost are `DescribeOrganizationalUnit`,
> `ListOrganizationalUnitsForParent` and `ListRoots`, which the two new
> lifecycles now claim; the three refusals it lost are the `never-probe` rows
> for `CreateOrganizationalUnit`, `UpdateOrganizationalUnit` and
> `DeleteOrganizationalUnit`, whose curated sentences left the recipe with
> them. No file under `compat/suites/` changed.
>
> §3.1's intended shape held: the generator emits a lifecycle over undeclared
> operations without complaint — nothing in `generate.go` consults the
> capability table on the lifecycle path, only on the probe path — so
> `organizations-gen-ou` is in `registry.generated.json` as `candidate`
> alongside the rest.
>
> **One correction to the expectation.** `organizations-gen-ou` does *not*
> record `unimplemented` for its eight tests today. Its setup calls `ListRoots`
> to obtain a `ParentId`, that operation is unimplemented, and a setup failure
> is `skip` for every test of the group with `setup failed: …` — the harness
> contract, held identically by all three interpreters
> (`python-sdk/lib/harness.py`, `cli/internal/harness/harness.go`,
> `node-js-sdk/src/lib/harness.ts`). `unimplemented` is reachable only for a
> test whose *own* call gets the 501, which is every test of a probe group and
> no test of a lifecycle whose setup cannot run. It costs the demonstration
> nothing — a `candidate` group is excluded from `--compare-baseline` and
> `--max-failures`, and a group that only skips cannot promote
> (§ "Generated groups soak in before they gate") — but it means the emulator
> half moves these tests from `skip` to `pass`, not from `unimplemented` to
> `pass`, and the criterion's wording should say so.
>
> Measured, python-sdk against a slim image of this branch, three consecutive
> identical runs: before, `organizations-gen-probe` 25 `unimplemented`, 0
> `fail`; after, `organizations-gen-probe` 22 `unimplemented`,
> `organizations-gen-root` 1 `unimplemented`, `organizations-gen-ou` 8 `skip`,
> 0 `fail` throughout.

> **Criterion 5, second half — demonstrated (2026-09-06, #1813).** The emulator
> half landed: `ListRoots` and the five organizational-unit operations are
> `StatusInert` on `internal/services/organizations` over the shared Tier 1
> runtime. **Criterion 5 is met**, by #1818 (the recipe) and this PR.
>
> **Regeneration produced no diff at all, and that is the result, not a
> shortfall.** `go run -tags dev ./cmd/compatgen` after the implementation
> rewrites `scenarios/organizations.json`, `gaps.json` and
> `registry.generated.json` byte-identically; `-check` is clean at the same
> commit as it was before. The generator's own view of the six operations did
> change — `capabilitiesFor` reads `capabilities.AllCapabilities`, where all
> six now carry a row — but nothing downstream of that had anything left to do:
> the recipe already gave each of them a lifecycle role, so `probeGroup` had
> already skipped them and `uncoveredImplemented` had nothing to refuse. Had
> the recipe *not* claimed them, that pass would have refused all six instead —
> five `probe-of-implemented-op` and, for `UpdateOrganizationalUnit`, one
> `update-without-mutable`. The reason regeneration is silent is that #1818 did
> the work a step earlier.
>
> So the corpus is invariant across the implementation landing, and the tests
> move on their own. That is criterion 5 in its strongest form — not "the
> generated tests changed to match the new implementation" but "the generated
> tests did not change, and started passing" — and it is what §3.1 promised:
> a lifecycle over undeclared operations "starts passing the day the service
> reaches Tier 1, with no test edit".
>
> **The run.** Two slim images, one built from `origin/main` (recipe landed,
> emulator not) and one from this branch, each driven by the *same* generated
> corpus through all three interpreters. Nine runs after the change — three per
> interpreter — were identical to each other, and each interpreter agreed with
> the other two:
>
> | group | before | after |
> | --- | --- | --- |
> | `organizations-gen-ou` (8 tests) | 8 `skip` | **8 `pass`** |
> | `organizations-gen-root` (1) | 1 `unimplemented` | **1 `pass`** |
> | `organizations-gen-probe` (22) | 22 `unimplemented` | 22 `unimplemented` |
> | `organizations-gen-policy` (8) | 8 `pass` | 8 `pass` |
> | `organizations-gen-organization` (1) | 1 `pass` | 1 `pass` |
>
> `0 fail` in every cell, before and after, in `python-sdk`, `node-js-sdk` and
> `cli` alike. The `skip → pass` reading is the correction the first-half note
> above predicted: the group's setup `ListRoots` was the 501, so its eight
> tests were skipped rather than recorded `unimplemented`.
>
> Zero trace: after all nine runs against one instance,
> `ListOrganizationalUnitsForParent` on the root returns `[]` and
> `ListPolicies` returns `[]`. Each lifecycle's own delete test removes the
> resource, so the group teardown then reports `skipped` with the modeled
> not-found — the same benign shape `organizations-gen-policy` has always had.
>
> **No file under `compat/suites/` changed** — not in #1818, which touched only
> the generated `registry.generated.json`, and not here, which touched nothing
> under it at all. That untouched directory is the whole of what the criterion
> asserts.

Acceptance criteria:

1. `organizations-gen-probe` covers the 62 undeclared operations; **all record
   `unimplemented`, none records `fail`**, in all three interpreter suites.
2. `organizations-gen-organization` exercises `DescribeOrganization` with a
   shape assertion (identity member present and ARN-shaped) and **passes** — the
   first compat coverage any `StatusInert` operation has ever had.
3. The group is independently runnable, creates nothing, and needs no teardown.
4. Three consecutive runs are identical; the groups promote from `candidate` to
   `gated` through the normal soak with no hand edits.
5. **The demonstration that justifies the whole plan:** when
   `inert-tier-rollout.md` implements the next `organizations` operations,
   regeneration alone moves them out of the probe group into a lifecycle group
   and their status flips from `unimplemented` to `pass` — with **zero
   hand-written test changes in any suite**. Show this end to end on at least
   one operation before declaring the pilot complete.

> **Run — 2026-09-06.** The recount above is unchanged at this commit: **34 of
> 63** operations covered by 34 tests in three groups, 25 of them probes, and 29
> refusals in `gaps.json`, every one `never-probe` with its own curated
> sentence. #1809 moved the *derivation* of those refusals from the recipe's
> hand-written `neverProbe` map to a default-deny verb rule with the map as the
> exception list, which changed which of them carry a curated sentence rather
> than a generated one; it changed no membership.
>
> Criterion 1's "the 62 undeclared operations" reads **25 of the 54** today. The
> other 29 are the `never-probe` refusals, which is the guard working rather
> than coverage lost, and criterion 5's demonstration is aimed at five of them.
>
> | Criterion | Status | Evidence |
> | --- | --- | --- |
> | 1 — the probe group's operations all record `unimplemented`, none `fail`, in all three suites | **Met** | all 25 do, in `python-sdk`, `node-js-sdk` and `cli` |
> | 2 — `DescribeOrganization` passes with a shape assertion | **Met** | it passes, as does the same ARN check inside `organizations-gen-policy/CreatePolicy`. First compat coverage any `StatusInert` operation has had |
> | 3 — independently runnable, creates nothing, needs no teardown | **Met for the two groups it describes** | `organizations-gen-organization` and `organizations-gen-probe`; the policy group creates a real policy and has a teardown |
> | 4 — three identical runs; the groups promote through the normal soak with no hand edits | **Half met** | the runs are identical in all three suites; nothing has promoted yet |
> | 5 — the regeneration demonstration | **Met — #1813** | the OU lifecycle recipe (#1818) then the inert emulator implementation; regeneration changed no byte of the corpus and the same generated tests went `skip`/`unimplemented` → `pass` in all three interpreters. See the criterion 5 note above |
>
> Criterion 4's second half is not blocked on anything: `promotions.json` is
> `groups: {}` because the nightly `promote` job (#1792, #1798) needs three
> agreeing nightly runs of its own, and the first of those can only start once
> the groups are on `main`, which they now are. Criterion 5 is two PRs in order
> — the recipe establishes the before state, then the emulator PR regenerates
> the corpus in the same commit, with nothing under `compat/suites/` touched.
> That untouched directory is the whole of what the criterion asserts.

### 4.3 Pilot budget

The two services add **≤ 90 s** to a full local run and **≤ 2 min** to the
slowest CI matrix job. Exceeding that means sharding lands before rollout, not
after.

**Met, measured 2026-09-06** over the seven pilot groups against a slim image on
loopback: `node-js-sdk` 1.2 s, `python-sdk` 1.6 s, `cli` 23 s — the slowest
suite at a quarter of the local budget, and all three together at 26 s. So
sharding is not what G2 needs, and §3.10 records why it would not be the lever
at fleet scale either.

`#1830` grew the `sqs-gen-batch` absence poll from 1 s to 5 s, because the Go
SDK cannot send `VisibilityTimeout: 0` and the recipe's 1 s hide needs a poll
that outlasts it (§3.10) — ~4 s per suite per run, still inside the budget.

**G4 wave 1, measured 2026-09-06/07** (Tier 0, one run each, no soak needed):
`batch` go-sdk 4 s / cli 3 s, `servicediscovery` go-sdk 3 s / cli 2 s,
`elastic-load-balancing` go-sdk 2 s / cli 3 s — each comfortably inside this
budget; see the §2 note dated 2026-09-07 for the full per-service table.

### 4.4 G2 handoff — what an interpreter author has to agree to

Added 2026-09-05 from #1709's own report; **resolved 2026-09-06 by #1817**.
Every decision below has been made, all three interpreters make it identically,
and each is now pinned normatively in
[compat/model/README.md](../../compat/model/README.md) — section by section,
where a fourth interpreter author will look for it. This section is the index
into that, and the record of how each was settled; the README is the spec.

**The contract, in one list.**

- **Error matching** (README § Errors). An `error` clause carries both the
  modeled `shape` and the wire `code` — for SQS's not-found,
  `QueueDoesNotExist` and `AWS.SimpleQueueService.NonExistentQueue` — and an
  interpreter accepts an error whose code, on any of a **closed list of
  surfaces**, equals **either**: the exception class name, `__type` raw and
  after the last `#`, `Error.Code`/`Code`/`code`, and `x-amzn-query-error`
  before the first `;`. Matching is by **equality, never containment** — a
  message that merely contains a code satisfies nothing — and a failure that
  states no code on any surface matches nothing at all. On an
  `awsQueryCompatible` service the header **replaces** the body's code rather
  than joining it. The soak settled the open half: Overcast sent no
  `x-amzn-query-error` at all, which #1816 fixed for SQS (closing #1810), so the
  same clause now matches through the header and through `__type`. Nine shared
  fixtures in `compat/model/testdata/errors/` hold the whole rule, and every
  interpreter runs all nine, skipping by name and with a reason any surface it
  cannot observe — a silently ignored fixture looks exactly like a passing one.
- **Names.** `$name` is `{OVERCAST_COMPAT_RUN_ID}-{group}-{suffix}`, with the
  group token the *whole* group name and no shortening anywhere — that is what
  makes the name-hygiene rule (§2.4) hold by construction.
- **`eventually`.** Exports from a `readback` inside it are applied on the
  attempt that passes, and only then.
- **Setup and teardown.** A setup failure — an error or an unresolvable `$ref` —
  reports **every** test in the group as `skip` with `setup failed: <error>`,
  and teardown still runs. Each teardown step is wrapped individually: one
  failure skips that step and the rest continue.
- **`equals`** is JSON equality *after* the SDK's own mapping, never string
  comparison; timestamps and blobs are never compared.
- **`isList`** holds when the path resolves to a list, empty or not — and when
  it does not resolve at all, because several AWS services omit an empty list
  member instead of serializing `[]` (SQS's `ListQueues` among them). A present
  value that is *not* a list still fails it. It is the check every `List*` probe
  carries, so getting it wrong fails 16 of the 25 `organizations` probes at
  once, and `nonEmpty` never substitutes for it.
- **Probe safety is the interpreter's rule too.** A probe group has no setup and
  no teardown, and every value it sends is a curated or synthetic literal that
  names nothing the run owns (§3.5 guard 5). An interpreter must not "helpfully"
  fill a missing member from context, retry a probe against a different
  identifier, or clean up after one — there is nothing to clean up, and a probe
  that reaches a real resource is the one failure mode a scenario file pointed
  at AWS cannot recover from.
- **Failure messages** carry, in order, the six fields listed in the README's
  § Failure messages: `group/test`, the operation, the exact params JSON sent,
  the assertion kind and path, expected versus actual, and the scenario file
  plus step index. `cmd/compatgen -explain <group>/<test> -lang <language>`
  renders the same test as pseudo-code so a failure reproduces by hand.
- **Landing a backend** means flipping that one suite in `scenarioBackends` in
  [cmd/compatgen/registry.go](../../cmd/compatgen/registry.go) in the
  interpreter's own PR, and regenerating. The table is per suite, so each of the
  three PRs flips its own entry and commits the regenerated
  `registry.generated.json`; until one suite is in it the generated registry
  stays `groups: []` by construction (§3.6), so the interpreter and the groups
  it can run arrive together. All three have now done exactly that, and the
  table reads `cli, node-js-sdk, python-sdk`. G3's typed backends follow the
  same route with no new mechanism.

**What the three interpreters had to be aligned on afterwards**, which is the
part of this section a G3 author should read first: none of the three is a
quirk of the language that had it, so each is available in any backend.

- `eventually` reported its budget as a *suffix* on the last attempt's message
  in one suite and a *prefix* in the others, so one generated group's give-up
  read differently depending on who ran it. The prefix form is the rule.
- A `matches` pattern the interpreter's own regex engine will not compile
  escaped as a raw language-level exception in one suite. It is an ordinary
  mismatch — expected `pattern <p>`, actual `unsupported pattern: <why>` — and
  the pattern is compiled *before* the value is looked at, so the report does
  not depend on what came back.
- A code-carrying surface was read in one spelling rather than every spelling
  the rule lists, and one suite fell back to a substring match when nothing
  parsed — which is precisely where the near miss that equality excludes comes
  back. The fixtures pin both directions: neither `NotFoundException` nor
  `ResourceNotFoundException` may satisfy a clause naming the other.

**Teardown after a failed setup** is the fourth, and it was a harness fault
rather than an interpreter one: three suites reported the group's tests as
`skip` and then returned without unwinding what setup had already created.
Fixed in `cli` (#1790), `go-sdk` (#1808) and `node-js-sdk` (#1812); `python-sdk`
never had it. The sentence in this list had been describing an intention.

**Where this list is coordinated.** G2 is tracked as **#1768**, which carries
this contract, the one-PR-per-suite breakdown (`python-sdk`, `node-js-sdk`,
`cli`) and the §4.1/§4.2 acceptance criteria as its definition of done. The
normative spec the three interpreters are written against is
`compat/model/README.md`; this section is the set of decisions they have to make
identically, not a second copy of it. Take an open question here to #1768 rather
than settling it in one suite.

**Fidelity assumptions to watch in the soak.** Each is a deliberate choice
recorded in `recipes/sqs.json`, and each is a plausible source of a first-run
surprise: the batch group tracks whichever entry arrives first, via
`Messages[0]`; the queue resource's `async` budget is 30 attempts 2 s apart, so
`DeleteQueue`'s absence check and the `ApproximateNumberOfMessages` read-back
each get the full minute AWS documents for those rather than the five seconds a
queue read-back would otherwise take; the `PurgeQueue` read-back allows a
minute of its own (12 attempts, 5 s apart) because AWS documents the counters
as lagging; `DeleteMessageBatch` quotes the receipt handle the `ReceiveMessage`
list test re-exports, because AWS asks a delete to carry the most recent one;
and every `ReceiveMessage` that must leave a message visible passes
`VisibilityTimeout: 0`, while the two that must leave it in flight do not,
because `ChangeMessageVisibility` on a visible message is `MessageNotInflight`
on AWS. **Every one of them held on the first run** — three identical runs in
each of the three suites, no fail and no flip — so they are recorded here as
settled choices rather than as things to watch. The two absence polls and the
`eventually` budgets among them are load-bearing, not slack: §3.10 says why
neither may be shortened for speed.

**One assertion was already known to fail, and it has since been fixed.**
Identity fields are asserted against the model's own pattern where RE2 can
express it. `organizations` models an organization ARN as
`^arn:aws:organizations::\d{12}:organization\/o-[a-z0-9]{10,32}$`, and the inert
implementation used to mint the organization as `o-overcast` — eight characters
after the `o-` where AWS requires ten to thirty-two — so
`organizations-gen-organization/DescribeOrganization` (§4.2's criterion 2) and
the ARN check in `organizations-gen-policy/CreatePolicy`, whose ARN embeds the
same id, both failed against Overcast. That was the generator doing its job: a
model-derived assertion catching an identifier a hand-written test would have
been written around. It was filed as **#1736** and fixed by **#1750**, which
derives the id deterministically from the account id as `o-` plus ten hex
characters
([internal/services/organizations/inert_policy.go](../../internal/services/organizations/inert_policy.go),
with `aws_id_pattern_test.go` holding the pattern). **Both pass in all three
suites** as of the 2026-09-06 run, which is what §4.2's criterion 2 asked for.
It is also the pattern to expect from the rest of the programme: #1816 is the
second instance, an SQS error surface the interpreters needed and Overcast was
not sending. A generated corpus finds fidelity bugs because it asserts what the
model says rather than what the emulator does.

---

## 5. Phasing

Status as of 2026-09-06 is in the §2 note: **G0 and G1 are done, and G2 is code
complete** — three interpreters merged, the pilot run, two items open. The
`Status` column below records that; `Contents` is left as written so the
original scope stays legible.

| Phase | Status | Contents | Effort | Acceptance gate |
| --- | --- | --- | --- | --- |
| **G0** Foundations | **Done** — #1356, #1357, #1367, #1370, and the loader tail under #1393, all seven suite PRs merged and the issue closed. `suites` scoping was honoured for every group in four suites and for generated groups only in `java-sdk`, `dotnet-sdk` and `rust-sdk` until #1737 aligned the three and re-seeded their baseline shards — see the §2 note | Shard `compat/baseline.json` → `compat/baseline/<suite>.json` (+ size budget); `--shard i/n` and `--generated-registry-file` in `cmd/compat`; `registry.generated.schema.json`; all 8 loaders read the generated sibling and fall back to a scenario resolver hook; `candidate`/`gated` state honoured by both gates; `compat/AGENTS.md` amendment for generated `suites` scoping + the lint that bounds it | M | With an **empty** generated registry, every gate, report and dashboard behaves exactly as today; baseline shards aggregate byte-identically; the scoping lint rejects a hand-written group that adds `suites` |
| **G1** Model layer | **Done** — `internal/awsmodel` #1359, shape snapshot via inert-tier I1 with `sqs` added in #1684, `cmd/compatgen` and `compat/model/` in #1709. The model-utilisation follow-ups (#1795, closed) then moved three derivations out of the recipes and into the generator — see the §2 note | Extract `internal/awsmodel` AST reader; `cmd/compatgen` skeleton; the pruned shape snapshot `models/aws/shapes/` + `shapes-sha256` (shared deliverable with [inert-tier-rollout.md](./inert-tier-rollout.md) Phase I1 — build once, whichever plan gets there first); IR + recipe JSON schemas; `--scaffold`, `--review-report`, `--explain`; `gaps.json` | M | `make compat-model-check` regenerates byte-identically offline; the sha gate catches a hand edit; the snapshot is within its size budget; scaffolding a service produces a recipe skeleton a human can complete |
| **G2** Pilot | **Done**, tracked as **#1768** (closed 2026-09-06). All three interpreters are merged — `python-sdk` #1787, `node-js-sdk` #1788 (+ #1796), `cli` #1790 — and the seven pilot groups run in all three suites with zero failures, identical across three runs, inside the §4.3 budget; the §2 note has the tally. Every §4.1 criterion and §4.2's 1–3 are met. #1813, the §4.2 criterion 5 regeneration demonstration, is met (#1818 then #1813): regeneration changed no byte of the corpus and the generated OU tests started passing on their own, in all three interpreters. The first candidate → gated promotion (machinery #1792/#1798) happened on 2026-09-06 as #1871, gating all nine groups; #1879 made the gate test state-aware. #1801 landed as #1823 | `python-sdk`, `node-js-sdk` and `cli` interpreters; `recipes/sqs.json` + `recipes/organizations.json`; the §4 acceptance criteria | L | Every §4.1 and §4.2 criterion met, including the regeneration demonstration in §4.2.5 |
| **G3** Typed backends | **Done**, tracked as **#1820**. All four typed backends landed, one PR each: `go-sdk` (#1830, plus #1836 for emit-time SDK type resolution and #1833 for the precedent notes), `java-sdk` (#1851), `dotnet-sdk` (#1848), `rust-sdk` (#1853). Every backend produces results identical, test for test, to the three interpreters and to each other — 39 `pass` / 23 `unimplemented` / 0 `fail` / 0 `skip`, three runs each — and every generated group's `suites` now lists all seven backends. §3.2's binding decision, measured rather than assumed: only `go-sdk` reads the vendored SDK at emit time; `java-sdk`, `dotnet-sdk` and `rust-sdk` derive types from the pinned model alone. G4 fleet rollout is unblocked; see the §2 note dated 2026-09-06 | Source emitters for `go-sdk`, then `java-sdk`, `dotnet-sdk`, `rust-sdk` (one suite per PR); member→field naming rules per language | L each | Generated source compiles in the suite's normal build; the pilot groups produce **identical** results to the interpreter suites; generated `suites` scoping widens automatically on regeneration |
| **G4** Tier-1 fleet rollout | **In progress**, tracked as **#1883**. **Wave 1 done** at Tier 0 (2026-09-07): `batch` #1881, `elastic-load-balancing` #1882 (+ classification fix #1889, closing #1884), `servicediscovery` #1887 — the inert tier's Phase I4 pilot trio, stacked bottom-up and merged. Every probe test lands `unimplemented` and every lifecycle group `skip` until the inert tier implements each service (the #1818 → #1821 precedent) — measured 0 `pass` / 0 `fail` in all three, batch 11 `unimplemented`/34 `skip`, servicediscovery 3/25, elastic-load-balancing 5/12; see the §2 note dated 2026-09-07 for the per-service table and the four generator faults the wave found. **Wave 2 done** (#1883, 2026-09-07): `secretsmanager` (#1897), `sns` (#1900), `kms` (#1899), `iam` (#1919, 180 modeled operations), chosen by implemented-operations-per-snapshot-byte rather than by smallest operation count; its snapshot/budget PR (`maxShapeSnapshotBytes` to 800 KiB) merged as #1891. Ten of the 26 new wave-1/wave-2 groups are gated on `main` (#1925); the rest stay `candidate` behind a known defect or a Tier-0 skip — see the §2 notes dated 2026-09-07 for per-service results, every defect filed, and the exact list. `elastic-load-balancing`'s rust nested-composite fault (#1885) is fixed by #1890, and #1896 (only `rust-sdk` read a nested `Error.Code`) is fixed by #1918 — `sns` and `iam`, both Query-protocol, are the proof — so no wave-1 or wave-2 group is scoped away from `rust-sdk` or blocked on Query error handling any more | One service per PR, ordered by [inert-tier-rollout.md](./inert-tier-rollout.md) then [full-emulation-priority.md](./full-emulation-priority.md); capped probe groups for [services-never-emulated.md](./services-never-emulated.md) | L, parallelizable per service | Per service: recipe reviewed, no unexplained refusal in `gaps.json`, soak passed, CI wall-clock within budget, coverage metric moves |
| **G5** Steady state | Not started | Weekly model-refresh PR regenerates scenarios; coverage becomes the dashboard headline; `--slowest N` latency census | S | A model-refresh PR shows added/removed operations per service and cannot break the gate; coverage per service/tier is published |
| **G6** Native-group migration (§3.11; overlaps G4/G5, starts any time after G3) | **In progress** — the mechanism landed with the pilot (#1898) and its prerequisites (#1916), and the first flip is merged: `sqs-queues` (#1932, closing the prerequisites tracker #1903, 2026-09-07), after the nightly `--compare-shadow` (run 34066538988) reported all 56 (suite, test) pairs in agreement in each of its three soak runs. The flip deleted 1,161 native lines across all seven suites and left every suite's `sqs-queues` row `pass` (8/8); the registry and baseline names did not move. `sqs-queues` carried no `dotnet-sdk`/`rust-sdk` parity debt going in, so the measurable gain is the union of assertions — `java-sdk` and `rust-sdk` gain the clauses their natives omitted. **G6 wave 1 chosen** (#1116, 2026-09-08): `kinesis-streams`, `logs-groups`, `eventbridge-rules`, `ecs-clusters`, `cognito-userpools`, `rds-instances` — 94 `dotnet-sdk`+`rust-sdk` rows; cross-service groups (`pipes-wiring`, `eventbridge-target-fanout`) wait on #1931. See the §3.11 notes dated 2026-09-07/08 for the two-PR shape and what the pilot found in the natives. The plan's original "94 hand-written groups" scope (below) is itself stale: `registry.json` has 141 groups / 803 tests as of 2026-09-08 | Port the existing 94 hand-written groups to authored IR scenarios, group by group: same registry names, one parallel soak cycle, results must match, then delete the per-language code. Exceptions file + lint for what stays native (streaming, presigned flows, the idiom suite). | L, parallelizable per group | Per group: soak-parity with the native predecessor, native code deleted, registry names unchanged; fleet-wide: rust/dotnet parity debt reaches zero via backends, the exceptions file is the only remaining native test code and every entry carries a reason |

Every phase begins with a failing check, lands as small independently
reviewable PRs, and leaves `main` green under both existing gates.

---

## 6. What "done" means

Done means all of the following hold simultaneously:

1. **Coverage is model-relative and published.** For every service in the
   allowlist, "operations with a compat test / operations in that service's
   tier" is a computed number on the dashboard, and for Tier 1 and Tier 2
   services it is at or near 100%.
2. **Adding an operation to the emulator costs one recipe line, not eight test
   files.** Implementing a new operation means: declare the capability,
   regenerate, review the diff. Eight suites gain the test.
3. **Refreshing the AWS model surfaces new operations automatically**, as
   candidate tests that can never break the gate.
4. **No generated test can be vacuous.** The IR cannot express a test without an
   assertion, and CI proves the invariant on every PR.
5. **The gate is still absolute and still trusted.** `--max-failures 0` holds,
   `flaky.json` is still empty or shrinking, and no generated test was ever
   quarantined to make a run green.
6. **Native per-suite test code is the audited exception, not the medium.**
   Behavioural depth lives in authored IR scenarios (written once, executed by
   every backend); the exceptions file is short, linted, and every entry has a
   reason. No test exists in eight hand-maintained copies, and adding a
   behavioural test costs one reviewed scenario, not eight implementations.
7. **The boundary is intact.** Nothing under `compat/` imports emulator Go code;
   the generator is a build-time tool whose output is committed data.

---

## 7. Open questions

1. **`shapes.json` size budget and contents — resolved 2026-09-05, measured.**
   No depth limit is needed at this scope, and the fallback of deriving shapes
   at generation time is not required. On the five committed services the
   snapshot is **307,497 B of the 336 KiB budget** — `batch` 102,309,
   `organizations` 96,119, `servicediscovery` 39,316,
   `elastic-load-balancing` 35,614, `sqs` 34,139 — which is roughly 1.2–2.3 KB
   per operation. Maximum reference depth is 6–7 for four of the five and
   **16** for `batch`. There is exactly one recursive shape in the set
   (`organizations`'s `HandshakeResource.Resources` → `HandshakeResources` →
   `HandshakeResource`), and the pruner terminates on it by taking the closure
   rather than by bounding depth, so recursion costs nothing to allow.
   Re-measure when the allowlist widens: the budget, not a depth cap, is the
   thing that has to hold.
2. **The `suites`-scoping amendment** to `compat/AGENTS.md` (§3.6) changes a rule
   that currently reads as absolute. It needs explicit reviewer agreement before
   G0 lands, not after.
3. **One name-mapping table, four consumers — decided 2026-09-05: there is no
   table.** The scenario header carries `sdkId`, `endpointPrefix`,
   `signingName`, `protocol`, `apiVersion` and `targetPrefix` and nothing
   SDK-specific; each backend derives its own package or class name from those,
   by the per-backend rules in
   [compat/model/README.md § Naming](../../compat/model/README.md). A table
   would need seven columns maintained by hand for every service the allowlist
   gains, which is the enumeration §3.11 exists to avoid.
   **The residual is a short list of known derivation breaks**, recorded in that
   section as per-backend follow-ups rather than smuggled into the IR: botocore's
   service name differs from the endpoint prefix for `elasticloadbalancing`
   → `elb`, `monitoring` → `cloudwatch`, `email` → `ses` and `states` →
   `stepfunctions`; and the Go SDK package is `sfn` for `SFN` and
   `elasticloadbalancing` for `ELB`, neither derivable from the SDK id. Neither
   pilot service breaks. Drift detection is still unbuilt: the first backend
   that needs an override should land the override table and a test that the
   client it names actually constructs.
4. **Should `na` be derivable?** When the CLI or an SDK genuinely lacks an
   operation, the registry wants `na`. That is partly mechanical (botocore knows
   what the CLI exposes) and partly not. Deriving it would remove a class of
   hand-maintained divergence; getting it wrong would silently erase coverage.
5. **Generated backends versus outstanding parity debt — resolved by §3.11.**
   Generation lands first; the G6 migration then burns `rust-sdk`'s 297 and
   `dotnet-sdk`'s 261 debt entries down as groups are ported to scenarios, since
   a scenario-backed group is implemented in every suite with a backend. Nobody
   hand-ports those 558 tests. The residual question is only sequencing detail:
   whether the debt file should distinguish "awaiting backend" from "awaiting
   port" while G6 is in flight.
6. **Baseline format at 10× scale.** Sharding buys headroom; it may not be
   enough at full Tier-1 coverage. Compressing, or making `pass` the implied
   default, both weaken the "failing and absent from the baseline" check that
   #462 depends on. Decide with measured file sizes at the end of G4, not now.
7. **Registry `service` keys are unvalidated.** Nothing asserts that a
   `registry.json` group's `service` matches an Overcast capability service key
   (the `cognito` case works only because
   [registry_data.go:76](../../internal/awsapi/registry_data.go) aliases
   `cognito-identity-provider`). Generated groups will use the capability key by
   construction; a lint should hold hand-written groups to the same rule, and it
   is cheap to add during G0.
8. **Probe safety rests on an operation's *name*, not on the model's own
   statement — added 2026-09-06.** §3.5's guard 5 refuses everything that is not
   a `Describe*`, `List*` or `Get*`
   ([cmd/compatgen/recipe.go](../../cmd/compatgen/recipe.go)). Smithy has a
   trait that says this outright, `smithy.api#readonly`, and the pruner does not
   keep it: it is absent from `shapeTraitAllowlist` in
   [cmd/awsmodelgen/shapes.go](../../cmd/awsmodelgen/shapes.go), so no committed
   snapshot carries it. The verb rule is a sound default and it is default-deny,
   so its errors cost coverage rather than safety — `CancelMessageMoveTask`
   (§4.1) is the pilot's one instance. But it is a heuristic standing in for a
   fact the model already knows, and the fix is one line in the pruner's
   allowlist plus a regeneration. Decide before G4 puts probe groups on a
   per-service cadence, because the cost of the heuristic scales with the
   allowlist and the exceptions are hand-written.
9. **The shared error-fixture corpus runs in two different places across the
   four typed backends — added 2026-09-06, filed as #1865; resolved
   2026-09-06 by #1895.** `go-sdk` answers
   `compat/model/testdata/errors` from a root checkout, in
   `compat-suite-unit-tests` plus a host `go test`; `java-sdk` and `rust-sdk`
   answer it in the same job (`mvn -B test` / `cargo test`, from the checkout)
   while their own Docker image builds — context `compat/suites/` — cannot
   reach the corpus and abort there rather than failing; `dotnet-sdk` widened
   its image's build context to `compat/` instead and answers the corpus
   inside the image build. Both shapes work, but the rule today is "whatever
   that suite's author chose," and `test.yml`'s job comment has already needed
   rewriting more than once to explain the exceptions. #1865 asks for one
   convention applied to all four, plus a check that a suite whose fixture
   test is skipped anywhere in CI fails loudly rather than reporting green —
   the gap #1851's review found: `java-sdk`'s fixture test asserted nothing on
   every push, because `compat-suite-unit-tests` excluded it on the assumption
   that the image build already covered it, and the image build's context
   could not reach the corpus at all. **#1895 moved every fixture test into
   `compat-suite-unit-tests` from a full checkout** — `dotnet-sdk`'s image
   context reverted to `compat/suites/` to match `java-sdk`/`rust-sdk` — and
   gated all of them behind `OVERCAST_COMPAT_FIXTURES_REQUIRED`, set only for
   that job: found and required there, skipped by name (with a reason) inside
   a narrower image build, and never silently vacuous either way.
10. **`$name` can exceed an AWS length limit the model never declares — added
    2026-09-07, filed as #1886.** The IR's name-hygiene rule (§2.4, §3.3)
    fixes `$name` as `{runId}-{group}-{suffix}`, and a recipe has no shorter
    spelling available. Real AWS caps some resource names well below what
    that produces without saying so in the model: an ELB Classic load
    balancer name is capped at 32 characters, and the `elastic-load-balancing`
    wave-1 recipe's own `$name` is 54 (the §2 note dated 2026-09-07). Overcast
    does not enforce the limit, so it accepts a create real AWS would refuse
    on the name alone — and so will the inert tier that implements it.
    Fixing it means either a modeled length constraint (which would make the
    generator refuse the operation instead of generating it) or a shorter
    `$name` scheme, and the second changes every generated name in the corpus
    at once — not a decision for one recipe PR. Decide the corpus-wide
    spelling before a second service with a comparable cap lands.
11. **Two G4 wave 1 follow-ups, both filed 2026-09-06, both now fixed.**
    #1885: the rust emitter cannot spell a composite nested inside
    a structure literal, which costs `batch-gen-jobdefinition`,
    `elastic-load-balancing-gen-loadbalancer` and
    `servicediscovery-gen-service` their `rust-sdk` row until it lands — a
    fix merged as #1890 on 2026-09-07. #1878: `rust-sdk` needed an
    XML→document conversion before a Query or
    REST-XML service could reach Tier 1 — it blocked nothing generated at the
    time, but would have blocked `elastic-load-balancing`'s own inert-tier
    implementation, and later `sns`'s and `iam`'s generated groups, from
    reaching `rust-sdk` parity. **Fixed 2026-09-06 by #1894**, which builds
    the rust-sdk scenario document from Query/REST-XML wire bodies as well as
    JSON. See the §2 note dated 2026-09-07 for what each cost while open.
12. **A member-less new operation could scope a whole probe group away from
    one suite, not just itself — added 2026-09-07, filed as #1921.** The
    existing lever for "the pinned SDK predates this operation" is indirect:
    withhold a required member's `values.json` literal so the probe is
    refused `unbound-required-member` (kms's `GetKeyLastUsage`, iam's
    `GetRoleTemplateVersion`). An operation with no required member has no
    such lever — iam's `GetAccountProperties` (added to the model in 2026)
    would be probed automatically, `go-sdk` would refuse it at emit time and
    take the whole probe group's `go-sdk` row with it, and `java-sdk`,
    `dotnet-sdk` and `rust-sdk` would each emit a call their SDK cannot
    compile; the iam recipe (#1919) parks it under `never-probe` with a
    sentence explaining the pin, which is a safety list carrying a pin fact.
    #1921 asks for a first-class `sdk-older-than-model:<Op>` refusal,
    declared per operation and lint-checked to name the pin that lifts it, so
    `never-probe` goes back to being only about safety.
13. **The IR cannot compare a member the SDKs deserialize differently —
    added 2026-09-07, filed as #1922.** IAM's five `policyDocumentType`
    members are URL-encoded JSON strings on the wire; botocore's
    `json_decode_policies` hook decodes them, so `python-sdk` and `cli` see an
    object where `go-sdk`'s field is a `*string` — measured on #1919 (`aws iam
    get-role` prints an object). `equals` is strict JSON-type equality, so no
    clause can assert the document's content across all seven suites, and the
    iam recipe asserts none. #1922 asks for an `equalsJSON` check kind: both
    sides parsed as JSON (a string operand URL-decoded first) and compared
    structurally, so the same clause holds regardless of which shape the
    backend saw.
