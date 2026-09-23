/**
 * groups/eventbridge.ts — EventBridge compat groups for the node-js-sdk suite.
 *
 * Status: NOT implemented in Overcast. Tests expected to fail with 501.
 *
 * Groups:
 *   eventbridge-buses         — custom event bus lifecycle
 *   eventbridge-events        — PutEvents
 *   eventbridge-target-fanout — PutEvents actually reaching a rule's targets
 *   eventbridge-patterns      — TestEventPattern (stateless, no setup/teardown)
 */

import {
  CreateEventBusCommand,
  DeleteEventBusCommand,
  DescribeEventBusCommand,
  ListEventBusesCommand,
  PutRuleCommand,
  DeleteRuleCommand,
  PutTargetsCommand,
  RemoveTargetsCommand,
  ListTargetsByRuleCommand,
  PutEventsCommand,
  TagResourceCommand,
  ListTagsForResourceCommand,
  TestEventPatternCommand,
  RuleState,
} from "@aws-sdk/client-eventbridge";
import {
  CreateQueueCommand,
  DeleteMessageCommand,
  DeleteQueueCommand,
  GetQueueAttributesCommand,
  ReceiveMessageCommand,
} from "@aws-sdk/client-sqs";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

export function makeEventBridgeGroups(suite: string): TestGroup[] {
  return [
    // ── eventbridge-buses ──────────────────────────────────────────────────
    {
      suite,
      service: "eventbridge",
      name: "eventbridge-buses",
      tests: [
        {
          name: "CreateEventBus",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new CreateEventBusCommand({ Name: `${ctx.runId}-bus` }),
            );
            assert.ok(resp.EventBusArn, "CreateEventBus: missing EventBusArn");
            (ctx as Record<string, unknown>)["_busArn"] = resp.EventBusArn;
          },
        },
        {
          name: "DescribeEventBus",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new DescribeEventBusCommand({ Name: `${ctx.runId}-bus` }),
            );
            assert.ok(resp.Arn, "DescribeEventBus: missing Arn");
          },
        },
        {
          name: "ListEventBuses",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new ListEventBusesCommand({ NamePrefix: ctx.runId }),
            );
            assert.ok(resp.EventBuses?.some((b) => b.Name === `${ctx.runId}-bus`), "ListEventBuses: bus not found");
          },
        },
        {
          name: "TagEventBus",
          op: "TagResource",
          fn: async (ctx) => {
            const busArn = (ctx as Record<string, unknown>)[
              "_busArn"
            ] as string;
            assert.ok(busArn, "no bus ARN");
            const { eventbridge } = makeClients(ctx);
            await eventbridge.send(
              new TagResourceCommand({
                ResourceARN: busArn,
                Tags: [{ Key: "env", Value: "compat" }],
              }),
            );
          },
        },
        {
          name: "ListEventBridgeTagsForResource",
          fn: async (ctx) => {
            const busArn = (ctx as Record<string, unknown>)[
              "_busArn"
            ] as string;
            assert.ok(busArn, "no bus ARN");
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new ListTagsForResourceCommand({ ResourceARN: busArn }),
            );
            assert.ok(resp.Tags?.some((t) => t.Key === "env"), "ListTagsForResource: tag not found");
          },
        },
        {
          name: "DeleteEventBus",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const busName = `${ctx.runId}-bus`;
            await eventbridge.send(
              new DeleteEventBusCommand({ Name: busName }),
            );
            const resp = await eventbridge.send(
              new ListEventBusesCommand({ NamePrefix: ctx.runId }),
            );
            assert.ok(!resp.EventBuses?.some((b) => b.Name === busName), `DeleteEventBus: event bus ${busName} still present after delete`);
          },
        },
      ],
      teardown: async (ctx) => {
        const { eventbridge } = makeClients(ctx);
        try {
          await eventbridge.send(
            new DeleteEventBusCommand({ Name: `${ctx.runId}-bus` }),
          );
        } catch {}
      },
    },

    // ── eventbridge-events ─────────────────────────────────────────────────
    {
      suite,
      service: "eventbridge",
      name: "eventbridge-events",
      tests: [
        {
          name: "PutEvents",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new PutEventsCommand({
                Entries: [
                  {
                    Source: `compat.${ctx.runId}`,
                    DetailType: "CompatTest",
                    Detail: JSON.stringify({
                      runId: ctx.runId,
                      test: "PutEvents",
                    }),
                    EventBusName: "default",
                  },
                ],
              }),
            );
            assert.ok(((resp.FailedEntryCount ?? 0)) <= (0), `PutEvents: ${resp.FailedEntryCount} failed entries`);
          },
        },
        {
          name: "PutEventsBatch",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const entries = Array.from({ length: 5 }, (_, i) => ({
              Source: `compat.${ctx.runId}`,
              DetailType: "CompatBatch",
              Detail: JSON.stringify({ index: i }),
              EventBusName: "default",
            }));
            const resp = await eventbridge.send(
              new PutEventsCommand({ Entries: entries }),
            );
            assert.ok(((resp.FailedEntryCount ?? 0)) <= (0), `PutEventsBatch: ${resp.FailedEntryCount} failed entries`);
          },
        },
      ],
    },

    // ── eventbridge-target-fanout ──────────────────────────────────────────
    //
    // Target fan-out: an event put on a bus reaches the rule's targets, with
    // the target's input transformation applied first. The group provisions
    // its own queues and rule so it never races the other EventBridge groups
    // (issue #388).
    {
      suite,
      service: "eventbridge",
      name: "eventbridge-target-fanout",
      setup: async (ctx) => {
        const { eventbridge, sqs } = makeClients(ctx);
        const store = ctx as Record<string, unknown>;
        for (const [key, name] of [
          ["_fanoutPlain", `oc-${ctx.runId}-fanout-plain`],
          ["_fanoutShaped", `oc-${ctx.runId}-fanout-shaped`],
        ] as const) {
          const created = await sqs.send(new CreateQueueCommand({ QueueName: name }));
          const attrs = await sqs.send(
            new GetQueueAttributesCommand({
              QueueUrl: created.QueueUrl,
              AttributeNames: ["QueueArn"],
            }),
          );
          store[`${key}Url`] = created.QueueUrl;
          store[`${key}Arn`] = attrs.Attributes?.QueueArn;
        }
        await eventbridge.send(
          new PutRuleCommand({
            Name: `oc-${ctx.runId}-fanout`,
            EventPattern: JSON.stringify({ source: [`oc.fanout.${ctx.runId}`] }),
            State: RuleState.ENABLED,
          }),
        );
      },
      teardown: async (ctx) => {
        const { eventbridge, sqs } = makeClients(ctx);
        const store = ctx as Record<string, unknown>;
        try {
          await eventbridge.send(
            new RemoveTargetsCommand({
              Rule: `oc-${ctx.runId}-fanout`,
              Ids: ["plain", "shaped"],
            }),
          );
        } catch {}
        try {
          await eventbridge.send(new DeleteRuleCommand({ Name: `oc-${ctx.runId}-fanout` }));
        } catch {}
        for (const key of ["_fanoutPlainUrl", "_fanoutShapedUrl"]) {
          const url = store[key] as string | undefined;
          if (!url) continue;
          try {
            await sqs.send(new DeleteQueueCommand({ QueueUrl: url }));
          } catch {}
        }
      },
      tests: [
        {
          name: "PutFanoutTargets",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const store = ctx as Record<string, unknown>;
            const resp = await eventbridge.send(
              new PutTargetsCommand({
                Rule: `oc-${ctx.runId}-fanout`,
                Targets: [
                  { Id: "plain", Arn: store["_fanoutPlainArn"] as string },
                  {
                    Id: "shaped",
                    Arn: store["_fanoutShapedArn"] as string,
                    InputTransformer: {
                      InputPathsMap: { order: "$.detail.orderId" },
                      InputTemplate: '{"order":"<order>"}',
                    },
                  },
                ],
              }),
            );
            assert.strictEqual(
              resp.FailedEntryCount ?? 0,
              0,
              `PutFanoutTargets: ${JSON.stringify(resp.FailedEntries)}`,
            );

            const listed = await eventbridge.send(
              new ListTargetsByRuleCommand({ Rule: `oc-${ctx.runId}-fanout` }),
            );
            assert.strictEqual(listed.Targets?.length, 2, "PutFanoutTargets: expected 2 targets");
            const shaped = listed.Targets?.find((t) => t.Id === "shaped");
            assert.ok(
              shaped?.InputTransformer?.InputTemplate,
              "PutFanoutTargets: InputTransformer did not round-trip",
            );
          },
        },
        {
          name: "PutEventsToQueueTarget",
          fn: async (ctx) => {
            const store = ctx as Record<string, unknown>;
            await putFanoutEvent(ctx, "queue-target");
            const body = await awaitFanoutMessage(ctx, store["_fanoutPlainUrl"] as string, "queue-target");
            assert.ok(
              body.includes(`oc.fanout.${ctx.runId}`) && body.includes("queue-target"),
              `PutEventsToQueueTarget: delivered body missing the event envelope: ${body}`,
            );
          },
        },
        {
          name: "PutEventsWithInputTransformer",
          fn: async (ctx) => {
            const store = ctx as Record<string, unknown>;
            await putFanoutEvent(ctx, "transformed");
            const body = await awaitFanoutMessage(ctx, store["_fanoutShapedUrl"] as string, "transformed");
            assert.strictEqual(
              body,
              '{"order":"transformed"}',
              "PutEventsWithInputTransformer: expected the rendered template",
            );
          },
        },
      ],
    },

    // ── eventbridge-patterns ───────────────────────────────────────────────
    //
    // TestEventPattern is a pure predicate — it does not touch any
    // durable resource, so the group needs no setup/teardown.
    {
      suite,
      service: "eventbridge",
      name: "eventbridge-patterns",
      tests: [
        {
          name: "TestEventPattern",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new TestEventPatternCommand({
                EventPattern: JSON.stringify({
                  source: ["compat.eventbridge-patterns"],
                  "detail-type": ["order.created"],
                }),
                Event: makePatternEvent(ctx),
              }),
            );
            assert.strictEqual(
              resp.Result,
              true,
              `TestEventPattern: expected Result=true, got ${resp.Result}`,
            );
          },
        },
        {
          name: "TestEventPatternNoMatch",
          op: "TestEventPattern",
          fn: async (ctx) => {
            const { eventbridge } = makeClients(ctx);
            const resp = await eventbridge.send(
              new TestEventPatternCommand({
                EventPattern: JSON.stringify({
                  source: ["compat.eventbridge-patterns.other"],
                }),
                Event: makePatternEvent(ctx),
              }),
            );
            assert.strictEqual(
              resp.Result,
              false,
              `TestEventPatternNoMatch: expected Result=false, got ${resp.Result}`,
            );
          },
        },
      ],
    },
  ];
}

