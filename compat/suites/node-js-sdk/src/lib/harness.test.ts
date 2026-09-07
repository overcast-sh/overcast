/**
 * Unit tests for runGroup()'s setup/test/teardown phasing.
 *
 * These are not compat tests — they need no emulator. They pin the rule the
 * IR states in compat/model/README.md § The scenario file, step 2: a failed
 * setup reports every test as `skip` and then still runs teardown. A group
 * whose setup created resources in an earlier step and then failed in a
 * later one has already left something behind, and teardown is the only
 * thing that will ever clean it up — skipping it leaks the resource.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { isUnimplemented, runGroup, type TestContext, type TestGroup } from "./harness.ts";

function makeCtx(): TestContext {
  return {
    endpoint: "",
    region: "us-east-1",
    runId: "test",
    log: () => {},
  } as TestContext;
}

/**
 * Run one group, capturing the NDJSON events it writes to stdout.
 *
 * process.stdout is shared with node:test's own reporter, which flushes on its
 * own schedule and lands inside the patched window whenever the group under
 * test awaits anything real. So the patch claims only what the harness emits —
 * one JSON object per line, always beginning `{"event":` — and passes
 * everything else through untouched. Swallowing it instead would eat the
 * reporter's output and make the run's own summary wrong.
 */
async function runGroupCapturingEvents(
  group: TestGroup,
): Promise<Record<string, unknown>[]> {
  const chunks: string[] = [];
  const realWrite = process.stdout.write.bind(process.stdout);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (process.stdout as any).write = (chunk: unknown, ...rest: unknown[]) => {
    const text =
      typeof chunk === "string" ? chunk : Buffer.from(chunk as Uint8Array).toString();
    if (text.startsWith('{"event":')) {
      chunks.push(text);
      return true;
    }
    return (realWrite as (c: unknown, ...r: unknown[]) => boolean)(chunk, ...rest);
  };
  try {
    await runGroup(group, makeCtx());
  } finally {
    process.stdout.write = realWrite;
  }
  return chunks
    .join("")
    .split("\n")
    .filter((l) => l.trim().length > 0)
    .map((l) => JSON.parse(l) as Record<string, unknown>);
}

describe("runGroup teardown after setup failure", () => {
  it("runs teardown even when setup throws", async () => {
    let teardownCalled = false;
    let testRan = false;

    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "sqs",
      name: "sqs-gen-queue",
      setup: async () => {
        throw new Error("boom");
      },
      tests: [
        { name: "GetQueueAttributes", fn: async () => { testRan = true; } },
        { name: "SetQueueAttributes", fn: async () => { testRan = true; } },
      ],
      teardown: async () => {
        teardownCalled = true;
      },
    };

    const events = await runGroupCapturingEvents(group);

    assert.equal(teardownCalled, true, "teardown must run after a failed setup");
    assert.equal(testRan, false, "no test function should run after a failed setup");

    const results = events.filter((e) => e["event"] === "test_result");
    assert.equal(results.length, 2);
    for (const r of results) {
      assert.equal(r["status"], "skip");
      assert.match(String(r["error"]), /^setup failed: .*boom/);
    }
  });

  it("reports the counts skip totals from a failed setup", async () => {
    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "sqs",
      name: "sqs-gen-queue",
      setup: async () => {
        throw new Error("boom");
      },
      tests: [
        { name: "GetQueueAttributes", fn: async () => {} },
        { name: "SetQueueAttributes", fn: async () => {} },
      ],
      teardown: async () => {},
    };

    const counts = await runGroup(group, makeCtx());
    assert.deepEqual(counts, {
      passed: 0,
      failed: 0,
      skipped: 2,
      unimplemented: 0,
      cancelled: 0,
    });
  });

  it("still propagates a teardown error to ctx.log rather than throwing", async () => {
    const logs: string[] = [];
    const ctx: TestContext = {
      endpoint: "",
      region: "us-east-1",
      runId: "test",
      log: (msg: string) => logs.push(msg),
    } as TestContext;

    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "sqs",
      name: "sqs-gen-queue",
      setup: async () => {
        throw new Error("setup boom");
      },
      tests: [{ name: "GetQueueAttributes", fn: async () => {} }],
      teardown: async () => {
        throw new Error("teardown boom");
      },
    };

    // Must not throw — teardown errors are logged, not propagated.
    await runGroup(group, ctx);

    assert.ok(
      logs.some((l) => l.includes("teardown error") && l.includes("teardown boom")),
      `expected a teardown error log, got: ${JSON.stringify(logs)}`,
    );
  });
});

