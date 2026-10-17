/**
 * Unit tests for the scenario executor.
 *
 * The SDK is not mocked: the executor's only view of it is the `Sender`
 * function, and these tests pass an in-memory one, so nothing here touches a
 * client, a socket or an emulator. What is pinned is the behaviour the
 * contract fixes and a real run would take minutes to demonstrate: exports
 * applied only on a passing clause, `eventually`'s retry budget, the error
 * forms, and the six fields every failure message carries.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  MAX_PAGES,
  runScenarioTest,
  runSetup,
  runTeardown,
  type ExecEnv,
} from "./executor.ts";
import type { Assertion, Call, ScenarioTest } from "./ir.ts";

const SCENARIO_FILE = "compat/model/scenarios/sqs.json";

type Handler = (params: Record<string, unknown>, attempt: number) => unknown;

interface Harness {
  env: ExecEnv;
  calls: Array<{ op: string; params: Record<string, unknown> }>;
  logs: string[];
  slept: number[];
}

/** An in-memory service. A handler may return a response or throw. */
function harness(
  handlers: Record<string, Handler | Handler[]>,
  seed: Record<string, unknown> = {},
): Harness {
  const calls: Harness["calls"] = [];
  const logs: string[] = [];
  const slept: number[] = [];
  const attempts = new Map<string, number>();

  const env: ExecEnv = {
    send: async (op, params) => {
      calls.push({ op, params });
      const attempt = attempts.get(op) ?? 0;
      attempts.set(op, attempt + 1);
      const handler = handlers[op];
      if (handler === undefined) throw new Error(`no handler for ${op}`);
      const fn = Array.isArray(handler)
        ? handler[Math.min(attempt, handler.length - 1)]
        : handler;
      return fn(params, attempt);
    },
    ctx: { runId: "oc-12345678", group: "sqs-gen-queue", bag: new Map(Object.entries(seed)) },
    scenarioFile: SCENARIO_FILE,
    log: (msg) => logs.push(msg),
    sleep: async (ms) => {
      slept.push(ms);
    },
  };
  return { env, calls, logs, slept };
}

function sdkError(name: string, extra: Record<string, unknown> = {}): Error {
  const err = new Error(`${name} raised by the fake service`);
  err.name = name;
  Object.assign(err, extra);
  return err;
}

async function failureOf(run: () => Promise<void>): Promise<Error> {
  try {
    await run();
  } catch (err) {
    assert.ok(err instanceof Error, `expected an Error, got ${String(err)}`);
    return err;
  }
  throw new assert.AssertionError({ message: "expected the run to fail" });
}

