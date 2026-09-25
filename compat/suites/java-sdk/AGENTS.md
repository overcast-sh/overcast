# AGENTS.md — java-sdk suite

> Conventions for AI agents and contributors working in
> `compat/suites/java-sdk/`.
>
> **Read [compat/AGENTS.md](../../AGENTS.md) first** — it contains the
> canonical teardown rules, separation boundary, and the `group:test`
> implementation-key rules that apply to every suite.
> This file covers only java-sdk-specific details.
>
> For quick-start, prerequisites, and env vars see [README.md](README.md).

---

## What this suite tests

Every AWS service operation reachable via the **AWS SDK for Java v2**'s sync
clients. It is the Java column of the compatibility matrix. Failures on
unimplemented services are correct and expected — they are the coverage gap
metric, not bugs to silence.

The suite mirrors the other SDK suites' service and operation coverage
(`registry.json` is the shared contract — see
[compat/AGENTS.md § Uniformity](../../AGENTS.md#2-uniformity--the-registry-is-the-contract))
while validating Java-specific SDK behaviour: builder-style client
configuration, checked `SdkServiceException`/`SdkClientException` shapes, and
the paginator API.

---

## Status

**Implemented.** `Main.serviceGroups()` wires 28 service group classes
covering S3, SQS, DynamoDB, SNS, Lambda, STS, KMS, Secrets Manager, SSM, IAM,
Kinesis, CloudWatch Logs, SES, EventBridge, CloudFormation, EC2, ECS, Cognito,
AppSync, API Gateway (v1 and v2), CloudFront, RDS, Step Functions, EventBridge
Pipes, WAFv2, Shield, ElastiCache and EFS. It runs in the compat CI matrix
(`.github/workflows/compat.yml`) alongside every other suite.

---

## Runtime

| Item       | Value                                                            |
| ---------- | ----------------------------------------------------------------- |
| Language   | Java 17+ (compiled with `--release 17`; verified against a Java 25 JDK) |
| Build tool | Maven 3.9+ (`pom.xml`), no wrapper checked in                     |
| AWS client | `software.amazon.awssdk:*` v2, BOM-managed, pinned in `pom.xml` (`2.40.0` as of this writing) |
| CI image   | `maven:3.9-eclipse-temurin-17-alpine` (build stage) → `eclipse-temurin:17-jre-alpine` (runtime) |

> SDK upgrade policy: [compat/AGENTS.md § SDK version pinning](../../AGENTS.md#sdk-version-pinning--upgrade-strategy).
> Note the pinned-versions table there names the BOM as the source of truth
> for this suite rather than a fixed version number, since every service
> artifact tracks the BOM.
>
> **The pin has a floor the generated groups set.** `cmd/compatgen` spells a
> generated call from the pinned *shape snapshot*, which is generated from a
> newer revision of the AWS model than any released SDK, so the BOM has to be at
> least as new as the operations the snapshot covers. `2.31.7` was not: five
> Organizations operations the generated probe group calls
> (`DescribeResponsibilityTransfer`, `ListInboundResponsibilityTransfers`,
> `ListOutboundResponsibilityTransfers`,
> `ListAccountsWithInvalidEffectivePolicy`,
> `ListEffectivePolicyValidationErrors`) do not exist in it, and `2.40.0` is the
> earliest release that declares all of them. That is the whole reason the pin
> moved. The arrangement is deliberate: a missing operation is a compile error
> naming the class, never a wrong request on the wire.

---

## File layout

```
compat/suites/java-sdk/
  AGENTS.md          ← you are here
  README.md          ← quick-start, prerequisites, env vars, architecture
  Dockerfile         ← Maven build stage + JRE runtime stage
  run.sh             ← builds/runs the Dockerfile; invoked by cmd/compat as `sh run.sh`
  pom.xml            ← AWS SDK v2 BOM + one dependency per service client

  src/main/java/io/overcast/compat/
    Main.java              ← entry point; wires clients, merges impls, loads registry, runs
    clients/
      AwsClients.java      ← lazily-initialised per-service client factory, plus
                             `configure` for the generated groups' own clients
    harness/
      TestContext.java     ← per-group state bag
      TestCase.java        ← record: name, fn, op, skip, depends
      TestGroup.java        ← record: suite, service, name, tests, setup, teardown
      TestFn.java           ← @FunctionalInterface for a test/setup/teardown body
      Runner.java           ← runs groups, emits NDJSON to stdout
      InteractiveRunner.java ← OVERCAST_COMPAT_INTERACTIVE command protocol
      Assertions.java       ← assertion helpers
    registry/
      Registry.java         ← loads registry.json + registry.generated.json, builds groups
      ScenarioBackend.java  ← extension point for generated groups (see below)
    scenario/               ← hand-written runtime for the generated groups (see below)
      Group.java            ← setup → tests → teardown, and every assertion kind
      Call.java             ← one call: op, raw params, typed build, client method, exports
      Clause/Check/Where/ErrorSpec.java ← the closed assertion vocabulary
      EqualsJson.java       ← equalsJSON: percent-decode, strict parse, compare as documents
      Values/Value/Binder/ContextBag.java ← $ref, $name, $concat, $index, $base64, $now, typed
      Doc/Json/Paths.java   ← SDK response → document, canonical JSON, path resolution
      Errors.java           ← error matching over this SDK's surfaces
      Failure/UnimplementedFailure.java ← the six-field message, and the 501 classification
    groups/
      ServiceGroup.java     ← interface every group class implements
      S3Group.java
      SqsGroup.java
      DynamoDbGroup.java
      …                     ← one file per AWS service, 28 in total
      ScenariosGen.java     ← GENERATED index of the generated group classes
      Scenarios<Service>Gen.java ← GENERATED, one per service in the scenario corpus

  src/test/java/io/overcast/compat/
    MainRegistrationTest.java           ← this suite's own registrations vs. real registry.json
    GeneratedGroupsRegistrationTest.java ← the generated groups resolve through the backend
    registry/RegistryTest.java          ← Registry loader unit tests
    registry/GeneratedRegistryTest.java ← registry.generated.json + ScenarioBackend resolution
    scenario/GroupExecutionTest.java    ← the runtime, against an in-memory fake service
    scenario/ValuesAndDocumentTest.java ← values, documents, paths, canonical JSON
    scenario/ErrorFixturesTest.java     ← the shared error-matching conformance fixtures
    scenario/EqualsJsonFixtureTest.java ← the shared equalsJSON fixture (compat/model/testdata/equalsjson)
    scenario/JavaSdkWireFactsTest.java  ← the SDK facts the emitter derives from the model
```

**One file per AWS service.** Never split a service across multiple group
files or merge two services into one file.

---

## Group anatomy

A group class implements `ServiceGroup`: three maps (`impls`, `setups`,
`teardowns`) merged into the suite's global registry at startup. This is a
trimmed excerpt of the real `S3Group` (`groups/S3Group.java`):

```java
package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.core.sync.RequestBody;
import software.amazon.awssdk.services.s3.S3Client;

import java.util.Map;

public final class S3Group implements ServiceGroup {

    private final AwsClients clients;

    public S3Group(AwsClients clients) {
        this.clients = clients;
    }

    private S3Client s3() { return clients.s3(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("s3-crud:CreateBucket", this::createBucket),
                Map.entry("s3-crud:PutObject",    this::putObject)
                // … one entry per test, keyed "group:test"
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.of("s3-crud", this::setupCrud);
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.of("s3-crud", ctx -> emptyAndDeleteBucket(ctx.getString("s3Bucket")));
    }

    private void setupCrud(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3crud";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3Bucket", bucket);
    }

    private void createBucket(TestContext ctx) throws Exception {
        String name = ctx.runId() + "-s3create";
        s3().createBucket(r -> r.bucket(name));
        try {
            var resp = s3().listBuckets();
            boolean found = resp.buckets().stream().anyMatch(b -> b.name().equals(name));
            Assertions.assertTrue(found, "CreateBucket: bucket " + name + " not found in listBuckets");
        } finally {
            emptyAndDeleteBucket(name);
        }
    }

    private void putObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        s3().putObject(r -> r.bucket(bucket).key("test-key"), RequestBody.fromString("hello world"));
        var head = s3().headObject(r -> r.bucket(bucket).key("test-key"));
        Assertions.assertGreaterThan(0L, head.contentLength(), "PutObject: ContentLength should be > 0");
    }

    private void emptyAndDeleteBucket(String bucket) {
        if (bucket == null) return;
        try { /* delete objects/versions/multipart uploads, then */ s3().deleteBucket(r -> r.bucket(bucket)); }
        catch (Exception ignored) {}
    }
}
```

Clients come from `clients.s3()`, `clients.sqs()`, etc. — never construct an
AWS SDK client inside a test method.

---

## Key types

```java
// harness/TestFn.java
@FunctionalInterface
public interface TestFn {
    void run(TestContext ctx) throws Exception;
}

// harness/TestCase.java
public record TestCase(String name, TestFn fn, String op, String skip, List<String> depends) {
    // op: AWS operation name for doc links (null = use name, "" = suppress)
    // skip: non-empty string emits this test as "skip" instead of running it
    // depends: names of other tests in the same group that must pass first
}

// harness/TestGroup.java
public record TestGroup(
    String suite, String service, String name,
    List<TestCase> tests,
    TestFn setup,     // nullable
    TestFn teardown   // nullable
) {}

// harness/TestContext.java
public final class TestContext {
    public String endpoint() { … }
    public String region()   { … }
    public String runId()    { … }
    public void   set(String key, Object value) { … }
    public <T> T  get(String key) { … }
    public String getString(String key) { … } // null-safe cast to String
    public void   log(String msg) { … }        // stderr only — stdout is NDJSON
}
```

`ServiceGroup` (`groups/ServiceGroup.java`) is the contract every group class
implements: `impls()`, `setups()`, `teardowns()`, plus a default
`sourceName()` (the class's simple name) used to label a duplicate-key error.

---

## Naming conventions

| Element         | Convention                                                      |
| --------------- | ---------------------------------------------------------------- |
| Impl map key    | `<group>:<test>` — **always group-qualified**, never bare (see [compat/AGENTS.md § Implementation keys](../../AGENTS.md#implementation-keys--grouptest-and-a-bad-key-aborts-the-run)) |
| Group name      | `<service>-<feature>` (kebab-case), e.g. `s3-crud`, `s3-multipart` |
| Test name       | PascalCase AWS operation name, e.g. `CreateBucket`, `PutObject`   |
| Resource prefix | `{ctx.runId()}-<short>`, e.g. `{runId}-s3crud`                    |
| Group class     | `<Service>Group`, e.g. `S3Group`, `DynamoDbGroup`                 |
| Group file      | `<Service>Group.java`, matching the class name                   |
| Context key     | camelCase string, e.g. `"s3Bucket"`, `"kmsKeyId"`                 |
| Package         | `io.overcast.compat.groups`                                      |

---

## Inter-test state

Use `ctx.set`/`ctx.get` (or `ctx.getString` for the common string case) to
pass data between sequential tests within a group:

```java
// In setup:
ctx.set("s3Bucket", bucket);

// In a later test:
String bucket = ctx.getString("s3Bucket");
```

Never rely on inter-group state — a fresh `TestContext` is created per group.
Never stash an AWS SDK client object in the context; it already lives in
`AwsClients`.

---

## Teardown rules (Java-specific)

The canonical teardown rules are in [compat/AGENTS.md](../../AGENTS.md).
Additional Java specifics:

- Suppress teardown exceptions with `catch (Exception ignored) {}` — never let
  one cleanup failure abort subsequent deletes.
- Use `ctx.getString("key")` in teardown — it returns `null` (not an
  exception) when setup failed before setting the value; guard with
  `if (bucket == null) return;`.
- For S3, abort incomplete multipart uploads and delete all object versions
  and delete markers before calling `deleteBucket` — see
  `emptyAndDeleteBucket` in `S3Group.java` for the reference implementation.
- `SdkServiceException` is the base class for AWS error responses; check
  `ex.statusCode() == 501` to detect an unimplemented operation.
- Use SDK paginators (e.g. `s3().listObjectsV2Paginator(...)`) for large
  result sets rather than a manual loop over continuation tokens.

---

## Error messages

Throw `AssertionError` (any `Exception` fails a test), including the runId
and enough context to diagnose the failure without re-running it. The
`Assertions` helper class (`harness/Assertions.java`) covers the common
shapes — `assertTrue`, `assertFalse`, `assertEquals`, `assertNotEquals`,
`assertNotNull`, `assertNull`, `assertNotBlank`, `assertContains`,
`assertGreaterThan`, `assertGreaterThanOrEqual`, `assertNotEmpty` — and each
throws `AssertionError` built from the message you pass it, appending
expected-vs-actual detail where the method has it to add:

```java
Assertions.assertTrue(found,
    "ListObjectsV2: test-key not found (runId=" + ctx.runId() + ")");
Assertions.assertEquals("hello world", body, "GetObject: body mismatch");
```

Prefer these over hand-rolled `if (!cond) throw new AssertionError(...)` —
they keep the message shape consistent across groups.

---

## Generated groups and the `ScenarioBackend` hook

`Registry.java` loads `registry.json` and its generated sibling
`registry.generated.json` (concatenated, hand-written groups first — see
[compat/AGENTS.md § registry.json](../../AGENTS.md#registryjson--canonical-test-matrix)),
validates this suite's impl keys against them, and builds the `TestGroup`
list `Runner.runSuite` executes.

A **generated** group (one with `"generated": true` in
`registry.generated.json`) is not implemented by a registered impl the way a
hand-written group is. `ScenarioBackend` (`registry/ScenarioBackend.java`) is
the extension point it resolves through: the last resolution step, after the
group-qualified and bare impl-key lookups and before the not-implemented
sentinel. **This suite implements it**, in `Main`, over the generated classes
`ScenariosGen.all(clients)` returns.

**The generated classes are emitted by `cmd/compatgen` and must never be edited
by hand.** `make generate-compat-model` rewrites them wholly from
`compat/model/scenarios/<service>.json`, and `make compat-model-check` fails if
the committed files are not byte-identical to what the generator produces. What
is emitted is the *data* plus the typed calls — one method per scenario test,
each building a real `<Op>Request` and calling a real client method. The
semantics live once, by hand, in `io.overcast.compat.scenario`, and
[compat/model/README.md](../../model/README.md) is normative for every rule in
there.

Four consequences are worth knowing before touching either half:

- **A generated group's setup and teardown are ordinary entries** in the maps
  `Main` passes to `Registry.buildGroups`; the backend hook resolves tests only.
  A generated group name always carries `-gen-`, which no hand-written group
  does, so the two halves cannot collide.
- **Adding a service to the generated corpus needs a `pom.xml` entry.** The
  emitter writes `software.amazon.awssdk.services.<service>` imports; it cannot
  add the Maven dependency that puts them on the classpath, and a missing one is
  a compile failure naming the package.
- **The BOM pin is a floor, not a preference** — see [Runtime](#runtime) above.
- **An enum reaches the request as its wire value, not as the enum class.** The
  emitter writes `.type("SERVICE_CONTROL_POLICY")` and
  `.attributeNamesWithStrings(List.of("All"))` — the SDK's String overload for a
  scalar enum, and its `<member>WithStrings` setter for a list of enums or a map
  with an enum key or value. `Enum.fromValue` is deliberately not used: a value
  the *pinned* SDK does not know becomes `UNKNOWN_TO_SDK_VERSION`, whose
  `toString` is the four-character string `"null"`, and that is what would go on
  the wire. `JavaSdkWireFactsTest` measures both halves. An enum nested deeper
  than one list or map inside the setter's own argument has no String form in the
  SDK at all and is refused (`java-emit-unsupported:<Member>`).
- **A group the registry marks `parallel` runs its tests concurrently.** Only a
  generated probe group is marked — a probe is one call that creates nothing,
  exports nothing and reads no other test's resource. `Runner` bounds the
  concurrency with the same `OVERCAST_COMPAT_PARALLEL_SLOTS` that bounds groups,
  emits the results in declaration order so the NDJSON is identical to a serial
  run's test for test, and falls back to serial if any test declares a
  `depends`, which the concurrent path cannot gate on.

A generated group scoped to `java-sdk` that the backend cannot resolve still
reports as a hard **failure** naming the group (`generated group "<group>" is
scoped to java-sdk but java-sdk has no scenario backend`), not a skip — see
`Registry#buildGroups`. `GeneratedGroupsRegistrationTest` is what catches that
before a run does.

## Registration tests

`src/test/java/io/overcast/compat/MainRegistrationTest.java` runs on `mvn
test`/`mvn package` (also inside the Docker build — see the Dockerfile) and
resolves this suite's real registrations against the real `registry.json`,
without starting a run:

- `registeredImplsResolveAgainstRegistry` — every impl key must resolve to a
  real `group:test` pair.
- `registeredImplsHaveNoDuplicateKeys` — no two `ServiceGroup` classes may
  register the same key (last-writer-wins would otherwise silently drop one
  implementation).
- `registeredImplKeysAreAllQualified` — every impl-map key must be
  group-qualified (`group:test`); a bare key is refused, because it silently
  becomes ambiguous the moment a second group declares the same test name
  (see [compat/AGENTS.md § Implementation keys](../../AGENTS.md#implementation-keys--grouptest-and-a-bad-key-aborts-the-run)).

`registry/RegistryTest.java` and `registry/GeneratedRegistryTest.java` cover
the loader itself (merge/validate/build behaviour and
`registry.generated.json` handling) independently of this suite's own
registrations. `GeneratedGroupsRegistrationTest.java` does the same for the
generated half: every generated test the registry scopes to this suite resolves
through the backend, every key is group-qualified, and nothing is registered
twice.

The `scenario/` tests cover the runtime the generated groups call into, against
an in-memory fake service rather than a live emulator, plus two things that are
not about this suite alone:

- **`ErrorFixturesTest`** runs the shared conformance corpus,
  `compat/model/testdata/errors`, through this suite's matcher, so a rule only
  one backend implements fails here rather than being discovered when a
  generated group disagrees with itself across suites. Every fixture and case it
  cannot observe is a named node that **aborts with the reason**, never a silent
  `continue`: a case missing from the tree is indistinguishable from one that
  passed.

  It is skipped whole when the corpus is not above the working directory, which
  is the case **inside the Docker build** — the image's context is
  `compat/suites/`, so the model directory is not in it. That is why CI runs this
  suite's `mvn -B test` from *this directory* in the checkout, as the `java-sdk
  suite` step of `Compat suite unit tests`
  ([.github/workflows/test.yml](../../../.github/workflows/test.yml)), rather
  than relying on the image build the way the rust-sdk suite does. The image
  build still runs everything else here.
- **`JavaSdkWireFactsTest`** measures, on the wire against a loopback server, the
  two SDK facts `cmd/compatgen` derives from the model instead of from the SDK: a
  builder setter takes the value whatever the member's optionality, and a boxed
  `0` is serialized rather than dropped. If a future SDK changed either answer
  the emitter would start writing requests that quietly omit a member, and this
  fails first.

Run all of these with `mvn -B test` from this directory — no live Overcast
instance required. That is the same command CI runs, so a green run here is the
whole of what that job checks.

---

## Adding a new group

1. Add the group and its tests to
   [compat/suites/registry.json](../registry.json) first (see
   [compat/AGENTS.md § Uniformity](../../AGENTS.md#2-uniformity--the-registry-is-the-contract)) —
   or confirm they are already there if another suite added them first.
2. Create or open `groups/<Service>Group.java`, implementing `ServiceGroup`.
3. Register every test under a **group-qualified** key (`"<group>:<test>"`)
   in `impls()`, and its setup/teardown (if any) in `setups()`/`teardowns()`.
4. Add the new group instance to `Main.serviceGroups()` if the service is new.
5. Add any new AWS SDK service dependency to `pom.xml` (it is BOM-managed —
   no version needed) and to `AwsClients.java` as a lazily-initialised field.
6. Run `mvn -B test` to confirm the registration tests pass.
7. Run `mvn -B package` and run the JAR against a live Overcast instance
   (see [README.md](README.md#running-the-suite)) to verify the NDJSON output.

---

## What agents must NOT do

- Never import from `internal/`, `router/`, or any Overcast server source
  tree — see [compat/AGENTS.md § Separation boundary](../../AGENTS.md#separation-boundary--non-negotiable).
- Never hard-code the endpoint — always go through `AwsClients`, which is
  configured from `ctx.endpoint()`/the `OVERCAST_ENDPOINT` environment
  variable at startup.
- Never use `Thread.sleep` with a fixed duration inside a test — use a poll
  loop with a maximum retry count for genuinely asynchronous behaviour.
- Never construct an AWS SDK client inside a test method — obtain it from the
  injected `AwsClients`. A generated group's own client is an exception in form
  only: it goes through `AwsClients.configure`, so there is still one description
  of how a client in this suite is configured.
- Never hand-edit `ScenariosGen.java` or `Scenarios<Service>Gen.java` — they are
  generator output, and `make compat-model-check` fails on a hand edit.
- Never register an impl key without the `group:` qualifier — the
  registration tests reject it, and CI's `mvn package` fails the image build.
- Never add a setup entry without a corresponding teardown entry.
- Never call `deleteBucket` without first emptying the bucket (objects,
  versions, delete markers, incomplete multipart uploads).
- Never schedule KMS key deletion without first deleting any aliases pointing
  to that key.
- Never write to `System.out` inside a test — the runner parses stdout as
  NDJSON; use `ctx.log()` (stderr) for diagnostics.
