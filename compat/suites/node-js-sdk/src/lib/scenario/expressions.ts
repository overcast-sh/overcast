/**
 * expressions.ts — value expressions, response paths and JSON comparison.
 *
 * The three primitives every assertion and every call parameter is built from
 * (compat/model/README.md § Values, § Paths, § Assertions):
 *
 *   evaluateValue()  `$lit` `$ref` `$name` `$concat` `$index` `$base64` → a JS value
 *   toDocument()     an SDK response → the IR's document (a blob as base64 text)
 *   resolvePath()    `$.Messages[0].ReceiptHandle` → found/not-found + value
 *   jsonEquals()     equality "as JSON", after the SDK's own mapping
 *
 * No conditionals, no arithmetic, no scripting: eight implementations have to
 * agree on every value.
 */

import type { Expression, Path, Value, ValueObject } from "./ir.ts";

/** The context a value is evaluated against. */
export interface EvalContext {
  /** OVERCAST_COMPAT_RUN_ID, or the suite's generated run id. */
  readonly runId: string;
  /** The scenario group name — the whole name, never shortened. */
  readonly group: string;
  /** Context path → exported value. Written by `export`, read by `$ref`. */
  readonly bag: Map<string, unknown>;
}

/**
 * An expression that could not be evaluated: an unresolvable `$ref`, a
 * `$concat` part that is not a string, an `$index` off the end of a list.
 *
 * Carried as its own error type so the executor can turn it into the step
 * failure the contract asks for (a setup step's unresolvable `$ref` skips the
 * whole group; a teardown step's skips that step alone).
 */
export class ExpressionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ExpressionError";
  }
}

/** True for a non-null, non-array object. */
function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Narrow a value object to an expression, or null if it is a plain structure.
 *
 * "An object with exactly one `$`-prefixed key is an expression" — so an
 * object with a `$`-key and anything else, or with an unknown `$`-key, is a
 * malformed scenario rather than a structure, and says so.
 */
export function asExpression(v: ValueObject): Expression | null {
  const keys = Object.keys(v);
  const dollar = keys.filter((k) => k.startsWith("$"));
  if (dollar.length === 0) return null;
  if (keys.length !== 1) {
    throw new ExpressionError(
      `value object mixes the expression key ${JSON.stringify(dollar[0])} ` +
        `with ${keys.length - 1} other key(s): ${JSON.stringify(keys)}`,
    );
  }
  const key = keys[0];
  switch (key) {
    case "$lit":
    case "$ref":
    case "$name":
    case "$concat":
    case "$index":
    case "$base64":
      return v as unknown as Expression;
    default:
      throw new ExpressionError(`unknown value expression ${JSON.stringify(key)}`);
  }
}

/**
 * Evaluate a value against the context.
 *
 * Structural JSON is walked leaf-first; an expression object is evaluated;
 * a scalar is itself. `$lit` is the escape hatch for an object whose keys
 * genuinely start with `$`, and its payload is never interpreted.
 */
export function evaluateValue(value: Value, ctx: EvalContext): unknown {
  if (Array.isArray(value)) return value.map((v) => evaluateValue(v, ctx));
  if (isRecord(value)) {
    const expr = asExpression(value as ValueObject);
    if (expr === null) {
      const out: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(value as ValueObject)) {
        out[k] = evaluateValue(v, ctx);
      }
      return out;
    }
    return evaluateExpression(expr, ctx);
  }
  return value;
}

/** Evaluate a call's `params` object, which is always a structure. */
export function evaluateParams(
  params: ValueObject,
  ctx: EvalContext,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(params)) out[k] = evaluateValue(v, ctx);
  return out;
}

function evaluateExpression(expr: Expression, ctx: EvalContext): unknown {
  if ("$lit" in expr) return expr.$lit;

  if ("$ref" in expr) {
    const path = expr.$ref;
    if (!ctx.bag.has(path)) {
      const known = [...ctx.bag.keys()].sort().join(", ") || "<empty>";
      throw new ExpressionError(
        `unresolvable $ref ${JSON.stringify(path)} — the context holds: ${known}`,
      );
    }
    return ctx.bag.get(path);
  }

  if ("$name" in expr) {
    // `{runId}-{group}-{suffix}`, with the group token the whole group name
    // and no shortening anywhere — that is what makes the name-hygiene rule
    // hold by construction (README § Values, plan § 4.4).
    return `${ctx.runId}-${ctx.group}-${expr.$name}`;
  }

  if ("$concat" in expr) {
    let out = "";
    for (const [i, part] of expr.$concat.entries()) {
      const v = evaluateValue(part, ctx);
      if (typeof v !== "string") {
        throw new ExpressionError(
          `$concat part ${i} evaluated to ${describe(v)}, want a string`,
        );
      }
      out += v;
    }
    return out;
  }

  if ("$base64" in expr) {
    // A blob: these bytes. The AWS SDK for JavaScript takes a Uint8Array for
    // a blob member and would serialize a string as its UTF-8 — which is why a
    // blob is never a plain string in the IR. The argument is base64 text: a
    // literal, or a $ref to a blob a previous call exported, which the context
    // bag holds in its document form (see toDocument).
    return decodeBase64(evaluateValue(expr.$base64 as Value, ctx));
  }

  const [listExpr, index] = expr.$index;
  const list = evaluateValue(listExpr, ctx);
  if (!Array.isArray(list)) {
    throw new ExpressionError(
      `$index expects a list, got ${describe(list)}`,
    );
  }
  if (index >= list.length) {
    throw new ExpressionError(
      `$index ${index} is past the end of a ${list.length}-element list`,
    );
  }
  return list[index];
}

