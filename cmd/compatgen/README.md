# compatgen

`compatgen` turns the pruned AWS shape snapshot (`models/aws/shapes/`) plus
the hand-curated recipes under `compat/model/recipes/` into the compat
scenario IR (`compat/model/scenarios/`), the refusal report
(`compat/model/gaps.json`), the generated registry sibling
(`compat/suites/registry.generated.json`) and — for the suites whose SDK has
no dynamic-dispatch API — the source they compile
(`compat/suites/go-sdk/internal/groups/scenarios_*_gen.go`,
`compat/suites/java-sdk/src/main/java/io/overcast/compat/groups/Scenarios*Gen.java`,
`compat/suites/dotnet-sdk/Groups/Scenarios*Gen.cs`,
`compat/suites/rust-sdk/src/groups/scenarios_*_gen.rs`). It is a build-time
tool whose output is committed data; nothing under `compat/` imports it or any
other emulator Go code.

The IR, the recipe format and the refusal vocabulary are documented in
[compat/model/README.md](../../compat/model/README.md). The design is
[docs/plans/compat-coverage-modelgen.md](../../docs/plans/compat-coverage-modelgen.md) §3.

## Usage

```sh
make generate-compat-model        # regenerate scenarios, gaps.json and the registry
make compat-model-check           # prove the committed output is byte-identical (CI)
```

The underlying command is `go run -tags dev ./cmd/compatgen`; the `dev` tag
is required because the capability table it reads
(`internal/capabilities/all.gen.go`) is dev-only, exactly as for `cmd/capgen`.

| Flag | Meaning |
| --- | --- |
| *(none)* | generate every recipe under `compat/model/recipes/` and rewrite every output |
| `-check` | regenerate in memory and compare byte-for-byte, writing nothing; also fails on a scenario file whose recipe is gone |
| `-scaffold <service>` | print a recipe skeleton for a service in the shape snapshot |
| `-review-report [service]` | print the Markdown review report for a PR body |
| `-explain <group>/<test> -lang <python\|node\|cli\|go\|java\|dotnet\|rust>` | render one test as pseudo-code — a generated group or an authored one |
| `-sample <n>` | scenarios rendered in the review report (default 3, fixed seed) |
| `-root <dir>` | repository root (default `.`) |

## Inputs

- `models/aws/shapes/<service>.json` — the pruned shape snapshot written by
  `cmd/awsmodelgen`. A recipe for a service the snapshot does not cover is
  refused with the instruction to add it to `models/aws/shapes-services.txt`
  and run `make generate-aws-operations`. The generator never reads the raw
  Smithy corpus, and never runs at test time.
- `compat/model/recipes/<service>.json` — validated against
  `recipe.schema.json` on load, then against the model: every operation must
  exist, every path must resolve, every literal must be of the member's
  kind and inside its constraints.
- `compat/model/authored/<group>.json` — an **authored scenario**: the same IR
  a recipe produces, written by hand to port one hand-written registry group
  (`docs/plans/compat-coverage-modelgen.md` §3.11). It is an input with no
  recipe, so nothing rewrites it; it is validated against
  `scenario.schema.json` and the IR's own rules on load, then against the
  model, and it reaches the four typed emitters through exactly the generation
  a recipe produces — under the emit key `authored-<group>`, so its source is
  `scenarios_authored_<group>_gen.*` rather than being folded into the
  service's own generated file. Its group and test names must be the registry
  group's, which is what keeps baseline, flaky and parity-debt history intact
  across the port. See
  [compat/model/README.md § Authored scenarios](../../compat/model/README.md#authored-scenarios).
- `compat/suites/registry.json` — read for that check and nothing else.
- `compat/model/values.json` — curated literals, validated the same way.
- `compat/model/promotions.json` — the candidate → gated soak ledger, and
  the one thing that can move a generated group's `state`. It is written only
  by `go run ./cmd/compat --promote-generated`, so the generator keeps sole
  ownership of `registry.generated.json`; an entry for a group no scenario
  produces is refused. Missing file, or a group with no entry: `candidate`.
- `internal/capabilities/all.gen.go` — which operations the emulator
  implements. An operation with a status other than `Unsupported` is
  implemented and may never sit in a probe group.
- `internal/awsapi` — the routing manifest, for the scenario file's `client`
  header (SDK id, protocol, API version, target prefix). It is *not* what
  ties a recipe to a snapshot: a recipe whose Overcast key differs from the
  model's says so itself, in its `model` field (`"service": "cognito"`,
  `"model": "cognito-identity-provider"`), and that is the name the snapshot
  file carries. The manifest's alias table is used only by `-scaffold`, to
  print the Overcast key for a model service.

