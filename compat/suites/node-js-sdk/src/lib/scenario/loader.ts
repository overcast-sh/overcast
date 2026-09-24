/**
 * loader.ts — read, validate and cache a scenario file.
 *
 * A generated registry group carries `scenario`, a repo-root-relative path
 * ("compat/model/scenarios/sqs.json"). It is resolved from the same base the
 * registry loader resolves registry.json from — two directories above
 * compat/suites/ — and never from the process CWD, because the suite is
 * started from the repo root, from its own directory and from inside a
 * container, and only one of those has a CWD worth trusting.
 *
 * Files are read lazily (nothing is opened until a test of that group is
 * built) and once (the parsed scenario is cached by resolved path).
 *
 * Validation is deliberate rather than schema-driven: this is the JSON
 * boundary, so every field is checked on the way in and everything past this
 * module is typed. compat/model/scenario.schema.json is the machine-readable
 * contract and `make compat-model-check` holds the generator to it; this is
 * the second line of defence, and its error messages name the JSON pointer of
 * whatever is wrong.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { checkEqualsJsonOperand } from "./expressions.ts";
import type {
  Assertion,
  Call,
  Check,
  Checks,
  ClientSpec,
  EventuallyAssertion,
  Path,
  Scenario,
  ScenarioGroup,
  ScenarioTest,
  Value,
  ValueObject,
  Where,
} from "./ir.ts";

/**
 * compat/suites/ — the directory registry.ts resolves "../../../registry.json"
 * to from src/lib/. This module sits one level deeper (src/lib/scenario/), so
 * the same directory is four levels up.
 */
const REGISTRY_DIR = new URL("../../../../", import.meta.url);

/** The repository root: compat/suites/ → compat/ → root. */
const REPO_ROOT = new URL("../../", REGISTRY_DIR);

const cache = new Map<string, Scenario>();

/**
 * Load and validate the scenario file at a repo-root-relative path.
 *
 * @param relPath the registry group's `scenario` field, "/"-separated.
 */
export function loadScenario(relPath: string): Scenario {
  const file = fileURLToPath(new URL(relPath, REPO_ROOT));
  const cached = cache.get(file);
  if (cached !== undefined) return cached;

  let raw: unknown;
  try {
    raw = JSON.parse(readFileSync(file, "utf8")) as unknown;
  } catch (err) {
    throw new Error(
      `${relPath}: cannot read scenario file (${file}): ${String(err)}`,
    );
  }
  const scenario = parseScenario(raw, relPath);
  cache.set(file, scenario);
  return scenario;
}

/** The group by name, or undefined — "not mine" for the backend. */
export function findGroup(
  scenario: Scenario,
  name: string,
): ScenarioGroup | undefined {
  return scenario.groups.find((g) => g.name === name);
}

/** The test by name within a group, or undefined. */
export function findTest(
  group: ScenarioGroup,
  name: string,
): ScenarioTest | undefined {
  return group.tests.find((t) => t.name === name);
}

// ─── Validation ───────────────────────────────────────────────────────────

class ScenarioParseError extends Error {
  constructor(file: string, pointer: string, problem: string) {
    super(`${file}${pointer}: ${problem}`);
    this.name = "ScenarioParseError";
  }
}

/** Validate raw JSON into a typed Scenario. */
export function parseScenario(raw: unknown, file: string): Scenario {
  const fail = (pointer: string, problem: string): never => {
    throw new ScenarioParseError(file, pointer, problem);
  };

  const obj = requireObject(raw, "", fail);
  if (obj["version"] !== 1) {
    fail("/version", `unsupported scenario version ${JSON.stringify(obj["version"])} (want 1)`);
  }
  const service = requireString(obj["service"], "/service", fail);
  const client = parseClient(obj["client"], "/client", fail);
  const groupsRaw = requireArray(obj["groups"], "/groups", fail);
  const groups = groupsRaw.map((g, i) => parseGroup(g, `/groups/${i}`, fail));

  return { version: 1, service, client, groups };
}

type Fail = (pointer: string, problem: string) => never;

function parseClient(raw: unknown, at: string, fail: Fail): ClientSpec {
  const o = requireObject(raw, at, fail);
  const spec: ClientSpec = {
    sdkId: requireString(o["sdkId"], `${at}/sdkId`, fail),
    endpointPrefix: requireString(o["endpointPrefix"], `${at}/endpointPrefix`, fail),
    protocol: requireString(o["protocol"], `${at}/protocol`, fail),
    apiVersion: requireString(o["apiVersion"], `${at}/apiVersion`, fail),
  };
  if (o["signingName"] !== undefined) {
    spec.signingName = requireString(o["signingName"], `${at}/signingName`, fail);
  }
  if (o["targetPrefix"] !== undefined) {
    spec.targetPrefix = requireString(o["targetPrefix"], `${at}/targetPrefix`, fail);
  }
  return spec;
}