describe("a test's primary call", () => {
  const test: ScenarioTest = {
    name: "CreateQueue",
    op: "CreateQueue",
    call: {
      op: "CreateQueue",
      params: { QueueName: { $name: "q" } },
      export: { "queue.url": "$.QueueUrl" },
    },
    assert: [{ kind: "responseField", checks: { "$.QueueUrl": { nonEmpty: true } } }],
  };

  it("sends the evaluated params and exports from the response", async () => {
    const h = harness({ CreateQueue: () => ({ QueueUrl: "http://q/1" }) });
    await runScenarioTest(h.env, test);
    assert.deepEqual(h.calls, [
      { op: "CreateQueue", params: { QueueName: "oc-12345678-sqs-gen-queue-q" } },
    ]);
    assert.equal(h.env.ctx.bag.get("queue.url"), "http://q/1");
  });

  it("fails with all six fields when the call raises", async () => {
    const h = harness({ CreateQueue: () => { throw sdkError("QueueDeletedRecently"); } });
    const err = await failureOf(() => runScenarioTest(h.env, test));
    assert.match(err.message, /^sqs-gen-queue\/CreateQueue: /); // 1: group/test
    assert.ok(err.message.includes("CreateQueue params=")); //     2 and 3
    assert.ok(err.message.includes('{"QueueName":"oc-12345678-sqs-gen-queue-q"}'));
    assert.ok(err.message.includes("call:")); //                   4: kind
    assert.ok(err.message.includes("expected the call to succeed")); // 5
    assert.ok(err.message.includes("QueueDeletedRecently"));
    assert.ok(err.message.endsWith(`(${SCENARIO_FILE} call)`)); //  6
  });

  it("fails naming the export path when it does not resolve", async () => {
    const h = harness({ CreateQueue: () => ({}) });
    const err = await failureOf(() => runScenarioTest(h.env, test));
    assert.ok(err.message.includes("export at $.QueueUrl"));
    assert.ok(err.message.includes("queue.url"));
    assert.ok(err.message.includes("<missing>"));
  });

  it("fails naming the parameter expression that could not evaluate", async () => {
    const h = harness({ DeleteQueue: () => ({}) });
    const err = await failureOf(() =>
      runScenarioTest(h.env, {
        name: "DeleteQueue",
        op: "DeleteQueue",
        call: { op: "DeleteQueue", params: { QueueUrl: { $ref: "queue.url" } } },
        assert: [{ kind: "responseField", checks: { "$.X": { nonEmpty: true } } }],
      }),
    );
    assert.ok(err.message.includes("params:"));
    assert.ok(err.message.includes("unresolvable $ref"));
    assert.equal(h.calls.length, 0, "no call is made when a parameter cannot be built");
  });

  it("carries the SDK's unimplemented markers through the wrapper", async () => {
    const h = harness({
      CreateQueue: () => {
        throw sdkError("ServiceException", { $metadata: { httpStatusCode: 501 } });
      },
    });
    const err = await failureOf(() => runScenarioTest(h.env, test));
    // harness.ts isUnimplemented() reads exactly this, so a probe of a Tier 0
    // operation still records `unimplemented` rather than `fail`.
    assert.deepEqual(
      (err as unknown as Record<string, unknown>)["$metadata"],
      { httpStatusCode: 501 },
    );
  });
});

describe("readback", () => {
  const test: ScenarioTest = {
    name: "SetQueueAttributes",
    op: "SetQueueAttributes",
    call: { op: "SetQueueAttributes", params: { QueueUrl: { $ref: "queue.url" } } },
    assert: [
      {
        kind: "readback",
        call: {
          op: "GetQueueAttributes",
          params: { QueueUrl: { $ref: "queue.url" } },
          export: { "queue.arn": "$.Attributes.QueueArn" },
        },
        checks: { "$.Attributes.VisibilityTimeout": { equals: "60" } },
      },
    ],
  };

  it("applies the read-back's exports when the checks pass", async () => {
    const h = harness(
      {
        SetQueueAttributes: () => ({}),
        GetQueueAttributes: () => ({
          Attributes: { VisibilityTimeout: "60", QueueArn: "arn:q" },
        }),
      },
      { "queue.url": "http://q/1" },
    );
    await runScenarioTest(h.env, test);
    assert.equal(h.env.ctx.bag.get("queue.arn"), "arn:q");
  });

  it("does not apply them when a check fails, and reports the clause's own op", async () => {
    const h = harness(
      {
        SetQueueAttributes: () => ({}),
        GetQueueAttributes: () => ({
          Attributes: { VisibilityTimeout: "30", QueueArn: "arn:q" },
        }),
      },
      { "queue.url": "http://q/1" },
    );
    const err = await failureOf(() => runScenarioTest(h.env, test));
    assert.equal(h.env.ctx.bag.has("queue.arn"), false);
    assert.ok(err.message.includes("GetQueueAttributes params="));
    assert.ok(err.message.includes("readback at $.Attributes.VisibilityTimeout"));
    assert.ok(err.message.includes('expected "60", actual "30"'));
    assert.ok(err.message.endsWith(`(${SCENARIO_FILE} assert[0])`));
  });
});

