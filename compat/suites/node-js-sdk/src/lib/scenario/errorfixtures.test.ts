/**
 * The shared error-matching conformance fixtures,
 * compat/model/testdata/errors.
 *
 * Three interpreters read the same documents and must agree about which
 * clauses they satisfy. Each suite writes this test once, against its own
 * matcher, so a rule only one backend implements fails somewhere rather than
 * being discovered when a generated group disagrees with itself across suites
 * (compat/model/README.md § Errors).
 *
 * A fixture whose surfaces this suite cannot see is skipped by name and with a
 * reason: a silently ignored fixture would look exactly like a passing one.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { errorCodes, errorMatches } from "./assertions.ts";
import type { ErrorSpec } from "./ir.ts";

/**
 * The fixture directory, resolved from this module's own location the way
 * loader.ts resolves a scenario path — never from the working directory,
 * which differs between `npm test` and `cmd/compat`. This file sits at
 * compat/suites/node-js-sdk/src/lib/scenario/, five directories below
 * compat/.
 */
const FIXTURE_DIR_URL = new URL(
  "../../../../../model/testdata/errors/",
  import.meta.url,
);
const FIXTURE_DIR = fileURLToPath(FIXTURE_DIR_URL);

/** The whole carrier vocabulary. A fixture naming anything else is a typo. */
const KNOWN_CARRIERS = new Set([
  "exceptionName",
  "bodyType",
  "bodyCode",
  "queryErrorHeader",
  "cliBanner",
]);

/**
 * What the AWS SDK for JavaScript puts in front of this suite: the error class
 * it minted, the deserialized body members, and `$response.headers`. The AWS
 * CLI's stderr banner belongs to another suite.
 */
const OBSERVED_CARRIERS = new Set([
  "exceptionName",
  "bodyType",
  "bodyCode",
  "queryErrorHeader",
]);

const WHAT_THIS_SUITE_SEES =
  "the SDK hands the interpreter an error object and $response.headers, " +
  "never a process's stderr";

/**
 * The strict reader. The cli suite decodes a fixture with
 * DisallowUnknownFields, so a key none of the three recognises has to be an
 * error here too: a field added to a fixture and silently ignored by two of
 * the backends is the drift these documents exist to prevent.
 */
const FIXTURE_KEYS = ["id", "title", "why", "carriers", "wire", "expect"];
const WIRE_KEYS = ["status", "exceptionName", "headers", "body", "stderr"];
const CASE_KEYS = ["name", "error", "matches", "via"];
const ERROR_KEYS = ["shape", "code"];

function strict(value: unknown, allowed: string[], where: string): void {
  assert.ok(
    value !== null && typeof value === "object" && !Array.isArray(value),
    `${where}: expected an object`,
  );
  const unknown = Object.keys(value as object)
    .filter((key) => !allowed.includes(key))
    .sort();
  assert.deepEqual(
    unknown,
    [],
    `${where}: unknown key(s) ${JSON.stringify(unknown)}; the fixture format ` +
      "is fixed by compat/model/README.md § Errors",
  );
}

interface FixtureWire {
  status?: number;
  exceptionName?: string;
  headers?: Record<string, string>;
  /**
   * A JSON object for a JSON wire, and a string — the raw XML bytes — for one
   * that is not (compat/model/README.md § Errors).
   */
  body?: Record<string, unknown> | string;
  stderr?: string;
}

interface FixtureCase {
  name: string;
  error: ErrorSpec;
  matches: boolean;
  via?: string;
}

interface Fixture {
  id: string;
  title: string;
  why: string;
  carriers: string[];
  wire: FixtureWire;
  expect: FixtureCase[];
}

/**
 * Every fixture, in file-name order. Called from inside a test rather than at
 * module scope: a missing directory here would otherwise throw during import
 * and take the whole file's tests with it, when what should fail is the one
 * assertion that says the conformance set is present.
 */
function loadFixtures(): Fixture[] {
  return readdirSync(FIXTURE_DIR)
    .filter((name) => name.endsWith(".json"))
    .sort()
    .map((name) => {
      const fixture = JSON.parse(
        readFileSync(new URL(name, FIXTURE_DIR_URL), "utf8"),
      ) as Fixture;
      strict(fixture, FIXTURE_KEYS, name);
      strict(fixture.wire, WIRE_KEYS, `${name}: wire`);
      for (const testCase of fixture.expect) {
        const where = `${name}: expect[${JSON.stringify(testCase.name)}]`;
        strict(testCase, CASE_KEYS, where);
        strict(testCase.error, ERROR_KEYS, `${where}.error`);
      }
      return fixture;
    });
}

/**
 * The fixture as this suite would have observed it: an Error carrying the
 * class name the SDK minted, the deserialized body members it lifts onto the
 * error, and the raw response the SDK attaches as `$response`.
 *
 * An XML wire is deserialized first, by the rule `@smithy/core`'s
 * `parseXmlBody` follows: the root element is dropped and its contents are the
 * document. So the AWS Query envelope
 * `<ErrorResponse><Error><Code>…</Error></ErrorResponse>` deserializes to a
 * nested `Error.Code`, and REST XML's bare `<Error><Code>…</Error>` to a
 * top-level `Code` — the two positions the Errors table's body-code row names,
 * from one and the same rule rather than from two.
 */
