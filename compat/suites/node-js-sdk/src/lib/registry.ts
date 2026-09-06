/**
 * registry.ts — types and loader for the shared cross-suite test registry.
 *
 * The registry (../../registry.json) is the single source of truth for which
 * test groups and test cases exist across ALL compat suites (Node.js, Python,
 * Go, Java, .NET, Rust, CLI, etc.).  Every suite runner should:
 *
 *   1. Load the registry (loadRegistry()).
 *   2. Register its own implementations by name.
 *   3. Call buildGroupsFromRegistry() to get a TestGroup[] where every
 *      registered test runs and every un-registered test emits "skip"
 *      automatically — keeping the dashboard matrix consistent.
 *
 * When you add a new test to the registry you get:
 *   - All suites show it as "skip" immediately (not silently absent).
 *   - The dashboard comparison view lines up correctly across suites.
 *   - You only implement it once per language.
 */

import { createRequire } from "node:module";
import type { TestCase, TestFn, TestGroup } from "./harness.ts";

// ─── Registry types ───────────────────────────────────────────────────────

export interface RegistryTestCase {
  /** PascalCase test name — must match the TestCase name used by runners. */
  name: string;
  /**
   * AWS API operation name when it differs from test name.
   * null means no documentation link (internal setup step).
   */
  op?: string | null;
  /**
   * Runtime capabilities required to run this test.
   * Suite runners skip the test if the capability is absent.
   */
  requires?: Array<"docker" | "smtp" | "network">;
  /**
   * If present, always emit as "skip" with this reason.
   * Only for tests that can never run in a standard compat environment.
   */
  skip?: string;
  /**
   * Names of other tests in the SAME group that must run (and pass) before
   * this test.  Runners topologically sort by dependency order and auto-skip
   * dependents when a dependency fails.
   */
  depends?: string[];
}

export interface RegistryGroup {
  service: string;
  /** Group name, e.g. "s3-crud". */
  name: string;
  /** Mark slow groups so they are scheduled first (longest-job-first). */
  slow?: boolean;
  /**
   * Restrict this group to specific suites; omitted for the normal case
   * where every SDK/CLI suite implements the group. Reserved for
   * `cdk-lifecycle` on a hand-written group; required (and mechanically
   * derived from backend availability) on a group from
   * registry.generated.json. See registry.schema.json's `suites`.
   */
  suites?: string[];
  tests: RegistryTestCase[];
  /** Only set on a group from registry.generated.json — see GeneratedRegistryGroup. */
  generated?: boolean;
  /**
   * The scenario IR file that resolves this group's tests. Always set on a
   * group from registry.generated.json; set on a hand-written group once it
   * has been *ported* — an authored scenario resolves its tests through this
   * suite's scenario backend and no suite implements them by hand any more
   * (docs/plans/compat-coverage-modelgen.md §3.11, #1903).
   */
  scenario?: string;
  /** Only set on a group from registry.generated.json — see GeneratedRegistryGroup. */
  state?: "candidate" | "gated";
  /**
   * Only set on a group from registry.generated.json, where cmd/compatgen
   * sets it on a probe group and on nothing else: the group's tests may run
   * concurrently with one another. See TestGroup.parallel in harness.ts for
   * what makes it safe.
   */
  parallel?: boolean;
}

export interface Registry {
  version: 1;
  comment?: string;
  groups: RegistryGroup[];
}

/**
 * The shared TestGroup shape plus the three fields only a group from
 * registry.generated.json carries. See registry.generated.schema.json.
 */
export interface GeneratedRegistryGroup extends RegistryGroup {
  generated: true;
  state: "candidate" | "gated";
  /** Mechanically derived from backend availability by cmd/compatgen — never hand-edited. */
  suites: string[];
}

export interface GeneratedRegistry {
  version: 1;
  comment?: string;
  groups: GeneratedRegistryGroup[];
}

// ─── Loader ───────────────────────────────────────────────────────────────

/**
 * Load registry.json, concatenated with registry.generated.json.
 *
 * See mergeRegistries() for the concatenation, scoping and collision rules.
 */
export function loadRegistry(): Registry {
  const require = createRequire(import.meta.url);
  // Three levels up from node-js-sdk/src/lib/ → suites/
  const handWritten = require("../../../registry.json") as Registry;
  return mergeRegistries(handWritten, loadGeneratedRegistry());
}

