/**
 * ir.ts — TypeScript types for the compat scenario IR.
 *
 * The normative description of these shapes is compat/model/README.md; the
 * machine-readable one is compat/model/scenario.schema.json. This file is
 * written from the README, not generated from the schema, and where the two
 * disagree that is a bug in one of them (README, § opening paragraph).
 *
 * Nothing here is Overcast-specific and nothing here is Node-specific: it is
 * the closed vocabulary every interpreter implements.
 */

/** Plain JSON, the only thing `$lit` can carry. */
export type JsonValue =
  | null
  | boolean
  | number
  | string
  | JsonValue[]
  | { [key: string]: JsonValue };

/**
 * A value expression, or structural JSON whose leaves are values.
 *
 * An object with exactly one `$`-prefixed key is an expression; any other
 * object is a structure or map. The distinction is made at evaluation time
 * (see expressions.ts `asExpression`), not by the type, because JSON gives us
 * no discriminator to narrow on before we look.
 */
export type Value = null | boolean | number | string | Value[] | ValueObject;

export interface ValueObject {
  [key: string]: Value;
}

/** The seven expression forms. `$`-keyed, exactly one key each. */
export type Expression =
  | { $lit: JsonValue }
  | { $ref: string }
  | { $name: string }
  | { $concat: Value[] }
  | { $index: [Value, number] }
  | { $base64: string | { $ref: string } }
  | { $now: { unit: string; offsetMillis?: number } };

/**
 * A response path: `$`, then any number of `.Member` or `[index]` segments.
 * Not JSONPath — no wildcards, filters, quoting or recursive descent.
 */
export type Path = string;

/** What an interpreter needs to construct a client (README § Naming). */
export interface ClientSpec {
  sdkId: string;
  endpointPrefix: string;
  signingName?: string;
  protocol: string;
  apiVersion: string;
  targetPrefix?: string;
}

/** One operation invocation, with its input and optional response exports. */
export interface Call {
  op: string;
  params: ValueObject;
  /** Context path → response path. Unresolvable is an error for the step. */
  export?: Record<string, Path>;
}

/** Exactly one check per path. */
export type Check =
  | { nonEmpty: true }
  | { isList: true }
  | { equals: Value }
  /**
   * A literal JSON object or array, never evaluated: no `$`-prefixed key at
   * any depth (README § Documents in a string).
   */
  | { equalsJSON: JsonValue }
  | { matches: string }
  | { missing: true };

/** Response path → check. */
export type Checks = Record<Path, Check>;

/** Item-relative path → expected value. `$` is the item itself. */
export type Where = Record<Path, Value>;

/**
 * The modeled shape name and the wire code. An interpreter accepts an error
 * whose reported code *or* type name equals *either* — the SDKs disagree
 * about which of the two they surface (README § Assertions, plan § 4.4).
 */
export interface ErrorSpec {
  shape: string;
  code: string;
}

export interface ResponseFieldAssertion {
  kind: "responseField";
  checks: Checks;
}

export interface ReadbackAssertion {
  kind: "readback";
  call: Call;
  checks: Checks;
}

export interface ListContainsAssertion {
  kind: "listContains";
  call?: Call;
  itemsPath: Path;
  where: Where;
}

/** `absent`, list form: no item of the list matches every `where` entry. */
export interface AbsentListAssertion {
  kind: "absent";
  call?: Call;
  itemsPath: Path;
  where: Where;
}

/** `absent`, error form: the call fails with `error`. */
export interface AbsentErrorAssertion {
  kind: "absent";
  call: Call;
  error: ErrorSpec;
}

export interface ErrorCodeAssertion {
  kind: "errorCode";
  error: ErrorSpec;
}

export interface EventuallyAssertion {
  kind: "eventually";
  maxAttempts: number;
  delayMs?: number;
  assert: RetryableAssertion;
}

/** The clauses `eventually` may wrap. */
export type RetryableAssertion =
  | ReadbackAssertion
  | ListContainsAssertion
  | AbsentListAssertion
  | AbsentErrorAssertion;

export type Assertion =
  | ResponseFieldAssertion
  | RetryableAssertion
  | ErrorCodeAssertion
  | EventuallyAssertion;

export interface ScenarioTest {
  name: string;
  op: string;
  call: Call;
  assert: Assertion[];
  depends?: string[];
}

export interface ScenarioGroup {
  name: string;
  kind: "lifecycle" | "probe";
  setup: Call[];
  tests: ScenarioTest[];
  teardown: Call[];
}

export interface Scenario {
  version: 1;
  service: string;
  client: ClientSpec;
  groups: ScenarioGroup[];
}