/** The shared event used by both eventbridge-patterns tests. */
function makePatternEvent(ctx: { runId: string; region: string }): string {
  return JSON.stringify({
    id: ctx.runId,
    "detail-type": "order.created",
    source: "compat.eventbridge-patterns",
    account: "000000000000",
    time: "2026-01-01T00:00:00Z",
    region: ctx.region,
    resources: [],
    detail: { orderId: "1" },
  });
}

type FanoutCtx = Parameters<NonNullable<TestGroup["setup"]>>[0];

async function putFanoutEvent(ctx: FanoutCtx, orderId: string): Promise<void> {
  const { eventbridge } = makeClients(ctx);
  const resp = await eventbridge.send(
    new PutEventsCommand({
      Entries: [
        {
          Source: `oc.fanout.${ctx.runId}`,
          DetailType: "FanoutTest",
          Detail: JSON.stringify({ orderId }),
        },
      ],
    }),
  );
  assert.strictEqual(resp.FailedEntryCount ?? 0, 0, "PutEvents: failed entries");
}

/**
 * Polls a target queue for a delivered message containing `want`.
 *
 * Both targets hang off one rule, so every event reaches both queues and a
 * queue may hold an earlier test's message; matching on `want` (and consuming
 * what does not match) keeps the tests order-independent. Delivery is
 * asynchronous, so a bounded poll replaces a fixed sleep.
 */
async function awaitFanoutMessage(
  ctx: FanoutCtx,
  queueUrl: string,
  want: string,
): Promise<string> {
  const { sqs } = makeClients(ctx);
  for (let attempt = 0; attempt < 15; attempt++) {
    const resp = await sqs.send(
      new ReceiveMessageCommand({
        QueueUrl: queueUrl,
        MaxNumberOfMessages: 10,
        WaitTimeSeconds: 1,
      }),
    );
    let matched: string | undefined;
    for (const message of resp.Messages ?? []) {
      try {
        await sqs.send(
          new DeleteMessageCommand({ QueueUrl: queueUrl, ReceiptHandle: message.ReceiptHandle }),
        );
      } catch {}
      if (message.Body?.includes(want)) matched = message.Body;
    }
    if (matched !== undefined) return matched;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`no message containing ${want} delivered to the target queue`);
}
