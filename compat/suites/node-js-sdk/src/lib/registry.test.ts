/**
 * Unit tests for the registry loader's impl-key resolution rules.
 *
 * These are not compat tests — they need no emulator. They pin the rules that
 * stop a run from reporting a result for a test that never executed:
 *
 * - a key that resolves to nothing aborts, instead of warning;
 * - a bare key for a name several groups declare is refused, instead of
 *   binding whichever group's implementation happened to be registered last;
 * - a key two group files both register is refused, instead of discarding one
 *   of the two implementations silently.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  ambiguousTestNames,
  buildGroupsFromRegistry,
  loadGeneratedRegistry,
  mergeImpls,
  mergeRegistries,
  testNameOwners,
  validateImpls,
  type GeneratedRegistry,
  type ImplMap,
  type ImplSource,
  type Registry,
} from "./registry.ts";
import { runGroup, type TestContext, type TestGroup } from "./harness.ts";

const noop = async () => {};

/** Run one group, capturing the NDJSON events it writes to stdout. */
async function runGroupCapturingEvents(
  group: TestGroup,
): Promise<Record<string, unknown>[]> {
  const chunks: string[] = [];
  const realWrite = process.stdout.write.bind(process.stdout);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (process.stdout as any).write = (chunk: string) => {
    chunks.push(chunk);
    return true;
  };
  try {
    await runGroup(group, {
      endpoint: "",
      region: "us-east-1",
      runId: "test",
      log: () => {},
    } as TestContext);
  } finally {
    process.stdout.write = realWrite;
  }
  return chunks
    .join("")
    .split("\n")
    .filter((l) => l.trim().length > 0)
    .map((l) => JSON.parse(l));
}

function onlyTestResult(
  events: Record<string, unknown>[],
): Record<string, unknown> {
  const found = events.find((e) => e["event"] === "test_result");
  assert.ok(found, `no test_result event in ${JSON.stringify(events)}`);
  return found;
}

/**
 * Two unrelated groups declaring a test of the same name, plus a name owned by
 * exactly one group — the shape that made a mis-binding possible.
 */
const twoGroupsOneName = (): Registry => ({
  version: 1,
  groups: [
    {
      service: "iam",
      name: "iam-users",
      tests: [{ name: "ListUsers" }, { name: "CreateUser" }],
    },
    {
      service: "cognito",
      name: "cognito-userpools",
      tests: [{ name: "ListUsers" }],
    },
  ],
});

function find(groups: TestGroup[], group: string, test: string) {
  const found = groups
    .find((g) => g.name === group)
    ?.tests.find((t) => t.name === test);
  assert.ok(found, `no test ${group}/${test} in built groups`);
  return found;
}

const build = (impls: ImplMap) =>
  buildGroupsFromRegistry(twoGroupsOneName(), impls, { suite: "node-js-sdk" });

describe("unresolvable impl keys abort", () => {
  it("rejects a key written with the old slash separator", () => {
    assert.throws(
      () =>
        validateImpls(
          twoGroupsOneName(),
          { "iam-users/CreateUser": noop },
          "node-js-sdk",
        ),
      (err: Error) =>
        err.message.includes("iam-users/CreateUser") &&
        err.message.includes("matches no registry entry") &&
        // The message must point at the colon form, since that is the fix.
        err.message.includes("iam-users:CreateUser"),
    );
  });

  it("rejects a key naming an unknown group", () => {
    assert.throws(
      () =>
        validateImpls(
          twoGroupsOneName(),
          { "iam-usres:CreateUser": noop },
          "node-js-sdk",
        ),
      /iam-usres:CreateUser/,
    );
  });

  it("rejects a key naming an unknown test", () => {
    assert.throws(
      () => validateImpls(twoGroupsOneName(), { CreateUsr: noop }, "node-js-sdk"),
      /CreateUsr/,
    );
  });
});

