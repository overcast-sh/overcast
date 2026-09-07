/**
 * harness.ts — Core test framework for the Overcast compat Node.js suite.
 *
 * Defines the TestGroup / TestCase shapes and the runGroup() executor that
 * emits NDJSON events to stdout as tests complete.
 *
 * Rules (see compat/AGENTS.md):
 * - Never write non-NDJSON to stdout — use ctx.log() for debug output.
 * - Tests throw to signal failure; returning void means pass.
 * - Teardown always runs, even if tests failed.
 */

export interface TestContext {
  /** Overcast endpoint base URL, e.g. "http://localhost:4566" */
  endpoint: string;
  /** AWS region, e.g. "us-east-1" */
  region: string;
  /**
   * Short unique prefix for resource names in this run.
   * Format: "oc-<8-char-hex>". Use to avoid collisions between runs.
   * Example: "oc-a3f9b12c"
   */
  runId: string;
  /**
   * Write a debug message to stderr (never stdout).
   * The Go runner surfaces these as WARN log lines.
   */
  log(msg: string): void;
  /** AbortSignal for cancellation support (interactive mode). */
  signal?: AbortSignal;
  /**
   * Per-group state bag. Use to pass values between sequential tests within
   * the same group (e.g. resource IDs created in an earlier test).
   * Keys are arbitrary strings; values are unknown.
   */
  [key: string]: unknown;
}

export type TestFn = (ctx: TestContext) => Promise<void>;

export interface TestCase {
  /** PascalCase name matching the AWS API operation where applicable. */
  name: string;
  fn: TestFn;
  /**
   * If set, the test is emitted as "skip" without running.
   * Pass a string reason (e.g. "requires Docker") so the dashboard can
   * explain why. Only use when the test requires external infrastructure
   * that isn't guaranteed to be present. Do NOT skip to hide a gap.
   */
  skip?: boolean | string;
  /**
   * AWS API operation name for documentation links.
   * - Omit: the test `name` is used as the operation name.
   * - String: use this name instead (e.g. when test name is a variant like
   *   "QueryWithLimit" but the real operation is "Query").
   * - `false`: suppress the doc link entirely for this test.
   */
  op?: string | false;
  /**
   * If set, the test is emitted as "na" (not applicable) without running.
   * Use this when the AWS SDK client does not yet expose this operation.
   * NA results are excluded from pass-rate calculations.
   */
  na?: string;
  /**
   * Names of other tests in the same group that must pass before this test
   * can run.  If any dependency failed or was skipped, this test is
   * automatically skipped with a reason referencing the failed dep.
   */
  depends?: string[];
}

export interface TestGroup {
  /** Suite name — passed in from runner.ts. */
  suite: string;
  /** AWS service name, e.g. "s3", "iam". */
  service: string;
  /**
   * Group identifier, e.g. "s3-crud". Used in NDJSON output.
   * Convention: "<service>-<feature>", all lowercase kebab.
   */
  name: string;
  tests: TestCase[];
  /**
   * Optional setup that runs before any tests in the group.
   * Failures here abort the group (all tests emitted as skip).
   */
  setup?: (ctx: TestContext) => Promise<void>;
  /**
   * Optional teardown that runs after all tests, even if they failed.
   * Must be fault-tolerant — wrap every delete in try/catch.
   */
  teardown?: (ctx: TestContext) => Promise<void>;
  /**
   * Lets the group's tests run concurrently with one another, bounded by the
   * same slot count that bounds concurrent groups. Only a generated probe
   * group sets it (`parallel` in registry.generated.json): its tests have no
   * setup, no teardown, no exports and no `depends`, so nothing orders them
   * and no test can observe another's outcome. Results are still emitted in
   * declaration order, so the only observable difference is the wall clock.
   */
  parallel?: boolean;
}

// ─── NDJSON event shapes ──────────────────────────────────────────────────

interface RunStartEvent {
  event: "run_start";
  suite: string;
  started_at: string;
  endpoint: string;
  version: "1";
  total_tests?: number;
}

