/**
 * Unit tests for the scenario loader.
 *
 * Two jobs: prove the validator rejects a malformed file with a message that
 * names where the problem is, and prove the real pilot corpus
 * (compat/model/scenarios/) parses — so a regeneration that changes the IR in
 * a way this interpreter does not understand fails here, without an emulator.
 *
 * Run with: npm run test:unit
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  findGroup,
  findTest,
  loadScenario,
  parseScenario,
} from "./loader.ts";

const minimal = {
  version: 1,
  service: "sqs",
  client: {
    sdkId: "SQS",
    endpointPrefix: "sqs",
    signingName: "sqs",
    protocol: "awsJson1_0",
    apiVersion: "2012-11-05",
    targetPrefix: "AmazonSQS",
  },
  groups: [
    {
      name: "sqs-gen-queue",
      kind: "lifecycle",
      setup: [{ op: "CreateQueue", params: { QueueName: { $name: "dlq" } } }],
      tests: [
        {
          name: "CreateQueue",
          op: "CreateQueue",
          call: { op: "CreateQueue", params: {}, export: { "queue.url": "$.QueueUrl" } },
          assert: [{ kind: "responseField", checks: { "$.QueueUrl": { nonEmpty: true } } }],
        },
      ],
      teardown: [],
    },
  ],
};

function clone(): Record<string, unknown> {
  return JSON.parse(JSON.stringify(minimal)) as Record<string, unknown>;
}

describe("parseScenario", () => {
  it("accepts a well-formed file and types it", () => {
    const scenario = parseScenario(minimal, "test.json");
    assert.equal(scenario.service, "sqs");
    assert.equal(scenario.client.sdkId, "SQS");
    assert.equal(scenario.groups[0].tests[0].assert[0].kind, "responseField");
  });

  it("refuses another version", () => {
    const raw = clone();
    raw["version"] = 2;
    assert.throws(() => parseScenario(raw, "test.json"), /\/version: unsupported/);
  });

  it("names the pointer of a malformed field", () => {
    const raw = clone() as { groups: Array<{ tests: Array<{ assert: unknown[] }> }> };
    raw.groups[0].tests[0].assert = [{ kind: "nope" }];
    assert.throws(
      () => parseScenario(raw, "test.json"),
      /test\.json\/groups\/0\/tests\/0\/assert\/0\/kind: unknown assertion kind/,
    );
  });

  it("refuses a test with no assertion clause", () => {
    const raw = clone() as { groups: Array<{ tests: Array<{ assert: unknown[] }> }> };
    raw.groups[0].tests[0].assert = [];
    assert.throws(() => parseScenario(raw, "test.json"), /at least one assertion/);
  });

  it("refuses a check that is not one of the six", () => {
    const raw = clone() as {
      groups: Array<{ tests: Array<{ assert: Array<{ checks: unknown }> }> }>;
    };
    raw.groups[0].tests[0].assert[0].checks = { "$.QueueUrl": { isBig: true } };
    assert.throws(() => parseScenario(raw, "test.json"), /unknown check "isBig"/);
  });

  it("carries an equalsJSON operand as data, and refuses one that is not a literal document", () => {
    const withOperand = (operand: unknown) => {
      const raw = clone() as {
        groups: Array<{ tests: Array<{ assert: Array<{ checks: unknown }> }> }>;
      };
      raw.groups[0].tests[0].assert[0].checks = { "$.Doc": { equalsJSON: operand } };
      return raw;
    };
    const doc = { Version: "2012-10-17", Statement: [{ Effect: "Allow" }] };
    const parsed = parseScenario(withOperand(doc), "test.json");
    const clause = parsed.groups[0].tests[0].assert[0];
    assert.ok(clause.kind === "responseField");
    assert.deepEqual(clause.checks["$.Doc"], { equalsJSON: doc });

    assert.throws(
      () => parseScenario(withOperand('{"a":1}'), "test.json"),
      /\/checks\/\$\.Doc\/equalsJSON: equalsJSON takes a JSON object or array/,
    );
    assert.throws(
      () => parseScenario(withOperand({ Statement: [{ Resource: { $name: "q" } }] }), "test.json"),
      /never evaluated, so it may not hold the key "\$name"/,
    );
  });

  it("refuses a path that is not the path grammar", () => {
    const raw = clone() as {
      groups: Array<{ tests: Array<{ assert: Array<{ checks: unknown }> }> }>;
    };
    raw.groups[0].tests[0].assert[0].checks = { "$.Messages[*]": { nonEmpty: true } };
    assert.throws(() => parseScenario(raw, "test.json"), /is not a response path/);
  });

  it("refuses an eventually that wraps a clause it may not wrap", () => {
    const raw = clone() as { groups: Array<{ tests: Array<{ assert: unknown[] }> }> };
    raw.groups[0].tests[0].assert = [
      {
        kind: "eventually",
        maxAttempts: 3,
        assert: { kind: "responseField", checks: { "$.QueueUrl": { nonEmpty: true } } },
      },
    ];
    assert.throws(() => parseScenario(raw, "test.json"), /eventually wraps/);
  });

  it("refuses an export on an absent clause's error-form call", () => {
    const raw = clone() as { groups: Array<{ tests: Array<{ assert: unknown[] }> }> };
    raw.groups[0].tests[0].assert = [
      {
        kind: "absent",
        call: {
          op: "GetQueueUrl",
          params: { QueueName: { $name: "dlq" } },
          export: { "queue.url": "$.QueueUrl" },
        },
        error: { shape: "QueueDoesNotExist", code: "QueueDoesNotExist" },
      },
    ];
    assert.throws(
      () => parseScenario(raw, "test.json"),
      /call\/export: an absent clause's error-form call.*no response to export from/,
    );
  });

  it("accepts an absent clause's error-form call with no export", () => {
    const raw = clone() as { groups: Array<{ tests: Array<{ assert: unknown[] }> }> };
    raw.groups[0].tests[0].assert = [
      {
        kind: "absent",
        call: { op: "GetQueueUrl", params: { QueueName: { $name: "dlq" } } },
        error: { shape: "QueueDoesNotExist", code: "QueueDoesNotExist" },
      },
    ];
    const scenario = parseScenario(raw, "test.json");
    assert.equal(scenario.groups[0].tests[0].assert[0].kind, "absent");
  });

  it("takes a $lit payload verbatim, `$`-keys and all", () => {
    const raw = clone() as {
      groups: Array<{ tests: Array<{ call: { params: Record<string, unknown> } }> }>;
    };
    raw.groups[0].tests[0].call.params = { Body: { $lit: { $ref: "not a reference" } } };
    const scenario = parseScenario(raw, "test.json");
    assert.deepEqual(scenario.groups[0].tests[0].call.params, {
      Body: { $lit: { $ref: "not a reference" } },
    });
  });
});

describe("the pilot corpus", () => {
  const files = [
    "compat/model/scenarios/sqs.json",
    "compat/model/scenarios/organizations.json",
  ];

  it("parses, and is found from the suite's own directory rather than the CWD", () => {
    for (const file of files) {
      const scenario = loadScenario(file);
      assert.equal(scenario.version, 1);
      assert.ok(scenario.groups.length > 0, `${file} has groups`);
      for (const group of scenario.groups) {
        assert.ok(group.tests.length > 0, `${group.name} has tests`);
        for (const test of group.tests) {
          assert.ok(test.assert.length > 0, `${group.name}/${test.name} asserts something`);
        }
      }
    }
  });

  it("caches by resolved path, so a file is read once", () => {
    assert.equal(loadScenario(files[0]), loadScenario(files[0]));
  });

  it("carries no setup and no teardown for a probe group", () => {
    for (const file of files) {
      for (const group of loadScenario(file).groups) {
        if (group.kind !== "probe") continue;
        assert.equal(group.setup.length, 0, `${group.name} sets nothing up`);
        assert.equal(group.teardown.length, 0, `${group.name} tears nothing down`);
      }
    }
  });

  it("resolves a group and a test by name, and says no to anything else", () => {
    const sqs = loadScenario(files[0]);
    const group = findGroup(sqs, "sqs-gen-queue");
    assert.ok(group);
    assert.ok(findTest(group, "SetQueueAttributes"));
    assert.equal(findTest(group, "NotATest"), undefined);
    assert.equal(findGroup(sqs, "sqs-queues"), undefined);
  });
});
