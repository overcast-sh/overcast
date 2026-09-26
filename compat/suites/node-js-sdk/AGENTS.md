# AGENTS.md — node-js-sdk suite

> Conventions for AI agents and contributors working in
> `compat/suites/node-js-sdk/`.
>
> For compat-wide conventions (wire format, isolation rules, Docker/CI) see
> [compat/AGENTS.md](../../AGENTS.md).
> For the root project conventions see [AGENTS.md](../../../AGENTS.md).
>
> **Separation boundary:** this suite uses **AWS SDK v3 without modification**
> — the only difference from talking to real AWS is `endpoint: ctx.endpoint`
> in the client config. It must never import from `internal/`, `router/`,
> `web/`, or any other part of the Overcast server source tree.

---

## What this suite tests

Every AWS service operation reachable via **AWS SDK v3 for JavaScript** — both
implemented and not-yet-implemented. The suite is the Node.js column of the
compatibility matrix. Failures on unimplemented services are **correct and
expected**; they are the coverage gap metric, not bugs to silence.

The SDK is configured identically to how production application code would
configure it for real AWS. The **only** difference is the `endpoint` field
pointing at `http://localhost:4566` instead of the default AWS endpoints.
No Overcast-specific headers, no special client wrappers, no test modes.

---

## Status