interface TestStartEvent {
  event: "test_start";
  suite: string;
  service: string;
  group: string;
  test: string;
}

interface TestResultEvent {
  event: "test_result";
  suite: string;
  service: string;
  group: string;
  test: string;
  status: "pass" | "fail" | "skip" | "unimplemented" | "na" | "cancelled";
  duration_ms: number;
  error?: string;
}

interface RunEndEvent {
  event: "run_end";
  suite: string;
  passed: number;
  failed: number;
  skipped: number;
  unimplemented: number;
  duration_ms: number;
}

// ─── Interactive-mode event shapes ────────────────────────────────────────

interface BuildingEvent {
  event: "building";
  suite: string;
  message: string;
}

interface ReadyEvent {
  event: "ready";
  suite: string;
  total_tests: number;
}

interface BatchCompleteEvent {
  event: "batch_complete";
  suite: string;
  batch_id: string;
  passed: number;
  failed: number;
  skipped: number;
  unimplemented: number;
  cancelled: number;
  duration_ms: number;
}

interface PongEvent {
  event: "pong";
  suite: string;
  running_test: string;
}

/** Emit a NDJSON event to stdout. */
export function emitEvent(
  event:
    | RunStartEvent
    | TestStartEvent
    | TestResultEvent
    | RunEndEvent
    | BuildingEvent
    | ReadyEvent
    | BatchCompleteEvent
    | PongEvent,
): void {
  process.stdout.write(JSON.stringify(event) + "\n");
}

/**
 * Returns true if the error represents an unimplemented operation in the
 * emulator. These are known feature gaps, not broken implementations.
 *
 * Every path below reads a **field of the response** — its status, its headers,
 * the error code the SDK parsed out of its body. None of them reads the
 * message, and none of them may: "501" appears in request ids, ARNs, resource
 * names and port numbers, and a sibling suite classifying by substring reported
 * a 400 `InvalidRequestException` as `unimplemented` on one CI run, flipping a
 * gated baseline row and failing an unrelated pull request (#1924).
 *
 * Five paths to detect "not implemented":
 *  1. HTTP 501: JSON-protocol services expose the status via
 *     `err.$metadata.httpStatusCode`.
 *  2. HTTP 501 on XML-protocol services (IAM, SES, STS, …): Overcast returns
 *     a JSON 501 body; the SDK's XML parser fails before populating
 *     `$metadata`. The raw HTTP response is still on `err.$response.statusCode`.
 *  3. The `x-emulator-unsupported` header Overcast sets alongside every 501
 *     (internal/protocol/errors.go), which survives a body neither parser
 *     could turn into a status.
 *  4. UnknownOperationException (HTTP 400): returned by JSON-protocol services
 *     (DynamoDB, CloudWatch Logs, DynamoDB Streams) and the router's JSON
 *     fallback when the target action is not registered.
 *  5. NotImplemented (HTTP 400): returned by the router's XML fallback for
 *     query-protocol services (IAM, STS, SES, etc.) whose action is not
 *     registered. The SDK parses this into `err.name === "NotImplemented"`.
 */
export function isUnimplemented(err: unknown): boolean {
  if (err == null || typeof err !== "object") return false;
  const e = err as Record<string, unknown>;

  // Path 1: standard SDK error with parsed metadata.
  const meta = e["$metadata"];
  if (meta != null && typeof meta === "object") {
    if ((meta as Record<string, unknown>)["httpStatusCode"] === 501)
      return true;
  }

  // Path 2: deserialization error wrapping a raw HTTP response.
  const resp = e["$response"];
  if (resp != null && typeof resp === "object") {
    const raw = resp as Record<string, unknown>;
    if (raw["statusCode"] === 501) return true;

    // Path 3: the emulator's own header on that raw response.
    const headers = raw["headers"];
    if (headers != null && typeof headers === "object") {
      const value = (headers as Record<string, unknown>)["x-emulator-unsupported"];
      if (String(value).toLowerCase() === "true") return true;
    }
  }

  // Path 4: UnknownOperationException — JSON-protocol "not registered" error.
  if (e["name"] === "UnknownOperationException") return true;
  if (e["__type"] === "UnknownOperationException") return true;

  // Path 5: NotImplemented — XML query-protocol "not registered" error (IAM, STS, etc.).
  if (e["name"] === "NotImplemented") return true;
  if (e["Code"] === "NotImplemented") return true;

  return false;
}