describe("ambiguous bare keys are refused", () => {
  it("rejects a bare key for a name several groups declare", () => {
    assert.throws(
      () => validateImpls(twoGroupsOneName(), { ListUsers: noop }, "node-js-sdk"),
      (err: Error) =>
        err.message.includes("ambiguous") &&
        err.message.includes("iam-users") &&
        err.message.includes("cognito-userpools"),
    );
  });

  it("accepts resolvable keys", () => {
    validateImpls(
      twoGroupsOneName(),
      {
        CreateUser: noop, // bare, single owner
        "iam-users:ListUsers": noop,
        "cognito-userpools:ListUsers": noop,
      },
      "node-js-sdk",
    );
  });
});

describe("buildGroupsFromRegistry resolution", () => {
  it("refuses the cross-group bare fallback", () => {
    const groups = build({ ListUsers: noop });
    assert.equal(
      find(groups, "cognito-userpools", "ListUsers").skip,
      "not yet implemented in node-js-sdk test suite",
    );
    assert.ok(
      find(groups, "iam-users", "ListUsers").skip,
      "iam-users/ListUsers bound to an ambiguous bare impl",
    );
  });

  it("binds a qualified key to its own group only", () => {
    const groups = build({ "iam-users:ListUsers": noop });
    assert.equal(find(groups, "iam-users", "ListUsers").skip, undefined);
    assert.ok(
      find(groups, "cognito-userpools", "ListUsers").skip,
      "cognito-userpools/ListUsers bound to iam-users' impl",
    );
  });

  it("still allows the bare fallback for an unambiguous name", () => {
    const groups = build({ CreateUser: noop });
    assert.equal(find(groups, "iam-users", "CreateUser").skip, undefined);
  });
});

describe("duplicate impl registrations abort", () => {
  /**
   * This is the gap validateImpls cannot close. The merge that builds the
   * suite's impl map is last-writer-wins, so one of the two implementations is
   * discarded before validation ever sees the map — and the surviving key
   * resolves perfectly well, so nothing is reported. The discarded test then
   * runs the other file's implementation under its own name.
   */
  const merge = (sources: ImplSource[]) => mergeImpls(sources, "node-js-sdk");

  it("rejects a key two sources both register", () => {
    assert.throws(
      () =>
        merge([
          { name: "lambda/lambda-crud", impls: { "lambda-crud:CreateFunction": noop } },
          { name: "appsync/lambda-crud", impls: { "lambda-crud:CreateFunction": noop } },
        ]),
      (err: Error) => {
        assert.match(err.message, /duplicate impl registration/);
        assert.match(err.message, /lambda-crud:CreateFunction/);
        // Both registering files must be named: the key alone does not say
        // where to look, and one of the two files is in the wrong.
        assert.match(err.message, /lambda\/lambda-crud/);
        assert.match(err.message, /appsync\/lambda-crud/);
        return true;
      },
    );
  });

  it("reads a single source's duplicate as such", () => {
    // "both X and Y" would be nonsense when X and Y are the same file.
    assert.throws(
      () =>
        merge([
          { name: "iam/iam-users", impls: { "iam-users:CreateUser": noop } },
          { name: "iam/iam-users", impls: { "iam-users:CreateUser": noop } },
        ]),
      /registered twice by "iam\/iam-users"/,
    );
  });

  it("reports every duplicate, not just the first", () => {
    // Fixing one duplicate must not merely reveal the next.
    assert.throws(
      () =>
        merge([
          {
            name: "iam/iam-users",
            impls: { "iam-users:ListUsers": noop, "iam-users:CreateUser": noop },
          },
          {
            name: "cognito/iam-users",
            impls: { "iam-users:ListUsers": noop, "iam-users:CreateUser": noop },
          },
        ]),
      (err: Error) => {
        assert.match(err.message, /2 duplicate impl registration\(s\)/);
        // Sorted by key, so the message is stable run to run.
        assert.ok(
          err.message.indexOf("iam-users:CreateUser") <
            err.message.indexOf("iam-users:ListUsers"),
          `problems not sorted by key:\n${err.message}`,
        );
        return true;
      },
    );
  });

  it("merges disjoint sources, each key keeping its own impl", () => {
    // Negative control.
    const iamList = async () => {};
    const iamCreate = async () => {};
    const cognitoList = async () => {};
    const merged = merge([
      {
        name: "iam/iam-users",
        impls: { "iam-users:ListUsers": iamList, CreateUser: iamCreate },
      },
      {
        name: "cognito/cognito-userpools",
        impls: { "cognito-userpools:ListUsers": cognitoList },
      },
    ]);
    assert.deepEqual(merged, {
      "iam-users:ListUsers": iamList,
      CreateUser: iamCreate,
      "cognito-userpools:ListUsers": cognitoList,
    });
  });
});