describe("runGroup teardown after a normal group", () => {
  it("runs teardown after tests pass", async () => {
    let teardownCalled = false;
    let setupCalled = false;

    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "sqs",
      name: "sqs-gen-queue",
      setup: async () => {
        setupCalled = true;
      },
      tests: [{ name: "GetQueueAttributes", fn: async () => {} }],
      teardown: async () => {
        teardownCalled = true;
      },
    };

    const counts = await runGroup(group, makeCtx());

    assert.equal(setupCalled, true);
    assert.equal(teardownCalled, true, "teardown must run after a normal group");
    assert.deepEqual(counts, {
      passed: 1,
      failed: 0,
      skipped: 0,
      unimplemented: 0,
      cancelled: 0,
    });
  });

  it("runs teardown even when a test fails", async () => {
    let teardownCalled = false;

    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "sqs",
      name: "sqs-gen-queue",
      tests: [
        {
          name: "GetQueueAttributes",
          fn: async () => {
            throw new Error("test boom");
          },
        },
      ],
      teardown: async () => {
        teardownCalled = true;
      },
    };

    const counts = await runGroup(group, makeCtx());

    assert.equal(teardownCalled, true, "teardown must run after a failed test");
    assert.equal(counts.failed, 1);
  });
});

// ─── Parallel probe groups (#1801) ───────────────────────────────────────────

/** A group of `n` tests, each of which resolves only once all `n` have
 *  started. Serially it never completes and the test times out; run
 *  concurrently it clears at once — so "did these overlap" is answered by the
 *  tests themselves, not by a wall-clock threshold a loaded CI machine can
 *  make lie. */
function barrierGroup(n: number): TestGroup {
  let arrived = 0;
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    suite: "node-js-sdk",
    service: "widgets",
    name: "widgets-gen-probe",
    parallel: true,
    tests: Array.from({ length: n }, (_, i) => ({
      name: `Probe${String(i).padStart(2, "0")}`,
      fn: async () => {
        arrived++;
        if (arrived === n) release();
        await released;
      },
    })),
  };
}

describe("parallel groups", () => {
  it("runs a parallel group's tests concurrently", async () => {
    const events = await runGroupCapturingEvents(barrierGroup(8));
    const results = events.filter((e) => e["event"] === "test_result");
    assert.equal(results.length, 8);
    assert.ok(
      results.every((r) => r["status"] === "pass"),
      "the barrier never cleared: the tests did not overlap",
    );
  });

  it("emits results in declaration order whatever order they finished in", async () => {
    // Test i settles after (8 - i) ms, so completion order reverses
    // declaration order; every third one fails, so statuses differ too.
    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "widgets",
      name: "widgets-gen-probe",
      parallel: true,
      tests: Array.from({ length: 8 }, (_, i) => ({
        name: `Probe${String(i).padStart(2, "0")}`,
        fn: async () => {
          await new Promise((resolve) => setTimeout(resolve, 8 - i));
          if (i % 3 === 0) throw new Error("boom");
        },
      })),
    };

    const events = await runGroupCapturingEvents(group);
    const results = events.filter((e) => e["event"] === "test_result");
    assert.deepEqual(
      results.map((r) => r["test"]),
      Array.from({ length: 8 }, (_, i) => `Probe${String(i).padStart(2, "0")}`),
    );
    assert.equal(results[0]!["status"], "fail");
    assert.equal(results[1]!["status"], "pass");
  });

  it("runs a group without the flag one test at a time, in order", async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const order: string[] = [];
    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "widgets",
      name: "widgets-gen-widget",
      tests: Array.from({ length: 5 }, (_, i) => {
        const name = `Probe${String(i).padStart(2, "0")}`;
        return {
          name,
          fn: async () => {
            inFlight++;
            maxInFlight = Math.max(maxInFlight, inFlight);
            order.push(name);
            await new Promise((resolve) => setTimeout(resolve, 1));
            inFlight--;
          },
        };
      }),
    };

    await runGroupCapturingEvents(group);
    assert.equal(
      maxInFlight,
      1,
      "tests overlapped in a group that did not ask for it",
    );
    assert.deepEqual(
      order,
      Array.from({ length: 5 }, (_, i) => `Probe${String(i).padStart(2, "0")}`),
    );
  });


  // A test the suite marked na or skip never ran and never will, so it reports
  // why it was marked — not "dependency failed", which would move an na into
  // the skip counter and replace a skip's own reason with a cascade message.
  it("lets an na/skip marker outrank the dependency gate", async () => {
    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "widgets",
      name: "widgets-gen-widget",
      tests: [
        {
          name: "First",
          fn: async () => {
            throw new Error("boom");
          },
        },
        {
          name: "Marked",
          depends: ["First"],
          skip: "requires docker",
          fn: async () => {},
        },
        {
          name: "Unavailable",
          depends: ["First"],
          na: "the SDK has no such command",
          fn: async () => {},
        },
        { name: "Ordinary", depends: ["First"], fn: async () => {} },
      ],
    };

    const events = await runGroupCapturingEvents(group);
    const byTest = new Map(
      events
        .filter((e) => e["event"] === "test_result")
        .map((e) => [e["test"] as string, e]),
    );
    assert.equal(byTest.get("Marked")?.["status"], "skip");
    assert.equal(byTest.get("Marked")?.["error"], "requires docker");
    assert.equal(byTest.get("Unavailable")?.["status"], "na");
    assert.equal(
      byTest.get("Ordinary")?.["error"],
      "dependency failed: First",
    );
  });

  // The concurrent path cannot express the dependency gate, so a group that
  // declares one runs in order even where the registry says parallel. The IR
  // never produces this combination — only a probe group is parallel, and a
  // probe has no exports — but a corpus that did must not silently lose the
  // cascade skip.
  it("falls back to serial when a test declares a dependency", async () => {
    const group: TestGroup = {
      suite: "node-js-sdk",
      service: "widgets",
      name: "widgets-gen-probe",
      parallel: true,
      tests: [
        {
          name: "First",
          fn: async () => {
            throw new Error("boom");
          },
        },
        { name: "Second", depends: ["First"], fn: async () => {} },
      ],
    };

    const events = await runGroupCapturingEvents(group);
    const results = events.filter((e) => e["event"] === "test_result");
    assert.equal(results[0]!["status"], "fail");
    assert.equal(
      results[1]!["status"],
      "skip",
      "the dependency gate was lost on the parallel path",
    );
  });
});