describe("eventually", () => {
  const test: ScenarioTest = {
    name: "CreateQueue",
    op: "CreateQueue",
    call: { op: "CreateQueue", params: {}, export: { "queue.url": "$.QueueUrl" } },
    assert: [
      {
        kind: "eventually",
        maxAttempts: 3,
        delayMs: 2000,
        assert: {
          kind: "readback",
          call: {
            op: "GetQueueAttributes",
            params: { QueueUrl: { $ref: "queue.url" } },
            export: { "queue.arn": "$.Attributes.QueueArn" },
          },
          checks: { "$.Attributes.QueueArn": { nonEmpty: true } },
        },
      },
    ],
  };

  it("retries until the clause holds and exports only from that attempt", async () => {
    const h = harness({
      CreateQueue: () => ({ QueueUrl: "http://q/1" }),
      GetQueueAttributes: [
        () => ({ Attributes: {} }),
        () => ({ Attributes: { QueueArn: "arn:late" } }),
      ],
    });
    await runScenarioTest(h.env, test);
    assert.equal(h.env.ctx.bag.get("queue.arn"), "arn:late");
    assert.deepEqual(h.slept, [2000], "one delay between the two attempts");
  });

  it("gives up after maxAttempts, reporting the last failure and the budget", async () => {
    const h = harness({
      CreateQueue: () => ({ QueueUrl: "http://q/1" }),
      GetQueueAttributes: () => ({ Attributes: {} }),
    });
    const err = await failureOf(() => runScenarioTest(h.env, test));
    assert.deepEqual(h.slept, [2000, 2000]);
    assert.ok(err.message.includes("readback at $.Attributes.QueueArn"));
    // The give-up prefix is fixed byte-for-byte by compat/model/README.md
    // § Failure messages; python-sdk and cli emit the same string.
    assert.ok(
      err.message.startsWith(
        "eventually gave up after 3 attempt(s) 2000ms apart; last failure: ",
      ),
      err.message,
    );
    // Field 6 names the inner clause, not the wrapper.
    assert.ok(err.message.includes(`${SCENARIO_FILE} assert[0].assert)`));
  });
});

describe("the error forms", () => {
  const notFound = { shape: "QueueDoesNotExist", code: "AWS.SimpleQueueService.NonExistentQueue" };

  const deleteQueue: ScenarioTest = {
    name: "DeleteQueue",
    op: "DeleteQueue",
    call: { op: "DeleteQueue", params: { QueueUrl: { $ref: "queue.url" } } },
    assert: [
      {
        kind: "absent",
        call: { op: "GetQueueAttributes", params: { QueueUrl: { $ref: "queue.url" } } },
        error: notFound,
      },
    ],
  };

  it("absent holds when the call raises either accepted code", async () => {
    const h = harness(
      {
        DeleteQueue: () => ({}),
        GetQueueAttributes: () => {
          throw sdkError("AWS.SimpleQueueService.NonExistentQueue");
        },
      },
      { "queue.url": "http://q/1" },
    );
    await runScenarioTest(h.env, deleteQueue);
  });

  it("absent fails when the call succeeds", async () => {
    const h = harness(
      { DeleteQueue: () => ({}), GetQueueAttributes: () => ({ Attributes: {} }) },
      { "queue.url": "http://q/1" },
    );
    const err = await failureOf(() => runScenarioTest(h.env, deleteQueue));
    assert.ok(err.message.includes("absent:"));
    assert.ok(
      err.message.includes(
        "expected an error (QueueDoesNotExist or AWS.SimpleQueueService.NonExistentQueue)",
      ),
    );
    assert.ok(err.message.includes("actual <no error>"));
  });

  it("errorCode expects the test's own call to fail", async () => {
    const test: ScenarioTest = {
      name: "GetQueueUrlMissing",
      op: "GetQueueUrl",
      call: { op: "GetQueueUrl", params: { QueueName: "nope" } },
      assert: [{ kind: "errorCode", error: notFound }],
    };
    const failing = harness({
      GetQueueUrl: () => {
        throw sdkError("QueueDoesNotExist");
      },
    });
    await runScenarioTest(failing.env, test);

    const succeeding = harness({ GetQueueUrl: () => ({ QueueUrl: "http://q/1" }) });
    const err = await failureOf(() => runScenarioTest(succeeding.env, test));
    assert.ok(err.message.includes("errorCode:"));
    assert.ok(err.message.includes("actual <no error>"));
  });

  it("errorCode fails on a different error, quoting both", async () => {
    const h = harness({
      GetQueueUrl: () => {
        throw sdkError("AccessDeniedException");
      },
    });
    const err = await failureOf(() =>
      runScenarioTest(h.env, {
        name: "GetQueueUrlMissing",
        op: "GetQueueUrl",
        call: { op: "GetQueueUrl", params: {} },
        assert: [{ kind: "errorCode", error: notFound }],
      }),
    );
    assert.ok(err.message.includes("QueueDoesNotExist"));
    assert.ok(err.message.includes("AccessDeniedException"));
  });
});