// ─── Blobs ────────────────────────────────────────────────────────────────

const BASE64_RE = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

/**
 * Decode a blob's document form: standard base64 with padding, in its one
 * canonical spelling. Node's own decoder is lenient — it reads the URL-safe
 * alphabet, skips whitespace and tolerates missing padding — so the text is
 * held to the alphabet first and to a round trip after, which is what refuses
 * a second spelling of the same bytes. compat/model/testdata/blobs pins what
 * every backend accepts and refuses.
 */
export function decodeBase64(text: unknown): Uint8Array {
  if (typeof text !== "string") {
    throw new ExpressionError(`$base64 takes base64 text, got ${describe(text)}`);
  }
  if (!BASE64_RE.test(text)) {
    throw new ExpressionError(
      `$base64 ${JSON.stringify(text)} is not standard padded base64`,
    );
  }
  const bytes = Buffer.from(text, "base64");
  const canonical = bytes.toString("base64");
  if (canonical !== text) {
    throw new ExpressionError(
      `$base64 ${JSON.stringify(text)} is not the canonical spelling of its bytes, ` +
        `which is ${JSON.stringify(canonical)}`,
    );
  }
  return new Uint8Array(bytes.buffer, bytes.byteOffset, bytes.byteLength);
}

/**
 * Map an SDK response to the IR's document form, which differs from what the
 * AWS SDK for JavaScript returns in one respect: a blob is a Uint8Array there
 * and its standard base64 text here, as it is in every other backend (the AWS
 * CLI prints it that way, and the typed suites render their SDK's blob type
 * the same). That is what an `equals` against a `$base64` compares, and what
 * an export of a blob puts in the context bag for a later `$base64` around a
 * `$ref` to decode. Everything else — a Date, `$metadata` — is left as the SDK
 * gave it.
 */
export function toDocument(value: unknown): unknown {
  if (value instanceof Uint8Array) return Buffer.from(value).toString("base64");
  if (Array.isArray(value)) return value.map(toDocument);
  if (isRecord(value) && isPlainObject(value)) {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) out[k] = toDocument(v);
    return out;
  }
  return value;
}

// ─── Paths ────────────────────────────────────────────────────────────────

type Segment = { member: string } | { index: number };

const SEGMENT_RE = /\.([A-Za-z_][A-Za-z0-9_\-:/]*)|\[(0|[1-9][0-9]*)\]/y;

/** Parse `$.Attributes.QueueArn` / `$.Messages[0].Body` into segments. */
export function parsePath(path: Path): Segment[] {
  if (!path.startsWith("$")) {
    throw new ExpressionError(`path ${JSON.stringify(path)} does not start with "$"`);
  }
  const segments: Segment[] = [];
  SEGMENT_RE.lastIndex = 1;
  while (SEGMENT_RE.lastIndex < path.length) {
    const at = SEGMENT_RE.lastIndex;
    const m = SEGMENT_RE.exec(path);
    if (m === null || m.index !== at) {
      throw new ExpressionError(
        `path ${JSON.stringify(path)} is malformed at offset ${at}`,
      );
    }
    if (m[1] !== undefined) segments.push({ member: m[1] });
    else segments.push({ index: Number(m[2]) });
  }
  return segments;
}

export type Resolution =
  | { readonly found: true; readonly value: unknown }
  | { readonly found: false };

const NOT_FOUND: Resolution = { found: false };

/**
 * Resolve a path against a response.
 *
 * "Does not resolve" means any segment is absent: a member the object does
 * not carry, an index past the end of a list, or a segment applied to a value
 * of the wrong kind (a member of a list, an index of an object). A member
 * whose value is `undefined` counts as absent — that is how the AWS SDK for
 * JavaScript spells "the service omitted it" — while an explicit `null`
 * resolves, so `missing` and `nonEmpty` can tell the two apart.
 */