**Implemented**, and the reference suite: `makeAllGroups()` composes 28 service
files, and this file is the canonical example of what a suite `AGENTS.md`
covers ([compat/AGENTS.md § Suite-specific conventions](../../AGENTS.md#suite-specific-conventions)).
It runs in the compat CI matrix (`.github/workflows/compat.yml`) alongside
every other suite.

---

## Runtime

| Item     | Value                                                                                                                                              |
| -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Runtime  | Node.js 22.18+/23.6+ (type stripping; no build step)                                                                                                |
| SDK      | `@aws-sdk/client-*` v3, pinned in `package.json`                                                                                                    |
| CI image | None of its own — GitHub Actions installs Node from `.node-version` and runs `npm ci` here; the compose path uses `.devcontainer/Dockerfile`, which already carries Node |

> SDK version pinning policy: see [compat/AGENTS.md § SDK version pinning](../../AGENTS.md#sdk-version-pinning--upgrade-strategy).

---

## File layout

```
compat/suites/node-js-sdk/
  AGENTS.md        ← you are here
  README.md        ← quick-start, env vars, architecture, adding a group
  Dockerfile       ← CI image; runs tsc --noEmit at build time
  package.json     ← @overcast/compat-node-js-sdk; ESM "type":"module"
  tsconfig.json    ← NodeNext, strict, ES2022
  src/
    runner.ts      ← entry point; imports all group factories; NDJSON orchestration
    lib/
      harness.ts   ← TestGroup, TestContext, runSuite(), emitEvent()
      clients.ts   ← makeClients(ctx) → typed client map; clientConfig(ctx)
      scenario/    ← the scenario interpreter for generated groups (see below)
    groups/
      s3.ts
      sqs.ts
      dynamodb.ts
      sns.ts
      lambda.ts
      ses.ts
      sts.ts
      secretsmanager.ts
      kms.ts
      ssm.ts
      eventbridge.ts
```

**One file per AWS service.** Never split a service across multiple files or
merge two services into one file.

---

## Key types

```typescript
interface TestContext {
  endpoint: string; // e.g. "http://localhost:4566"
  region: string; // e.g. "us-east-1"
  runId: string; // e.g. "oc-a1b2c3d4" — prefix all resource names
  log: (msg: string) => void; // writes to stderr only
  [key: string]: unknown; // inter-test state bag (see below)
}

interface TestCase {
  name: string;
  fn: (ctx: TestContext) => Promise<void>; // throw to fail; return to pass
}

interface TestGroup {
  suite: string;
  service: string;
  name: string;
  tests: TestCase[];
  teardown?: (ctx: TestContext) => Promise<void>;
}
```

---

## Group anatomy

```typescript
import type { TestGroup } from "../lib/harness.js";
import { makeClients } from "../lib/clients.js";
import {
  CreateBucketCommand,
  DeleteBucketCommand,
  ListBucketsCommand,
} from "@aws-sdk/client-s3";

export function makeS3Groups(suite: string): TestGroup[] {
  return [
    {
      suite,
      service: "s3",
      name: "s3-crud",
      tests: [
        {
          name: "CreateBucket",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            await s3.send(new CreateBucketCommand({ Bucket: bucket }));
            const { Buckets = [] } = await s3.send(new ListBucketsCommand({}));
            if (!Buckets.some((b) => b.Name === bucket)) {
              throw new Error(`bucket ${bucket} not found after CreateBucket`);
            }
          },
        },
      ],
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        try {
          await s3.send(
            new DeleteBucketCommand({ Bucket: `${ctx.runId}-s3-crud` }),
          );
        } catch {}
      },
    },
  ];
}
```

---

## Naming conventions

| Element         | Convention                                                        |
| --------------- | ----------------------------------------------------------------- |
| Group name      | `<service>-<feature>` (kebab-case), e.g. `s3-crud`, `iam-roles`   |
| Test name       | PascalCase AWS operation name where possible, e.g. `CreateBucket` |
| Resource prefix | `` `${ctx.runId}-<group-short-name>` `` (no trailing slash/dash)  |
| Export function | `make<Service>Groups(suite: string): TestGroup[]`                 |
| File name       | Lowercase service name: `s3.ts`, `cloudwatch-logs.ts`             |

---

## Generated groups — `lib/scenario/`

A registry group carrying `generated: true` has no implementation under
`src/groups/` and must not acquire one. It names a scenario file under
`compat/model/scenarios/`, and `runner.ts` hands
`buildGroupsFromRegistry` a `scenarioBackend` (plus the group's
`setup`/`teardown`) built by `lib/scenario/backend.ts`, which interprets that
file. The normative description of the IR is
[compat/model/README.md](../../model/README.md); the modules split by
responsibility (`ir`, `loader`, `expressions`, `assertions`, `executor`,
`client`, `failure`, `backend`) and every one of them is unit-tested without an
emulator.

Rules that apply to this directory in particular:

- **The separation boundary holds here too.** The interpreter builds its
  clients by dynamic `import("@aws-sdk/client-<kebab(sdkId)>")` and configures
  them with `clientConfig()` from `lib/clients.ts` — never a second copy of the
  configuration rules, never an Overcast-specific header or code path.
- **A service the scenarios cover needs its `@aws-sdk/client-*` package in
  `package.json`**, at the suite's pinned version line. A missing one is
  reported as "add it to package.json", not as a failing test.
- **Never edit a scenario file, and never edit `registry.generated.json`.**
  Both are rewritten wholly by `cmd/compatgen`; change a recipe and regenerate.
- **Never special-case a group, a service or an error inside the interpreter.**
  If a scenario cannot be executed faithfully, that is a recipe or an IR
  question, not a branch in `executor.ts`.
- **Failure messages carry the six fields** `compat/model/README.md`
  § Failure messages lists, assembled in exactly one place (`failure.ts`), and
  an SDK error's unimplemented markers are copied onto the thrown error so a
  probe of an unimplemented operation still records `unimplemented`.

---

## Inter-test state

Sequential tests within a group can share state via the context index bag:

```typescript
// first test: stash the created resource ARN
ctx["_topicArn"] = resp.TopicArn;

// later test: read it back (cast because the bag is unknown)
const arn = ctx["_topicArn"] as string;
```

**Rules:**

- Keys must start with `_` to distinguish from built-in context fields.
- Never rely on inter-group state — `ctx` is fresh for every group.
- Never stash mutable SDK objects; stash only plain values (strings, numbers).

---

## Error messages

Errors should identify what failed and what was expected:

```typescript
throw new Error(
  `expected Bucket in ListBuckets but got none (runId=${ctx.runId})`,
);
throw new Error(`item not found after PutItem: pk=${pk}`);
throw new Error(
  `expected StatusCode 200, got ${resp.$metadata.httpStatusCode}`,
);
```

---

## Teardown rules

1. Always wrap individual delete calls in `try/catch` — partial failures must
   not abort teardown of other resources.
2. Tear down in **reverse creation order** (e.g. remove role from instance
   profile before deleting the instance profile; delete objects before the
   bucket; delete subnet before VPC).
3. Never skip teardown because a test failed — put all cleanup in `teardown`,
   not inside the test `fn`.
4. Teardown is best-effort; a stale test resource is far less harmful than a
   failing compat run caused by teardown throwing.
5. **Clean up ALL resources, including incidental ones.** If a test creates a
   resource as a side effect of testing something else (e.g. an access key
   created to test `CreateAccessKey`, an inline policy attached to a user, a
   role attached to an instance profile, a subscription created when
   subscribing a queue to a topic), that incidental resource **must** also be
   cleaned up in `teardown`. Relying on the parent resource's delete to
   cascade is acceptable only when AWS guarantees the cascade (e.g. deleting
   a DynamoDB table removes all items; deleting a log group removes all
   streams). When the cascade is not guaranteed, add explicit delete calls.
6. **Resources that require pre-conditions before deletion must handle them in
   teardown.** For example: detach a managed policy before deleting the role;
   remove a role from an instance profile before deleting the profile; disable
   a CloudFront distribution before deleting it; fetch a fresh `LockToken`
   before deleting a WAF Web ACL (the token changes after each mutating call).
7. **Incomplete multipart uploads are invisible to `ListObjectsV2`** — if a
   group creates or might leave an in-progress multipart upload, teardown must
   call `ListMultipartUploads` and abort each one before deleting the bucket.
8. **Every group that creates at least one durable resource must have a
   `teardown` function.** The only acceptable exception is a group that is
   entirely read-only (e.g. `DescribeInstances`, `GetCallerIdentity`). Tests
   that delete a resource inline as the last step in a happy-path sequence are
   **not** a substitute for `teardown` — the `teardown` function exists
   precisely to handle failures that skip that last step.

After every test run — whether the tests pass or fail — the emulator must be
left in the same state it was in before the run. **Zero trace.**

---

## TypeScript rules

- `"type": "module"` in package.json — all imports must use `.js` extensions
  (TypeScript resolves these to the corresponding `.ts` source at dev time).
- No `require()`. No CommonJS.
- Strict mode is enforced via `tsconfig.json` — no `// @ts-ignore` without a
  comment explaining why.
- Do not use barrel `index.ts` files; import directly from the target module.
- `tsc --noEmit` runs at Docker image build time — the image will fail to build
  if there are type errors.

---

## Adding a new test group

1. Create or edit `src/groups/<service>.ts`.
2. Export `make<Service>Groups(suite: string): TestGroup[]`.
3. Import and register in `src/runner.ts`:
   ```typescript
   import { makeSecretsManagerGroups } from "./groups/secretsmanager.js"
   // ...add to the groups array
   ...makeSecretsManagerGroups(SUITE),
   ```
4. Run `npx tsc --noEmit` to verify — no type errors allowed.
5. Update the groups table in `README.md`.

---

## What agents must NOT do in this suite

- Never configure SDK clients in any way that differs from real AWS usage
  other than setting `endpoint: ctx.endpoint`. No special Overcast headers,
  no custom request handlers, no HTTP interceptors.
- Never mock the AWS SDK (`jest.mock`, `vi.mock`, manual `__mocks__`).
- Never use `process.exit()` inside a test `fn` — throw instead.
- Never write to **stdout** inside a test — use `ctx.log()` which writes to stderr.
- Never share state **across groups** via module-level variables.
- Never omit `.js` on local imports (TypeScript + NodeNext ESM requires this).
- Never add a `devDependency` that requires a native build (`node-gyp`).
- Never skip a test to hide a gap — let it run, let it fail, record the result.