/**
 * Load registry.generated.json if present, or an empty registry.
 *
 * A missing file is **not** an error: suite images, CI artifacts and
 * branches cut before the file existed must keep working, so this returns
 * `{ version: 1, groups: [] }`. A present-but-unparsable file, a `version`
 * other than 1, or a group missing `generated`, `state` or `suites` is a
 * load error — thrown, exactly as a malformed registry.json is today
 * (loadRegistry() does no validation of its own and simply lets `require()`
 * throw a SyntaxError).
 *
 * @param path Override the file location (absolute, or resolved the way
 *             `require()` resolves any specifier). Defaults to
 *             registry.json's sibling. Exists so tests can point at a
 *             fixture without writing over — or depending on the current
 *             contents of — the real file.
 */
export function loadGeneratedRegistry(path?: string): GeneratedRegistry {
  const require = createRequire(import.meta.url);
  const target = path ?? "../../../registry.generated.json";

  let data: GeneratedRegistry;
  try {
    data = require(target) as GeneratedRegistry;
  } catch (err) {
    const code = (err as NodeJS.ErrnoException | null)?.code;
    if (code === "MODULE_NOT_FOUND") {
      return { version: 1, groups: [] };
    }
    throw err;
  }

  if ((data.version as unknown) !== 1) {
    throw new Error(
      `${target}: unsupported registry.generated.json version ` +
        `${JSON.stringify(data.version)} (want 1)`,
    );
  }
  for (const rg of data.groups) {
    const missing = (["generated", "state", "suites"] as const).filter(
      (k) => !(k in rg),
    );
    if (missing.length > 0) {
      throw new Error(
        `${target}: generated group "${rg.name}" is missing required ` +
          `field(s): ${missing.join(", ")}`,
      );
    }
  }
  return data;
}

/**
 * Concatenate hand-written and generated groups: hand-written groups first,
 * in file order; generated groups appended after, in file order. Do not
 * re-sort beyond what the loader already does (e.g. slow-first).
 *
 * A group name present in both files is a load error naming the group.
 * `cmd/compat` already lints this; this is the second line of defence, same
 * posture as the existing ambiguous-name defence in validateImpls().
 */
export function mergeRegistries(
  handWritten: Registry,
  generated: GeneratedRegistry,
): Registry {
  const hwNames = new Set(handWritten.groups.map((g) => g.name));
  for (const rg of generated.groups) {
    if (hwNames.has(rg.name)) {
      throw new Error(
        `registry.generated.json: group "${rg.name}" is also declared in ` +
          `registry.json — a generated group may never reuse a ` +
          `hand-written name`,
      );
    }
  }
  return {
    ...handWritten,
    groups: [...handWritten.groups, ...generated.groups],
  };
}

// ─── Builder ─────────────────────────────────────────────────────────────

export type ImplMap = Record<string, TestFn>;

/**
 * Resolves an implementation for a generated group's test that has no
 * registered impl. Given the group name, test name, and the group's
 * `scenario` path (undefined if absent), it may return a TestFn to run, or
 * undefined to say it cannot handle this test either. `src/lib/scenario`
 * provides one (#1393, #1113 phase G2), and `runner.ts` wires it in.
 */
export type ScenarioBackend = (
  group: string,
  test: string,
  scenario: string | undefined,
) => TestFn | undefined;

export interface BuildOptions {
  suite: string;
  /** Which capability flags this runner supports. Default: []. */
  capabilities?: Array<"docker" | "smtp" | "network">;
  /** Optional setup functions keyed by group name. */
  setup?: Record<
    string,
    (ctx: import("./harness.ts").TestContext) => Promise<void>
  >;
  /** Optional teardown functions keyed by group name. */
  teardown?: Record<
    string,
    (ctx: import("./harness.ts").TestContext) => Promise<void>
  >;
  /**
   * Optional resolver consulted for a test with no registered impl, before
   * falling back to the not-implemented sentinel (hand-written groups) or the
   * interim fail rule (generated groups). `src/lib/scenario` provides one,
   * and `runner.ts` wires it in (#1393).
   */
  scenarioBackend?: ScenarioBackend;
}

