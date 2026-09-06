/**
 * backend.ts — the suite's scenario backend, and the group setup/teardown
 * that comes with it.
 *
 * `buildGroupsFromRegistry()` consults `opts.scenarioBackend` for any test
 * with no static implementation (#1393). This module supplies that resolver
 * for the generated groups, plus the `setup`/`teardown` entries those groups
 * need, both derived from the scenario file the registry group names. Nothing
 * in registry.ts or harness.ts changes: the hook and the two maps were already
 * there, and a scenario group is wired through them exactly as a hand-written
 * one is.
 *
 * The context bag lives on the harness TestContext, which runGroup() shares
 * between a group's setup, its tests and its teardown and copies per group —
 * so `export` in setup is visible to every test, and nothing crosses a group
 * boundary.
 */

import type { TestContext, TestFn } from "../harness.ts";
import type { Registry, ScenarioBackend } from "../registry.ts";
import { makeSdkSender } from "./client.ts";
import {
  runScenarioTest,
  runSetup,
  runTeardown,
  realSleep,
} from "./executor.ts";
import type { ExecEnv } from "./executor.ts";
import { findGroup, findTest, loadScenario } from "./loader.ts";
import type { Scenario } from "./ir.ts";

/** Where the per-group context bag lives on the harness TestContext. */
const BAG_KEY = "_scenarioContext";

type GroupHook = (ctx: TestContext) => Promise<void>;

export interface ScenarioSupport {
  /** Pass as `BuildOptions.scenarioBackend`. */
  backend: ScenarioBackend;
  /** Merge into `BuildOptions.setup`. */
  setup: Record<string, GroupHook>;
  /** Merge into `BuildOptions.teardown`. */
  teardown: Record<string, GroupHook>;
}

export interface ScenarioSupportOptions {
  /** The suite name, to skip generated groups scoped to other suites. */
  suite: string;
}

/**
 * Build the backend and the setup/teardown maps for every group in the
 * registry that names a scenario file and is in scope for this suite.
 *
 * The condition is `scenario`, not `generated`. A *ported* group — a
 * hand-written group whose tests an authored IR scenario resolves
 * (docs/plans/compat-coverage-modelgen.md §3.11 step 3, #1903) — carries
 * `scenario` and is not generated, and it needs these hooks exactly as a
 * generated group does. Gating on `generated` here while makeScenarioBackend
 * below gates on `scenario` gave a ported lifecycle group all of its tests and
 * none of its setup, so every one of them ran against a fixture that was never
 * created.
 *
 * A scenario file that cannot be read or does not validate is reported per
 * test, as a failure with the parser's message, rather than thrown here: a
 * throw would happen while the runner is still assembling groups at module
 * load and would take the whole suite down over one bad file.
 */
export function makeScenarioSupport(
  registry: Registry,
  opts: ScenarioSupportOptions,
): ScenarioSupport {
  const setup: Record<string, GroupHook> = {};
  const teardown: Record<string, GroupHook> = {};

  for (const rg of registry.groups) {
    if (!rg.scenario) continue;
    if (rg.suites && !rg.suites.includes(opts.suite)) continue;
    const scenarioFile = rg.scenario;

    let group;
    try {
      group = findGroup(loadScenario(scenarioFile), rg.name);
    } catch {
      // Reported per test by the backend below, which runs after this loop
      // and reproduces the same error with the same message.
      continue;
    }
    if (!group) continue;

    // Register the hook for every scenario-resolved group, unconditionally,
    // and let an empty calls list make it a no-op. A probe group carries no
    // setup and no teardown steps — there is nothing to set up and nothing to
    // clean up — but that is a property of the scenario file's calls list, not
    // a reason to withhold the hook itself; withholding it was a distinction
    // without a difference that this backend used to make on its own. The
    // python interpreter always registers the hooks, and this is the rule
    // the README will pin.
    const setupCalls = group.setup;
    setup[rg.name] = async (ctx) => {
      await runSetup(envFor(ctx, rg.name, scenarioFile), setupCalls);
    };
    const teardownCalls = group.teardown;
    teardown[rg.name] = async (ctx) => {
      await runTeardown(envFor(ctx, rg.name, scenarioFile), teardownCalls);
    };
  }

  return { backend: makeScenarioBackend(opts), setup, teardown };
}

/** The resolver `BuildOptions.scenarioBackend` takes. */
function makeScenarioBackend(
  opts: ScenarioSupportOptions,
): ScenarioBackend {
  return (groupName, testName, scenarioFile) => {
    if (scenarioFile === undefined) return undefined;

    let scenario: Scenario;
    try {
      scenario = loadScenario(scenarioFile);
    } catch (err) {
      // The registry says this group is generated, names this file and scopes
      // it to us, so a file we cannot read is our failure to report — loudly,
      // per test — not a group to hand back as "not mine".
      return () => Promise.reject(err instanceof Error ? err : new Error(String(err)));
    }

    const group = findGroup(scenario, groupName);
    if (!group) return undefined;
    const test = findTest(group, testName);
    if (!test) return undefined;

    const fn: TestFn = async (ctx) => {
      await runScenarioTest(envFor(ctx, groupName, scenarioFile), test);
    };
    return fn;
  };
}

function envFor(
  ctx: TestContext,
  group: string,
  scenarioFile: string,
): ExecEnv {
  const scenario = loadScenario(scenarioFile);
  return {
    send: makeSdkSender(scenario.client, {
      endpoint: ctx.endpoint,
      region: ctx.region,
    }),
    ctx: { runId: ctx.runId, group, bag: bagFor(ctx) },
    scenarioFile,
    log: (msg) => {
      ctx.log(msg);
    },
    sleep: realSleep,
  };
}

/** The group's context bag, created on first use. */
function bagFor(ctx: TestContext): Map<string, unknown> {
  const existing = ctx[BAG_KEY];
  if (existing instanceof Map) return existing as Map<string, unknown>;
  const bag = new Map<string, unknown>();
  ctx[BAG_KEY] = bag;
  return bag;
}