describe("the suite's real registrations", () => {
  /**
   * They must resolve against the real registry.json, and no two group files
   * may claim the same key. These are the checks that catch a mis-binding
   * before a run reports one — in `npm run test:unit` rather than in results
   * that silently describe the wrong test.
   *
   * makeImplMap merges through mergeImpls, so building it at all is the
   * duplicate check.
   */
  it("merge without duplicate keys and resolve against registry.json", async () => {
    const { makeAllGroups, makeImplMap } = await import("../groups/index.ts");
    const { loadRegistry } = await import("./registry.ts");

    const impls = makeImplMap(makeAllGroups("node-js-sdk"), "node-js-sdk");
    assert.ok(Object.keys(impls).length > 0, "no impls collected");
    validateImpls(loadRegistry(), impls, "node-js-sdk");
  });
});

describe("owner tracking", () => {
  it("reports which groups claim each name", () => {
    const registry = twoGroupsOneName();
    assert.ok(ambiguousTestNames(registry).has("ListUsers"));
    assert.ok(!ambiguousTestNames(registry).has("CreateUser"));
    assert.deepEqual(testNameOwners(registry).get("ListUsers"), [
      "cognito-userpools",
      "iam-users",
    ]);
  });
});

/**
 * registry.generated.json (#1393): a missing file is a no-op, an empty file
 * is a no-op, and a non-empty one is concatenated onto the hand-written
 * groups without disturbing them.
 *
 * These assert the *invariant* ("a missing/empty generated file changes
 * nothing"), never a fact about the checked-in file's current contents — see
 * the loader contract's note on not pinning a fact another in-flight branch
 * (the compatgen generator) is about to change.
 */