/**
 * Build a TestGroup[] from the registry, filling missing impls with auto-skip.
 *
 * @param registry  Loaded from loadRegistry().
 * @param impls     Map of test name → async test function.
 * @param opts      Suite name, capabilities, per-group setup/teardown.
 *
 * @example
 * ```ts
 * const groups = buildGroupsFromRegistry(loadRegistry(), {
 *   CreateBucket: async (ctx) => { ... },
 *   PutObject:    async (ctx) => { ... },
 * }, { suite: "python-sdk" });
 * ```
 */
// eslint-disable-next-line @typescript-eslint/no-empty-function
const noop = async () => {};

/**
 * A TestFn that fails with `message` — the interim result for a generated
 * group in scope with no impl and no scenario backend (#1393). Throwing
 * (rather than skip/na) is deliberate: the harness's isUnimplemented() only
 * fires on a wrapped 501 response, so this always surfaces as "fail", never
 * "unimplemented" or a pass.
 *
 * Throws the raw string rather than an Error: runGroup() formats a thrown
 * Error as `${err.name}: ${err.message}`, which would prefix "Error: " onto
 * the message the interim rule requires verbatim. `err instanceof Error` is
 * false for a string, so the harness falls back to String(err) and the NDJSON
 * `error` field carries this text byte-for-byte.
 */
function scenarioBackendMissing(message: string): TestFn {
  // eslint-disable-next-line @typescript-eslint/only-throw-error
  return async () => {
    throw message;
  };
}

/**
 * Topologically sort tests within a group using their `depends` edges.
 * Tests with no dependencies come first; tests whose deps are all
 * resolved come next.  Falls back to the registry declaration order
 * for tests at the same dependency depth.
 */
function topoSort(tests: RegistryTestCase[]): RegistryTestCase[] {
  const byName = new Map(tests.map((t) => [t.name, t]));
  const sorted: RegistryTestCase[] = [];
  const visited = new Set<string>();
  const visiting = new Set<string>(); // cycle detection

  function visit(t: RegistryTestCase): void {
    if (visited.has(t.name)) return;
    if (visiting.has(t.name)) return; // cycle — break it
    visiting.add(t.name);
    for (const dep of t.depends ?? []) {
      const depTest = byName.get(dep);
      if (depTest) visit(depTest);
    }
    visiting.delete(t.name);
    visited.add(t.name);
    sorted.push(t);
  }

  for (const t of tests) visit(t);
  return sorted;
}

