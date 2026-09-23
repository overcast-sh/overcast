/**
 * executor.ts — run a scenario group's setup, tests and teardown.
 *
 * The order compat/model/README.md § The scenario file fixes:
 *
 *   1. an empty context;
 *   2. every setup call — a failure reports every test as `skip` with
 *      `setup failed: <message>`, where `<message>` is the failing step's own
 *      six-field failure message, and teardown still runs;
 *   3. every test in order (the registry's `depends` gives the loader the
 *      order and the usual dependency skip);
 *   4. every teardown call, each individually wrapped: an error or an
 *      unresolvable `$ref` skips that call and continues with the next.
 *
 * Steps 2 and 4 are wired to harness.ts's own `setup`/`teardown` hooks, which
 * already implement the skip-the-group and always-run halves of the rule, so
 * this module supplies the calls and nothing else.
 */

import {
  ExpressionError,
  evaluateParams,
  resolvePath,
  toDocument,
} from "./expressions.ts";
import type { EvalContext } from "./expressions.ts";
import {
  acceptedCodes,
  errorMatches,
  evaluateChecks,
  evaluateListAbsent,
  evaluateListContains,
} from "./assertions.ts";
import type { Mismatch } from "./assertions.ts";
import { describeError, failureMessage, scenarioFailure } from "./failure.ts";
import type { FailureSite } from "./failure.ts";
import type { Assertion, Call, ScenarioTest } from "./ir.ts";
import type { Sender } from "./client.ts";

/** Everything a step needs that is not in the scenario file. */
export interface ExecEnv {
  send: Sender;
  /** runId, group name and the context bag. */
  ctx: EvalContext;
  /** Repo-relative scenario path, for field 6 of a failure message. */
  scenarioFile: string;
  /** Debug output; harness's ctx.log (stderr, never stdout). */
  log: (msg: string) => void;
  /** Injected so tests do not wait in real time. */
  sleep: (ms: number) => Promise<void>;
}

/** The default sleep: a real timer. */
export function realSleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

interface CallResult {
  /** The evaluated params, exactly as sent — field 3 of a failure message. */
  params: Record<string, unknown>;
  response: unknown;
}

/** The primary call's outcome, shared by every clause of the test. */
interface Primary {
  op: string;
  params: Record<string, unknown>;
  response: unknown;
  /** Set when the call threw — only legal for a test with an errorCode clause. */
  error?: unknown;
}

// ─── Setup and teardown ───────────────────────────────────────────────────

/**
 * Run every setup call in order.
 *
 * Throws the failure message as a bare string rather than an Error so that
 * harness.ts's `setup failed: ${String(err)}` reads exactly as the contract
 * spells it, instead of gaining an "Error: " prefix. Same reasoning, and same
 * shape, as registry.ts's scenarioBackendMissing().
 */
export async function runSetup(env: ExecEnv, calls: Call[]): Promise<void> {
  for (const [i, call] of calls.entries()) {
    const site: FailureSite = {
      group: env.ctx.group,
      test: "<setup>",
      scenarioFile: env.scenarioFile,
      step: `setup[${i}]`,
    };
    try {
      const result = await performCall(env, site, call);
      applyExports(env, site, call, result);
    } catch (err) {
      // eslint-disable-next-line @typescript-eslint/only-throw-error
      throw err instanceof Error ? err.message : String(err);
    }
  }
}

/**
 * Run every teardown call in order, each wrapped on its own: an error or an
 * unresolvable `$ref` skips that call and the rest still run.
 */
export async function runTeardown(env: ExecEnv, calls: Call[]): Promise<void> {
  for (const [i, call] of calls.entries()) {
    const site: FailureSite = {
      group: env.ctx.group,
      test: "<teardown>",
      scenarioFile: env.scenarioFile,
      step: `teardown[${i}]`,
    };
    try {
      const result = await performCall(env, site, call);
      applyExports(env, site, call, result);
    } catch (err) {
      env.log(
        `[${env.ctx.group}] teardown ${call.op} skipped: ${describeError(err)}`,
      );
    }
  }
}

// ─── A test ───────────────────────────────────────────────────────────────