// ─── Concurrency semaphore ───────────────────────────────────────────────────

/**
 * Limits how many async tasks run concurrently.
 * Each call to `run()` acquires a slot, awaits fn(), then releases.
 */
export class Semaphore {
  private slots: number;
  private readonly queue: Array<() => void> = [];

  constructor(slots: number) {
    this.slots = slots;
  }

  async run<T>(fn: () => Promise<T>): Promise<T> {
    await new Promise<void>((resolve) => {
      if (this.slots > 0) {
        this.slots--;
        resolve();
      } else {
        this.queue.push(resolve);
      }
    });
    try {
      return await fn();
    } finally {
      const next = this.queue.shift();
      if (next) {
        next();
      } else {
        this.slots++;
      }
    }
  }
}

// ─── Timeout helper ───────────────────────────────────────────────────────────

const TEST_TIMEOUT_MS = 120_000; // 120 s per individual test / setup call — Docker cold starts can take ~30-60s under load

/**
 * Race a promise against a deadline. Rejects with a clear timeout message
 * if `ms` elapses before the promise settles.
 *
 * NOTE: The original promise is NOT cancelled (JS has no cancellation without
 * AbortController). It will eventually settle on its own, but since we
 * immediately move on to the next test / group, the dangling promise cannot
 * produce a second test_result event — it is simply ignored.
 */
function withTimeout<T>(
  promise: Promise<T>,
  ms: number,
  label: string,
): Promise<T> {
  let handle!: ReturnType<typeof setTimeout>;
  const timeout = new Promise<never>((_, reject) => {
    handle = setTimeout(
      () =>
        reject(new Error(`${label} timed out after ${Math.round(ms / 1000)}s`)),
      ms,
    );
  });
  return Promise.race([promise, timeout]).finally(() =>
    clearTimeout(handle),
  ) as Promise<T>;
}

// ─── Abort helpers ────────────────────────────────────────────────────────

/** Return true if the error represents an AbortController cancellation. */
function isAbortError(err: unknown): boolean {
  if (err instanceof DOMException && err.name === "AbortError") return true;
  if (
    err != null &&
    typeof err === "object" &&
    (err as Record<string, unknown>).name === "AbortError"
  )
    return true;
  return false;
}

/**
 * Race a promise against an AbortSignal. Rejects with AbortError if the
 * signal fires before the promise settles. Properly cleans up the listener.
 */
function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) {
    return Promise.reject(new DOMException("Cancelled", "AbortError"));
  }
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(new DOMException("Cancelled", "AbortError"));
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (val) => {
        signal.removeEventListener("abort", onAbort);
        resolve(val);
      },
      (err) => {
        signal.removeEventListener("abort", onAbort);
        reject(err);
      },
    );
  });
}

// ─── Runner ───────────────────────────────────────────────────────────────

export interface RunGroupOptions {
  /** Map of "group:test" → AbortController for cancellation support. */
  abortControllers?: Map<string, AbortController>;
  /** Batch ID for event correlation in interactive mode. */
  batchId?: string;
}

/** Run a single test group, emitting one test_result per test. */
interface GroupCounts {
  passed: number;
  failed: number;
  skipped: number;
  unimplemented: number;
  cancelled: number;
}

/**
 * Run a group's setup, if it has one. Returns true if the test phase should
 * run.
 *
 * A setup failure emits one skip per test with the failure reason and
 * returns false — it does NOT run teardown itself. The IR requires teardown
 * to run either way (compat/model/README.md § The scenario file): a setup
 * that failed partway through has already created whatever its successful
 * steps produced, and that is exactly the run that leaks if the caller skips
 * teardown too.
 */