function parseGroup(raw: unknown, at: string, fail: Fail): ScenarioGroup {
  const o = requireObject(raw, at, fail);
  const kind = requireString(o["kind"], `${at}/kind`, fail);
  if (kind !== "lifecycle" && kind !== "probe") {
    fail(`${at}/kind`, `unknown group kind ${JSON.stringify(kind)}`);
  }
  const tests = requireArray(o["tests"], `${at}/tests`, fail);
  if (tests.length === 0) fail(`${at}/tests`, "a group has at least one test");
  return {
    name: requireString(o["name"], `${at}/name`, fail),
    kind,
    setup: requireArray(o["setup"], `${at}/setup`, fail).map((c, i) =>
      parseCall(c, `${at}/setup/${i}`, fail),
    ),
    tests: tests.map((t, i) => parseTest(t, `${at}/tests/${i}`, fail)),
    teardown: requireArray(o["teardown"], `${at}/teardown`, fail).map((c, i) =>
      parseCall(c, `${at}/teardown/${i}`, fail),
    ),
  };
}

function parseTest(raw: unknown, at: string, fail: Fail): ScenarioTest {
  const o = requireObject(raw, at, fail);
  const asserts = requireArray(o["assert"], `${at}/assert`, fail);
  if (asserts.length === 0) {
    fail(`${at}/assert`, "a test has at least one assertion clause");
  }
  const test: ScenarioTest = {
    name: requireString(o["name"], `${at}/name`, fail),
    op: requireString(o["op"], `${at}/op`, fail),
    call: parseCall(o["call"], `${at}/call`, fail),
    assert: asserts.map((a, i) => parseAssertion(a, `${at}/assert/${i}`, fail)),
  };
  if (o["depends"] !== undefined) {
    test.depends = requireArray(o["depends"], `${at}/depends`, fail).map((d, i) =>
      requireString(d, `${at}/depends/${i}`, fail),
    );
  }
  return test;
}

function parseCall(raw: unknown, at: string, fail: Fail): Call {
  const o = requireObject(raw, at, fail);
  const call: Call = {
    op: requireString(o["op"], `${at}/op`, fail),
    params: parseValueObject(o["params"], `${at}/params`, fail),
  };
  if (o["export"] !== undefined) {
    const exports = requireObject(o["export"], `${at}/export`, fail);
    const out: Record<string, Path> = {};
    for (const [k, v] of Object.entries(exports)) {
      out[k] = requirePath(v, `${at}/export/${k}`, fail);
    }
    call.export = out;
  }
  return call;
}

function parseAssertion(raw: unknown, at: string, fail: Fail): Assertion {
  const o = requireObject(raw, at, fail);
  const kind = requireString(o["kind"], `${at}/kind`, fail);
  switch (kind) {
    case "responseField":
      return { kind, checks: parseChecks(o["checks"], `${at}/checks`, fail) };
    case "readback":
      return {
        kind,
        call: parseCall(o["call"], `${at}/call`, fail),
        checks: parseChecks(o["checks"], `${at}/checks`, fail),
      };
    case "errorCode":
      return { kind, error: parseErrorSpec(o["error"], `${at}/error`, fail) };
    case "listContains":
    case "absent":
      return parseListOrError(o, kind, at, fail);
    case "eventually": {
      const inner = parseAssertion(o["assert"], `${at}/assert`, fail);
      if (
        inner.kind !== "readback" &&
        inner.kind !== "listContains" &&
        inner.kind !== "absent"
      ) {
        fail(
          `${at}/assert/kind`,
          `eventually wraps readback, listContains or absent, not ${inner.kind}`,
        );
      }
      const clause: EventuallyAssertion = {
        kind,
        maxAttempts: requireInteger(o["maxAttempts"], `${at}/maxAttempts`, fail, 1),
        assert: inner,
      };
      if (o["delayMs"] !== undefined) {
        clause.delayMs = requireInteger(o["delayMs"], `${at}/delayMs`, fail, 0);
      }
      return clause;
    }
    default:
      return fail(`${at}/kind`, `unknown assertion kind ${JSON.stringify(kind)}`);
  }
}

/** `listContains`, and `absent` in either of its two forms. */
function parseListOrError(
  o: Record<string, unknown>,
  kind: "listContains" | "absent",
  at: string,
  fail: Fail,
): Assertion {
  if (kind === "absent" && o["error"] !== undefined) {
    const call = parseCall(o["call"], `${at}/call`, fail);
    if (call.export !== undefined) {
      fail(
        `${at}/call/export`,
        "an absent clause's error-form call is expected to fail, so it has " +
          "no response to export from",
      );
    }
    return {
      kind,
      call,
      error: parseErrorSpec(o["error"], `${at}/error`, fail),
    };
  }
  const itemsPath = requirePath(o["itemsPath"], `${at}/itemsPath`, fail);
  const where = parseWhere(o["where"], `${at}/where`, fail);
  const call =
    o["call"] === undefined ? undefined : parseCall(o["call"], `${at}/call`, fail);
  return kind === "listContains"
    ? { kind: "listContains", call, itemsPath, where }
    : { kind: "absent", call, itemsPath, where };
}

