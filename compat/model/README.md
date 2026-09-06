# The compat scenario model

This directory is the model-driven half of the compat suite: the inputs a
human curates, the scenario IR `cmd/compatgen` generates from them, the
scenarios a human writes directly in that same IR, and the schemas all of it
is held to. Interpreters (`python-sdk`, `node-js-sdk`, `cli`) execute the
scenario files; typed-SDK suites compile them to source. This page is the
normative description of the IR — an interpreter is written from it, and where
it and `scenario.schema.json` disagree, that is a bug in one of them.

Design: [docs/plans/compat-coverage-modelgen.md](../../docs/plans/compat-coverage-modelgen.md) §3.
Generator: [cmd/compatgen/README.md](../../cmd/compatgen/README.md).

## Layout

| Path | Who writes it | What it is |
| --- | --- | --- |
| `recipes/<service>.json` | a human | one recipe per service — the curated layer (`recipe.schema.json`) |
| `values.json` | a human | curated literals for required members no recipe binds (`values.schema.json`) |
| `promotions.json` | `cmd/compat --promote-generated` | the candidate → gated soak ledger, read by the generator to emit each group's `state` (`promotions.schema.json`) |
| `promotions.go` | a human | package `compatmodel`: the Go shape of `promotions.json`, its version and its strict reader, shared by the one command that writes the ledger and the one that reads it |
| `scenarios/<service>.json` | `cmd/compatgen` | the scenario IR, one file per service (`scenario.schema.json`) |
| `authored/<group>.json` | a human | an authored scenario: the same IR, written by hand to port one hand-written registry group — see [Authored scenarios](#authored-scenarios) |
| `gaps.json` | `cmd/compatgen` | every operation the generator refused, with a reason (`gaps.schema.json`) |
| `testdata/errors/*.json` | a human | the shared error-matching conformance fixtures every interpreter's unit tests run — see [Errors](#errors) |
| `../suites/registry.generated.json` | `cmd/compatgen` | the generated registry sibling every loader concatenates |

`<service>` is the Overcast capability key, exactly as a registry group's
`service` field spells it. Generated files are rewritten wholly on every run;
never edit them.

`promotions.json` is an **input**, not generator output — which is the point.
A generated group's `state` has to move from `candidate` to `gated` once the
nightly soak has watched it agree with itself, and letting that soak rewrite
the field in `registry.generated.json` would put two tools in charge of one
generated file. So the soak writes the ledger, the generator reads it, and
`-check` stays byte-identical across a promotion: regeneration is still a pure
function of committed inputs. Do not hand-edit it to gate a group — the entry
is the evidence of N agreeing runs, and typing one is the hand edit the soak
exists to replace. See
[docs/plans/compat-coverage-modelgen.md](../../docs/plans/compat-coverage-modelgen.md) § 3.6.

## The scenario file

```jsonc
{
  "version": 1,
  "service": "sqs",
  "client": { "sdkId": "SQS", "endpointPrefix": "sqs", "signingName": "sqs",
              "protocol": "awsJson1_0", "apiVersion": "2012-11-05", "targetPrefix": "AmazonSQS",
              "awsQueryCompatible": true },
  "groups": [
    {
      "name": "sqs-gen-queue",
      "kind": "lifecycle",
      "setup":    [ <call>, ... ],
      "tests":    [ <test>, ... ],
      "teardown": [ <call>, ... ]
    },
    {
      "name": "sqs-gen-probe",
      "kind": "probe",
      "parallel": true,
      "setup":    [],
      "tests":    [ <test>, ... ],
      "teardown": []
    }
  ]
}
```

A **group** is a registry group. Its name is `<service>-gen-<resource>` for a
generated lifecycle group and `<service>-gen-probe` for the probe group; an
[authored](#authored-scenarios) one is named for the group it ports. Every
group is independently runnable: `setup` creates what it needs, `teardown` removes it,
and nothing crosses group boundaries.

An interpreter runs one group as:

1. Create an empty **context** — a map from context path to value.
2. Run every `setup` call in order. A failure (an error, or an unresolvable
   `$ref`) reports every test in the group as `skip` with the reason
   `setup failed: <message>`, where `<message>` is the failing step's own
   [failure message](#failure-messages), all six fields of it.
3. Run every test in order (the registry's `depends` gives the loader the
   same order and the usual dependency skip).
4. Run every `teardown` call in order, each one individually wrapped: an
   error or an unresolvable `$ref` skips that call — logged, not swallowed —
   and the next one still runs. Teardown never fails a group.

Every group carries both `setup` and `teardown`, and an **empty list is a
no-op, not a missing phase**: a harness may register a hook that does nothing
or register none at all, but nothing else may differ with the list's length.
**Teardown runs after a failed setup**, in every backend. A setup that failed
on its third step has already created what its first two made, and no test
will run to remove it, so skipping teardown there is exactly when it is most
needed. A probe group's two lists are empty by construction, which makes "a
probe creates nothing" a property of the file rather than a convention each
interpreter has to remember; each interpreter has a test asserting it.

**`parallel`** says step 3 may run the group's tests concurrently rather than
one after another, bounded by whatever slot count the interpreter already uses
for concurrent groups (`OVERCAST_COMPAT_PARALLEL_SLOTS`, default 8). It is
present and `true` on every probe group and on no lifecycle group, because it
is a restatement of what a probe group already is: no setup, no teardown, no
exports, and every test one call with curated literals, so no test can create,
consume or observe anything another one touches. A lifecycle group is the
opposite — its tests hand resources to each other through the context, and the
registry's `depends` records it.

Two rules come with it. **Results are still reported in the group's own test
order**, whatever order the calls finished in: the dashboard, the baseline and
the flake detector all read that stream, and an order that depended on which
call answered first would be diff noise for nothing. And **an interpreter that
ignores the flag is still correct** — it runs the group in order, which is what
every backend did before the field existed. That is what makes the flag safe to
add to the IR ahead of a backend implementing it, and why it is the wall clock,
never the result, that changes. It earns its place because a probe group is the
biggest thing a process-spawning backend runs: `organizations-gen-probe` is 25
`aws` invocations that share nothing, and one at a time that is 21 s of the cli
suite's wall clock against 4 s eight at a time.

### `client`

What is needed to construct a client without a naming table of the
interpreter's own (§7.3 of the plan). `protocol` is the Smithy protocol trait
name (`awsJson1_0`, `awsJson1_1`, `awsQuery`, `ec2Query`, `restJson1`,
`restXml`, `rpcv2Cbor`, `rpcv2Json`); `targetPrefix` is present for the two
AWS JSON protocols, and the `X-Amz-Target` header is `<targetPrefix>.<Op>`.
Per-SDK package names are deliberately not in the file — see
[Naming](#naming).

`awsQueryCompatible` is the service's `aws.protocols#awsQueryCompatible`
trait: the service was migrated from the Query protocol, and AWS still returns
the Query error code in an `x-amzn-query-error` response header alongside the
JSON body. It is present on every scenario, `true` or `false`, so that `false`
and "this file predates the field" cannot be confused. What an interpreter
does with it is under [Errors](#errors).

### A call

```jsonc
{ "op": "CreateQueue",
  "params": { "QueueName": { "$name": "q" }, "Attributes": { "VisibilityTimeout": "30" } },
  "export": { "queue.url": "$.QueueUrl" } }
```

`op` is the AWS operation name; `params` is the input, member by member,
each a [value](#values); `export` (optional) sets context paths from the
response — see [Exports](#exports).

### A test

```jsonc
{ "name": "SetQueueAttributes",
  "op": "SetQueueAttributes",
  "call": <call>,
  "assert": [ <assertion>, ... ],
  "depends": ["CreateQueue"] }
```

`name` is the registry test name and `op` the operation it exercises (they
differ only for variants: `SendMessageBeforePurge` has `op: SendMessage`).
`assert` always has at least one clause — the schema says `minItems: 1`, and
the generator's only test constructor takes the first clause as a
non-optional argument, so a test with nothing to assert cannot be written.
`depends` names the earlier tests in the group whose exports this test
consumes; it is mirrored into the registry, where the loaders already honour
it.

A test passes when its call succeeds and every clause holds, in order. The
primary call's response is what `responseField` and call-less `listContains`
clauses look at. If a clause of kind `errorCode` is present, the primary call
is *expected* to fail: catch the error and check its code. Such a test
carries no `export` and no clause that reads the primary response (the
generator refuses one), so every other clause makes a call of its own. No
derived path emits `errorCode` yet — the negative-path variants of §3.4 are
authored, in an `operations` entry — but the kind is part of the IR and every
interpreter implements it.

### Assertions

The set is closed. `kind` selects the fields.

| Kind | Fields | Holds when |
| --- | --- | --- |
| `responseField` | `checks` | every check holds against the test's own response |
| `readback` | `call`, `checks` | `call` succeeds and every check holds against *its* response; the call's `export`s are applied |
| `listContains` | `itemsPath`, `where`, optional `call` | the list at `itemsPath` (of `call`'s response, else the test's own) is non-empty and contains an item matching every `where` entry |
| `absent` (list form) | `itemsPath`, `where`, optional `call` | no item of the list at `itemsPath` matches every `where` entry; a missing list counts as empty |
| `absent` (error form) | `call`, `error` | `call` fails with `error` |
| `errorCode` | `error` | the test's own call fails with `error` |
| `eventually` | `maxAttempts`, `delayMs`, `assert` | the inner clause (`readback`, `listContains` or `absent`) holds on some attempt: evaluate it, and on failure wait `delayMs` and try again, at most `maxAttempts` times in all. Exports from a `readback` inside are applied when the attempt passes |

**`checks`** maps a [path](#paths) to exactly one of:

| Check | Holds when |
| --- | --- |
| `{"nonEmpty": true}` | the path resolves to a value that is not `null`, `""`, `[]` or `{}`; numbers and booleans are never empty |
| `{"isList": true}` | the path resolves to a list, **empty or not** — or does not resolve at all. It exists because `nonEmpty` cannot say "this is a page of results": a single-page `List*` legally returns an empty page |
| `{"equals": <value>}` | the path resolves and the value is equal, as JSON, to the evaluated expression — by JSON type, with no coercion, so `1` never equals `"1"` and `true` never equals `1` |
| `{"matches": "<regex>"}` | the path resolves to a string matching the regular expression (RE2-compatible syntax; anchored only where the pattern anchors itself). A pattern the interpreter's own engine will not compile fails the check as an ordinary mismatch — expected `pattern <p>`, actual `unsupported pattern: <why>` — never as an exception out of the evaluator |
| `{"missing": true}` | the path does not resolve — any segment absent. A member the service sent as JSON `null` **resolves**, so `missing` fails on it, and so does `nonEmpty` |

Everything that reads a list — `listContains`, `absent` in its list form and
the `isList` check — treats **an absent list and an empty one alike, and a
present non-list as a failure**. A service that omits an empty list member
must not read differently from one that serializes `[]`, and several AWS
services (SQS's `ListQueues` among them) do omit it. So absence passes
`absent` and `isList` and fails `listContains`; a present value that is not a
list fails all three.

**`where`** maps an item-relative path to a value; `$` is the item itself
(`{"$": {"$ref": "queue.url"}}` for a list of strings). An item matches when
every entry is equal, as JSON.

**`error`** carries the clause's two accepted spellings of one error — see
[Errors](#errors).

Equality "as JSON": compare the SDK's value after mapping it to JSON the way
the SDK itself would (a boto3 `int` is a JSON number, a `bool` a boolean, a
`dict` an object), and then compare in the JSON type system directly. There is
no coercion and none may be added: the generator only ever emits an `equals`
literal of the member's modeled kind, so a cross-type comparison means the
response disagrees with the model, which is the disagreement the check exists
to catch. Timestamps and blobs are never compared. The same rule governs a
`where` entry.

### Errors

An `error` clause — `errorCode`, and `absent` in its error form — carries the
modeled `shape` name and the wire `code`: the `awsQueryError` code where the
service declares one, else the shape name again. For SQS's missing queue those
are `QueueDoesNotExist` and `AWS.SimpleQueueService.NonExistentQueue`.

SDKs disagree about which of the two they surface and about where they put it,
so an interpreter accepts an error when **any** of these surfaces equals
**either** of the clause's two values:

| Surface | Where it comes from |
| --- | --- |
| the exception's class or type name | the class an SDK minted for a modeled error, or a `name` field on the error object |
| `__type`, raw and after the last `#` | the AWS JSON protocols' error body: `com.amazonaws.sqs#QueueDoesNotExist` states the same code as `QueueDoesNotExist` |
| `Error.Code`, `Code`, `code` | the parsed error body, in whichever spelling the protocol and the SDK use |
| the `x-amzn-query-error` header, before the first `;` | the header an `awsQueryCompatible` service sends, as `<code>;<Sender\|Receiver>` |

Each backend reads the surfaces it actually has. The CLI's whole view of a
failure is a process's stderr, so it reads its own banner —
`An error occurred (<Code>) when calling the <Op> operation:` — and a JSON
error body the CLI echoed rather than modeled; no response header ever reaches
it.

The match is an **equality** against a code parsed out of one of those
surfaces, never containment over the whole message. Containment cannot tell a
code from a code that ends with it: a clause naming `NotFoundException` would
be satisfied by a `ResourceNotFoundException`, which is a different error from
a different branch of the service, and by the word appearing anywhere in the
SDK's prose. Splitting a surface at `#` and at `;` and nowhere else is what
keeps that true. An error matching neither accepted value, or a call that
succeeds, fails the clause.

**A failure that states no code on any surface matches nothing.** There is no
containment fallback for it, and the absence of one is the rule rather than an
omission: an SDK error or a CLI stderr with no parseable code is no evidence
that the service raised the named error, and matching it by containment would
reinstate the near miss above on exactly the inputs where nothing has checked
the string's shape. `Could not connect to the endpoint URL:
"…/000000000000/QueueDoesNotExist-probe"` contains a code and states none. The
clause fails, and field 5 of its message names the raw text, which is what a
reader needs to see: the call never got far enough to state a code.

`client.awsQueryCompatible` says whether the header surface can appear at all.
When it is `true`, AWS also returns the Query code in `x-amzn-query-error` — a
missing SQS queue answers `AWS.SimpleQueueService.NonExistentQueue;Sender` —
so an SDK that surfaces the header rather than the JSON `__type` reports the
code with a fault suffix on it, matching neither accepted value literally
until the interpreter splits it. When it is `false` there is no such header
and the JSON `__type`/`code`, or the CLI banner, is the only carrier. Overcast
does not send the header yet (#1810; #1816 adds it), so today the same clause
matches through the body against the emulator and through the header against
AWS — which is exactly why an interpreter may not depend on one carrier.

In an SDK that resolves one code per response the header **replaces** the
body's, rather than sitting beside it, and that is what makes one surface's two
readings worth stating rather than assuming.
`botocore.parsers.BaseJSONParser._do_query_compatible_error_parse` overwrites
the `__type`-derived `Error.Code` with the Query code whenever
`x-amzn-query-error` is present, so boto3 reports
`AWS.SimpleQueueService.NonExistentQueue` and the modeled shape is no longer
readable from the body at all — it survives as the exception class botocore
mints, and as `Error.QueryErrorCode`. The AWS CLI renders that same
`Error.Code` in its banner, so one `aws sqs delete-queue` prints two different
codes for one error:

```text
An error occurred (QueueDoesNotExist) when calling the DeleteQueue operation
An error occurred (AWS.SimpleQueueService.NonExistentQueue) when calling the DeleteQueue operation
```

The first is Overcast today, the second AWS — measured with aws-cli 2.36.18 and
botocore 1.43.67 against a stub answering the same body with and without the
header. Neither is a spelling an interpreter may assume, which is why a
generated clause always carries both.

**The conformance fixtures.** `testdata/errors/` holds one JSON document per
raw error a suite may observe, and every interpreter runs all of them in its
own unit tests. A suite that does not observe a fixture's carriers, or the
carrier a particular expectation matches through, **skips it by name and with
a reason**: a silently ignored fixture looks exactly like a passing one.

| Field | What it says |
| --- | --- |
| `carriers` | which surfaces of `wire` state the code. The vocabulary is closed — `exceptionName`, `bodyType`, `bodyCode`, `queryErrorHeader`, `cliBanner` — and each suite asserts it, so a typo cannot skip quietly in all three at once |
| `wire` | the raw observation: `status`, `headers`, `body` and the `exceptionName` an SDK would mint, or `stderr` for a CLI failure |
| `expect[]` | one clause each — a clause naming the shape, one naming the code, one naming a near miss. `error` is the clause's `{shape, code}`, `matches` the outcome, `via` the carrier a matching clause matches through |

Every reader is strict: an unknown key anywhere in a fixture is an error, not
a field the suite that added it happens to ignore. A carrier stays in the
vocabulary only while some expectation matches through it. `x-amzn-errortype`
did not: it sat on `rest-json-code-member`'s wire beside the body member that
fixture is about, where `RestJSONParser._inject_error_code` would in fact have
preferred it — so a suite would have matched through the header while the
fixture claimed the body. The header is off that wire and out of the list.

`via` names the carrier the expectation matches through, and a suite that does
not observe that carrier skips the expectation rather than asserting it. Where
one wire would let two suites match through two different carriers, the
fixture names the one without which the match is unreachable: with
`x-amzn-query-error` present, the body no longer carries the shape for
botocore, so both of `sqs-query-compatible-header`'s positive expectations are
`via` the header, and the cli suite — which cannot see a header — skips them
and reads the same error from its own banner in `cli-banner-query-compatible`.

A fixture with **no** carriers states no code anywhere, and every suite runs
it: there is nothing to observe and so nothing to skip, and its expectations
are necessarily all `matches: false`. `cli-no-parseable-code` is the one, and
the rule above is what it pins.

### Exports

A call's `export` maps a context path to a [path](#paths) in that call's own
response. An export whose path does not resolve is a failure of the step that
carries it, naming the path — not a silently unset context value for the next
`$ref` to be blamed for.

**A clause's exports are applied only once the clause holds.** That is what
lets a failing attempt inside an `eventually` leave the context bag exactly as
it found it instead of writing a stale reading for a later clause to `$ref`: a
`readback`'s exports go in after its checks pass, and a `listContains` or a
list-form `absent` carrying a call of its own follows the same rule. A primary
call's exports are applied as soon as that call succeeds, before any clause
runs.

The error form of `absent` may not carry an `export` at all — its call is
expected to fail, so there is no response to export from. The loader refuses
the file rather than letting the run discover it.

### Paths

`$` is the response; `.Name` selects a structure member or map key; `[n]`
selects a list element (zero-based). `$.Attributes.QueueArn`,
`$.Messages[0].ReceiptHandle`, `$.Tags.compat`. Nothing else — no wildcards,
filters, quoting or recursive descent. Member names are the modeled names,
which is what every SDK and the CLI's JSON output use.

A path **resolves** when every segment is present. A member the service sent
as JSON `null` is present, and resolves to `null`; a member it omitted does
not resolve, and neither does an index past the end of a list. `undefined` in
an SDK's object model is absence, not a value. Absent and `null` are different
answers from the service and the IR keeps them apart: `missing` holds only for
the first, and `nonEmpty` fails for both.

**The document is protocol-independent; where it comes from is not.** A path
means the same thing whether the service answers AWS JSON, REST JSON, AWS Query
or REST XML, and every backend must resolve it to the same value — but each
builds the document from what it has:

| Backend | The document |
| --- | --- |
| python-sdk, node-js-sdk, cli | the parsed response the SDK or `aws --output json` already holds |
| java-sdk, dotnet-sdk | the SDK's response object, walked by accessor |
| go-sdk | the SDK's output struct, reflected over — `internal/scenario/document.go` |
| rust-sdk | the **raw response body**, kept by an interceptor: `aws-sdk-*` output types carry no `serde` derive and Rust has no reflection. JSON is itself; XML goes through `src/scenario/xml.rs`, which drops the root, unwraps the `<Op>Response`/`<Op>Result` envelope, flattens `<member>` lists and folds `<entry>` maps — to the same member names, because an element is named for its member |

One difference follows from that last row and is worth stating rather than
discovering. **The XML wire carries no scalar types**: `<Interval>30</Interval>`
and `<Enabled>true</Enabled>` are text. Every model-backed backend types them
from the model, so a recipe writes the modeled type — `equals: 30`,
`equals: true` — and rust-sdk compares its text against that literal's own
spelling to reach the same answer. It stays an equality: `"30"` is 30 and
`"30x"` is nothing. What a scenario may **not** do for a Query or REST XML
service is depend on the difference between `30` and `"30"`, because rust-sdk
is the one backend that cannot see it.

### Values

A value is JSON. An object with exactly one `$`-prefixed key is an
expression; any other object is a structure or map whose values are values;
an array is a list of values; a scalar is itself.

| Expression | Evaluates to |
| --- | --- |
| `{"$lit": <json>}` | the JSON, verbatim, never interpreted (use it for an object whose keys start with `$`) |
| `{"$ref": "queue.url"}` | the context value at that path; unresolvable is an error for the step |
| `{"$name": "q"}` | the string `{runId}-{group}-q`, where `runId` is the suite's run id (`OVERCAST_COMPAT_RUN_ID`) and `group` the group name — deterministic within a run, so the same suffix names the same resource wherever it appears in the group |
| `{"$concat": [<part>, ...]}` | the parts joined; a part that is a bare string is a literal, otherwise it is an expression that must evaluate to a string |
| `{"$index": [<value>, n]}` | element `n` of a list-valued expression |

No conditionals, no arithmetic, no scripting: eight implementations have to
agree on every value.

**A scenario may not depend on sending a member's modeled default.** A typed
SDK that gives a defaulted member a value-typed field cannot tell "unset" from
"set to the default", and omits it: the AWS SDK for Go v2 serializes
`ReceiveMessage`'s `VisibilityTimeout` only `if v.VisibilityTimeout != 0`, so a
scenario asking for `0` silently asks for the queue's own timeout instead. The
value is not wrong, it simply never reaches the wire, and the backends then
disagree about what the service was told. Where a recipe needs the effect of a
default, say it with a value the SDK will send — SQS's message recipes ask for
a one-second visibility timeout rather than a zero — and give the call that
must observe the result a poll long enough to cover it.

The go-sdk emitter enforces this rather than trusting it: it knows which
members the vendored SDK made value-typed, so a zero written into one is
refused at generation time as `go-emit-unsupported:<Member>` instead of
becoming a request that quietly omits it. The rule is about a typed SDK's
inability to tell "unset" from "set to the default", so it does not bind every
typed SDK equally: rust-sdk's fluent setters take the value and record that it
was set, so a zero there is sent. A scenario still may not depend on the
default, because the backends have to agree with each other.

The rule is about a *value-typed* member, so it binds only the SDKs that have
one. The AWS SDK for .NET v4 gives every numeric member a nullable value type
and treats "set" as "not null", so a zero written into one is sent — measured,
not assumed, with a wire capture of `ReceiveMessage` against a local sink — and
the dotnet-sdk emitter needs no such refusal.

`$name` is the only way a generated test names a resource, which is what
makes the name-hygiene convention (`{runId}-<group-token>-…`) hold by
construction: the group token is the whole group name.

### Failure messages

Every interpreter failure message must carry, in this order:

1. `group/test` — `sqs-gen-queue/SetQueueAttributes`
2. the operation — of the primary call, or of the clause's own call for
   `readback`, `listContains` and `absent`
3. the **exact params JSON sent**, after evaluating every expression
4. the assertion kind and, for `checks`/`where`, the path
5. expected vs actual — the check, and the value the path resolved to (or
   `<missing>`); for an error clause, the accepted codes and the error
   actually raised (or `<no error>`)
6. the scenario file and the step index — `compat/model/scenarios/sqs.json`
   `assert[2]` (or `setup[1]`, `call`, `teardown[0]`)

A failure that is not an assertion carries the same six fields, and names one
of three kinds in field 4: **`call`** — the operation raised; **`params`** — a
value expression could not be evaluated, and field 3 then shows the params as
the scenario file writes them, because nothing was sent; **`export`** — an
export path did not resolve. Routing every failure through one message builder
is how an interpreter makes that true by construction rather than by
remembering it at each throw site.

**A 501 is not a failure.** An emulator answering "not implemented" has to
reach the harness as its own `unimplemented` classification, never as a
`fail` — it is what the probe groups exist to record. Re-raising the SDK's
error unchanged and wrapping it while copying the markers the harness sniffs
are both fine; losing the classification is not.

**`eventually` gives up with the budget in front of the last attempt's
message**, byte for byte:

```text
eventually gave up after <N> attempt(s) <M>ms apart; last failure: <the six fields>
```

No space before `ms`. It is a prefix rather than a suffix because the six-field
message ends in the scenario file and step index, which is where a reader looks
next. Bare, the last attempt's failure is indistinguishable from a clause
evaluated once, and the two want opposite fixes: a real disagreement, or a poll
budget too short for how long this service takes to settle. All three
interpreters emit the same string, so one generated group's give-up reads
identically whichever suite reports it.

`go run -tags dev ./cmd/compatgen -explain sqs-gen-queue/SetQueueAttributes -lang python`
renders the same test as pseudo-code so a failure can be reproduced by hand.

### Naming

The IR carries `sdkId`, `endpointPrefix` and `signingName` and nothing
SDK-specific. Each backend derives what it needs:

| Backend | Client | Operation |
| --- | --- | --- |
| python-sdk | `boto3.client(<endpointPrefix>)` — botocore's service name is the endpoint prefix for both pilot services | `getattr(client, xform_name(op))(**params)` |
| node-js-sdk | `@aws-sdk/client-<lower(sdkId), whitespace/underscore to "-">` (so DynamoDB → dynamodb, not dynamo-db), class `<sdkId's alphanumerics>Client` | `new <Op>Command(params)` |
| cli | `aws <endpointPrefix> <kebab(op)> --cli-input-json '<params>'` | same |
| go-sdk | `github.com/aws/aws-sdk-go-v2/service/<lower(sdkId, spaces removed)>` | `client.<Op>(ctx, &<Op>Input{…})` |
| java-sdk | `<pascalCase(sdkId)>Client` | `client.<unCapitalize(op)>(<pascalCase(op)>Request.builder()…)` |
| dotnet-sdk | `Amazon<PascalCase(sdkId)>Client` | `client.<Op>Async(new <Op>Request {…})` |
| rust-sdk | `aws_sdk_<lower(sdkId), non-alphanumerics removed>` | `client.<snake(op)>()…send()`, member setters `.<snake(member)>(…)` |

**go-sdk, java-sdk, dotnet-sdk and rust-sdk are source emitters, not
interpreters**, so their rows are the naming tables `cmd/compatgen/emit_go.go`,
`emit_java.go`, `emit_dotnet.go` and `emit_rust.go` render through, and
`-explain <group>/<test> -lang go|java|dotnet|rust` prints the statements each
emitter writes. Where the four differ is where a member's type comes from, and
between them they are the plan's typed-backend binding decision in full. For go,
**the member's type is read from the vendored SDK at generation time, never
derived from the model's nullability.**
Whether smithy-go made a member a pointer or a value does not follow from the
pinned snapshot — the snapshot and the vendored SDK are generated from
different revisions of the same AWS model, and for SQS's `ReceiveMessage` they
already disagree about three members the pilot sends. So the emitter loads
`aws-sdk-go-v2/service/<pkg>` from the suite's own module and writes
`in.QueueUrl = aws.String(…)`, `in.MaxNumberOfMessages = 10`,
`in.Type = types.PolicyType("…")` from what it finds there. A member the SDK
has no field for is refused rather than emitted.

**java-sdk resolves its spellings from the pinned model instead**, and that is
the other side of the same decision: the AWS SDK for Java v2 boxes every scalar,
so a builder setter takes the value whatever the member's optionality and a
boxed `0` really is serialized — nothing about a member's Java type follows from
the SDK rather than from the model. Two of its naming rules are worth stating
because they disagree, and an emitter that derived either from the other would
not compile. A **class** — the client, an operation's `<Op>Request`, every enum
and structure — is the name run through the SDK code generator's `pascalCase`,
which splits on word boundaries and lower-cases each part:
`ListAWSServiceAccessForOrganization` becomes
`ListAwsServiceAccessForOrganizationRequest`. A **method or setter** is the raw
name run through its `unCapitalize`, which lower-cases only the leading run of
capitals: the client method for that same operation is
`listAWSServiceAccessForOrganization`. Organizations declares both spellings on
one line.

The splitter's camel rule is a **zero-width split** —
`split("(?<=[a-z])(?=[A-Z]([a-zA-Z]|[0-9]))")` — rather than a replacement that
consumes the characters after the boundary, and the difference shows up only
where two boundaries touch. Batch's `ListJobsByConsumableResource` has one at
`sBy` and another at `yCo`; consuming makes `By` part of the word after it and
names `ListJobsByconsumableResourceRequest`, which the SDK does not declare.

An **enum** is spelled as its wire value rather than as the enum class. The SDK
gives every enum-typed member a String form — a scalar enum's is an overload of
the same name, a list of enums or a map with an enum key or value gets a second
setter named `<member>WithStrings` — and both send the model's own value
unchanged, which is what `go-sdk`'s `types.PolicyType("…")` does too. Spelling
it `Type.fromValue("…")` would not: a value the pinned SDK does not know becomes
`UNKNOWN_TO_SDK_VERSION`, whose `toString` is the four-character string `"null"`,
and that reaches the wire.

The one thing the model cannot answer is whether the *pinned* SDK is new enough
to have the operation at all — the shape snapshot is generated from a newer
revision of the AWS model than any released SDK. That axis is answered by the
suite's own `mvn package`, which fails naming the missing class, and the fix is
the version pin in `compat/suites/java-sdk/pom.xml`.

**dotnet-sdk reads the model too, for its own three reasons**, and they are
measured rather than assumed. AWSSDK for .NET v4 made every value-typed member
nullable (`int?`), so setting one to `0` really does send `0` — a wire capture
of `ReceiveMessage` shows `"VisibilityTimeout":0` present when set and the
member absent when not, which is why this backend has no counterpart to
go-sdk's value-typed-zero refusal either. C#'s target-typed `new()` and
collection expressions spell a map, a list and a nested structure without naming
their types. And an enum is a `ConstantClass` with an implicit conversion from
`string`, so it takes a bare string literal or a deferred expression alike. It
meets the same limit java-sdk does — whether the *pinned* package has the
operation at all is not a question the model answers — and gets the same answer
one step later: a compile error in the suite's own `dotnet publish`, fixed by
the version pin in `compat/suites/dotnet-sdk/OvercastCompat.csproj`.

**rust-sdk reads the model too**, and needs no lookup either; the fluent
builder is why. A setter takes the value itself (`.queue_name(impl Into<String>)`,
`.max_number_of_messages(i32)`), never an `Option`, so a member's optionality
never reaches the call site — and a value set to its type's zero is sent like
any other value, so the § Values rule below costs that emitter no refusal.
Everything else it needs is the modeled kind, at every depth: the members of a
structure literal are spelled by the same rules the operation's own builder is,
so a structure inside a structure is another builder chain and a list or map
inside one is the same repeated setter. Three facts it derives are worth
naming, because getting any of them wrong is a compile error in the suite: a
builder is fallible exactly where the structure has a required member
(`Tag::builder()…build()?` but `Account::builder()…build()`); a member whose
`snake_case` name is a Rust keyword becomes a raw identifier (Organizations'
`Type` is `r#type`); and a structure or enum **type** is the shape name run
through smithy-rs's own pascal case rather than the shape name verbatim, so an
acronym run normalises — Batch models `CEState` and `JQState`, and the crate
declares `CeState` and `JqState`. What the model cannot answer is whether the
vendored crate has the operation at all: a crate older than the pinned snapshot is a compile
failure of the suite rather than a generation-time refusal, because
`cmd/compatgen` has no Rust toolchain to ask.

One further difference is on the response side, and it is why rust-sdk's runtime
reads the **response body off the wire** rather than the SDK's output struct.
Every rule the IR states about a response is stated over JSON; the interpreters
hold the parsed response and the Go emitter reflects over the struct, but
`aws-sdk-*` output types carry no `serde` derive at the pinned versions and Rust
has no reflection, so the only alternative would be a generated converter per
output shape — from accessor signatures a Go program cannot read. The two AWS
JSON protocols in scope serialize modeled member names verbatim, so the document
a path is walked over is spelled exactly as the scenario file spells it, and the
SDK still deserializes on its own path, so a body it cannot parse still fails
the call.

Where a derivation is known to break, the interpreter needs a small override
table of its own, and the plan asks for those to be recorded as follow-ups
rather than smuggled into the IR, each landing with the first scenario that
needs it. `elastic-load-balancing` is the first, and it landed two of the six
rows below; the other four are still waiting for a scenario that names one:

| Backend | The override it will need | Status |
| --- | --- | --- |
| cli | the `aws` command name is the endpoint prefix except for four services: `elasticloadbalancing` → `elb`, `monitoring` → `cloudwatch`, `email` → `ses`, `states` → `stepfunctions` | **landed** with `elastic-load-balancing`, the first scenario to name one — `awsCommand` in `compat/suites/cli/internal/scenario/executor.go`. All four entries, because they are one documented set |
| python-sdk | botocore's service name differs from the endpoint prefix for the same four | **landed** with the same recipe — `botocore_service` in `compat/suites/python-sdk/lib/scenario/executor.py`. `boto3.client("elasticloadbalancing")` raises `UnknownServiceError`, and the suite's own corpus test resolves through the table so it stays right as the corpus grows |
| go-sdk | the package for `Cost Explorer` is `costexplorer` (derivable), but `SFN` is `sfn`, which does not follow from the SDK id. Elastic Load Balancing does not break it after all: its SDK id is `Elastic Load Balancing`, not `ELB`, so the rule already gives `elasticloadbalancing` | not yet implemented — nothing in scope needs it |
| java-sdk | the SDK's `customization.config` may rename a service or a shape outright, which no rule over the SDK id or the shape name reproduces | not yet implemented — nothing in scope is renamed, and the suite's `mvn package` is what would say so |
| dotnet-sdk | the namespace and client class are `Amazon.<sdkId with spaces removed>`/`Amazon<sdkId with spaces removed>Client`, which is right for SQS and Organizations, but the .NET SDK keeps several services' older long names: `SNS` is `SimpleNotificationService`, `STS` is `SecurityToken`, `IAM` is `IdentityManagement`, `SSM` is `SimpleSystemsManagement`, `KMS` is `KeyManagementService` and `DynamoDB` is `DynamoDBv2` | **landed** with `kms` and `sns`, the first scenarios to name one — `dotnetSDKName` in `cmd/compatgen/emit_dotnet.go`. Two rows, not six, because the namespace and the client stem **diverge** for three of them (`Amazon.SecurityToken` but `AmazonSecurityTokenServiceClient`; `Amazon.IdentityManagement` but `AmazonIdentityManagementServiceClient`; `Amazon.DynamoDBv2` but `AmazonDynamoDBClient`), so the table carries both stems and each row waits for the `dotnet publish` that proves it |
| rust-sdk | the crate is the SDK id's letters, not its word boundaries — `DynamoDB` is `aws_sdk_dynamodb` and `Cost Explorer` is `aws_sdk_costexplorer`, both derivable — but `SFN` breaks exactly as it does for go-sdk. Elastic Load Balancing does not: `aws_sdk_elasticloadbalancing` is the crate | not yet implemented — no scenario names one |

Each table is added with the first scenario that needs it, in the interpreter
that needs it, and never by adding a per-SDK name to the IR: carrying `sdkId`,
`endpointPrefix` and `signingName` is what lets eight backends derive eight
different things from three model facts.

## Recipes

A recipe says what the model cannot: which operation creates a resource,
which response field identifies it, how it is read back, what to mutate and
where the change shows up, how to delete it and what not-found looks like.
Structure is generated from that; semantics stay curated. Start from
`go run -tags dev ./cmd/compatgen -scaffold <service>` and complete by hand
against the real AWS API.

```jsonc
{
  "service": "sqs",
  "resources": [{
    "id": "queue",                      // context prefix and group name (sqs-gen-queue)
    "requires": ["dlq"],                // created in setup first, deleted in teardown last
    "create":  { "op": "CreateQueue", "params": { "QueueName": { "$name": "q" } } },
    "exports": { "url": "$.QueueUrl" }, // from the create response
    "derived": [{ "export": "arn", "op": "GetQueueAttributes",
                  "params": { "QueueUrl": { "$ref": "queue.url" }, "AttributeNames": ["QueueArn"] },
                  "path": "$.Attributes.QueueArn" }],
    "binds":   { "QueueUrl": "queue.url", "QueueArn": "queue.arn" },
    "read":    { "op": "GetQueueAttributes", "params": { "AttributeNames": ["All"] },
                 "identityPath": "$.Attributes.QueueArn", "identity": "arn" },
    "reads":   [{ "op": "GetQueueUrl", "params": { "QueueName": { "$name": "q" } },
                  "identityPath": "$.QueueUrl", "identity": "url" }],
    "list":    { "op": "ListQueues", "params": { "QueueNamePrefix": { "$name": "q" } },
                 "itemsPath": "$.QueueUrls", "identityPath": "$", "identity": "url" },
    "mutable": [{ "op": "SetQueueAttributes", "member": "Attributes.VisibilityTimeout",
                  "from": "30", "to": "60", "readPath": "$.Attributes.VisibilityTimeout" }],
    "tags":    { "tag": { "op": "TagQueue", "member": "Tags" },
                 "untag": { "op": "UntagQueue", "member": "TagKeys" },
                 "list": { "op": "ListQueueTags", "path": "$.Tags" } },
    "delete":  { "op": "DeleteQueue" },
    "notFound": { "error": "QueueDoesNotExist" },
    "async":   { "maxAttempts": 10, "delayMs": 500 },
    "operations": [ { "op": "PurgeQueue", "assert": [ <assertion>, ... ] } ]
  }]
}
```

| Field | Meaning |
| --- | --- |
| `id` | the resource's name in context paths and its group |
| `setupOnly` | exists only to be required by others; no group of its own |
| `requires` | resources created in setup before this one (topological), deleted in teardown after it |
| `create` | the create call. Omit it for a **pre-existing** resource (an organization): nothing creates or deletes it, its `exports` come from its `read`, and that read is emitted as a test rather than as setup. Everything else a recipe can declare still applies — further `reads`, `mutable`, `tags` and authored `operations` all get their tests, so such a group is not the read alone |
| `create.assert` | authored clauses that verify the create in place of the derived read-back and list-membership, for a resource whose read cannot simply be replayed. At least one of them must call the service again, or the create is refused (`no-readback-path`): restating the create's own response is not a read-back |
| `exports` | export name → path in the create response |
| `derived` | exports that need a second call (an ARN only `GetQueueAttributes` returns); each becomes a read-back in the create test |
| `binds` | input member name → context path, for every operation the resource takes part in — binding rule 1 |
| `read` | the read-back: `identityPath` must equal the export `identity` names (or, without `identity`, match the model's pattern). `consuming: true` marks a read that changes state (`ReceiveMessage`); it is emitted once, as its own test, and never used as a read-back. `exports` take values from the read response |
| `reads` | further reads, each its own test |
| `list` | the list-membership check: an item at `itemsPath` whose `identityPath` equals the `identity` export. `itemsPath` is optional — see [What the generator derives](#what-the-generator-derives). Its own test runs last before delete; `exports` taken from that response are therefore the freshest values the delete can carry (SQS wants a delete to quote the most recent receipt handle) |
| `mutable` | one entry per update: `member` (dotted input path) is set to `to`, then `read` must show `readPath == to`. `from` is merged into the create params, so the update is a real change and the create's read-back asserts the initial value |
| `tags` | tag/untag/list operations; the generator reads whether the service carries tags as a string map or a list of `{Key, Value}` from the model and emits tag → list → untag tests with the literal `compat=scenario` |
| `delete` | the delete call; absence is proven by `notFound` (the read must fail with that error) or, failing that, by `list` non-membership |
| `notFound` | the modeled error shape the read raises after delete. Optional — see [What the generator derives](#what-the-generator-derives) |
| `async` | declares the resource eventually consistent: every clause that verifies by calling the service again is wrapped in `eventually` — the derived read-back, list-membership and absence clauses, and authored clauses too. A clause that only reads the test's own response is left alone (retrying it would re-read one fixed response), and so is an authored `eventually`, whose budget its author already chose |
| `operations` | authored coverage for operations outside the lifecycle vocabulary, written in the IR's own assertion vocabulary; `name` gives a variant test name when the operation already has a test. An update-family operation (`Update*`, `Set*`, `Put*`, `Tag*`, `Untag*`) needs at least one clause that calls the service again, exactly as the derived path needs a `mutable` entry, or it is refused (`update-without-readback`) |

### What the generator derives

Two of those fields the model already settles, so a recipe that leaves them
out gets them for free. Both remain writable, and a written value is used as
written: the derivation covers the common shape, not every service.

| Field | Derived from | When it is not derived |
| --- | --- | --- |
| `notFound.error` | the `read` operation's own modeled errors, when exactly one is not-found-shaped — a shape name ending in `NotFound`, `NotFoundException`, `DoesNotExist` or `NonExistent` | the read is `consuming`, the resource has no `delete`, or the read declares no such error or more than one (SQS's `ReceiveMessage` declares both `QueueDoesNotExist` and `KmsNotFound`) |
| `list.itemsPath` | the list operation's output: the member `@paginated` names as its `items`, else the sole top-level list member | two lists and no trait to choose between them, or no list at all — the `list` is refused (`ambiguous-list-page`) rather than guessed at |

The match on the error name is on the **suffix**, not anywhere in the name.
Under-deriving costs a recipe line; over-deriving would have a delete assert
the wrong error, so a service that spells one of those words in the middle of
a longer name is left to the recipe.

`notFound` is the one field where an override may not simply disagree. If the
recipe names one shape and the read declares exactly one candidate that is a
different shape, one of the two is wrong — quietly preferring either hides it
— so generation stops with an error naming both. `list.itemsPath` has no such
rule: a page can legally sit deeper in the response than a top-level member,
and the derivation only ever looks at the top level.

Two fields sit outside the resource list, and both are exceptions to one
rule: **probes are default-deny by verb**. A probe calls an operation the
emulator does not implement, so nothing in the scenario undoes it and against
a real account nothing would. Only a `Describe*`, `List*` or `Get*` — matched
at a word boundary, so a `Listen*` operation is not a `List*` — is probed at
all. Everything else is refused `never-probe` before it is bound, with a
generated sentence saying so.

**`neverProbe`** denies an operation the verb rule would have allowed, and
**`allowProbe`** allows one it would have refused. Each entry is one sentence,
and the sentence is the whole of the exception:

```jsonc
{
  "service": "organizations",
  "neverProbe": {
    // A read verb that is not a read. Only a human knows this.
    "GetSessionToken": "Rotates the token it returns, so every holder of the old one is broken by the call.",
    // A write the verb rule already refuses. The entry is still worth having:
    // its sentence replaces the generated one in gaps.json, and "cannot be
    // reopened" tells a reader far more than "not a read operation".
    "CloseAccount": "Closes a member account. AWS suspends it immediately and permanently deletes its resources; the account cannot be reopened."
  },
  "allowProbe": {
    // A read AWS happens to spell with another verb.
    "Scan": "Reads a page of items and changes nothing; DynamoDB simply does not call it List."
  },
  "resources": [ ... ]
}
```

An `allowProbe` entry naming an operation that already starts with a read verb
is refused as saying nothing, and an operation may not appear in both maps.
`smithy.api#readonly` would settle the question outright, but `cmd/awsmodelgen`
does not keep the trait, so the committed snapshots do not carry it.

Every `params` object lists only what the binder does not supply: the
generator binds each remaining required member by rule (an explicit bind,
an export of the same name — recorded for review —, a curated value, a
constraint-derived scalar) and **refuses** the operation when none applies.
A recipe that contradicts the model — an unknown member, a literal of the
wrong kind, a path that resolves to nothing, a `$name` that cannot fit the
member's length — is an error, not a refusal.

Test order inside a lifecycle group is fixed: create, reads, mutations,
tag/list/untag, authored operations, list, delete. An operation gets at most
one derived test per group; a read or list whose operation already has one
is folded and noted in the review report.

### Scaffolding

`go run -tags dev ./cmd/compatgen -scaffold <service>` proposes a skeleton of
the above: one resource per `Create`/`Get`|`Describe`/`List`/`Update`|`Set`/
`Delete` name cluster, with a `Get*`/`Set*`/`Tag*` on a noun *under* the noun
the create names folded into that cluster — `GetQueueAttributes` and
`SetQueueAttributes` are the queue's read and its mutation, not a resource
called `queueattributes`. A Smithy `resource` shape is used where the model
has one; at the pinned revision no service in scope declares any.

The skeleton is a time-saver, never an authority, so it is written to show
which of its own values you can lean on. Three markers do that, and
`recipe.schema.json` refuses all three — a skeleton cannot become a recipe by
deleting the comment at the top:

| Marker | Means |
| --- | --- |
| `$comment` | a derived value, and the trait or rule that produced it: `itemsPath from @paginated.items`, `identity path from the identity-member rule over the CreateQueue response: QueueUrl`, `from the read's modeled errors: QueueDoesNotExist`. Check it — otherwise a wrong derivation looks exactly like a right one |
| `$todo` | a value only you can supply, with a one-line hint. Every field of the vocabulary above that the model cannot propose carries one, including the ones this service turns out not to need, so you decide rather than never seeing the question |
| `$review` | the lifecycle's create or delete is not read-only-safe by the verb rule, and the proposal needs confirming against real AWS before it is kept |

The `$todo` vocabulary is the whole curated half: `requires`, `derived`,
every `binds` target, `mutable.member`/`from`/`to`/`readPath`, `tags`, `async`
and `operations`. `tags` names the Tag/Untag/List\*Tags operations name
clustering found, and leaves the member names and the read-back path to you:
`aws.api#taggable` hangs off a resource shape, so no service in scope carries
it.

A path the identity-member rule lands on that turns out to be a structure,
list or map is *not* proposed as an identity. `$.Organization` and
`$.CreateAccountStatus` are envelopes, and which member inside identifies the
resource is a choice only you can make, so those come back as a `$todo`
listing what the envelope holds.

`$review` follows the default-deny reasoning the probe rule uses. `Create*`
and `Delete*` are never read-only-safe against a live account, so a lifecycle
earns an unmarked proposal only where the model shows the run undoing its own
create: a delete operation, **and** a modeled not-found error proving the
delete took effect. Organizations is the worked example — `CreateAccount` and
`CreateGovCloudAccount` have no delete at all, because an AWS account cannot
be deleted, and `DeleteOrganization` has no not-found error to verify it
with, so all three are marked; `organizationalunit` and `policy`, which have
both, are not.

## Authored scenarios

Everything above describes an IR file `cmd/compatgen` writes from a recipe.
The same IR is also written **by hand**, and that is the middle layer of
[docs/plans/compat-coverage-modelgen.md](../../docs/plans/compat-coverage-modelgen.md)
§3.11: behavioural intent no recipe can reach — send a message and receive it,
publish to a topic subscribed to a queue, FIFO ordering, DLQ redrive — written
once instead of eight times in eight languages.

| | Generated | Authored |
| --- | --- | --- |
| Lives in | `scenarios/<service>.json` | `authored/<group>.json` |
| Written by | `cmd/compatgen` from a recipe | a human |
| Group name | `<service>-gen-<resource>` | the registry group it ports |
| Rewritten every run | yes | never — it is an input |
| Emitted source | `scenarios_<service>_gen.*` | `scenarios_authored_<group>_gen.*` |

An authored file is an **input**, so it sits beside `scenarios/` rather than
inside it: everything in that directory is rewritten wholly on every run and
must never be edited, and an input filed among the outputs is the one mistake
this layout exists to make impossible. The file's base name is the hand-written
registry group it ports, it holds exactly one group, and prose belongs in a
`$comment` on the file, the group or a test — the one place the IR carries any,
because an authored scenario is the review artifact for something that used to
be seven per-language implementations.

Everything else is identical. The schema is the same, the structural rules are
the same, the interpreters open it through the registry group's `scenario`
field exactly as they open a generated one, and the four typed suites compile
emitted source from it through the same emitters. `-check` covers it: the file
itself is never rewritten, but the source and the registry entry produced from
it are.

Three things the generator checks that a generated file cannot need, because a
human wrote the names:

- **The names are the registry's.** The group is `<group>` or
  `<group>-shadow`; the test names, their `op` and their `depends` are the
  hand-written group's, in its order. Those are the join keys for
  `compat/baseline/`, `compat/flaky.json`, `compat/parity-debt.json` and the
  dashboard's history, so a scenario that quietly renamed a test would soak
  green and then orphan one baseline entry *per suite* on the flip.
- **The `client` block is the model's.** The interpreters build their client
  from it, so a stale copy is a scenario talking to the wrong wire format.
- **Every call is the model's.** An unknown operation, or a member the
  operation does not take, is an error rather than a refusal: a refusal is the
  generator declining to write something, and nobody wrote this but a human.

### Porting a hand-written group — two PRs and a soak between them

§3.11's migration is deliberately not one change, because the first one deletes
nothing:

1. **The port.** Author `authored/<group>.json` with the group named
   `<group>-shadow`, and regenerate. It registers as a generated group carrying
   `shadowOf: <group>`, in state `candidate`, so it runs in every suite and
   gates nothing, beside the natives it will replace.
2. **The soak.** One nightly cycle, then
   `go run ./cmd/compat --compare-shadow --results-file <run>` joins the two on
   (suite, test) and reports every pair that answered differently, exiting
   non-zero if any did. The nightly runs it beside the promotion soak and
   writes the comparison into its step summary, which is what the next PR
   cites. A pair where one half reported nothing is a divergence too: a suite
   that ran the native group and not the shadow has proved nothing about the
   port.
3. **The flip.** Rename the group in the authored file to `<group>`, move the
   registry entry into the hand-written `registry.json` with a `scenario`
   field, delete the native implementations, regenerate.

A divergence blocks step 3, never the gate, and is triaged as an IR
expressiveness gap or a latent bug in one of the eight copies — which is how
such bugs get found, and why a ported group's clauses are the **union** of what
the natives assert rather than any one native's. Where the natives disagree
about a literal, one is chosen and the choice is stated in that test's
`$comment`.

A shadow group never promotes. `--promote-generated` skips it: that soak asks
whether a group agrees with itself, which is not the question a shadow is being
asked, and a group with a scheduled deletion date has no business in the gate.
`cmd/compat`'s lint and `scripts/validate-compat-registry.py` both refuse a
shadow that names a group the hand-written registry does not declare, one whose
test names have drifted from it, and one somebody has gated.

## Refusals

`gaps.json` lists every operation the generator did not produce, keyed by
service and operation, with a stable reason:

| Reason | Meaning |
| --- | --- |
| `unbound-required-member:<Member>` | no rule supplied a legal value; add a `binds` entry or a `values.json` literal |
| `update-without-mutable` | an implemented Update/Set/Put/Tag/Untag operation with no `mutable` or `tags` entry |
| `update-without-readback` | the same operation authored under `operations`, with no clause that calls the service again — it would assert only that the service echoed the request |
| `no-readback-path` | the role exists but nothing can verify it: a create whose read, list and authored `create.assert` between them make no call of their own; a delete with neither `notFound` nor `list`, or one whose `notFound` has no non-consuming read to raise it; a mutation whose read consumes |
| `probe-of-implemented-op` | an implemented operation the recipe gives no role — it may not be probed, so it needs a role |
| `probe-binds-live-resource:<Member>` | a probe would have bound that member to a value exported from a resource the run owns. Add a curated literal to `values.json` — deliberately nonexistent, so the call misses — or leave the operation refused |
| `never-probe` | the operation is not a `Describe*`, `List*` or `Get*` and no `allowProbe` entry says it is safe, or the recipe's `neverProbe` names it. The detail is the recipe's curated sentence where it has one, and a generated one where it does not |
| `ambiguous-list-page` | a `list` with no `itemsPath`, whose operation's output holds two lists with no `@paginated` `items` trait to choose between them, or no list at all. Give the resource an explicit `list.itemsPath` |
| `no-output-to-assert` | a probe of an operation that returns nothing a probe can assert: no output at all, or no identity member and no single list to check the shape of. Reading back the resource it names would assert something that was already true before the call, so there is nothing honest to assert |
| `setup-refused:<resource>` | a required resource could not be bound |
| `unsupported-tag-shape:<Shape>` | the tag member is neither a string map nor a list of `{Key, Value}`. `<Shape>` is the bare shape name; the qualified Smithy id is in the detail |
| `dotnet-emit-unsupported:<Member>` | the dotnet-sdk emitter cannot write that member as C#: its modeled kind has no C# literal (a timestamp, blob, document, union, bigInteger or bigDecimal), a value expression is bound to a composite member, which has no scalar slot to land in, or an integer literal falls outside the C# type's range — C# range-checks an integral literal at compile time, and a compile error in this backend is suite-wide rather than scoped to one group. It scopes the group away from `dotnet-sdk` exactly as `go-emit-unsupported` does for `go-sdk`. It is shorter than that list because the two emitters read different things: the .NET emitter never asks the SDK, so it has no "the SDK renamed it" refusal — see [Naming](#naming) |
| `go-emit-unsupported:<Member>` | the go-sdk emitter cannot write that member as typed Go: its modeled kind has no IR literal (a timestamp, blob, document or union), or the vendored SDK has no `<Op>Input`, no field for the member, or a field of a type no literal builds, or the member is value-typed and the scenario sets it to its zero value (see § Values). It is the one reason here that does **not** mean "no test": the operation is generated and the interpreters run it, and the group is scoped away from `go-sdk` in the generated registry instead, because a suite listed against a group it cannot compile would report as a hard failure |
| `java-emit-unsupported:<Member>` | the java-sdk emitter cannot write that member as typed Java: its modeled kind has no IR literal (a timestamp, blob, document or union), the model gives the operation no such member, the literal is of the wrong JSON type for the member's kind, a value expression is bound to a composite member, or the value is an explicit `null` — which the AWS SDK for Java v2 spells as "unset" and so cannot send. It is scoped away from `java-sdk` on the same terms as the row above. Unlike the Go emitter it needs no SDK lookup and refuses no zero: every Java scalar is boxed, so a builder setter takes the value whatever the member's optionality and a boxed `0` is serialized (measured by the suite's own `JavaSdkWireFactsTest`). What the model cannot answer — whether the *pinned* SDK has the operation at all — is answered by the suite's `mvn package`, as a compile error rather than a wrong request |
| `rust-emit-unsupported:<Member>` | the rust-sdk emitter cannot write that member as typed Rust: its modeled kind has no IR literal (a timestamp, blob, document or union), or a value expression is bound to a composite member, which has no scalar slot to land in. A composite the scenario writes out as a literal is not a cause at any depth — a structure inside a structure is another builder chain, a list inside one is the repeated setter smithy-rs appends through, and a map inside one the two-argument insert. Its list is shorter than go-sdk's because a fluent setter takes the value rather than an `Option`, so there is no pointer-vs-value question and no zero-value case. It scopes the group away from `rust-sdk` on the same terms as the row above |

Refusals are a feature. Fixing one is a line in a recipe or in
`values.json`; guessing is never an option.

### What a probe asserts

One clause, on the probe's own response. The generator picks the output's
**identity member** — the first member, in `Arn`, `Id`, `Url`, `Name`,
`Handle`, `Status`, `State` suffix order, that is a scalar; else the first
required member; else the first member at all — and asserts it non-empty.

A **pagination token is never the identity**, and neither is a list. The
token — the member `@paginated` names as its `outputToken`, or any member (or
member target) named `NextToken`, `Marker`, `NextMarker`,
`ContinuationToken`, `NextContinuationToken`, `PaginationToken`, or ending in
`Token` or `Marker` — is exactly the field AWS omits when there is nothing
left to page, so `nonEmpty` on it asserts the opposite of a correct
single-page answer. A list is excluded because a probe populates nothing, so
its length says nothing about the service.

That leaves the `List*` operations, whose only assertable output *is* a list.
They get `{"isList": true}` on the page — the member `@paginated` names as
its `items`, else the output's sole list-typed top-level member — which is
true of a correct empty page, true of an omitted one, and false of a present
response that is not a list at all. Absence is accepted because several AWS
services omit an empty list member instead of serializing `[]` (SQS's
`ListQueues` among them), and a probe run against real AWS must pass there
too. An operation with two lists and no `items` to choose between them, or
with nothing but a token, is refused (`no-output-to-assert`).

### What a probe may bind

A probe is the one generated call no create/delete pair contains: the
emulator does not implement the operation, so nothing undoes it, and the same
scenario file is meant to be runnable against real AWS. That is why only a
read verb is probed at all (above), and why binding rules 1 and 2 are switched
off inside a probe group. A probe binds only curated
literals from `values.json` and constraint-derived ones — syntactically valid
and deliberately nonexistent — so the call misses rather than lands. The two
refusals above (`probe-binds-live-resource`, `never-probe`) are the two ways
that rule shows up in the gap report, and a probe group's `setup` and
`teardown` are empty because it has nothing to set up.

### What rule 4 will and will not derive

Rule 4 (§3.3) derives a literal only where the model's constraints enumerate
or bound the legal values: the first member of an enum; a range minimum; and
`false` for a required boolean — the shape has exactly
two legal values and `false` is the one that asks the service to do less (no
dry run, no force, no cascade), so the choice is exhaustive rather than a
guess.

"First member" means the order the shape snapshot carries, not sorted order —
but the snapshot cannot always carry one. A `type: enum` shape holds its
members in a JSON object, and `cmd/awsmodelgen` writes object keys through
`encoding/json`, which sorts them, so for those shapes the model's declaration
order is already lost by the time the generator reads it and the pick is the
alphabetically first value. Every enum in the committed snapshots is of that
form today. A `smithy.api#enum` trait is a JSON array and does keep the
model's order, which is the case `EnumValues` preserves; recovering it for the
other form means teaching `cmd/awsmodelgen` to emit an ordered member list.
Either way the pick is deterministic, which is what the byte-identical
regeneration gate needs.

§3.3's fourth candidate, "the shortest legal string for a pattern", is
deliberately not implemented: a pattern constrains a string's *syntax*, never
its *reference*, so the shortest match for `^arn:aws:.*` is a well-formed ARN
of something that does not exist and may not even name the right service. The
emulator accepts far more of those than AWS does, which is exactly the class
of value §3.10 says belongs in the gap report — so the member is refused and
a human writes the literal.
