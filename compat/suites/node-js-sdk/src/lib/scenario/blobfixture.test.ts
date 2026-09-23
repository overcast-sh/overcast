/**
 * The shared blob-value conformance fixture, compat/model/testdata/blobs.
 *
 * Every backend agrees on one document form for a blob — its canonical
 * standard base64 text (compat/model/README.md § Values) — and this is where
 * node-js-sdk proves it against the same cases every other suite reads:
 * `$base64` decodes to exactly the fixture's bytes, the SDK's Uint8Array
 * renders back to exactly that text, an `equals` against the `$base64` holds,
 * and every spelling the fixture calls invalid is refused rather than decoded
 * into something else. The second half runs a blob round trip through the
 * executor against an in-memory Sender: bytes go to the SDK, an exported blob
 * sits in the bag as text, and a `$base64` around a `$ref` to it hands the SDK
 * the same bytes again.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { runScenarioTest, type ExecEnv } from "./executor.ts";
import {
  ExpressionError,
  decodeBase64,
  describe as describeValue,
  evaluateValue,
  jsonEquals,
  toDocument,
  type EvalContext,
} from "./expressions.ts";
import type { ScenarioTest, Value } from "./ir.ts";

/** Resolved from this module's own location, as errorfixtures.test.ts does. */
const FIXTURE = fileURLToPath(
  new URL("../../../../../model/testdata/blobs/blobs.json", import.meta.url),
);

interface BlobCase {
  name: string;
  base64: string;
  hex?: string;
}

interface BlobFixture {
  $comment: string;
  valid: BlobCase[];
  invalid: BlobCase[];
}

function loadFixture(): BlobFixture {
  const fixture = JSON.parse(readFileSync(FIXTURE, "utf8")) as BlobFixture;
  const unknown = Object.keys(fixture).filter(
    (k) => !["$comment", "valid", "invalid"].includes(k),
  );
  assert.deepEqual(unknown, [], `unknown keys in ${FIXTURE}`);
  assert.ok(
    fixture.valid.length > 0 && fixture.invalid.length > 0,
    "the blob fixture may not be skipped by emptying it",
  );
  return fixture;
}

function ctx(bag: Record<string, unknown> = {}): EvalContext {
  return { runId: "oc", group: "g", bag: new Map(Object.entries(bag)) };
}

const fixture = loadFixture();

describe("the shared blob fixture", () => {
  for (const c of fixture.valid) {
    it(`valid/${c.name}`, () => {
      const want = Buffer.from(c.hex ?? "", "hex");
      const literal = evaluateValue({ $base64: c.base64 } as Value, ctx());
      assert.ok(literal instanceof Uint8Array);
      assert.deepEqual(Buffer.from(literal), want);
      const viaRef = evaluateValue(
        { $base64: { $ref: "rec.data" } } as Value,
        ctx({ "rec.data": c.base64 }),
      );
      assert.deepEqual(Buffer.from(viaRef as Uint8Array), want);
      assert.deepEqual(toDocument({ Data: new Uint8Array(want) }), { Data: c.base64 });
      assert.ok(jsonEquals(c.base64, literal), "equals $base64 holds against the text");
    });
  }
  for (const c of fixture.invalid) {
    it(`invalid/${c.name}`, () => {
      assert.throws(() => decodeBase64(c.base64), ExpressionError);
      assert.throws(
        () =>
          evaluateValue(
            { $base64: { $ref: "rec.data" } } as Value,
            ctx({ "rec.data": c.base64 }),
          ),
        ExpressionError,
      );
    });
  }
  it("renders a blob as its base64 text in a message", () => {
    assert.equal(
      describeValue({ Data: new TextEncoder().encode("record-1") }),
      '{"Data":"cmVjb3JkLTE="}',
    );
  });
});

describe("a blob round trip through the executor", () => {
  it("sends bytes, exports text, and decodes a $ref back to the same bytes", async () => {
    const calls: Array<Record<string, unknown>> = [];
    const bag = new Map<string, unknown>();
    const env: ExecEnv = {
      send: async (_op, params) => {
        calls.push(params);
        return { Echo: new TextEncoder().encode("record-1") };
      },
      ctx: { runId: "oc", group: "kinesis-records", bag },
      scenarioFile: "compat/model/authored/kinesis-records.json",
      log: () => {},
      sleep: async () => {},
    };
    const put: ScenarioTest = {
      name: "Put",
      op: "PutRecord",
      call: {
        op: "PutRecord",
        params: { Data: { $base64: "cmVjb3JkLTE=" } },
        export: { "rec.data": "$.Echo" },
      },
      assert: [
        { kind: "responseField", checks: { "$.Echo": { equals: { $base64: "cmVjb3JkLTE=" } } } },
      ],
    };
    const again: ScenarioTest = {
      name: "Again",
      op: "PutRecord",
      call: { op: "PutRecord", params: { Data: { $base64: { $ref: "rec.data" } } } },
      assert: [
        {
          kind: "responseField",
          checks: { "$.Echo": { equals: { $base64: { $ref: "rec.data" } } } },
        },
      ],
    };
    await runScenarioTest(env, put);
    assert.equal(bag.get("rec.data"), "cmVjb3JkLTE=");
    await runScenarioTest(env, again);
    assert.equal(calls.length, 2);
    for (const params of calls) {
      assert.ok(params.Data instanceof Uint8Array);
      assert.equal(Buffer.from(params.Data).toString("utf8"), "record-1");
    }
  });
});
