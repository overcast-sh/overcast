/**
 * assertions.ts — the predicates behind the closed assertion set.
 *
 * Everything here is pure: it takes a response (or an error) that somebody
 * else fetched and says whether a clause holds, returning `null` when it does
 * and the expected/actual pair when it does not. Making the calls, retrying
 * them and applying exports is executor.ts's job — keeping the two apart is
 * what lets every clause share one failure message and lets the unit tests
 * cover the semantics without a client at all.
 *
 * The semantics are compat/model/README.md § Assertions, and the three that
 * are easy to get subtly wrong are spelled out at their implementations:
 * `isList` (absence passes), `nonEmpty` (numbers and booleans never empty)
 * and the error forms (code *or* type name, against shape *or* code).
 */

import {
  describeClipped,
  evaluateValue,
  isEmpty,
  jsonEquals,
  resolvePath,
} from "./expressions.ts";
import type { EvalContext } from "./expressions.ts";
import type { Check, Checks, ErrorSpec, Path, Where } from "./ir.ts";

/** Why a clause did not hold — fields 4 and 5 of the failure message. */
export interface Mismatch {
  /** The checks/where path, when the clause has one. */
  path?: string;
  expected: string;
  actual: string;
}

/** `actual`'s rendering — the shared clip helper, so a big page is elided. */
const render = describeClipped;

/**
 * Every check against one response, in declaration order. The first that
 * fails is the one reported.
 */
export function evaluateChecks(
  response: unknown,
  checks: Checks,
  ctx: EvalContext,
): Mismatch | null {
  for (const [path, check] of Object.entries(checks)) {
    const mismatch = evaluateCheck(response, path, check, ctx);
    if (mismatch !== null) return mismatch;
  }
  return null;
}

function evaluateCheck(
  response: unknown,
  path: Path,
  check: Check,
  ctx: EvalContext,
): Mismatch | null {
  const found = resolvePath(response, path);
  const actual = found.found ? render(found.value) : "<missing>";

  if ("missing" in check) {
    if (!found.found) return null;
    return { path, expected: "the path not to resolve", actual };
  }

  if ("isList" in check) {
    // Absence passes: a single-page List* legally returns an empty page, and
    // several AWS services (SQS's ListQueues among them) omit the member
    // entirely rather than serialize []. A present non-list still fails.
    if (!found.found) return null;
    if (Array.isArray(found.value)) return null;
    return { path, expected: "a list (or nothing)", actual };
  }

  if (!found.found) {
    return { path, expected: expectationOf(check, ctx), actual: "<missing>" };
  }

  if ("nonEmpty" in check) {
    // null, "", [] and {} are empty; numbers and booleans never are.
    if (!isEmpty(found.value)) return null;
    return { path, expected: "a non-empty value", actual };
  }

  if ("matches" in check) {
    // The pattern is compiled before the value is looked at, so what the
    // failure says does not depend on what came back: a pattern this engine
    // rejects reads "unsupported pattern" whether the path resolved to a
    // string or to a number. python and cli report it the same way, and
    // compiling second would have made one broken scenario file fail two
    // different ways across the three.
    let re: RegExp;
    try {
      re = new RegExp(check.matches);
    } catch (err) {
      // The model's patterns are RE2-compatible; a pattern legal there but not
      // in JS (a `(?P<name>…)` named group, say) must not throw out of the
      // evaluator — it has to fail the clause like any other mismatch, with
      // the same six-field message everything else gets.
      return {
        path,
        expected: `pattern ${check.matches}`,
        actual: `unsupported pattern: ${err instanceof Error ? err.message : String(err)}`,
      };
    }
    if (typeof found.value !== "string") {
      return { path, expected: `a string matching /${check.matches}/`, actual };
    }
    if (re.test(found.value)) return null;
    return { path, expected: `a string matching /${check.matches}/`, actual };
  }

  const expected = evaluateValue(check.equals, ctx);
  if (jsonEquals(found.value, expected)) return null;
  return { path, expected: render(expected), actual };
}

function expectationOf(check: Check, ctx: EvalContext): string {
  if ("nonEmpty" in check) return "a non-empty value";
  if ("matches" in check) return `a string matching /${check.matches}/`;
  // isList and missing are handled before this is reached — both hold on a
  // path that does not resolve, so neither has an expectation to render here.
  return "the path to resolve";
}

/**
 * `listContains`: the list at `itemsPath` is non-empty and contains an item
 * matching every `where` entry. A missing list fails it (it counts as empty).
 */
export function evaluateListContains(
  response: unknown,
  itemsPath: Path,
  where: Where,
  ctx: EvalContext,
): Mismatch | null {
  const items = resolveItems(response, itemsPath);
  if (items.problem !== null) return items.problem;
  if (items.list.some((item) => itemMatches(item, where, ctx))) return null;
  return {
    path: itemsPath,
    expected: `an item matching ${renderWhere(where, ctx)}`,
    actual: render(items.list),
  };
}