export function buildGroupsFromRegistry(
  registry: Registry,
  impls: ImplMap,
  opts: BuildOptions,
): TestGroup[] {
  const caps = new Set(opts.capabilities ?? []);
  const ambiguous = ambiguousTestNames(registry);

  const groups = registry.groups
    .filter((rg) => {
      // General suites-scoping: a group scoped to specific suites is not
      // loaded at all outside that scope — no tests, no skips, no results.
      // This subsumes the old `rg.service !== "cdk"` special case:
      // cdk-lifecycle is currently the only hand-written group declaring
      // `suites` (["cdk"]), and it also happens to have service === "cdk",
      // so for today's registry.json the two checks are behaviour-identical
      // for every non-cdk suite (node-js-sdk included). Generated groups
      // (#1393) always declare `suites`, mechanically derived from backend
      // availability by cmd/compatgen, so this is now a general rule rather
      // than a single-group carve-out.
      if (rg.suites && !rg.suites.includes(opts.suite)) return false;
      return true;
    })
    .map((rg) => {
    // Topologically sort tests by their declared dependencies so that
    // prerequisites always execute before the tests that need them.
    const sortedTests = topoSort(rg.tests);

    const tests: TestCase[] = sortedTests.map((rt): TestCase => {
      // Resolve op: registry null means suppress doc link (false in harness);
      // registry string overrides the test name; registry undefined means use test name.
      const op: string | false | undefined =
        rt.op === null ? false : (rt.op ?? undefined);

      const depends = rt.depends;

      // Static registry-level skip (annotated in the JSON).
      if (rt.skip) {
        return { name: rt.name, fn: noop, op, skip: rt.skip, depends };
      }

      // Capability gate — skip if the runner can't satisfy the requirement.
      if (rt.requires && rt.requires.some((c) => !caps.has(c))) {
        const missing = rt.requires.filter((c) => !caps.has(c));
        return {
          name: rt.name,
          fn: noop,
          op,
          skip: `requires ${missing.join(", ")} (not available in this environment)`,
          depends,
        };
      }

      // Look up by group-qualified key first ("groupName:testName"), then fall
      // back to the bare test name.  The bare fallback is refused for a name
      // claimed by more than one group: it would bind this group to another
      // group's implementation and report its result as ours.  validateImpls
      // rejects such a registration outright; this is the second line of
      // defence, so a mis-bind cannot occur even if validation is bypassed.
      const qualifiedKey = `${rg.name}:${rt.name}`;
      const bareUsable = !ambiguous.has(rt.name);
      const hasImpl = qualifiedKey in impls || (bareUsable && rt.name in impls);
      if (!hasImpl) {
        const scenarioFn = opts.scenarioBackend?.(rg.name, rt.name, rg.scenario);
        if (scenarioFn) {
          return { name: rt.name, fn: scenarioFn, op, depends };
        }

        if (rg.generated) {
          // Interim rule (#1393): `suites` on a generated group is derived
          // from backend availability by cmd/compatgen, so a suite named in
          // it that cannot execute the group is a generator/loader bug, and
          // it has to be loud — never a skip (which reads as "not
          // implemented yet", i.e. debt) and never `na`. Because `candidate`
          // groups are excluded from the compare-baseline/max-failures gates
          // by cmd/compat (#1367) this cannot red a build until a group is
          // `gated`, at which point it is a real regression and should. What
          // it can never do is report as a pass or a skip.
          const message =
            `generated group "${rg.name}" is scoped to ${opts.suite} but ` +
            `${opts.suite} has no scenario backend`;
          return { name: rt.name, fn: scenarioBackendMissing(message), op, depends };
        }

        // No implementation yet — surface as skip so the dashboard shows it.
        return {
          name: rt.name,
          fn: noop,
          op,
          skip: `not yet implemented in ${opts.suite} test suite`,
          depends,
        };
      }

      const fn = impls[qualifiedKey] ?? (bareUsable ? impls[rt.name] : undefined);
      if (fn == null) {
        // Explicitly registered as null/undefined → SDK does not expose this.
        return {
          name: rt.name,
          fn: noop,
          op,
          na: `not yet supported by the AWS JavaScript SDK v3`,
          depends,
        };
      }

      return { name: rt.name, fn, op, depends };
    });

    const group: TestGroup = {
      suite: opts.suite,
      service: rg.service,
      name: rg.name,
      tests,
      parallel: rg.parallel === true,
    };
    if (opts.setup?.[rg.name]) group.setup = opts.setup[rg.name];
    if (opts.teardown?.[rg.name]) group.teardown = opts.teardown[rg.name];
    return group;
  });

  // Longest-job-first: schedule slow groups before fast ones so they start
  // early and finish in parallel with the many quick groups instead of
  // becoming a long tail at the end of the run.
  const slowGroups = new Set(
    registry.groups.filter((g) => g.slow).map((g) => g.name),
  );
  groups.sort((a, b) => {
    const as = slowGroups.has(a.name) ? 1 : 0;
    const bs = slowGroups.has(b.name) ? 1 : 0;
    return bs - as; // slow first
  });

  return groups;
}

// ─── Validation ───────────────────────────────────────────────────────────

/** Map each registry test name to the sorted groups that declare it. */
export function testNameOwners(registry: Registry): Map<string, string[]> {
  const owners = new Map<string, Set<string>>();
  for (const g of registry.groups) {
    for (const t of g.tests) {
      let groups = owners.get(t.name);
      if (!groups) owners.set(t.name, (groups = new Set()));
      groups.add(g.name);
    }
  }
  return new Map(
    [...owners].map(([name, groups]) => [name, [...groups].sort()]),
  );
}

/**
 * Test names that more than one registry group declares.
 *
 * A bare-name implementation cannot serve these. `ListUsers` belongs to both
 * `iam-users` and `cognito-userpools`, so a bare `ListUsers` impl binds
 * whichever group happens to resolve it — and the loser silently runs the
 * other service's test and reports the result as its own. Suites must register
 * the group-qualified key for an ambiguous name.
 */