describe("generated registry loading", () => {
  let tmpDir: string;

  const withTmpDir = () => {
    tmpDir = mkdtempSync(join(tmpdir(), "oc-registry-generated-"));
    return () => rmSync(tmpDir, { recursive: true, force: true });
  };

  it("a missing file is a no-op", () => {
    const cleanup = withTmpDir();
    try {
      const missing = join(tmpDir, "does-not-exist.json");
      const generated = loadGeneratedRegistry(missing);
      assert.deepEqual(generated, { version: 1, groups: [] });
    } finally {
      cleanup();
    }
  });

  it("a missing file leaves build output unchanged", () => {
    const cleanup = withTmpDir();
    try {
      const handWritten: Registry = {
        version: 1,
        groups: [
          {
            service: "s3",
            name: "s3-crud",
            tests: [{ name: "CreateBucket" }],
          },
        ],
      };
      const missing = join(tmpDir, "does-not-exist.json");
      const withGenerated = mergeRegistries(
        handWritten,
        loadGeneratedRegistry(missing),
      );
      const without = buildGroupsFromRegistry(handWritten, {}, {
        suite: "node-js-sdk",
      });
      const merged = buildGroupsFromRegistry(withGenerated, {}, {
        suite: "node-js-sdk",
      });
      assert.deepEqual(
        without.map((g) => g.name),
        merged.map((g) => g.name),
      );
    } finally {
      cleanup();
    }
  });

  it("the checked-in file loads and leaves hand-written groups alone", () => {
    // Exercises the real, checked-in registry.generated.json at its default
    // sibling-of-registry.json location, and asserts only the invariant: it
    // loads, and concatenating it disturbs nothing hand-written. Never a fact
    // about its current contents — the file is empty exactly while no suite
    // has a scenario backend (the scenarioBackends table in
    // cmd/compatgen/registry.go), and one has had one since G2 (#1768).
    const generated = loadGeneratedRegistry();
    assert.equal(generated.version, 1);

    const handWritten: Registry = {
      version: 1,
      groups: [
        {
          service: "s3",
          name: "s3-crud",
          tests: [{ name: "CreateBucket" }],
        },
      ],
    };
    const merged = mergeRegistries(handWritten, generated);
    assert.deepEqual(
      merged.groups.slice(0, handWritten.groups.length).map((g) => g.name),
      handWritten.groups.map((g) => g.name),
    );
    assert.equal(
      merged.groups.length,
      handWritten.groups.length + generated.groups.length,
    );
  });

  it("a synthetic non-empty file is concatenated after hand-written groups", () => {
    const cleanup = withTmpDir();
    try {
      const handWritten: Registry = {
        version: 1,
        groups: [
          {
            service: "s3",
            name: "s3-crud",
            tests: [{ name: "CreateBucket" }],
          },
        ],
      };
      const path = join(tmpDir, "registry.generated.json");
      const generatedJson: GeneratedRegistry = {
        version: 1,
        groups: [
          {
            service: "kinesis",
            name: "kinesis-streams",
            tests: [{ name: "CreateStream" }],
            generated: true,
            state: "candidate",
            suites: ["node-js-sdk"],
          },
        ],
      };
      writeFileSync(path, JSON.stringify(generatedJson));
      const generated = loadGeneratedRegistry(path);
      const merged = mergeRegistries(handWritten, generated);
      assert.deepEqual(
        merged.groups.map((g) => g.name),
        ["s3-crud", "kinesis-streams"],
      );
    } finally {
      cleanup();
    }
  });

  it("a present but unparsable file is a load error", () => {
    const cleanup = withTmpDir();
    try {
      const path = join(tmpDir, "registry.generated.json");
      writeFileSync(path, "{not valid json");
      assert.throws(() => loadGeneratedRegistry(path));
    } finally {
      cleanup();
    }
  });

  it("a wrong version is a load error", () => {
    const cleanup = withTmpDir();
    try {
      const path = join(tmpDir, "registry.generated.json");
      writeFileSync(path, JSON.stringify({ version: 2, groups: [] }));
      assert.throws(
        () => loadGeneratedRegistry(path),
        /unsupported registry\.generated\.json version/,
      );
    } finally {
      cleanup();
    }
  });

  it("a group missing generated/state/suites is a load error", () => {
    const cleanup = withTmpDir();
    try {
      const path = join(tmpDir, "registry.generated.json");
      writeFileSync(
        path,
        JSON.stringify({
          version: 1,
          groups: [
            {
              service: "kinesis",
              name: "kinesis-streams",
              tests: [{ name: "CreateStream" }],
              // Missing generated/state/suites.
            },
          ],
        }),
      );
      assert.throws(
        () => loadGeneratedRegistry(path),
        /kinesis-streams/,
      );
    } finally {
      cleanup();
    }
  });

  it("a name collision between the two files is a load error", () => {
    const handWritten: Registry = {
      version: 1,
      groups: [
        { service: "s3", name: "s3-crud", tests: [{ name: "CreateBucket" }] },
      ],
    };
    const generated: GeneratedRegistry = {
      version: 1,
      groups: [
        {
          service: "s3",
          name: "s3-crud",
          tests: [{ name: "PutObject" }],
          generated: true,
          state: "candidate",
          suites: ["node-js-sdk"],
        },
      ],
    };
    assert.throws(
      () => mergeRegistries(handWritten, generated),
      /s3-crud/,
    );
  });
});