function asSdkError(wire: FixtureWire): Error {
  const body = typeof wire.body === "string"
    ? parseXmlDocument(wire.body)
    : (wire.body ?? {});
  const err = new Error(String(body["message"] ?? body["Message"] ?? "")) as Error &
    Record<string, unknown>;
  if (wire.exceptionName !== undefined) err.name = wire.exceptionName;
  for (const key of ["__type", "Code", "code", "Error"]) {
    if (body[key] !== undefined) err[key] = body[key];
  }
  err["$response"] = {
    statusCode: wire.status ?? 400,
    headers: { ...(wire.headers ?? {}) },
  };
  return err;
}

/**
 * The element-only subset of XML that AWS error bodies are written in, as the
 * document `parseXmlBody` would have produced: elements become object keys,
 * a leaf is its text, and the root element is dropped.
 *
 * Deliberately small. Attributes, namespaces, CDATA, repeated siblings and
 * mixed content are all things a real response body has and an error body does
 * not, and every one of them belongs to the SDK's parser rather than to a test
 * that renders one wire. A fixture that needed any of them would be a fixture
 * this suite should not be answering by hand.
 */
function parseXmlDocument(source: string): Record<string, unknown> {
  const token =
    /<\?[\s\S]*?\?>|<!--[\s\S]*?-->|<\/([A-Za-z_][\w.:-]*)\s*>|<([A-Za-z_][\w.:-]*)[^>]*?(\/?)>/g;
  const stack: { name: string; children: Record<string, unknown>; text: string }[] = [
    { name: "", children: {}, text: "" },
  ];
  let consumed = 0;
  for (let match = token.exec(source); match !== null; match = token.exec(source)) {
    const top = stack[stack.length - 1]!;
    top.text += source.slice(consumed, match.index);
    consumed = token.lastIndex;
    if (match[0].startsWith("<?") || match[0].startsWith("<!--")) continue;
    if (match[1] !== undefined) {
      const node = stack.pop()!;
      const parent = stack[stack.length - 1]!;
      parent.children[node.name] =
        Object.keys(node.children).length > 0 ? node.children : decodeEntities(node.text.trim());
    } else if (match[3] === "/") {
      top.children[match[2]!] = "";
    } else {
      stack.push({ name: match[2]!, children: {}, text: "" });
    }
  }
  const root = stack[0]!.children;
  const rootName = Object.keys(root)[0];
  const contents = rootName === undefined ? undefined : root[rootName];
  return contents !== null && typeof contents === "object"
    ? (contents as Record<string, unknown>)
    : {};
}

const ENTITIES: Record<string, string> = {
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
  "&quot;": '"',
  "&apos;": "'",
};

function decodeEntities(text: string): string {
  return text.replace(/&(?:amp|lt|gt|quot|apos);/g, (entity) => ENTITIES[entity] ?? entity);
}

/**
 * Why this suite must skip an expectation, or undefined when it must run it.
 * A fixture with no carriers states no code anywhere: every suite runs it,
 * because there is nothing to miss and every expectation on it is negative.
 */
function skipReason(fixture: Fixture, testCase: FixtureCase): string | undefined {
  if (
    fixture.carriers.length > 0 &&
    !fixture.carriers.some((c) => OBSERVED_CARRIERS.has(c))
  ) {
    return `this suite reads none of the fixture's surfaces (${fixture.carriers.join(", ")}): ${WHAT_THIS_SUITE_SEES}`;
  }
  if (
    testCase.matches &&
    (testCase.via === undefined || !OBSERVED_CARRIERS.has(testCase.via))
  ) {
    return `this expectation matches through ${JSON.stringify(testCase.via)}, which this suite does not observe: ${WHAT_THIS_SUITE_SEES}`;
  }
  return undefined;
}

describe("the shared error fixtures", () => {
  it("has fixtures to run", () => {
    assert.ok(
      loadFixtures().length > 0,
      `no fixtures in ${FIXTURE_DIR}: the shared conformance set may not be ` +
        "skipped by deleting it",
    );
  });

  it("names only carriers the vocabulary declares", () => {
    for (const fixture of loadFixtures()) {
      for (const carrier of fixture.carriers) {
        assert.ok(
          KNOWN_CARRIERS.has(carrier),
          `${fixture.id}: unknown carrier ${JSON.stringify(carrier)}; the ` +
            "vocabulary is fixed by compat/model/README.md § Errors",
        );
      }
    }
  });

  it("agrees with every fixture", async (t) => {
    let checked = 0;
    for (const fixture of loadFixtures()) {
      for (const testCase of fixture.expect) {
        const skip = skipReason(fixture, testCase);
        if (skip === undefined) checked++;
        await t.test(`${fixture.id}: ${testCase.name}`, { skip }, () => {
          const observed = asSdkError(fixture.wire);
          assert.equal(
            errorMatches(observed, testCase.error),
            testCase.matches,
            `${fixture.id}: the error reports ${JSON.stringify(errorCodes(observed))}, ` +
              `the clause names ${JSON.stringify(testCase.error)}`,
          );
        });
      }
    }
    assert.ok(
      checked > 0,
      "every fixture was skipped: this suite is asserting nothing about " +
        "error matching",
    );
  });
});