describe("list clauses on the test's own response", () => {
  it("listContains reads the primary response when the clause has no call", async () => {
    const h = harness({ ListQueues: () => ({ QueueUrls: ["http://q/1"] }) }, {
      "queue.url": "http://q/1",
    });
    await runScenarioTest(h.env, {
      name: "ListQueues",
      op: "ListQueues",
      call: { op: "ListQueues", params: {} },
      assert: [
        { kind: "listContains", itemsPath: "$.QueueUrls", where: { $: { $ref: "queue.url" } } },
      ],
    });
    assert.equal(h.calls.length, 1);
  });

  it("a missing list fails listContains with <missing>", async () => {
    const h = harness({ ListQueues: () => ({}) }, { "queue.url": "http://q/1" });
    const err = await failureOf(() =>
      runScenarioTest(h.env, {
        name: "ListQueues",
        op: "ListQueues",
        call: { op: "ListQueues", params: {} },
        assert: [
          { kind: "listContains", itemsPath: "$.QueueUrls", where: { $: { $ref: "queue.url" } } },
        ],
      }),
    );
    assert.ok(err.message.includes("listContains at $.QueueUrls"));
    assert.ok(err.message.includes("<missing>"));
  });
});

describe("paginating a list", () => {
  const paginate = (extra: Record<string, unknown> = {}) => ({
    pageSize: 1,
    inputToken: "NextToken",
    outputToken: "NextToken",
    pageSizeMember: "MaxResults",
    ...extra,
  });
  const testOf = (
    kind: "listContains" | "absent",
    extra: Record<string, unknown> = {},
    wrap?: { maxAttempts: number },
  ): ScenarioTest => {
    const clause = {
      kind,
      call: { op: "ListQueues", params: { QueueNamePrefix: "oc-" } },
      itemsPath: "$.QueueUrls",
      where: { $: "http://q" },
      paginate: paginate(),
      ...extra,
    } as unknown as Assertion;
    return {
      name: "ListQueues",
      op: "DeleteQueue",
      call: { op: "DeleteQueue", params: {} },
      assert: [
        wrap === undefined
          ? clause
          : ({ kind: "eventually", maxAttempts: wrap.maxAttempts, delayMs: 0, assert: clause } as Assertion),
      ],
    };
  };
  const pages = (...answers: unknown[]): Handler => (_params, attempt) => {
    const answer = answers[Math.min(attempt, answers.length - 1)];
    if (answer instanceof Error) throw answer;
    return answer;
  };
  const page = (urls: string[] | undefined, token?: unknown) => ({
    ...(urls === undefined ? {} : { QueueUrls: urls }),
    ...(token === undefined ? {} : { NextToken: token }),
  });
  const listCalls = (h: Harness) => h.calls.filter((c) => c.op === "ListQueues").map((c) => c.params);

  it("walks every page, feeding the token back with the page size on each", async () => {
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t1"), page(["http://b"], "t2"), page(["http://q"])),
    });
    await runScenarioTest(h.env, testOf("listContains"));
    assert.deepEqual(listCalls(h), [
      { QueueNamePrefix: "oc-", MaxResults: 1 },
      { QueueNamePrefix: "oc-", MaxResults: 1, NextToken: "t1" },
      { QueueNamePrefix: "oc-", MaxResults: 1, NextToken: "t2" },
    ]);
  });

  it("stops at a page with no token: absent, null or empty", async () => {
    for (const last of [page(["http://q"]), page(["http://q"], null), page(["http://q"], "")]) {
      const h = harness({ DeleteQueue: () => ({}), ListQueues: pages(last) });
      await runScenarioTest(h.env, testOf("listContains"));
      assert.equal(listCalls(h).length, 1);
    }
  });

  it("matches where against the union, every entry on one item", async () => {
    const items = (...pairs: Array<[string, string]>) => ({
      Items: pairs.map(([Id, Kind]) => ({ Id, Kind })),
    });
    const test = testOf("listContains", { itemsPath: "$.Items", where: { "$.Id": "b", "$.Kind": "x" } });
    const split = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages({ ...items(["a", "x"]), NextToken: "t" }, items(["b", "y"])),
    });
    await failureOf(() => runScenarioTest(split.env, test));
    const together = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages({ ...items(["a", "x"]), NextToken: "t" }, items(["b", "x"])),
    });
    await runScenarioTest(together.env, test);
  });

  it("a miss says how many pages were walked and what was found, quoting page one's params", async () => {
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t1"), page(["http://b"], "t2"), page(["http://c"])),
    });
    const err = await failureOf(() => runScenarioTest(h.env, testOf("listContains")));
    assert.ok(err.message.includes("walked 3 page(s), 3 item(s): "));
    assert.ok(err.message.includes('["http://a","http://b","http://c"]'));
    assert.ok(err.message.includes("listContains at $.QueueUrls"));
    assert.ok(err.message.includes('"MaxResults":1'));
    assert.ok(!err.message.includes("NextToken"), "page one's params carry no token");
  });

  it("listContains still needs a non-empty list; a page without the list is empty", async () => {
    for (const answers of [[page([])], [page(undefined)], [page(undefined, "t"), page([])]]) {
      const h = harness({ DeleteQueue: () => ({}), ListQueues: pages(...answers) });
      await failureOf(() => runScenarioTest(h.env, testOf("listContains")));
    }
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(undefined, "t1"), page(["http://q"])),
    });
    await runScenarioTest(h.env, testOf("listContains"));
  });

  it("minPages fails a single page that happens to hold everything", async () => {
    const test = testOf("listContains", { paginate: paginate({ minPages: 2 }) });
    const single = harness({ DeleteQueue: () => ({}), ListQueues: pages(page(["http://q"])) });
    const err = await failureOf(() => runScenarioTest(single.env, test));
    assert.ok(err.message.includes("expected at least 2 page(s) at a page size of 1"));
    assert.ok(err.message.includes("walked 1 page(s), 1 item(s)"));
    const split = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t"), page(["http://q"])),
    });
    await runScenarioTest(split.env, test);
  });

  it("a service that never stops fails at the page cap instead of hanging", async () => {
    const h = harness({ DeleteQueue: () => ({}), ListQueues: pages(page(["http://a"], "again")) });
    const err = await failureOf(() => runScenarioTest(h.env, testOf("listContains")));
    assert.equal(listCalls(h).length, MAX_PAGES);
    assert.ok(err.message.includes(`no NextToken on a page within ${MAX_PAGES} pages`));
    assert.ok(
      err.message.includes(
        `walked ${MAX_PAGES} page(s), ${MAX_PAGES} item(s), and the last still returned a NextToken`,
      ),
    );
  });

  it("walks to the end even once the item is found", async () => {
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://q"], "t1"), page(["http://b"])),
    });
    await runScenarioTest(h.env, testOf("listContains"));
    assert.equal(listCalls(h).length, 2);
  });

  it("absent looks at every page", async () => {
    const clean = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t"), page(["http://b"])),
    });
    await runScenarioTest(clean.env, testOf("absent"));
    const dirty = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t"), page(["http://q"])),
    });
    const err = await failureOf(() => runScenarioTest(dirty.env, testOf("absent")));
    assert.ok(err.message.includes("walked 2 page(s), 2 item(s): "));
    assert.ok(err.message.includes("no item matching"));
  });

  it("a token that is not a string fails the clause", async () => {
    const h = harness({ DeleteQueue: () => ({}), ListQueues: pages(page(["http://q"], 7)) });
    const err = await failureOf(() => runScenarioTest(h.env, testOf("listContains")));
    assert.ok(err.message.includes("expected a string token, or none"));
  });

  it("a call failing on a later page is a call failure carrying that page's params", async () => {
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(page(["http://a"], "t1"), sdkError("Boom")),
    });
    const err = await failureOf(() => runScenarioTest(h.env, testOf("listContains")));
    assert.ok(err.message.includes("call:"));
    assert.ok(err.message.includes('"NextToken":"t1"'));
  });

  it("inside eventually every attempt walks again from the first page", async () => {
    const h = harness({
      DeleteQueue: () => ({}),
      ListQueues: pages(
        page(["http://a"], "t1"), page(["http://b"]), // attempt 1: no match
        page(["http://a"], "t1"), page(["http://q"]), // attempt 2: match
      ),
    });
    await runScenarioTest(h.env, testOf("listContains", {}, { maxAttempts: 3 }));
    const sent = listCalls(h);
    assert.equal(sent.length, 4);
    assert.ok(!("NextToken" in sent[2]!));
  });
});

