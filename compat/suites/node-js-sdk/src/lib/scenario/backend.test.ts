/**
 * Unit tests for the naming derivation and for the backend's wiring into the
 * registry loader.
 *
 * The naming table is compat/model/README.md § Naming: the scenario file
 * carries `sdkId` and nothing SDK-specific, so a broken derivation is a whole
 * service that cannot be executed. The cases below are every shape of sdkId
 * lib/clients.ts already imports a client for.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { clientClassName, commandClassName, packageName } from "./client.ts";
import { makeScenarioSupport } from "./backend.ts";
import type { Registry } from "../registry.ts";

describe("naming", () => {
  it("derives the package name by lowercasing and hyphenating spaces", () => {
    const cases: Array<[string, string]> = [
      ["SQS", "@aws-sdk/client-sqs"],
      ["Organizations", "@aws-sdk/client-organizations"],
      ["Secrets Manager", "@aws-sdk/client-secrets-manager"],
      ["CloudWatch Logs", "@aws-sdk/client-cloudwatch-logs"],
      ["Cognito Identity Provider", "@aws-sdk/client-cognito-identity-provider"],
      ["API Gateway", "@aws-sdk/client-api-gateway"],
      ["ApiGatewayV2", "@aws-sdk/client-apigatewayv2"],
      // The reason this is not a camel-case split: both of these are one word
      // in the package name even though they carry an interior capital.
      ["DynamoDB", "@aws-sdk/client-dynamodb"],
      ["ElastiCache", "@aws-sdk/client-elasticache"],
      ["WAFV2", "@aws-sdk/client-wafv2"],
    ];
    for (const [sdkId, expected] of cases) {
      assert.equal(packageName(sdkId), expected, sdkId);
    }
  });

  it("derives the client and command class names", () => {
    assert.equal(clientClassName("SQS"), "SQSClient");
    assert.equal(clientClassName("Organizations"), "OrganizationsClient");
    assert.equal(clientClassName("Cognito Identity Provider"), "CognitoIdentityProviderClient");
    assert.equal(commandClassName("CreateQueue"), "CreateQueueCommand");
  });
});

/** A registry naming the real pilot groups, as cmd/compatgen writes them. */
function registryWith(groups: Array<{ name: string; scenario: string }>): Registry {
  return {
    version: 1,
    groups: groups.map((g) => ({
      service: g.name.split("-")[0],
      name: g.name,
      generated: true,
      state: "candidate" as const,
      scenario: g.scenario,
      suites: ["node-js-sdk"],
      tests: [],
    })),
  };
}

const SQS = "compat/model/scenarios/sqs.json";
const ORGS = "compat/model/scenarios/organizations.json";