## Adding a service

1. Make sure its shapes are in the snapshot (`models/aws/shapes-services.txt`).
2. `go run -tags dev ./cmd/compatgen -scaffold <service> > compat/model/recipes/<service>.json`
   and complete the skeleton against the real AWS API semantics. It marks its
   own three kinds of line and the schema refuses each of them, so it cannot
   be mistaken for a finished recipe: `$comment` on a derived value names the
   trait or rule that produced it, `$todo` stands where only a human can
   supply the value, and `$review` marks a lifecycle whose create or delete is
   not read-only-safe by the verb rule. See
   [compat/model/README.md § Scaffolding](../../compat/model/README.md#scaffolding).
3. `make generate-compat-model`, then read `gaps.json`: each refusal is a
   line in the recipe or in `values.json`, or a deliberate gap.
4. Put `go run -tags dev ./cmd/compatgen -review-report <service>` in the PR
   body. It lists operations covered vs modeled, every refusal with its
   reason, every automatic name-match binding (the riskiest inference the
   generator makes), every curated or synthetic value it bound, and a fixed
   sample of scenarios rendered as pseudo-code.

One service per PR: the reviewable artifacts are the recipe, the values
entries and the gap report.

## What the generator will not do

- Guess a value. A required member no rule can bind refuses the operation
  (`unbound-required-member:<Member>`). Rule 4 derives a literal only where
  the model's constraints enumerate or bound the legal values: the first
  member of an enum (the snapshot's own order where it survives the snapshot —
  see `compat/model/README.md`), a range minimum, and
  `false` for a required boolean — two legal values, of which `false` is the
  one asking the service to do less. §3.3's fourth candidate,
  "the shortest legal string for a pattern", is deliberately **not**
  implemented: a pattern constrains a string's syntax, never its reference,
  so the shortest match for `^arn:aws:.*` is a well-formed ARN of something
  that does not exist. The emulator accepts far more of those than AWS does,
  which is the class of value §3.10 says belongs in the gap report.
- Reshape a bound value. Rule 1 hands the member what the export holds, or
  that value inside a **one-element list** where the service models the member
  as a list of it — ELB Classic's `LoadBalancerNames` beside the singular
  `LoadBalancerName`, written `"LoadBalancerNames": ["loadbalancer.name"]`.
  One level of wrapping is the whole of it: no second element, no nesting and
  no conversion. A wrap the model contradicts is an error naming the member,
  not a refusal, exactly as a mistyped literal in `params` is.
- Point a probe at anything the run owns. A probe is the one generated call
  no create/delete pair contains, so rules 1 and 2 are off inside a probe
  group: it binds only curated or synthetic literals. A member only a live
  export could supply refuses the operation
  (`probe-binds-live-resource:<Member>`), and an operation a recipe lists
  under `neverProbe` — irreversible even with a stranger's identifiers, or
  taking no identifier that could miss — is refused before it is bound
  (`never-probe`).
- Emit a test without an assertion. The only test constructor takes its first
  assertion as a non-optional argument, the schema says `minItems: 1`, and
  `validateScenario` re-checks the finished file.
- Probe an implemented operation, or generate an update without a declared
  mutation, or a create/delete with no read-back path. Authored coverage is
  held to the same rule rather than exempted from it: only a clause that
  calls the service again — a `readback`, a `listContains` or `absent` with
  its own call, or an `eventually` around one — counts as verifying anything,
  so an authored `create.assert` made only of `responseField` clauses is
  refused (`no-readback-path`), and an authored update-family operation whose
  clauses all read its own response is refused (`update-without-readback`).
- Assert something the call cannot have changed. A probe of an operation
  that returns nothing is refused (`no-output-to-assert`) rather than given a
  read-back of the resource it names, which would hold whether or not the
  call did anything.
- Assert a pagination token. `identityMember` skips the member `@paginated`
  names as its `outputToken` and every member named like one (`NextToken`,
  `Marker`, anything ending in `Token` or `Marker`): that field is precisely
  what AWS omits on a single-page answer, so asserting it non-empty asserts
  the opposite of a correct response. A `List*` left with only its page gets
  `{"isList": true}` on that list instead — true of an empty or omitted page
  (some services omit the member instead of serializing `[]`), false of a
  present response that is not a list — and one with neither an identity nor
  a single list is refused (`no-output-to-assert`).
- Emit a timestamp, blob or document literal: the SDKs disagree on how those
  are passed and an interpreter has no model to convert with, so such a
  member stays unbound and the operation is refused.
- Write a group to the registry while no suite has a scenario backend. The
  `scenarioBackends` table in `registry.go` is that availability. It was
  empty until the G2 interpreters landed, so the registry stayed `groups: []`
  and the empty-file gate kept holding; the scenario files and `gaps.json`
  are fully generated regardless.
- List a suite against a group its backend cannot execute. `suites` is
  derived from backend availability, so a group a source emitter refused is
  scoped to the other backends instead — and a group no backend can run is
  left out of the registry altogether. Listing it anyway would turn the
  refusal into a hard failure in that suite, whose loader treats a generated
  test with no backend as a coverage hole rather than a skip.

## Source emitters

The three interpreter suites execute the IR at run time. The typed SDKs
cannot: they have no public dynamic-dispatch API, and
[the plan](../../docs/plans/compat-coverage-modelgen.md) §3.2 rejects reaching
into their marshaller layers to fake one, because the reason for running eight
suites is that each exercises its own real typed serialization path. So
`emit_go.go` writes Go instead — one function per scenario test, each building
a real `*sqs.CreateQueueInput` and calling a real client method — which the
`go-sdk` suite's ordinary build compiles; `emit_java.go` writes Java the same
way, one method per test building a real `CreateQueueRequest`, compiled by the
`java-sdk` suite's `mvn package`; and `emit_rust.go` writes Rust the same way,
one fluent builder chain per test, compiled by `cargo build`.

What is emitted is the *data* plus the typed calls. The semantics — the
context bag, `$name`/`$ref`, the closed check set, error matching,
`eventually`, the six-field failure message — are written once by hand in each
suite (`compat/suites/go-sdk/internal/scenario`,
`compat/suites/java-sdk/.../io/overcast/compat/scenario`,
`compat/suites/rust-sdk/src/scenario`) and never re-emitted.

### Where the types come from

A member's Go spelling depends on what smithy-go made of it, and the pinned
shape snapshot cannot say: the snapshot and the vendored SDK are generated from
different revisions of the same AWS model, and for the pilot service they
already disagree — `ReceiveMessage`'s `MaxNumberOfMessages`,
`VisibilityTimeout` and `WaitTimeSeconds` target `NullableInteger` in
`models/aws/shapes/sqs.json`, which says pointer, and are plain `int32` fields
in `aws-sdk-go-v2/service/sqs`.

So `gosdktypes.go` asks the SDK, with `golang.org/x/tools/go/packages`, loading
`github.com/aws/aws-sdk-go-v2/service/<pkg>` from **the `go-sdk` suite's own
module** — the module the emitted source is compiled in, so the answer is the
one the compiler will give. `emit_go_spell.go` turns each `<Op>Input` field's
declared type into source:

| field type | value | emitted |
| --- | --- | --- |
| `*string` | `"blue"` | `aws.String("blue")` |
| `int32` | `30` | `30` |
| `types.PolicyType` | `"SCP"` | `types.PolicyType("SCP")` |
| `map[string]string` | `{"a":"b"}` | `map[string]string{"a": "b"}` |
| `[]types.Tag` | `[{"Key":"k"}]` | `[]types.Tag{{Key: aws.String("k")}}` |
| `*string` | `{"$ref":"q"}` | `aws.String(scenario.Bind[string](b, "M", scenario.Ref("q")))` |

Only the last row leaves anything to run time, and only because a `$ref` cannot
be known before the run: `scenario.Bind` converts the evaluated value to the
one scalar type the field wants. Nothing reflects.

Two things keep the emitter honest:

- **One naming table.** Everything it knows about spelling Go is in
  `emit_go.go`'s `goName*` functions and `emit_go_spell.go`'s `goSpeller`, and
  `-explain -lang go` renders through the same `goInputLines`, so the
  pseudo-code a reader reproduces a failure with is the source the emitter
  wrote — pointers and all. `TestExplainGoRendersTheEmittedCall` asserts it.
- **It refuses rather than guesses.** Five things produce
  `go-emit-unsupported:<Member>` in `gaps.json`, and the group is then scoped
  away from `go-sdk` rather than emitted as a guess or silently dropped:

  | refusal | what it means |
  | --- | --- |
  | the modeled kind has no IR literal | a timestamp, blob, document or union |
  | `<Op>Input` is not declared | the vendored SDK is older than the pinned model |
  | the SDK has no field for the member | smithy-go renamed or dropped it |
  | the field's type has no Go literal | a union, or a type from a third package |
  | a value-typed member is set to its zero value | the SDK would not serialize it (`compat/model/README.md` § Values) |

  Nothing in the pilot corpus reaches any of them.

### Where the Java types come from — and why they are not read

`emit_java.go` reads no SDK. The plan's binding decision (§3.2) asks a typed
backend to resolve its SDK's field types at emit time *wherever the SDK's
nullability is not derivable from the model*, and for the AWS SDK for Java v2 it
is: every scalar is boxed, so a builder setter takes the value whatever the
member's optionality and a boxed `0` really is serialized. The suite measures
both facts on the wire — `JavaSdkWireFactsTest` sends `ReceiveMessage`'s
`VisibilityTimeout` as `0` through a real client against a loopback server — so
that the emitter's licence to skip the lookup is a measurement rather than a
claim. It is also why the Java table has no counterpart to the Go emitter's
value-typed-zero refusal: the two backends genuinely differ there.

| modeled member | value | emitted |
| --- | --- | --- |
| `string` | `"blue"` | `"blue"` |
| `integer` | `30` | `30` |
| `long` | `30` | `30L` |
| `byte` | `1` | `(byte) 1` |
| `float` | `1.5` | `1.5f` |
| `boolean` | `false` | `false` |
| enum `Color` | `"blue"` | `"blue"`, into `.color(String)` |
| `list<QueueAttributeName>` | `["All"]` | `List.of("All")`, into `.…WithStrings` |
| `map<QueueAttributeName,string>` | `{"a":"b"}` | `Map.of("a", "b")`, into `.…WithStrings` |
| `list<structure Tag>` | `[{"Key":"k"}]` | `List.of(Tag.builder().key("k").build())` |
| `string` | `{"$ref":"q"}` | `b.string("M", Values.ref("q"))` |

**An enum is spelled as its wire value, never as the enum class.** The SDK gives
every enum-typed member a String form — a scalar enum's is an overload of the
same name, and a list of enums or a map with an enum key or value gets a second
setter named `<member>WithStrings` — and both send the model's own value
unchanged. `Enum.fromValue` would not: a value the **pinned** SDK does not know
becomes `UNKNOWN_TO_SDK_VERSION`, whose `toString` is the four-character string
`"null"`, and that is what goes on the wire. `JavaSdkWireFactsTest` measures both
halves. It also puts this backend where the Go one already is —
`types.QueueAttributeName("All")` passes the model's value straight through too.
One list or map down is as far as the String form reaches; an enum nested deeper
is refused.

Two things keep it honest, and they are the same two the Go emitter has:

- **One naming table.** Everything it knows about spelling Java is in
  `emit_java.go`'s `javaName*` functions and `emit_java_spell.go`'s
  `javaSpeller`, and `-explain -lang java` renders through the same
  `javaRequestLines`. `TestExplainJavaRendersTheEmittedCall` asserts it.
- **It refuses rather than guesses.** These produce
  `java-emit-unsupported:<Member>` in `gaps.json`, and the group is then scoped
  away from `java-sdk`: a modeled kind with no IR literal; a member the model
  does not give the operation, an operation with a unit input included; a
  literal of the wrong JSON type; a number that is not whole, or is out of range
  for the member's Java type; a modeled type with no Java literal at all
  (`bigInteger`, `bigDecimal`); a map the model does not key by a string; an
  enum nested deeper than the SDK's String form reaches; a value expression
  bound to a composite member; and an explicit `null`, which the SDK spells as
  "unset" and so cannot send. Nothing in the pilot corpus reaches any of them.

What the model cannot answer is whether the **pinned** SDK has the operation at
all — the shape snapshot is generated from a newer revision of the AWS model
than any released SDK, and for the pilot corpus five Organizations operations
are newer than `2.31.7`. That axis is answered by the compiler: an operation the
pinned SDK does not declare is a `mvn package` failure naming the class, not a
wrong request, and the fix is the version pin in
`compat/suites/java-sdk/pom.xml`. It is the one axis the compiler answers and
the model cannot; spelling enums as their wire values takes the *other* pin
hazard — a value the pinned SDK does not know — out of the emitter entirely.

The emitted bytes go through `go/format` before they are written, and
generation fails if they will not parse. Every source emitter has a golden file
under `testdata/golden/` holding what it writes for the fixture service, so its
output is reviewed as a diff rather than inferred from the generator's code.
Only Go has a formatter this program can run; the others produce their file's
final layout directly.

### The .NET emitter, and why it reads the model instead

`emit_dotnet.go` writes the `dotnet-sdk` suite's
`Groups/Scenarios<Service>Gen.cs` on the same split — data plus typed calls,
with the semantics written once by hand in that suite's `Scenario/` namespace.
What differs is where the member's type comes from: the .NET emitter never
loads the SDK. Three measured facts make that safe, and the emitter's own
header records how each was measured:

| fact | consequence |
| --- | --- |
| AWSSDK v4 made every value-typed member nullable (`int?`) | a zero really is sent, so the value-typed-zero refusal go-sdk needs has nothing to refuse |
| C# target-typing spells the composites | `["All"]`, `new() { ["k"] = "v" }` and `[new() { Id = "1" }]` name no SDK type at all |
| an enum is a `ConstantClass` with an implicit conversion from `string` | a bare string literal, and a `Bind<string>` result, both assign |

So the whole spelling table is driven by the member's *modeled* kind, and the
emitted file names exactly one SDK type per call: `<Op>Request`. The cost is
stated rather than hidden — this backend cannot refuse an operation the
vendored SDK does not declare, or a member it renamed, because it never asks;
both become a compile error in the suite's own build, which is loud but
suite-wide rather than scoped to one group. `OvercastCompat.csproj` pins the
package versions that keep them from arising, and says so.

Three refusals remain, all read off the model, and all three scope the group
away from `dotnet-sdk` in the registry:

| refusal | what it means |
| --- | --- |
| the modeled kind has no C# literal | a timestamp, blob, document, union, bigInteger or bigDecimal |
| a value expression on a composite member | `$ref`/`$name` resolve into one scalar slot, never a list |
| an integer literal outside the C# type's range | C# range-checks an integral literal at compile time, and a compile error here is suite-wide |

`-explain -lang dotnet` renders through the same `dotnetInputLines`, so the
pseudo-code a reader reproduces a failure with is the source the emitter wrote;
`TestExplainDotnetRendersTheEmittedCall` asserts it.

There is no formatter for the emitted C# — the .NET SDK ships none this suite
runs — so the layout `emit_dotnet.go` writes is the layout committed, and
`testdata/golden/ScenariosWidgetsGen.cs.golden` is where it is reviewed. The
proof that it *compiles* is the dotnet-sdk suite's own Docker build.

### The Rust emitter reads the model instead

`emit_rust.go` needs no SDK lookup, and the reason is the fluent builder: a
setter takes the value itself — `.queue_name(impl Into<String>)`,
`.max_number_of_messages(i32)` — never an `Option`. A member's optionality
never reaches the call site, so the question that forces the Go emitter to load
the vendored SDK does not arise, and neither does its consequence: a value set
to its type's zero is sent exactly as any other value is, so Rust has no
zero-value refusal. Everything the spelling does need — string, enum, integer
width, list, map, structure — is the modeled kind, which the pinned snapshot
carries.

| modeled kind | value | emitted |
| --- | --- | --- |
| string | `"blue"` | `.description("blue")` |
| enum | `"SCP"` | `.r#type(aws_sdk_organizations::types::PolicyType::from("SCP"))` |
| integer | `30` | `.visibility_timeout(30)` |
| map | `{"a":"b"}` | `.attributes(K::from("a"), "b")`, one call per entry |
| list of strings | `["compat"]` | `.tag_keys("compat")`, one call per element |
| list of structures | `[{"Key":"k"}]` | `.tags(types::Tag::builder().key("k").build()…)` |
| structure | `{"Enabled":true}` | `.cross_zone_load_balancing(types::CrossZoneLoadBalancing::builder().enabled(true).build())` |
| any of them, `{"$ref":"q"}` | | `.queue_url(b.string("QueueUrl")?)` |

The last row is the only thing left to run time, and it is not the expression:
the runtime evaluates a call's whole params tree before anything is sent — that
evaluation is failure-message field 3 — and the typed call reads one leaf of it
back **by path**. So an expression is spelled once, as data, and the value the
SDK is handed is the value the failure message quotes.

A structure's own members are spelled by those same rows, at any depth: a
nested structure is another builder chain, a nested list the same repeated
setter, a nested map the same two-argument insert. Batch's
`RegisterJobDefinition` sends a list of `ResourceRequirement` inside
`containerProperties`, and comes out as one expression:

```rust
.container_properties(
    aws_sdk_batch::types::ContainerProperties::builder()
        .image("public.ecr.aws/amazonlinux/amazonlinux:2023")
        .resource_requirements(
            aws_sdk_batch::types::ResourceRequirement::builder()
                .r#type(aws_sdk_batch::types::ResourceType::from("VCPU"))
                .value("1")
                .build()
        )
        .build()
)
```

Only the indent follows the nesting; the type name, the raw identifier and
whether `build()` is fallible are each asked of the nested shape rather than
inherited from the shape around it.

Two of the model's answers are derived rather than read, and both are stated
here because getting either wrong is a compile error in the suite:

- **A builder is fallible exactly where the structure has a required member.**
  That is smithy-rs's rule, and it follows from the model, so `.build()` is
  written bare for `Account` and with a `map_err(…)?` for `Tag`.
- **A member whose `snake_case` name is a Rust keyword is a raw identifier.**
  Organizations models a member called `Type`; the setter is `r#type`.
- **A structure or enum type is smithy-rs's pascal case of the shape name, not
  the shape name.** An acronym run normalises to a single capital, so Batch's
  `CEState` and `JQState` are `CeState` and `JqState`. Every shape in the pilot
  corpus was already its own pascal case, which is why the difference did not
  surface until `batch`.

What the model cannot answer is whether the vendored crate has the operation at
all. A crate older than the pinned snapshot is a **compile failure of the
suite** rather than a generation-time refusal, because `cmd/compatgen` has no
Rust toolchain to ask; the rust-sdk Dockerfile builds before it runs, so it is
loud, and the fix is a pin in `compat/suites/rust-sdk/Cargo.toml`.

Two things produce `rust-emit-unsupported:<Member>`: a member whose modeled
kind has no IR literal (a timestamp, blob, document or union), and an
expression bound to a composite member, which has no scalar slot to land in.
Neither is reached by the current corpus. A composite **literal** is not one of
them at any depth — that was a third cause until #1885, and it cost three of
the twelve G4 wave-1 groups their `rust-sdk` row for nothing more than a
structure member the emitter declined to open.

There is no formatter to run over the emitted Rust — this is a Go program, and
CI's docs job carries no Rust toolchain — so the layout `emit_rust.go` writes
is the output's layout, and the generated files are deliberately left out of
`cargo fmt`. `testdata/golden/scenarios_widgets_gen.rs.golden` is reviewed as a
diff exactly as the Go golden is; the proof that the result *compiles* is the
rust-sdk suite's own Docker build.

## Determinism

Regeneration is byte-identical: sorted keys, struct fields in declaration
order, two-space indentation, no HTML escaping, LF line endings, one trailing
newline. `TestCommittedCorpus_isInSyncWithTheGenerator` proves the committed
corpus is what the generator produces (the offline analogue of the shape
snapshot's `shapes-sha256` gate), and `make compat-model-check` runs the same
check from the command line. CI runs both.

## Tests

`go test -tags dev ./cmd/compatgen` runs unit tests over a fixture service
under `testdata/` (shapes, recipe and values). The emitted Go is proved to
parse and to be gofmt-clean here, while the proof that it *compiles* is the
`go-sdk` suite's own build. The emitted Java, C# and Rust are proved
deterministic and golden-identical here, and the proof that each compiles is
its own suite's Docker build.

**Which tests read which SDK.** Only the Go emitter reads one — the Java, .NET
and Rust emitters resolve against the fixture model the rest of the generator's
tests already load, which is what makes their tests hermetic without a second
module. The Go emitter needs real Go types, so
`testdata/awssdk` is a checked-in stand-in for the AWS SDK for Go v2: a module
of its own, under the SDK's own module path, declaring the fixture service's
input structs and nothing else. Every test of that emitter — the golden file,
the spelling table, each refusal, the `-explain` agreement — resolves against
it, so it type-checks real Go with no module cache and no network.

The four tests that regenerate the *committed* corpus need the real vendored
SDK instead, out of `compat/suites/go-sdk/go.mod`. They skip when its
dependencies cannot be resolved, which keeps `go test` runnable offline; the
unconditional gate is `make compat-model-check`, whose second command is
`go run -tags dev ./cmd/compatgen -check` and which fails outright. That is
also the only place a real fetch can happen — the first run in a fresh
environment downloads what type-checking the two pilot services needs, and the
module cache serves every run after it.

`OVERCAST_UPDATE_GOLDEN=1 go test -tags dev -run TestEmit ./cmd/compatgen`
rewrites `testdata/golden/scenarios_widgets_gen.go.golden`,
`testdata/golden/ScenariosWidgetsGen.java.golden` and
`testdata/golden/ScenariosWidgetsGen.cs.golden`. Read the diff
before committing it — the golden file is the review artifact for what the
emitter writes, and one regenerated without being read proves nothing. Its eight resources between
them carry every recipe role — a full lifecycle, three pre-existing resources, a
setup-only resource whose create cannot be bound and one that requires it,
authored operations, an authored create assertion, an async budget, a
list-shaped identifier bound from a scalar export through a one-element-list
`binds` entry (ELB Classic's `LoadBalancerNames`), and every
tag shape `detectTagShape` accepts: a string map, a list of `{Key, Value}`
structures, a list of `{TagKey, TagValue}` structures (KMS's spelling), and an
untag member that takes a list of key-only structures instead of bare strings
(ELB Classic's `TagKeyOnly`) — so the suite exercises every binding rule,
every refusal reason, the assertion contract, determinism, scaffolding,
explain rendering and the review report, plus the sync and schema checks over
the committed corpus.