export function ambiguousTestNames(registry: Registry): Set<string> {
  return new Set(
    [...testNameOwners(registry)]
      .filter(([, groups]) => groups.length > 1)
      .map(([name]) => name),
  );
}

/** One group's contribution to the suite's impl map, labelled with where it
 * came from so a collision can name both sides. */
export interface ImplSource {
  name: string;
  impls: ImplMap;
}

/**
 * Flatten per-group impl maps into the single map the loader resolves against,
 * refusing any key that two sources both register.
 *
 * The merge used to be a plain assignment into one object — last writer wins,
 * and silently. Two group files both producing `lambda-crud:CreateFunction`
 * left one implementation unreachable with nothing said about it, and the run
 * reported a result for whichever one survived. `validateImpls` cannot catch
 * this: by the time it sees the flattened map the discarded implementation is
 * already gone, and the surviving key resolves perfectly well.
 *
 * @throws if any key is registered more than once.
 */
export function mergeImpls(sources: ImplSource[], suite: string): ImplMap {
  const merged: ImplMap = {};
  const owner = new Map<string, string>(); // key → source that registered it first

  const problems: string[] = [];
  for (const source of sources) {
    for (const [key, fn] of Object.entries(source.impls)) {
      const first = owner.get(key);
      if (first !== undefined) {
        problems.push(duplicateProblem(key, first, source.name));
        continue;
      }
      owner.set(key, source.name);
      merged[key] = fn;
    }
  }

  if (problems.length === 0) return merged;
  throw new Error(
    `[${suite}] ${problems.length} duplicate impl registration(s):\n  - ` +
      problems.sort().join("\n  - "),
  );
}

/** One collision. The two sources are the same when a single group registers
 * the key twice. */
function duplicateProblem(key: string, first: string, second: string): string {
  const where =
    first === second
      ? `is registered twice by "${first}"`
      : `is registered by both "${first}" and "${second}"`;
  return (
    `impl "${key}" ${where} — one of the two would be silently ` +
    `discarded; remove or re-key one`
  );
}

/**
 * Reject impl keys that cannot be bound to exactly one registry test.
 *
 * This used to warn on stderr (fatal only under OVERCAST_COMPAT_STRICT=1),
 * which meant an unresolvable key was a line nobody read while the test it was
 * meant to implement quietly fell back to another group's implementation and
 * reported a pass. Two registrations are refused:
 *
 * - a key matching no registry entry — a typo, a stale name, or the wrong
 *   separator (every suite uses "group:test"; "group/test" is not accepted);
 * - a bare key for a test name that several groups declare, which cannot say
 *   which group it implements.
 *
 * @throws if any registration is unusable.
 */
export function validateImpls(
  registry: Registry,
  impls: ImplMap,
  suite: string,
): void {
  const owners = testNameOwners(registry);
  const registryNames = new Set(
    registry.groups.flatMap((g) => [
      ...g.tests.map((t) => t.name),
      ...g.tests.map((t) => `${g.name}:${t.name}`),
    ]),
  );

  const problems: string[] = [];
  for (const name of Object.keys(impls).sort()) {
    if (!registryNames.has(name)) {
      let msg = `impl "${name}" matches no registry entry`;
      if (name.includes("/")) {
        // The Java suite used "group/test" until the separator was unified;
        // a key copied from it resolves to nothing here.
        msg +=
          ` (group-qualified keys use ":", not "/" — did you mean ` +
          `"${name.replace("/", ":")}"?)`;
      }
      problems.push(msg);
      continue;
    }
    const claimedBy = owners.get(name) ?? [];
    if (claimedBy.length > 1) {
      // Naming every candidate rather than guessing one: only the author knows
      // which group this implementation is for, and binding it to the wrong
      // one is the failure this check exists to prevent.
      const candidates = claimedBy.map((g) => `"${g}:${name}"`).join(", ");
      problems.push(
        `impl "${name}" is ambiguous: groups [${claimedBy.join(", ")}] all ` +
          `declare a test named "${name}" — qualify it with the group it ` +
          `implements, one of: ${candidates}`,
      );
    }
  }

  if (problems.length === 0) return;
  throw new Error(
    `[${suite}] ${problems.length} unusable impl registration(s):\n  - ` +
      problems.join("\n  - "),
  );
}