/** Run one scenario test: the primary call, then every clause in order. */
export async function runScenarioTest(
  env: ExecEnv,
  test: ScenarioTest,
): Promise<void> {
  const site: FailureSite = {
    group: env.ctx.group,
    test: test.name,
    scenarioFile: env.scenarioFile,
    step: "call",
  };

  // A test with an errorCode clause *expects* the primary call to fail, and
  // the generator refuses such a test an export or a clause that reads the
  // primary response, so nothing downstream needs the response.
  const expectsError = test.assert.some((a) => a.kind === "errorCode");

  const primary: Primary = {
    op: test.call.op,
    params: {},
    response: undefined,
  };

  try {
    const result = await performCall(env, site, test.call);
    primary.params = result.params;
    primary.response = result.response;
    if (!expectsError) applyExports(env, site, test.call, result);
  } catch (err) {
    if (!expectsError || !isCallError(err)) throw err;
    const detail = callErrorDetail(err);
    primary.params = detail.params;
    primary.error = detail.cause;
  }

  for (const [i, clause] of test.assert.entries()) {
    await runAssertion(env, { ...site, step: `assert[${i}]` }, clause, primary);
  }
}

async function runAssertion(
  env: ExecEnv,
  site: FailureSite,
  clause: Assertion,
  primary: Primary,
): Promise<void> {
  switch (clause.kind) {
    case "responseField": {
      if (primary.error !== undefined) {
        throw fail(site, primary.op, primary.params, "responseField", {
          expected: "the call to succeed",
          actual: describeError(primary.error),
        });
      }
      const mismatch = guard(site, primary.op, primary.params, "responseField", () =>
        evaluateChecks(primary.response, clause.checks, env.ctx),
      );
      if (mismatch !== null) {
        throw fail(site, primary.op, primary.params, "responseField", mismatch);
      }
      return;
    }

    case "readback": {
      const result = await performCall(env, site, clause.call);
      const mismatch = guard(site, clause.call.op, result.params, "readback", () =>
        evaluateChecks(result.response, clause.checks, env.ctx),
      );
      if (mismatch !== null) {
        throw fail(site, clause.call.op, result.params, "readback", mismatch);
      }
      // "the call's exports are applied" — and only now, so a clause that
      // failed never overwrites a context value with a stale reading, and an
      // `eventually` applies them on the attempt that passes and only then.
      applyExports(env, site, clause.call, result);
      return;
    }

    case "listContains":
    case "absent": {
      // `absent` has two forms; only the error form carries `error`.
      if ("error" in clause) {
        await runAbsentError(env, site, clause.call, clause.error);
        return;
      }
      const source = clause.call
        ? await performCall(env, site, clause.call)
        : { params: primary.params, response: primary.response };
      const op = clause.call ? clause.call.op : primary.op;
      const mismatch = guard(site, op, source.params, clause.kind, () =>
        clause.kind === "listContains"
          ? evaluateListContains(source.response, clause.itemsPath, clause.where, env.ctx)
          : evaluateListAbsent(source.response, clause.itemsPath, clause.where, env.ctx),
      );
      if (mismatch !== null) {
        throw fail(site, op, source.params, clause.kind, mismatch);
      }
      if (clause.call) applyExports(env, site, clause.call, source);
      return;
    }

    case "errorCode": {
      if (primary.error === undefined) {
        throw fail(site, primary.op, primary.params, "errorCode", {
          expected: `an error (${acceptedCodes(clause.error)})`,
          actual: "<no error>",
        });
      }
      if (errorMatches(primary.error, clause.error)) return;
      throw fail(
        site,
        primary.op,
        primary.params,
        "errorCode",
        {
          expected: `an error (${acceptedCodes(clause.error)})`,
          actual: describeError(primary.error),
        },
        primary.error,
      );
    }

    case "eventually": {
      const delayMs = clause.delayMs ?? 0;
      const inner: FailureSite = { ...site, step: `${site.step}.assert` };
      let last: unknown;
      for (let attempt = 1; attempt <= clause.maxAttempts; attempt++) {
        try {
          await runAssertion(env, inner, clause.assert, primary);
          return;
        } catch (err) {
          last = err;
          if (attempt < clause.maxAttempts && delayMs > 0) await env.sleep(delayMs);
        }
      }
      // The last failure's message is the reported one; say how hard we tried.
      throw prefixAttempts(last, clause.maxAttempts, delayMs);
    }
  }
}

/** `absent`, error form: the call must fail with the named error. */
async function runAbsentError(
  env: ExecEnv,
  site: FailureSite,
  call: Call,
  spec: { shape: string; code: string },
): Promise<void> {
  let params: Record<string, unknown>;
  try {
    params = evaluateParams(call.params, env.ctx);
  } catch (err) {
    throw expressionFailure(site, call.op, err);
  }
  try {
    await env.send(call.op, params);
  } catch (err) {
    if (errorMatches(err, spec)) return;
    throw fail(
      site,
      call.op,
      params,
      "absent",
      {
        expected: `an error (${acceptedCodes(spec)})`,
        actual: describeError(err),
      },
      err,
    );
  }
  throw fail(site, call.op, params, "absent", {
    expected: `an error (${acceptedCodes(spec)})`,
    actual: "<no error>",
  });
}