describe("setup and teardown", () => {
  const setup: Call[] = [
    { op: "CreateQueue", params: { QueueName: { $name: "dlq" } }, export: { "dlq.url": "$.QueueUrl" } },
    {
      op: "GetQueueAttributes",
      params: { QueueUrl: { $ref: "dlq.url" } },
      export: { "dlq.arn": "$.Attributes.QueueArn" },
    },
  ];

  it("runs setup in order, threading exports through the context", async () => {
    const h = harness({
      CreateQueue: () => ({ QueueUrl: "http://q/dlq" }),
      GetQueueAttributes: (params) => {
        assert.equal(params["QueueUrl"], "http://q/dlq");
        return { Attributes: { QueueArn: "arn:dlq" } };
      },
    });
    await runSetup(h.env, setup);
    assert.equal(h.env.ctx.bag.get("dlq.arn"), "arn:dlq");
  });

  it("throws the bare message so the harness reads `setup failed: <message>`", async () => {
    const h = harness({
      CreateQueue: () => {
        throw sdkError("ServiceUnavailable");
      },
    });
    await assert.rejects(
      () => runSetup(h.env, setup),
      (err: unknown) => {
        assert.equal(typeof err, "string", "a bare string, not an Error");
        assert.ok(String(err).startsWith("sqs-gen-queue/<setup>: CreateQueue params="));
        assert.ok(String(err).endsWith(`(${SCENARIO_FILE} setup[0])`));
        return true;
      },
    );
  });

  it("wraps each teardown call on its own and keeps going", async () => {
    const h = harness(
      {
        DeleteMessage: () => {
          throw sdkError("ReceiptHandleIsInvalid");
        },
        DeleteQueue: () => ({}),
      },
      { "queue.url": "http://q/1", "dlq.url": "http://q/dlq" },
    );
    await runTeardown(h.env, [
      { op: "DeleteMessage", params: { QueueUrl: { $ref: "queue.url" }, ReceiptHandle: { $ref: "message.receiptHandle" } } },
      { op: "DeleteQueue", params: { QueueUrl: { $ref: "queue.url" } } },
      { op: "DeleteQueue", params: { QueueUrl: { $ref: "dlq.url" } } },
    ]);
    // The first step's $ref is unresolvable, so it is skipped without a call;
    // the two deletes still run.
    assert.deepEqual(
      h.calls.map((c) => c.op),
      ["DeleteQueue", "DeleteQueue"],
    );
    assert.equal(h.logs.length, 1);
    assert.ok(h.logs[0].includes("teardown DeleteMessage skipped"));
  });

  it("skips a teardown call that raises and still runs the rest", async () => {
    const h = harness(
      {
        DeleteQueue: [
          () => {
            throw sdkError("QueueDoesNotExist");
          },
          () => ({}),
        ],
      },
      { "queue.url": "http://q/1", "dlq.url": "http://q/dlq" },
    );
    await runTeardown(h.env, [
      { op: "DeleteQueue", params: { QueueUrl: { $ref: "queue.url" } } },
      { op: "DeleteQueue", params: { QueueUrl: { $ref: "dlq.url" } } },
    ]);
    assert.equal(h.calls.length, 2);
    assert.equal(h.logs.length, 1);
  });
});
