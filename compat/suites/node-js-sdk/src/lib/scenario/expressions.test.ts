/**
 * Unit tests for the scenario IR's value expressions, response paths and
 * JSON comparison (compat/model/README.md § Values, § Paths, § Assertions).
 *
 * No emulator and no network: these pin the semantics eight interpreters have
 * to agree on, and every one of them is cheap to get subtly wrong.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  ExpressionError,
  MAX_DESCRIBE,
  describeClipped,
  evaluateParams,
  evaluateValue,
  isEmpty,
  jsonEquals,
  parsePath,
  resolvePath,
  type EvalContext,
} from "./expressions.ts";

function ctxWith(entries: Record<string, unknown> = {}): EvalContext {
  return {
    runId: "oc-12345678",
    group: "sqs-gen-queue",
    bag: new Map(Object.entries(entries)),
  };
}

describe("evaluateValue", () => {
  it("passes scalars, lists and structures through", () => {
    const ctx = ctxWith();
    assert.equal(evaluateValue("plain", ctx), "plain");
    assert.equal(evaluateValue(30, ctx), 30);
    assert.equal(evaluateValue(false, ctx), false);
    assert.equal(evaluateValue(null, ctx), null);
    assert.deepEqual(evaluateValue(["a", 1], ctx), ["a", 1]);
    assert.deepEqual(evaluateValue({ Attributes: { VisibilityTimeout: "30" } }, ctx), {
      Attributes: { VisibilityTimeout: "30" },
    });
  });

  it("$lit is never interpreted", () => {
    const ctx = ctxWith({ "queue.url": "u" });
    assert.deepEqual(evaluateValue({ $lit: { $ref: "queue.url" } }, ctx), {
      $ref: "queue.url",
    });
  });

  it("$ref reads the context bag", () => {
    const ctx = ctxWith({ "queue.url": "http://q" });
    assert.equal(evaluateValue({ $ref: "queue.url" }, ctx), "http://q");
  });

  it("$ref names the path and what is in scope when it cannot resolve", () => {
    const ctx = ctxWith({ "dlq.url": "http://d" });
    assert.throws(
      () => evaluateValue({ $ref: "queue.url" }, ctx),
      (err: unknown) =>
        err instanceof ExpressionError &&
        err.message.includes('"queue.url"') &&
        err.message.includes("dlq.url"),
    );
  });

  it("$name is {runId}-{group}-{suffix}, unshortened", () => {
    assert.equal(
      evaluateValue({ $name: "q" }, ctxWith()),
      "oc-12345678-sqs-gen-queue-q",
    );
  });

  it("$concat joins literal and expression parts", () => {
    const ctx = ctxWith({ "dlq.arn": "arn:aws:sqs:us-east-1:1:d" });
    assert.equal(
      evaluateValue(
        { $concat: ['{"deadLetterTargetArn":"', { $ref: "dlq.arn" }, '","maxReceiveCount":"5"}'] },
        ctx,
      ),
      '{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:1:d","maxReceiveCount":"5"}',
    );
  });

  it("$concat refuses a part that is not a string", () => {
    assert.throws(
      () => evaluateValue({ $concat: ["a", { $ref: "n" }] }, ctxWith({ n: 5 })),
      ExpressionError,
    );
  });

  it("$index selects an element and refuses one past the end", () => {
    const ctx = ctxWith({ "queue.urls": ["a", "b"] });
    assert.equal(evaluateValue({ $index: [{ $ref: "queue.urls" }, 1] }, ctx), "b");
    assert.throws(
      () => evaluateValue({ $index: [{ $ref: "queue.urls" }, 2] }, ctx),
      ExpressionError,
    );
  });

  it("refuses an unknown $-key and a mixed object", () => {
    assert.throws(() => evaluateValue({ $nope: 1 }, ctxWith()), ExpressionError);
    assert.throws(
      () => evaluateValue({ $ref: "a", Other: 1 }, ctxWith()),
      ExpressionError,
    );
  });

  it("evaluates a params object member by member", () => {
    const ctx = ctxWith({ "dlq.arn": "arn:d" });
    assert.deepEqual(
      evaluateParams(
        {
          QueueName: { $name: "q" },
          Attributes: { RedrivePolicy: { $concat: ["<", { $ref: "dlq.arn" }, ">"] } },
          AttributeNames: ["All"],
        },
        ctx,
      ),
      {
        QueueName: "oc-12345678-sqs-gen-queue-q",
        Attributes: { RedrivePolicy: "<arn:d>" },
        AttributeNames: ["All"],
      },
    );
  });
});

describe("paths", () => {
  const response = {
    QueueUrl: "http://q",
    Attributes: { QueueArn: "arn:q", VisibilityTimeout: "30" },
    Messages: [{ MessageId: "m1", Body: "hi" }],
    Tags: { compat: "scenario" },
    Empty: null,
    Omitted: undefined,
  };

  it("parses the whole grammar and nothing else", () => {
    assert.deepEqual(parsePath("$"), []);
    assert.deepEqual(parsePath("$.Messages[0].Body"), [
      { member: "Messages" },
      { index: 0 },
      { member: "Body" },
    ]);
    assert.throws(() => parsePath("Messages"), ExpressionError);
    assert.throws(() => parsePath("$.Messages[*]"), ExpressionError);
    assert.throws(() => parsePath("$..Body"), ExpressionError);
  });

  it("resolves members, map keys and list indices", () => {
    assert.deepEqual(resolvePath(response, "$.QueueUrl"), {
      found: true,
      value: "http://q",
    });
    assert.deepEqual(resolvePath(response, "$.Attributes.QueueArn"), {
      found: true,
      value: "arn:q",
    });
    assert.deepEqual(resolvePath(response, "$.Tags.compat"), {
      found: true,
      value: "scenario",
    });
    assert.deepEqual(resolvePath(response, "$.Messages[0].MessageId"), {
      found: true,
      value: "m1",
    });
    assert.equal((resolvePath(response, "$") as { value: unknown }).value, response);
  });

  it("does not resolve when any segment is absent", () => {
    assert.deepEqual(resolvePath(response, "$.Tags.other"), { found: false });
    assert.deepEqual(resolvePath(response, "$.Messages[1].Body"), { found: false });
    assert.deepEqual(resolvePath(response, "$.QueueUrl.Nested"), { found: false });
    assert.deepEqual(resolvePath(response, "$.Messages.Body"), { found: false });
    assert.deepEqual(resolvePath(response, "$.Attributes[0]"), { found: false });
    // The SDK spells "the service omitted it" as undefined.
    assert.deepEqual(resolvePath(response, "$.Omitted"), { found: false });
    // An explicit null still resolves, so `missing` and `nonEmpty` differ.
    assert.deepEqual(resolvePath(response, "$.Empty"), { found: true, value: null });
  });
});

describe("jsonEquals", () => {
  it("compares in the JSON type system, without coercion", () => {
    assert.ok(jsonEquals("30", "30"));
    assert.ok(jsonEquals(30, 30));
    assert.ok(!jsonEquals(30, "30"));
    assert.ok(!jsonEquals("30", 30));
    assert.ok(jsonEquals(true, true));
    assert.ok(!jsonEquals(true, 1));
    assert.ok(jsonEquals(null, null));
    assert.ok(!jsonEquals(null, ""));
  });

  it("compares lists and objects structurally, ignoring undefined members", () => {
    assert.ok(jsonEquals(["a", "b"], ["a", "b"]));
    assert.ok(!jsonEquals(["a", "b"], ["b", "a"]));
    assert.ok(jsonEquals({ a: 1, b: undefined }, { a: 1 }));
    assert.ok(!jsonEquals({ a: 1 }, { a: 1, b: 2 }));
  });

  it("never treats a timestamp as equal", () => {
    const when = new Date(0);
    assert.ok(!jsonEquals(when, new Date(0)));
  });

  it("compares a blob as its base64 text, on either side", () => {
    assert.ok(jsonEquals(new Uint8Array([1]), new Uint8Array([1])));
    assert.ok(jsonEquals("AQ==", new Uint8Array([1])));
    assert.ok(jsonEquals(new Uint8Array([1]), "AQ=="));
    assert.ok(!jsonEquals(new Uint8Array([1]), new Uint8Array([2])));
    assert.ok(!jsonEquals(new Uint8Array([1]), [1]));
  });
});

describe("describeClipped", () => {
  it("passes a short value through unchanged", () => {
    assert.equal(describeClipped({ QueueUrl: "http://q/1" }), '{"QueueUrl":"http://q/1"}');
  });

  it("elides a value past MAX_DESCRIBE characters, one helper for every caller", () => {
    const big = { QueueUrls: Array.from({ length: 500 }, (_, i) => `http://q/${i}`) };
    const out = describeClipped(big);
    assert.ok(out.endsWith("… (elided)"));
    assert.equal(out.length, MAX_DESCRIBE + "… (elided)".length);
  });
});

describe("isEmpty", () => {
  it("treats null, empty string, list and object as empty", () => {
    assert.ok(isEmpty(null));
    assert.ok(isEmpty(undefined));
    assert.ok(isEmpty(""));
    assert.ok(isEmpty([]));
    assert.ok(isEmpty({}));
  });

  it("never treats a number or a boolean as empty", () => {
    assert.ok(!isEmpty(0));
    assert.ok(!isEmpty(false));
    assert.ok(!isEmpty("x"));
    assert.ok(!isEmpty([0]));
    assert.ok(!isEmpty({ a: 1 }));
  });
});