// ─── Calls, exports and failures ──────────────────────────────────────────

const CALL_ERROR = Symbol.for("overcast.compat.scenario.callError");

/**
 * Evaluate a call's params and send it. An SDK error becomes a failure whose
 * message carries the six fields and whose cause is the original error, so
 * the harness's unimplemented detection still sees a 501.
 */
async function performCall(
  env: ExecEnv,
  site: FailureSite,
  call: Call,
): Promise<CallResult> {
  let params: Record<string, unknown>;
  try {
    params = evaluateParams(call.params, env.ctx);
  } catch (err) {
    throw expressionFailure(site, call.op, err);
  }
  try {
    // The response becomes the IR's document here, once, so a blob reads as
    // its base64 text in every clause and every export (toDocument).
    return { params, response: toDocument(await env.send(call.op, params)) };
  } catch (err) {
    const failure = fail(
      site,
      call.op,
      params,
      "call",
      { expected: "the call to succeed", actual: describeError(err) },
      err,
    );
    // Marked so runScenarioTest can tell "the primary call raised" (which an
    // errorCode clause expects) from every other kind of failure.
    Object.defineProperty(failure, CALL_ERROR, {
      value: { params, cause: err },
      enumerable: false,
    });
    throw failure;
  }
}

function isCallError(err: unknown): boolean {
  return err instanceof Error && CALL_ERROR in err;
}

interface CallErrorDetail {
  params: Record<string, unknown>;
  cause: unknown;
}

function callErrorDetail(err: unknown): CallErrorDetail {
  return (err as unknown as Record<symbol, CallErrorDetail>)[CALL_ERROR];
}

/**
 * Write a call's exports into the context bag. A path that does not resolve
 * is a failure for the step that carries it, naming the path.
 */
function applyExports(
  env: ExecEnv,
  site: FailureSite,
  call: Call,
  result: CallResult,
): void {
  if (!call.export) return;
  for (const [contextPath, responsePath] of Object.entries(call.export)) {
    const found = resolvePath(result.response, responsePath);
    if (!found.found) {
      throw fail(site, call.op, result.params, "export", {
        path: responsePath,
        expected: `the path to resolve, for the context value ${contextPath}`,
        actual: "<missing>",
      });
    }
    env.ctx.bag.set(contextPath, found.value);
  }
}

/**
 * Run a predicate that may throw an ExpressionError (an `equals` or a `where`
 * whose expected value is a `$ref`) and turn that into a step failure.
 */
function guard(
  site: FailureSite,
  op: string,
  params: Record<string, unknown>,
  kind: string,
  evaluate: () => Mismatch | null,
): Mismatch | null {
  try {
    return evaluate();
  } catch (err) {
    if (!(err instanceof ExpressionError)) throw err;
    throw fail(site, op, params, kind, {
      expected: "an evaluable expected value",
      actual: err.message,
    });
  }
}

function expressionFailure(
  site: FailureSite,
  op: string,
  err: unknown,
): Error {
  if (!(err instanceof ExpressionError)) return err instanceof Error ? err : new Error(String(err));
  return scenarioFailure(
    failureMessage(site, {
      op,
      params: "<not evaluated>",
      kind: "params",
      expected: "every parameter expression to evaluate",
      actual: err.message,
    }),
  );
}

function fail(
  site: FailureSite,
  op: string,
  params: unknown,
  kind: string,
  mismatch: Mismatch,
  cause?: unknown,
): Error {
  const detail = {
    op,
    params,
    kind,
    expected: mismatch.expected,
    actual: mismatch.actual,
    ...(mismatch.path === undefined ? {} : { path: mismatch.path }),
  };
  return scenarioFailure(failureMessage(site, detail), cause);
}

/**
 * Put the retry budget in front of the last attempt's message.
 *
 * The prefix is byte-for-byte the one compat/model/README.md § Failure
 * messages fixes, and the sibling interpreters emit the same string, so one
 * generated group's give-up reads identically whichever suite reports it and
 * a log search finds all three. It is a prefix rather than a suffix because
 * the six-field message ends in the scenario file and step index, which is
 * where a reader looks next.
 *
 * The message is rewritten in place rather than rethrown as a new Error: the
 * unimplemented markers scenarioFailure() copied onto this error are what
 * keeps a 501 inside an `eventually` classified as unimplemented, and a fresh
 * Error would drop them.
 */
function prefixAttempts(err: unknown, maxAttempts: number, delayMs: number): unknown {
  if (!(err instanceof Error)) return err;
  err.message =
    `eventually gave up after ${maxAttempts} attempt(s) ${delayMs}ms apart; ` +
    `last failure: ${err.message}`;
  return err;
}