function parseChecks(raw: unknown, at: string, fail: Fail): Checks {
  const o = requireObject(raw, at, fail);
  const keys = Object.keys(o);
  if (keys.length === 0) fail(at, "checks needs at least one entry");
  const out: Checks = {};
  for (const [path, check] of Object.entries(o)) {
    requirePath(path, `${at}/${path}`, fail);
    out[path] = parseCheck(check, `${at}/${path}`, fail);
  }
  return out;
}

function parseCheck(raw: unknown, at: string, fail: Fail): Check {
  const o = requireObject(raw, at, fail);
  const keys = Object.keys(o);
  if (keys.length !== 1) {
    fail(at, `a check carries exactly one key, got ${JSON.stringify(keys)}`);
  }
  const key = keys[0];
  switch (key) {
    case "nonEmpty":
      if (o[key] !== true) fail(`${at}/${key}`, `${key} is always true`);
      return { nonEmpty: true };
    case "isList":
      if (o[key] !== true) fail(`${at}/${key}`, `${key} is always true`);
      return { isList: true };
    case "missing":
      if (o[key] !== true) fail(`${at}/${key}`, `${key} is always true`);
      return { missing: true };
    case "matches":
      return { matches: requireString(o["matches"], `${at}/matches`, fail) };
    case "equals":
      return { equals: parseValue(o["equals"], `${at}/equals`, fail) };
    case "equalsJSON":
      // Data, not a value: it is never walked for expressions, so an operand
      // that would need one is refused here rather than read either way.
      try {
        return { equalsJSON: checkEqualsJsonOperand(o["equalsJSON"]) };
      } catch (err) {
        return fail(`${at}/equalsJSON`, err instanceof Error ? err.message : String(err));
      }
    default:
      return fail(at, `unknown check ${JSON.stringify(key)}`);
  }
}

function parseWhere(raw: unknown, at: string, fail: Fail): Where {
  const o = requireObject(raw, at, fail);
  if (Object.keys(o).length === 0) fail(at, "where needs at least one entry");
  const out: Where = {};
  for (const [path, value] of Object.entries(o)) {
    requirePath(path, `${at}/${path}`, fail);
    out[path] = parseValue(value, `${at}/${path}`, fail);
  }
  return out;
}

function parseErrorSpec(
  raw: unknown,
  at: string,
  fail: Fail,
): { shape: string; code: string } {
  const o = requireObject(raw, at, fail);
  return {
    shape: requireString(o["shape"], `${at}/shape`, fail),
    code: requireString(o["code"], `${at}/code`, fail),
  };
}

/**
 * A value is JSON whose object leaves may be expressions. The expression
 * forms are narrowed at evaluation time (expressions.ts `asExpression`), so
 * validation here is the JSON shape only: no functions, no undefined.
 */
function parseValue(raw: unknown, at: string, fail: Fail): Value {
  if (raw === null) return null;
  switch (typeof raw) {
    case "string":
    case "number":
    case "boolean":
      return raw;
    case "object":
      break;
    default:
      return fail(at, `a value cannot be a ${typeof raw}`);
  }
  if (Array.isArray(raw)) return raw.map((v, i) => parseValue(v, `${at}/${i}`, fail));
  return parseValueObject(raw, at, fail);
}

function parseValueObject(raw: unknown, at: string, fail: Fail): ValueObject {
  const o = requireObject(raw, at, fail);
  const out: ValueObject = {};
  for (const [k, v] of Object.entries(o)) {
    // `$lit` takes its payload verbatim and never interprets it, so it is the
    // one place arbitrary JSON passes through unwalked.
    out[k] = k === "$lit" ? (v as Value) : parseValue(v, `${at}/${k}`, fail);
  }
  return out;
}

// ─── Primitives ───────────────────────────────────────────────────────────

function requireObject(
  raw: unknown,
  at: string,
  fail: Fail,
): Record<string, unknown> {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    fail(at || "/", `expected an object, got ${kindOf(raw)}`);
  }
  return raw as Record<string, unknown>;
}

function requireArray(raw: unknown, at: string, fail: Fail): unknown[] {
  if (!Array.isArray(raw)) fail(at, `expected an array, got ${kindOf(raw)}`);
  return raw;
}

function requireString(raw: unknown, at: string, fail: Fail): string {
  if (typeof raw !== "string") fail(at, `expected a string, got ${kindOf(raw)}`);
  return raw;
}

function requireInteger(
  raw: unknown,
  at: string,
  fail: Fail,
  min: number,
): number {
  if (typeof raw !== "number" || !Number.isInteger(raw) || raw < min) {
    fail(at, `expected an integer >= ${min}, got ${kindOf(raw)}`);
  }
  return raw;
}

const PATH_RE = /^\$(\.[A-Za-z_][A-Za-z0-9_\-:/]*|\[(0|[1-9][0-9]*)\])*$/;

function requirePath(raw: unknown, at: string, fail: Fail): Path {
  const s = requireString(raw, at, fail);
  if (!PATH_RE.test(s)) fail(at, `${JSON.stringify(s)} is not a response path`);
  return s;
}

function kindOf(raw: unknown): string {
  if (raw === null) return "null";
  if (Array.isArray(raw)) return "an array";
  if (raw === undefined) return "nothing";
  return `a ${typeof raw}`;
}