async function runSetupPhase(
  group: TestGroup,
  ctx: TestContext,
  counts: GroupCounts,
): Promise<boolean> {
  if (!group.setup) return true;
  try {
    await withTimeout(
      group.setup(ctx),
      TEST_TIMEOUT_MS,
      `setup ${group.name}`,
    );
    return true;
  } catch (err) {
    // Emit all tests as skipped if setup fails
    const reason = `setup failed: ${String(err)}`;
    for (const tc of group.tests) {
      emitEvent({
        event: "test_result",
        suite: group.suite,
        service: group.service,
        group: group.name,
        test: tc.name,
        status: "skip",
        duration_ms: 0,
        error: reason,
      });
      counts.skipped++;
    }
    ctx.log(`[${group.name}] ${reason}`);
    return false;
  }
}

/**
 * How many things this suite may do at once: groups in `runSuite`, and the
 * tests of one parallel group in `runTestsConcurrently`.
 *
 * `OVERCAST_COMPAT_PARALLEL_SLOTS` is injected by the Go runner from the CPU
 * count and the number of active suites; default 8. One number bounds both
 * because it answers one question — how much load this machine should put on
 * the emulator at once — and a second knob would only let the two drift apart.
 */
function parallelSlots(): number {
  return Math.max(
    1,
    parseInt(process.env["OVERCAST_COMPAT_PARALLEL_SLOTS"] ?? "8", 10) || 8,
  );
}

/** One test's events, held rather than emitted so a concurrent group can emit
 *  them in declaration order. */
interface TestOutcome {
  events: (TestStartEvent | TestResultEvent)[];
  /** The status the result event carried, for the counters and the gate. */
  status: TestResultEvent["status"];
}

/** The `op` field, spelled exactly as it has always been: absent when the test
 *  declares none, empty when it declares `false` to suppress the doc link. */
function opField(tc: TestCase): { op?: string } {
  return tc.op !== undefined ? { op: tc.op === false ? "" : tc.op } : {};
}

function resultEvent(
  group: TestGroup,
  tc: TestCase,
  status: TestResultEvent["status"],
  duration_ms: number,
  error?: string,
): TestResultEvent {
  return {
    event: "test_result",
    suite: group.suite,
    service: group.service,
    group: group.name,
    test: tc.name,
    status,
    duration_ms,
    ...(error !== undefined ? { error } : {}),
    ...opField(tc),
  };
}

/** Fold one outcome into the group counters and, for a serial run, into the
 *  set the dependency gate reads. `na` is counted nowhere: it is excluded from
 *  pass-rate calculations, and it does not say a dependent's prerequisite is
 *  missing. */
function record(
  outcome: TestOutcome,
  name: string,
  counts: GroupCounts,
  failedOrSkipped: Set<string> | null,
): void {
  switch (outcome.status) {
    case "pass":
      counts.passed++;
      return;
    case "na":
      return;
    case "skip":
      counts.skipped++;
      break;
    case "unimplemented":
      counts.unimplemented++;
      break;
    case "cancelled":
      counts.cancelled++;
      break;
    default:
      counts.failed++;
  }
  failedOrSkipped?.add(name);
}

/**
 * Run one test, or report its na/skip marker without running it, and return
 * the events it produces.
 *
 * The context is the caller's to supply: a serial run passes the group's, a
 * concurrent one passes each test a shallow copy, because `ctx.signal` is
 * per-test and a shared object would hand every test the last one's
 * AbortController.
 */
async function runOne(
  group: TestGroup,
  ctx: TestContext,
  tc: TestCase,
  options: RunGroupOptions | undefined,
): Promise<TestOutcome> {
  return marker(group, tc) ?? (await execute(group, ctx, tc, options));
}

/** The outcome a test carries instead of running — na or skip — or null. */
function marker(group: TestGroup, tc: TestCase): TestOutcome | null {
  if (tc.na) {
    return { events: [resultEvent(group, tc, "na", 0, tc.na)], status: "na" };
  }
  if (tc.skip) {
    const reason = typeof tc.skip === "string" ? tc.skip : undefined;
    return {
      events: [resultEvent(group, tc, "skip", 0, reason)],
      status: "skip",
    };
  }
  return null;
}

