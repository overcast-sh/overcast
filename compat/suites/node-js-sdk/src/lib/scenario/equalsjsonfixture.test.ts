/**
 * The shared `equalsJSON` conformance fixture, compat/model/testdata/equalsjson.
 *
 * `equalsJSON` compares a member holding a JSON document by value rather than
 * by text (compat/model/README.md § Documents in a string). This SDK hands an
 * IAM policy document over as the percent-encoded string AWS sends, while
 * botocore decodes it for python-sdk and cli, so this is where node-js-sdk
 * proves it reaches the same verdict as all of them: the percent-decoding,
 * the documents that are equal and the ones that are not, and the operands
 * every reader refuses.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { evaluateChecks } from "./assertions.ts";
import {
  ExpressionError,
  checkEqualsJsonOperand,
  percentDecode,
  type EvalContext,
} from "./expressions.ts";
import type { JsonValue } from "./ir.ts";

/** Resolved from this module's own location, as nowfixture.test.ts does. */
const FIXTURE = fileURLToPath(
  new URL("../../../../../model/testdata/equalsjson/equalsjson.json", import.meta.url),
);

interface EqualsJsonFixture {
  $comment: string;
  decode: Array<{ name: string; text: string; decoded: string }>;
  holds: Array<{ name: string; actual: unknown; expected: JsonValue }>;
  fails: Array<{ name: string; actual: unknown; expected: JsonValue }>;
  invalidExpected: Array<{ name: string; expected: unknown }>;
}

/**
 * Every section's case keys, so a key this reader does not know — a new
 * field every backend is meant to honour — fails here rather than being
 * ignored.
 */
const SECTIONS: Record<string, string[]> = {
  decode: ["name", "text", "decoded"],
  holds: ["name", "actual", "expected"],
  fails: ["name", "actual", "expected"],
  invalidExpected: ["name", "expected"],
};

function loadFixture(): EqualsJsonFixture {
  const fixture = JSON.parse(readFileSync(FIXTURE, "utf8")) as Record<string, unknown>;
  const unknown = Object.keys(fixture).filter((k) => k !== "$comment" && !(k in SECTIONS));
  assert.deepEqual(unknown, [], `unknown keys in ${FIXTURE}`);
  for (const [section, keys] of Object.entries(SECTIONS)) {
    const cases = fixture[section];
    assert.ok(
      Array.isArray(cases) && cases.length > 0,
      `the equalsJSON fixture may not be skipped by emptying ${section}`,
    );
    for (const c of cases as Array<Record<string, unknown>>) {
      const extra = Object.keys(c).filter((k) => !keys.includes(k));
      assert.deepEqual(extra, [], `unknown keys in ${section} case ${String(c["name"])}`);
    }
  }
  return fixture as unknown as EqualsJsonFixture;
}

const fixture = loadFixture();

const ctx: EvalContext = { runId: "oc", group: "iam-gen-role", bag: new Map() };

/** One equalsJSON check against a response whose `$.Doc` is `actual`. */
function check(actual: unknown, expected: JsonValue) {
  return evaluateChecks({ Doc: actual }, { "$.Doc": { equalsJSON: expected } }, ctx);
}

describe("the shared equalsJSON fixture", () => {
  it("decode: percent-decodes exactly as botocore does", () => {
    for (const c of fixture.decode) {
      assert.equal(percentDecode(c.text), c.decoded, c.name);
    }
  });

  it("holds", () => {
    for (const c of fixture.holds) {
      assert.equal(check(c.actual, c.expected), null, c.name);
    }
  });

  it("fails", () => {
    for (const c of fixture.fails) {
      assert.notEqual(check(c.actual, c.expected), null, c.name);
    }
  });

  it("invalidExpected: refuses the operand", () => {
    for (const c of fixture.invalidExpected) {
      assert.throws(() => checkEqualsJsonOperand(c.expected), ExpressionError, c.name);
    }
  });
});

describe("an equalsJSON failure", () => {
  it("shows a document that differs decoded", () => {
    assert.deepEqual(check('{"a":1,"b":2}', { a: 1 }), {
      path: "$.Doc",
      expected: 'equalsJSON {"a":1}',
      actual: 'document {"a":1,"b":2}',
    });
  });

  it("shows a value that is not a document as it came", () => {
    assert.deepEqual(check("not a policy", {}), {
      path: "$.Doc",
      expected: "equalsJSON {}",
      actual: 'not a JSON document: "not a policy"',
    });
  });

  it("shows a path that does not resolve as missing", () => {
    assert.deepEqual(evaluateChecks({}, { "$.Doc": { equalsJSON: {} } }, ctx), {
      path: "$.Doc",
      expected: "equalsJSON {}",
      actual: "<missing>",
    });
  });

  it("refuses an operand that slipped past the loader rather than reading it", () => {
    assert.throws(
      () => check("{}", { $ref: "role.policy" }),
      /never evaluated/,
    );
  });
});