describe("the scenario backend", () => {
  const support = makeScenarioSupport(
    registryWith([
      { name: "sqs-gen-queue", scenario: SQS },
      { name: "sqs-gen-probe", scenario: SQS },
      { name: "organizations-gen-policy", scenario: ORGS },
      { name: "organizations-gen-probe", scenario: ORGS },
    ]),
    { suite: "node-js-sdk" },
  );

  it("resolves a test the scenario declares", () => {
    assert.equal(typeof support.backend("sqs-gen-queue", "SetQueueAttributes", SQS), "function");
  });

  it("says `not mine` for an unknown group, an unknown test, and no scenario", () => {
    assert.equal(support.backend("sqs-queues", "CreateQueue", SQS), undefined);
    assert.equal(support.backend("sqs-gen-queue", "NotATest", SQS), undefined);
    assert.equal(support.backend("sqs-queues", "CreateQueue", undefined), undefined);
  });

  it("registers setup and teardown for every generated group", () => {
    assert.equal(typeof support.setup["sqs-gen-queue"], "function");
    assert.equal(typeof support.teardown["sqs-gen-queue"], "function");
    // A probe group's scenario carries no setup/teardown calls, but the hook
    // is registered all the same — an empty calls list makes it a no-op
    // rather than a reason to withhold the hook (backend.ts, #1788 review).
    assert.equal(typeof support.setup["sqs-gen-probe"], "function");
    assert.equal(typeof support.teardown["sqs-gen-probe"], "function");
    assert.equal(typeof support.setup["organizations-gen-probe"], "function");
    assert.equal(typeof support.teardown["organizations-gen-probe"], "function");
    // organizations-gen-policy creates its policy in the CreatePolicy test,
    // not in setup, but still has to delete it — so its setup hook is
    // registered too, and it is the one that turns out to run nothing.
    assert.equal(typeof support.setup["organizations-gen-policy"], "function");
    assert.equal(typeof support.teardown["organizations-gen-policy"], "function");
  });

  it("a probe group's setup and teardown hooks run nothing", async () => {
    const ctx = {
      endpoint: "http://unused.invalid",
      region: "us-east-1",
      runId: "oc-test",
      log: () => {
        throw new Error("a no-op hook must not log anything");
      },
    };
    // Neither hook evaluates a call or touches the SDK sender: if it did, it
    // would need a real endpoint and this would hang or reject instead of
    // resolving immediately.
    await support.setup["sqs-gen-probe"](ctx);
    await support.teardown["sqs-gen-probe"](ctx);
    await support.setup["organizations-gen-probe"](ctx);
    await support.teardown["organizations-gen-probe"](ctx);
    // organizations-gen-policy's setup hook is registered (previous test) but
    // the scenario gives it no setup calls, so it must also run nothing.
    await support.setup["organizations-gen-policy"](ctx);
  });

  it("reports an unreadable scenario file per test instead of aborting the suite", async () => {
    const missing = "compat/model/scenarios/does-not-exist.json";
    const broken = makeScenarioSupport(
      registryWith([{ name: "ghost-gen-thing", scenario: missing }]),
      { suite: "node-js-sdk" },
    );
    assert.deepEqual(broken.setup, {});
    const fn = broken.backend("ghost-gen-thing", "Whatever", missing);
    assert.equal(typeof fn, "function");
    await assert.rejects(
      () => fn!({ endpoint: "", region: "", runId: "", log: () => {} }),
      /cannot read scenario file/,
    );
  });

  it("ignores a generated group scoped to another suite", () => {
    const other = makeScenarioSupport(
      registryWith([{ name: "sqs-gen-queue", scenario: SQS }]),
      { suite: "python-sdk" },
    );
    assert.deepEqual(other.setup, {});
    assert.deepEqual(other.teardown, {});
  });
});

/**
 * A ported group: a hand-written registry entry carrying `scenario` and no
 * `generated` flag, its tests resolved by an authored scenario rather than by
 * a per-language implementation (docs/plans/compat-coverage-modelgen.md §3.11
 * step 3, #1903).
 *
 * The file used is the real authored one, under the name it carries since the
 * flip. What these cases are about is the *absent `generated` flag*.
 */
const AUTHORED = "compat/model/authored/sqs-queues.json";

function portedRegistry(name: string, scenario: string): Registry {
  return {
    version: 1,
    groups: [{ service: "sqs", name, scenario, tests: [] }],
  };
}

describe("a ported hand-written group", () => {
  const support = makeScenarioSupport(portedRegistry("sqs-queues", AUTHORED), {
    suite: "node-js-sdk",
  });

  it("resolves its tests through the scenario backend", () => {
    assert.equal(
      typeof support.backend("sqs-queues", "SetQueueAttributes", AUTHORED),
      "function",
    );
  });

  it("gets its setup and teardown hooks", () => {
    // This is the half that used to be missing: makeScenarioSupport gated on
    // `generated` while the backend gated on `scenario`, so a ported lifecycle
    // group got every one of its tests and none of its setup — and ran them
    // all against a queue that was never created.
    assert.equal(typeof support.setup["sqs-queues"], "function");
    assert.equal(typeof support.teardown["sqs-queues"], "function");
  });

  it("is still scoped away from a suite its `suites` excludes", () => {
    const scoped = portedRegistry("sqs-queues", AUTHORED);
    scoped.groups[0].suites = ["python-sdk"];
    const other = makeScenarioSupport(scoped, { suite: "node-js-sdk" });
    assert.deepEqual(other.setup, {});
    assert.deepEqual(other.teardown, {});
  });
});