/** Run the test function and classify its outcome. */
async function execute(
  group: TestGroup,
  ctx: TestContext,
  tc: TestCase,
  options: RunGroupOptions | undefined,
): Promise<TestOutcome> {
  // Per-test AbortController for cancellation support.
  const ac = new AbortController();
  const acKey = `${group.name}:${tc.name}`;
  options?.abortControllers?.set(acKey, ac);
  ctx.signal = ac.signal;

  const startEvent: TestStartEvent = {
    event: "test_start",
    suite: group.suite,
    service: group.service,
    group: group.name,
    test: tc.name,
  };
  const start = Date.now();
  try {
    await withTimeout(raceAbort(tc.fn(ctx), ac.signal), TEST_TIMEOUT_MS, tc.name);
    return {
      events: [startEvent, resultEvent(group, tc, "pass", Date.now() - start)],
      status: "pass",
    };
  } catch (err) {
    const duration_ms = Date.now() - start;
    const error =
      err instanceof Error ? `${err.name}: ${err.message}` : String(err);
    const status: TestResultEvent["status"] = isAbortError(err)
      ? "cancelled"
      : isUnimplemented(err)
        ? "unimplemented"
        : "fail";
    return {
      events: [
        startEvent,
        resultEvent(
          group,
          tc,
          status,
          duration_ms,
          status === "cancelled" ? "cancelled" : error,
        ),
      ],
      status,
    };
  } finally {
    options?.abortControllers?.delete(acKey);
  }
}

/**
 * Run every test in a group, honouring skip/na markers, the dependency gate,
 * per-test timeouts and cancellation.
 *
 * A group marked `parallel` whose tests declare no dependencies runs them
 * concurrently; everything else runs in declaration order. Both halves of that
 * condition are load-bearing. The concurrent path cannot express the
 * dependency gate — it would have to decide what to skip from outcomes that
 * have not happened yet — so a group declaring one is run serially even where
 * the registry says parallel. The IR never produces that combination (only a
 * probe group is parallel, and a probe has no exports for a `depends` to
 * consume), which is why this is a guard rather than a scheduler.
 */
async function runTestsPhase(
  group: TestGroup,
  ctx: TestContext,
  options: RunGroupOptions | undefined,
  counts: GroupCounts,
): Promise<void> {
  const hasDependencies = group.tests.some((tc) => (tc.depends?.length ?? 0) > 0);
  if (group.parallel && !hasDependencies) {
    await runTestsConcurrently(group, ctx, options, counts);
    return;
  }
  await runTestsInOrder(group, ctx, options, counts);
}

/** The serial path: one test at a time, in declaration order, each event
 *  emitted as it happens so the dashboard sees the group progress. */
async function runTestsInOrder(
  group: TestGroup,
  ctx: TestContext,
  options: RunGroupOptions | undefined,
  counts: GroupCounts,
): Promise<void> {
  const failedOrSkipped = new Set<string>();

  for (const tc of group.tests) {
    // An na/skip marker outranks the dependency gate: a test the suite never
    // intended to run here reports why it was marked, not what happened to
    // something it does not depend on.
    let outcome = marker(group, tc);
    if (outcome === null) {
      // Dependency gate — skip if any declared dependency failed or was skipped.
      const failedDeps = (tc.depends ?? []).filter((d) =>
        failedOrSkipped.has(d),
      );
      outcome =
        failedDeps.length > 0
          ? {
              events: [
                resultEvent(
                  group,
                  tc,
                  "skip",
                  0,
                  `dependency failed: ${failedDeps.join(", ")}`,
                ),
              ],
              status: "skip",
            }
          : await execute(group, ctx, tc, options);
    }
    record(outcome, tc.name, counts, failedOrSkipped);
    for (const ev of outcome.events) emitEvent(ev);
  }
}