describe("generated group suites scoping", () => {
  const registryWithSuites = (suites: string[]): Registry => ({
    version: 1,
    groups: [
      {
        service: "kinesis",
        name: "kinesis-streams",
        tests: [{ name: "CreateStream" }],
        generated: true,
        state: "candidate",
        suites,
      },
    ],
  });

  it("an out-of-scope group is not loaded at all", () => {
    const groups = buildGroupsFromRegistry(registryWithSuites(["go-sdk"]), {}, {
      suite: "node-js-sdk",
    });
    assert.deepEqual(groups, []);
  });

  it("an in-scope group is loaded", () => {
    const groups = buildGroupsFromRegistry(
      registryWithSuites(["node-js-sdk"]),
      {},
      { suite: "node-js-sdk" },
    );
    assert.deepEqual(
      groups.map((g) => g.name),
      ["kinesis-streams"],
    );
  });

  it("hand-written suites scoping is unaffected (cdk-lifecycle shape)", () => {
    const registry: Registry = {
      version: 1,
      groups: [
        {
          service: "cdk",
          name: "cdk-lifecycle",
          tests: [{ name: "DeployStack" }],
          suites: ["cdk"],
        },
        {
          service: "s3",
          name: "s3-crud",
          tests: [{ name: "CreateBucket" }],
        },
      ],
    };
    const groups = buildGroupsFromRegistry(registry, {}, {
      suite: "node-js-sdk",
    });
    assert.deepEqual(
      groups.map((g) => g.name),
      ["s3-crud"],
    );
  });
});

/**
 * A generated group in scope with no registered impl and no scenario backend
 * must FAIL, not skip and not na (#1393). Until the G2 interpreters land,
 * this is the only signal that a suite named in a generated group's `suites`
 * cannot actually run it.
 */
describe("generated group interim fail rule", () => {
  const registryWithScenario = (scenario?: string): Registry => ({
    version: 1,
    groups: [
      {
        service: "kinesis",
        name: "kinesis-streams",
        tests: [{ name: "CreateStream" }],
        generated: true,
        state: "candidate",
        suites: ["node-js-sdk"],
        ...(scenario !== undefined ? { scenario } : {}),
      },
    ],
  });

  it("no impl and no backend yields fail with the exact message", async () => {
    const groups = buildGroupsFromRegistry(registryWithScenario(), {}, {
      suite: "node-js-sdk",
    });
    assert.equal(groups.length, 1);
    const tc = groups[0]?.tests[0];
    assert.equal(tc?.skip, undefined);
    assert.equal(tc?.na, undefined);

    const events = await runGroupCapturingEvents(groups[0]!);
    const result = onlyTestResult(events);
    assert.equal(result["status"], "fail");
    assert.equal(
      result["error"],
      'generated group "kinesis-streams" is scoped to node-js-sdk but ' +
        "node-js-sdk has no scenario backend",
    );
  });

  it("the scenario backend is consulted first", async () => {
    const seen: Array<[string, string, string | undefined]> = [];
    const groups = buildGroupsFromRegistry(
      registryWithScenario("scenarios/kinesis.ir.json"),
      {},
      {
        suite: "node-js-sdk",
        scenarioBackend: (group, test, scenario) => {
          seen.push([group, test, scenario]);
          return async () => {};
        },
      },
    );
    assert.deepEqual(seen, [
      ["kinesis-streams", "CreateStream", "scenarios/kinesis.ir.json"],
    ]);

    const events = await runGroupCapturingEvents(groups[0]!);
    assert.equal(onlyTestResult(events)["status"], "pass");
  });

  it("a declining scenario backend falls back to fail", async () => {
    const groups = buildGroupsFromRegistry(registryWithScenario(), {}, {
      suite: "node-js-sdk",
      scenarioBackend: () => undefined,
    });
    const events = await runGroupCapturingEvents(groups[0]!);
    assert.equal(onlyTestResult(events)["status"], "fail");
  });

  it("a hand-written group keeps the skip sentinel", () => {
    // Hand-written groups keep today's sentinel behaviour, byte-for-byte —
    // only `generated` groups get the new fail rule.
    const registry: Registry = {
      version: 1,
      groups: [
        { service: "s3", name: "s3-crud", tests: [{ name: "CreateBucket" }] },
      ],
    };
    const groups = buildGroupsFromRegistry(registry, {}, {
      suite: "node-js-sdk",
    });
    assert.equal(
      groups[0]?.tests[0]?.skip,
      "not yet implemented in node-js-sdk test suite",
    );
  });
});
