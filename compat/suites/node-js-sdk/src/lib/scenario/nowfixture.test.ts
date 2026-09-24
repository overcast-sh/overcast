/**
 * The shared `$now` conformance fixture, compat/model/testdata/now.
 *
 * `$now` is the client's clock when a call is made, in epoch milliseconds,
 * plus an offset (compat/model/README.md § Values). This is where node-js-sdk
 * proves it against the same cases every other suite reads: each valid
 * spelling evaluates to the fixture's value with the clock pinned to its
 * instant, each invalid one is refused, the clock is read once per call
 * however many `$now`s the params hold, and a `$now` is never evaluated
 * outside a call's params.
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
  checkNowArguments,
  evaluateParams,
  evaluateValue,
  type EvalContext,
} from "./expressions.ts";
import type { ScenarioTest, Value, ValueObject } from "./ir.ts";

/** Resolved from this module's own location, as blobfixture.test.ts does. */
const FIXTURE = fileURLToPath(
  new URL("../../../../../model/testdata/now/now.json", import.meta.url),
);

interface NowFixture {
  $comment: string;
  instant: number;
  valid: Array<{ name: string; now: unknown; value: number }>;
  invalid: Array<{ name: string; now: unknown }>;
  invalidArguments: Array<{ name: string; unit: string; offsetMillis: number }>;
  call: { tickMillis: number; params: ValueObject; sent: Record<string, unknown> };
}

function loadFixture(): NowFixture {
  const fixture = JSON.parse(readFileSync(FIXTURE, "utf8")) as NowFixture;
  const unknown = Object.keys(fixture).filter(
    (k) => !["$comment", "instant", "valid", "invalid", "invalidArguments", "call"].includes(k),
  );
  assert.deepEqual(unknown, [], `unknown keys in ${FIXTURE}`);
  assert.ok(
    fixture.valid.length > 0 && fixture.invalid.length > 0 && fixture.invalidArguments.length > 0,
    "the $now fixture may not be skipped by emptying it",
  );
  return fixture;
}

const fixture = loadFixture();

/** A clock that moves on every time it is read, from the fixture's instant. */
function tickingClock(): () => number {
  let next = fixture.instant;
  return () => {
    const reading = next;
    next += fixture.call.tickMillis;
    return reading;
  };
}

function ctx(extra: Partial<EvalContext> = {}): EvalContext {
  return { runId: "oc", group: "g", bag: new Map(), ...extra };
}

describe("the shared $now fixture", () => {
  for (const c of fixture.valid) {
    it(`valid/${c.name}`, () => {
      const got = evaluateValue({ $now: c.now } as Value, ctx({ nowMillis: fixture.instant }));
      assert.equal(got, c.value);
    });
  }
  for (const c of fixture.invalid) {
    it(`invalid/${c.name}`, () => {
      assert.throws(
        () => evaluateValue({ $now: c.now } as Value, ctx({ nowMillis: fixture.instant })),
        ExpressionError,
      );
    });
  }
  for (const c of fixture.invalidArguments) {
    it(`invalidArguments/${c.name}`, () => {
      assert.throws(() => checkNowArguments(c.unit, c.offsetMillis), ExpressionError);
    });
  }
  it("reads the clock once per call", () => {
    const sent = evaluateParams(fixture.call.params, ctx({ clock: tickingClock() }));
    assert.deepEqual(sent, fixture.call.sent);
  });
  it("never evaluates a $now outside a call's params", () => {
    assert.throws(
      () => evaluateValue({ $now: { unit: "epochMillis" } } as Value, ctx({ clock: tickingClock() })),
      ExpressionError,
    );
  });
});

describe("$now through the executor", () => {
  it("sends the client's clock as a number, one reading per call", async () => {
    const calls: Array<Record<string, unknown>> = [];
    const env: ExecEnv = {
      send: async (_op, params) => {
        calls.push(params);
        return { nextSequenceToken: "t" };
      },
      ctx: { runId: "oc", group: "logs-events", bag: new Map(), clock: tickingClock() },
      scenarioFile: "compat/model/authored/logs-events.json",
      log: () => {},
      sleep: async () => {},
    };
    const put: ScenarioTest = {
      name: "PutLogEvents",
      op: "PutLogEvents",
      call: { op: "PutLogEvents", params: fixture.call.params },
      assert: [{ kind: "responseField", checks: { "$.nextSequenceToken": { nonEmpty: true } } }],
    };
    await runScenarioTest(env, put);
    await runScenarioTest(env, put);
    assert.equal(calls.length, 2);
    assert.deepEqual(calls[0], fixture.call.sent);
    // The second call read the clock once more, a tick later.
    const second = calls[1].logEvents as Array<{ timestamp: number }>;
    assert.deepEqual(
      second.map((e) => e.timestamp),
      [fixture.instant + fixture.call.tickMillis - 1, fixture.instant + fixture.call.tickMillis],
    );
  });
});