/**
 * Run the group's tests through a bounded queue, then emit their events in
 * declaration order.
 *
 * Emitting in order rather than as each settles is what keeps this stream
 * identical to the serial path's, test for test. The dashboard, the baseline
 * and the flake detector all read it, and a result order that depended on
 * which SDK call answered first would be a new source of diff noise for no
 * benefit.
 *
 * No dependency bookkeeping: this path is taken only when no test declares
 * one, so the set a serial run maintains would be read by nobody. Each test
 * gets its own shallow copy of the context, because `ctx.signal` is per-test.
 */
async function runTestsConcurrently(
  group: TestGroup,
  ctx: TestContext,
  options: RunGroupOptions | undefined,
  counts: GroupCounts,
): Promise<void> {
  const sem = new Semaphore(parallelSlots());
  const outcomes = await Promise.all(
    group.tests.map((tc) => sem.run(() => runOne(group, { ...ctx }, tc, options))),
  );
  outcomes.forEach((outcome, i) => {
    record(outcome, group.tests[i]!.name, counts, null);
    for (const ev of outcome.events) emitEvent(ev);
  });
}

export async function runGroup(
  group: TestGroup,
  ctx: TestContext,
  options?: RunGroupOptions,
): Promise<{
  passed: number;
  failed: number;
  skipped: number;
  unimplemented: number;
  cancelled: number;
}> {
  const counts: GroupCounts = {
    passed: 0,
    failed: 0,
    skipped: 0,
    unimplemented: 0,
    cancelled: 0,
  };

  const setupOk = await runSetupPhase(group, ctx, counts);
  if (setupOk) {
    await runTestsPhase(group, ctx, options, counts);
  }

  // Teardown always runs, even after a failed setup — a setup that failed
  // partway through has already created whatever its successful steps
  // produced, and that is exactly the run that leaks if teardown is skipped.
  if (group.teardown) {
    try {
      await group.teardown(ctx);
    } catch (err) {
      ctx.log(`[${group.name}] teardown error: ${String(err)}`);
    }
  }

  return counts;
}

/** Run all groups in parallel, emitting run_start and run_end events.
 *
 * Each group receives its own shallow copy of the context so that
 * per-group state stored via `ctx[key] = value` does not leak between
 * concurrent groups. Node.js is single-threaded so stdout writes from
 * `emitEvent` never interleave, but the state isolation is still required
 * for correctness.
 */
export async function runSuite(
  suite: string,
  groups: TestGroup[],
  ctx: Omit<TestContext, "log">,
): Promise<void> {
  const log = (msg: string): void => {
    process.stderr.write(`[compat:${suite}] ${msg}\n`);
  };
  const baseCtx = { ...ctx, log } as unknown as TestContext;

  const suiteStart = Date.now();

  // Compute total test count upfront for progress tracking.
  const totalTests = groups.reduce((sum, g) => sum + g.tests.length, 0);

  emitEvent({
    event: "run_start",
    suite,
    started_at: new Date().toISOString(),
    endpoint: ctx.endpoint as string,
    version: "1",
    total_tests: totalTests,
  });

  // Limit concurrent group execution to avoid overwhelming the emulator.
  const sem = new Semaphore(parallelSlots());

  // Run all groups through the semaphore; each gets its own context copy.
  const groupResults = await Promise.all(
    groups.map((group) => sem.run(() => runGroup(group, { ...baseCtx }))),
  );

  let totalPassed = 0;
  let totalFailed = 0;
  let totalSkipped = 0;
  let totalUnimplemented = 0;
  for (const { passed, failed, skipped, unimplemented } of groupResults) {
    totalPassed += passed;
    totalFailed += failed;
    totalSkipped += skipped;
    totalUnimplemented += unimplemented;
    // cancelled is tracked in interactive mode only; ignored here
  }

  emitEvent({
    event: "run_end",
    suite,
    passed: totalPassed,
    failed: totalFailed,
    skipped: totalSkipped,
    unimplemented: totalUnimplemented,
    duration_ms: Date.now() - suiteStart,
  });
}

/** Generate a short random ID for resource name prefixes. */
export function makeRunId(): string {
  return (
    "oc-" +
    Math.floor(Math.random() * 0xffffffff)
      .toString(16)
      .padStart(8, "0")
  );
}
