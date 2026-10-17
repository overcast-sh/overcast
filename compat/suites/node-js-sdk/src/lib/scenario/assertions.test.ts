/**
 * Unit tests for the closed assertion set's predicates.
 *
 * `isList` and `nonEmpty` get the most attention on purpose: `isList` is the
 * check every `List*` probe carries — getting it wrong fails 16 of the 25
 * `organizations` probes at once — and `nonEmpty` is the one every other
 * probe carries.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  acceptedCodes,
  errorCodes,
  errorMatches,
  evaluateChecks,
  evaluateListAbsent,
  evaluateListContains,
} from "./assertions.ts";
import type { EvalContext } from "./expressions.ts";

const ctx: EvalContext = {
  runId: "oc-12345678",
  group: "sqs-gen-queue",
  bag: new Map<string, unknown>([["queue.url", "http://q/1"]]),
};

describe("checks", () => {
  it("nonEmpty rejects null, empty string, list and object", () => {
    for (const value of [null, "", [], {}]) {
      assert.notEqual(
        evaluateChecks({ F: value }, { "$.F": { nonEmpty: true } }, ctx),
        null,
      );
    }
  });

  it("nonEmpty never fails on a number or a boolean", () => {
    assert.equal(evaluateChecks({ F: 0 }, { "$.F": { nonEmpty: true } }, ctx), null);
    assert.equal(
      evaluateChecks({ F: false }, { "$.F": { nonEmpty: true } }, ctx),
      null,
    );
  });

  it("nonEmpty holds on a timestamp, which the SDK hands back as a Date", () => {
    // A Date has no enumerable own properties, so it must not be mistaken for
    // `{}` — README § Values: `nonEmpty` holds on every form a timestamp takes.
    assert.equal(
      evaluateChecks({ F: new Date(0) }, { "$.F": { nonEmpty: true } }, ctx),
      null,
    );
  });

  it("nonEmpty on a missing path reports <missing>", () => {
    const mismatch = evaluateChecks({}, { "$.F": { nonEmpty: true } }, ctx);
    assert.equal(mismatch?.actual, "<missing>");
    assert.equal(mismatch?.path, "$.F");
  });

  it("isList accepts a list, an empty list and an omitted member", () => {
    assert.equal(evaluateChecks({ L: ["a"] }, { "$.L": { isList: true } }, ctx), null);
    assert.equal(evaluateChecks({ L: [] }, { "$.L": { isList: true } }, ctx), null);
    assert.equal(evaluateChecks({}, { "$.L": { isList: true } }, ctx), null);
  });

  it("isList rejects a present value that is not a list", () => {
    assert.notEqual(
      evaluateChecks({ L: "nope" }, { "$.L": { isList: true } }, ctx),
      null,
    );
    assert.notEqual(evaluateChecks({ L: {} }, { "$.L": { isList: true } }, ctx), null);
  });

  it("equals compares as JSON, including against a $ref", () => {
    assert.equal(
      evaluateChecks({ U: "http://q/1" }, { "$.U": { equals: { $ref: "queue.url" } } }, ctx),
      null,
    );
    assert.notEqual(
      evaluateChecks({ U: "http://q/2" }, { "$.U": { equals: { $ref: "queue.url" } } }, ctx),
      null,
    );
    assert.notEqual(evaluateChecks({ V: 30 }, { "$.V": { equals: "30" } }, ctx), null);
  });

  it("matches applies the regex unanchored unless the pattern anchors", () => {
    const pattern = "^p-[0-9a-zA-Z_]{8,128}$";
    assert.equal(
      evaluateChecks({ Id: "p-abcdefgh" }, { "$.Id": { matches: pattern } }, ctx),
      null,
    );
    assert.notEqual(
      evaluateChecks({ Id: "p-abc" }, { "$.Id": { matches: pattern } }, ctx),
      null,
    );
    assert.notEqual(
      evaluateChecks({ Id: 42 }, { "$.Id": { matches: pattern } }, ctx),
      null,
    );
  });

  it("matches turns a pattern JS cannot compile into a mismatch, not a throw", () => {
    // Legal RE2 (and PCRE) named-group syntax; JS spells it (?<name>…) instead
    // and throws a SyntaxError from `new RegExp` on this one.
    const pattern = "(?P<name>foo)";
    const mismatch = evaluateChecks({ Id: "foo" }, { "$.Id": { matches: pattern } }, ctx);
    assert.ok(mismatch !== null);
    assert.equal(mismatch.path, "$.Id");
    assert.equal(mismatch.expected, `pattern ${pattern}`);
    assert.match(mismatch.actual, /^unsupported pattern: /);
  });

  it("endsWith and startsWith evaluate a value expression and compare plain text", () => {
    const url = { Url: "http://q/oc-12345678-sqs-gen-queue-q", N: 3 };
    const suffix = { $concat: ["/", { $name: "q" }] };
    assert.equal(evaluateChecks(url, { "$.Url": { endsWith: suffix } }, ctx), null);
    assert.equal(evaluateChecks(url, { "$.Url": { endsWith: { $name: "q" } } }, ctx), null);
    assert.equal(evaluateChecks(url, { "$.Url": { startsWith: "http://q/" } }, ctx), null);
    // Case-sensitive, anchored at its end, and text only.
    assert.equal(
      evaluateChecks(url, { "$.Url": { contains: { $concat: ["q/", { $name: "q" }] } } }, ctx),
      null,
    );
    assert.notEqual(evaluateChecks(url, { "$.Url": { contains: "q/x" } }, ctx), null);
    assert.notEqual(evaluateChecks(url, { "$.Url": { endsWith: "-Q" } }, ctx), null);
    assert.notEqual(evaluateChecks(url, { "$.Url": { endsWith: "http://q" } }, ctx), null);
    assert.notEqual(evaluateChecks(url, { "$.Url": { startsWith: "q/" } }, ctx), null);
    assert.notEqual(evaluateChecks(url, { "$.N": { endsWith: "3" } }, ctx), null);
    assert.notEqual(evaluateChecks(url, { "$.Nope": { endsWith: "x" } }, ctx), null);

    const mismatch = evaluateChecks(url, { "$.Url": { endsWith: "-nope" } }, ctx);
    assert.ok(mismatch !== null);
    assert.equal(mismatch.path, "$.Url");
    assert.equal(mismatch.expected, 'a string ending with "-nope"');
  });

  it("an affix operand that is not a non-empty string fails the check", () => {
    const url = { Url: "http://q/1" };
    for (const operand of [{ $lit: 7 }, ""]) {
      const mismatch = evaluateChecks(url, { "$.Url": { startsWith: operand } }, ctx);
      assert.ok(mismatch !== null, JSON.stringify(operand));
      assert.equal(mismatch.expected, "a non-empty string operand");
    }
  });

  it("reports an unsupported pattern whatever the value's type is", () => {
    // The pattern is compiled before the value is looked at, so a broken
    // scenario file reads the same way against a number as against a string —
    // which is how python and cli report it, and what stops one file failing
    // two different ways across the three interpreters.
    const pattern = "(?P<name>foo)";
    const mismatch = evaluateChecks({ Id: 42 }, { "$.Id": { matches: pattern } }, ctx);
    assert.ok(mismatch !== null);
    assert.equal(mismatch.expected, `pattern ${pattern}`);
    assert.match(mismatch.actual, /^unsupported pattern: /);
  });

  it("missing holds when any segment is absent and fails when it resolves", () => {
    assert.equal(
      evaluateChecks({ Tags: {} }, { "$.Tags.compat": { missing: true } }, ctx),
      null,
    );
    assert.equal(evaluateChecks({}, { "$.Tags.compat": { missing: true } }, ctx), null);
    assert.notEqual(
      evaluateChecks(
        { Tags: { compat: "scenario" } },
        { "$.Tags.compat": { missing: true } },
        ctx,
      ),
      null,
    );
  });

  it("present holds for any resolved value, null and empties included", () => {
    const response = { A: [], B: {}, C: "", D: 0, E: false, F: null, G: { H: "x" } };
    for (const member of ["A", "B", "C", "D", "E", "F", "G"]) {
      assert.equal(evaluateChecks(response, { [`$.${member}`]: { present: true } }, ctx), null, member);
    }
    assert.equal(evaluateChecks(response, { "$.G.H": { present: true } }, ctx), null);
  });

  it("present fails only when a segment is absent, naming the path and <missing>", () => {
    for (const path of ["$.Absent", "$.G.Nope", "$.A[0]", "$.F.Nope"]) {
      const mismatch = evaluateChecks(
        { A: [], F: null, G: { H: "x" } },
        { [path]: { present: true } },
        ctx,
      );
      assert.ok(mismatch !== null, path);
      assert.equal(mismatch.path, path);
      assert.equal(mismatch.expected, "the path to resolve");
      assert.equal(mismatch.actual, "<missing>");
    }
  });

  it("present is the negation of missing on every shape", () => {
    for (const response of [{}, { X: null }, { X: [] }, { X: "v" }]) {
      const present = evaluateChecks(response, { "$.X": { present: true } }, ctx) === null;
      const missing = evaluateChecks(response, { "$.X": { missing: true } }, ctx) === null;
      assert.notEqual(present, missing, JSON.stringify(response));
    }
  });

  it("reports the first failing check, in declaration order", () => {
    const mismatch = evaluateChecks(
      { A: "a", B: "" },
      { "$.A": { equals: "a" }, "$.B": { nonEmpty: true } },
      ctx,
    );
    assert.equal(mismatch?.path, "$.B");
  });
});

describe("listContains / absent", () => {
  const response = {
    Policies: [{ Id: "p-1" }, { Id: "p-2" }],
    QueueUrls: ["http://q/1", "http://q/9"],
    Tags: [{ Key: "compat", Value: "scenario" }],
  };

  it("matches an item on every where entry", () => {
    assert.equal(
      evaluateListContains(response, "$.Policies", { "$.Id": "p-2" }, ctx),
      null,
    );
    assert.notEqual(
      evaluateListContains(response, "$.Policies", { "$.Id": "p-3" }, ctx),
      null,
    );
    assert.equal(
      evaluateListContains(
        response,
        "$.Tags",
        { "$.Key": "compat", "$.Value": "scenario" },
        ctx,
      ),
      null,
    );
    assert.notEqual(
      evaluateListContains(
        response,
        "$.Tags",
        { "$.Key": "compat", "$.Value": "other" },
        ctx,
      ),
      null,
    );
  });

  it("`$` is the item itself, for a list of strings", () => {
    assert.equal(
      evaluateListContains(response, "$.QueueUrls", { $: { $ref: "queue.url" } }, ctx),
      null,
    );
  });

  it("a missing list fails listContains and satisfies absent", () => {
    assert.notEqual(evaluateListContains({}, "$.QueueUrls", { $: "x" }, ctx), null);
    assert.equal(evaluateListAbsent({}, "$.QueueUrls", { $: "x" }, ctx), null);
  });

  it("absent fails when an item matches, and names the item", () => {
    assert.equal(evaluateListAbsent(response, "$.Tags", { "$.Key": "other" }, ctx), null);
    const mismatch = evaluateListAbsent(response, "$.Tags", { "$.Key": "compat" }, ctx);
    assert.ok(mismatch?.actual.includes("compat"));
  });
});

describe("error matching", () => {
  const spec = {
    shape: "QueueDoesNotExist",
    code: "AWS.SimpleQueueService.NonExistentQueue",
  };

  it("accepts the shape name or the wire code, from any surface", () => {
    assert.ok(errorMatches({ name: "QueueDoesNotExist" }, spec));
    assert.ok(errorMatches({ name: "AWS.SimpleQueueService.NonExistentQueue" }, spec));
    assert.ok(errorMatches({ __type: "com.amazonaws.sqs#QueueDoesNotExist" }, spec));
    assert.ok(errorMatches({ Code: "AWS.SimpleQueueService.NonExistentQueue" }, spec));
    assert.ok(
      errorMatches(
        {
          name: "ServiceException",
          $response: {
            headers: { "x-amzn-query-error": "AWS.SimpleQueueService.NonExistentQueue;Sender" },
          },
        },
        spec,
      ),
    );
  });

  it("reads the code out of a nested Error node, not only the top level", () => {
    // The AWS Query envelope, deserialized: the code is only ever inside the
    // error node, and reading the top level alone found none at all — which
    // failed every errorCode clause against a Query service here while the
    // backends that did read it passed (#1896).
    assert.ok(
      errorMatches(
        { Error: { Type: "Sender", Code: "NoSuchEntity", Message: "…" } },
        { shape: "NoSuchEntity", code: "NoSuchEntity" },
      ),
    );
    // The fault beside it is not a code, and a near miss is still a miss.
    assert.ok(
      !errorMatches({ Error: { Type: "Sender", Code: "NoSuchEntity" } }, { shape: "Sender", code: "Sender" }),
    );
    assert.ok(
      !errorMatches({ Error: { Code: "NoSuchEntity" } }, { shape: "SuchEntity", code: "SuchEntity" }),
    );
    // A nested Error that is not an object states nothing.
    assert.deepEqual(errorCodes({ Error: "NoSuchEntity" }), []);
  });

  it("rejects another error and a non-error", () => {
    assert.ok(!errorMatches({ name: "AccessDenied" }, spec));
    assert.ok(!errorMatches(undefined, spec));
    assert.deepEqual(errorCodes("not an object"), []);
  });

  it("names both spellings only when they differ", () => {
    assert.equal(
      acceptedCodes(spec),
      "QueueDoesNotExist or AWS.SimpleQueueService.NonExistentQueue",
    );
    assert.equal(
      acceptedCodes({ shape: "PolicyNotFoundException", code: "PolicyNotFoundException" }),
      "PolicyNotFoundException",
    );
  });
});