export function resolvePath(root: unknown, path: Path): Resolution {
  let current: unknown = root;
  for (const seg of parsePath(path)) {
    if ("member" in seg) {
      if (!isRecord(current)) return NOT_FOUND;
      if (!Object.prototype.hasOwnProperty.call(current, seg.member)) {
        return NOT_FOUND;
      }
      current = current[seg.member];
    } else {
      if (!Array.isArray(current)) return NOT_FOUND;
      if (seg.index >= current.length) return NOT_FOUND;
      current = current[seg.index];
    }
    if (current === undefined) return NOT_FOUND;
  }
  return { found: true, value: current };
}

// ─── Comparison ───────────────────────────────────────────────────────────

/**
 * Equality "as JSON", after the SDK's own mapping.
 *
 * Coercion rule: there is none. The AWS SDK for JavaScript already maps a
 * modeled integer to a `number`, a boolean to a `boolean`, a string to a
 * `string`, a map or structure to a plain object and a list to an array — so
 * the SDK-returned value and the evaluated expected value are compared in
 * the JSON type system directly, and a `number` is never equal to a `string`.
 * The generator only ever emits an `equals` literal of the member's modeled
 * kind (README § Assertions), so a cross-type comparison means the response
 * disagrees with the model, which is exactly what the check should catch.
 * SQS's queue attributes are modeled as a map of strings, which is why the
 * scenario compares `"30"` and not `30`.
 *
 * A blob compares as its document form, base64 text, whichever side it is on:
 * a `$base64` evaluates to a Uint8Array and a response blob is already text.
 * Timestamps are never compared — the generator does not emit them — so a
 * `Date` reaching here is never equal to anything.
 */
export function jsonEquals(actual: unknown, expected: unknown): boolean {
  if (actual instanceof Uint8Array) actual = toDocument(actual);
  if (expected instanceof Uint8Array) expected = toDocument(expected);
  if (actual === expected) return true;
  if (actual === null || expected === null) return false;
  if (Array.isArray(actual) || Array.isArray(expected)) {
    if (!Array.isArray(actual) || !Array.isArray(expected)) return false;
    if (actual.length !== expected.length) return false;
    return actual.every((v, i) => jsonEquals(v, expected[i]));
  }
  if (isRecord(actual) && isRecord(expected)) {
    // A Date or a Uint8Array is an object but not a JSON object; refuse it
    // rather than comparing key sets that happen to both be empty.
    if (!isPlainObject(actual) || !isPlainObject(expected)) return false;
    const ak = Object.keys(actual).filter((k) => actual[k] !== undefined);
    const ek = Object.keys(expected).filter((k) => expected[k] !== undefined);
    if (ak.length !== ek.length) return false;
    return ak.every(
      (k) =>
        Object.prototype.hasOwnProperty.call(expected, k) &&
        jsonEquals(actual[k], expected[k]),
    );
  }
  return false;
}

function isPlainObject(v: Record<string, unknown>): boolean {
  const proto = Object.getPrototypeOf(v) as unknown;
  return proto === Object.prototype || proto === null;
}

/**
 * `nonEmpty`: not `null`, `""`, `[]` or `{}`. Numbers and booleans are never
 * empty, so `0` and `false` pass (README § Assertions).
 */
export function isEmpty(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  if (typeof value === "string") return value === "";
  if (Array.isArray(value)) return value.length === 0;
  if (typeof value === "number" || typeof value === "boolean") return false;
  if (isRecord(value)) {
    return Object.values(value).every((v) => v === undefined);
  }
  return false;
}

/** A short human description of a value, for an error message. */
export function describe(v: unknown): string {
  if (v === undefined) return "<missing>";
  if (typeof v === "bigint") return `${v}n`;
  try {
    // A blob is shown as its document form, base64 text — which is also the
    // params JSON the CLI sends for the same call — rather than as the
    // index-keyed object JSON.stringify makes of a Uint8Array.
    const s = JSON.stringify(v, (_key, value: unknown) =>
      value instanceof Uint8Array ? toDocument(value) : value,
    );
    return s === undefined ? String(v) : s;
  } catch {
    return String(v);
  }
}

/**
 * Longest rendered value before it is elided. A ListQueues page, or a
 * scenario's params object, can both be big — one clip helper covers both,
 * so nothing in a failure message is allowed to run away unbounded.
 */
export const MAX_DESCRIBE = 2000;

/** `describe()`, elided past `MAX_DESCRIBE` characters. */
export function describeClipped(v: unknown): string {
  const s = describe(v);
  return s.length <= MAX_DESCRIBE ? s : `${s.slice(0, MAX_DESCRIBE)}… (elided)`;
}