/**
 * `absent`, list form: no item of the list at `itemsPath` matches every
 * `where` entry. A missing list counts as empty, so it holds.
 */
export function evaluateListAbsent(
  response: unknown,
  itemsPath: Path,
  where: Where,
  ctx: EvalContext,
): Mismatch | null {
  const found = resolvePath(response, itemsPath);
  if (!found.found) return null;
  if (!Array.isArray(found.value)) {
    return { path: itemsPath, expected: "a list (or nothing)", actual: render(found.value) };
  }
  const match = found.value.find((item) => itemMatches(item, where, ctx));
  if (match === undefined) return null;
  return {
    path: itemsPath,
    expected: `no item matching ${renderWhere(where, ctx)}`,
    actual: render(match),
  };
}

function resolveItems(
  response: unknown,
  itemsPath: Path,
): { list: unknown[]; problem: Mismatch | null } {
  const found = resolvePath(response, itemsPath);
  if (!found.found) {
    return {
      list: [],
      problem: { path: itemsPath, expected: "a non-empty list", actual: "<missing>" },
    };
  }
  if (!Array.isArray(found.value)) {
    return {
      list: [],
      problem: { path: itemsPath, expected: "a list", actual: render(found.value) },
    };
  }
  return { list: found.value, problem: null };
}

/** An item matches when every `where` entry is equal, as JSON. `$` is the item. */
function itemMatches(item: unknown, where: Where, ctx: EvalContext): boolean {
  for (const [path, expectedValue] of Object.entries(where)) {
    const found = resolvePath(item, path);
    if (!found.found) return false;
    if (!jsonEquals(found.value, evaluateValue(expectedValue, ctx))) return false;
  }
  return true;
}

function renderWhere(where: Where, ctx: EvalContext): string {
  const parts = Object.entries(where).map(
    ([path, value]) => `${path}=${render(evaluateValue(value, ctx))}`,
  );
  return `{${parts.join(", ")}}`;
}

// ─── Errors ───────────────────────────────────────────────────────────────

/**
 * An error matches when its reported code *or* its type name equals the
 * clause's `shape` *or* its `code`.
 *
 * The two spellings exist because SDKs disagree about which they surface:
 * for SQS's not-found they are `QueueDoesNotExist` (the modeled shape) and
 * `AWS.SimpleQueueService.NonExistentQueue` (the awsQueryError code). The
 * JSON protocol carries whichever the service chose in `__type`, an XML one
 * carries it in the `Code` of its error node, and AWS also sends the legacy
 * one in the `x-amzn-query-error` header, so all of those surfaces are read.
 * Nothing here is conditioned on Overcast.
 */
export function errorMatches(err: unknown, spec: ErrorSpec): boolean {
  const accepted = new Set([spec.shape, spec.code]);
  return errorCodes(err).some((code) => accepted.has(code));
}

/** Every code an SDK error reports, in the spellings a clause may name. */
export function errorCodes(err: unknown): string[] {
  if (err === null || typeof err !== "object") return [];
  const e = err as Record<string, unknown>;
  const out: string[] = [];

  const add = (value: unknown): void => {
    if (typeof value !== "string" || value === "") return;
    out.push(value);
    // Smithy ids ("com.amazonaws.sqs#QueueDoesNotExist") and the header's
    // "<code>;<fault>" form both carry the bare code after a separator.
    const hash = value.lastIndexOf("#");
    if (hash >= 0) out.push(value.slice(hash + 1));
    const semi = value.indexOf(";");
    if (semi >= 0) out.push(value.slice(0, semi));
  };

  add(e["name"]);
  add(e["__type"]);
  add(e["Code"]);
  add(e["code"]);

  // The nested half of the Errors table's body-code row. An AWS Query service
  // states its code only inside the ErrorResponse envelope's error node, and
  // REST XML's bare <Error> body reads the same way wherever a parser keeps
  // the root element rather than dropping it — which is the position the
  // SDK's own loadQueryErrorCode and loadRestXmlErrorCode read. Reading only
  // the top level saw no code at all in a Query error, and every errorCode
  // clause against a Query service failed here while passing in the backends
  // that did read it (#1896).
  const nested = e["Error"];
  if (nested !== null && typeof nested === "object") {
    add((nested as Record<string, unknown>)["Code"]);
    add((nested as Record<string, unknown>)["code"]);
  }

  const response = e["$response"];
  if (response !== null && typeof response === "object") {
    const headers = (response as Record<string, unknown>)["headers"];
    if (headers !== null && typeof headers === "object") {
      add((headers as Record<string, unknown>)["x-amzn-query-error"]);
    }
  }
  return out;
}

/** "QueueDoesNotExist or AWS.SimpleQueueService.NonExistentQueue" */
export function acceptedCodes(spec: ErrorSpec): string {
  return spec.shape === spec.code ? spec.shape : `${spec.shape} or ${spec.code}`;
}