// ── unimplemented classification (#1924) ─────────────────────────────────────

/**
 * The error shape the AWS SDK v3 hands a caller: the parsed error code as the
 * `name`, and `$metadata` carrying the status and request id of the exchange.
 */
function sdkError(
  name: string,
  message: string,
  httpStatusCode: number,
  headers?: Record<string, string>,
): Error {
  const err = new Error(message);
  err.name = name;
  Object.assign(err, {
    $metadata: {
      httpStatusCode,
      requestId: "5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77",
    },
    ...(headers === undefined
      ? {}
      : { $response: { statusCode: httpStatusCode, headers } }),
  });
  return err;
}

describe("isUnimplemented", () => {
  it("reports a 400 as a failure however its prose reads", () => {
    // The bug #1924 fixed elsewhere: a sibling suite matched a bare "501"
    // anywhere in the error text, and a request id was enough to put one
    // there — reporting a test that asserts an InvalidRequestException as
    // `unimplemented`, which flipped a gated baseline row on CI run
    // 34064243252 and failed an unrelated pull request.
    const err = sdkError(
      "InvalidRequestException",
      "No Lambda rotation function ARN is associated with this secret.",
      400,
    );
    assert.equal(isUnimplemented(err), false);
  });

  it("reports a 400 whose resource name contains 501 as a failure", () => {
    const err = sdkError(
      "ResourceNotFoundException",
      "Secrets Manager can't find the specified secret: oc-501abcde-rotate",
      400,
    );
    assert.equal(isUnimplemented(err), false);
  });

  it("reports a real 501 as unimplemented", () => {
    const err = sdkError(
      "NotImplemented",
      "This operation is not implemented by the emulator",
      501,
      { "x-emulator-unsupported": "true" },
    );
    assert.equal(isUnimplemented(err), true);
  });

  it("reads the emulator's header when neither parser produced a status", () => {
    const err = new Error("Deserialization error");
    Object.assign(err, {
      $response: { statusCode: undefined, headers: { "x-emulator-unsupported": "true" } },
    });
    assert.equal(isUnimplemented(err), true);
  });

  it("reports an unknown operation as unimplemented at 400", () => {
    const err = sdkError("UnknownOperationException", "Unknown operation: Frobnicate", 400);
    assert.equal(isUnimplemented(err), true);
  });
});
