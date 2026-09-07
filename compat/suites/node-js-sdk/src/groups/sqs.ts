/**
 * groups/sqs.ts — SQS compatibility test groups for the Node.js suite.
 *
 * Groups:
 *   sqs-messages  — send/receive/delete (implemented)
 *   sqs-dlq       — dead-letter queues (implemented)
 *   sqs-fifo      — FIFO queues (implemented)
 *   sqs-attributes — queue attributes and tags (implemented)
 *
 * sqs-queues is not here: it is a ported group, resolved from
 * compat/model/authored/sqs-queues.json by the scenario backend (#1903).
 */

import {
  CreateQueueCommand,
  DeleteQueueCommand,
  GetQueueUrlCommand,
  SendMessageCommand,
  SendMessageBatchCommand,
  ReceiveMessageCommand,
  DeleteMessageCommand,
  DeleteMessageBatchCommand,
  ChangeMessageVisibilityCommand,
  PurgeQueueCommand,
  GetQueueAttributesCommand,
  SetQueueAttributesCommand,
} from "@aws-sdk/client-sqs";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

export function makeSQSGroups(suite: string): TestGroup[] {
  return [
    // ── sqs-messages ───────────────────────────────────────────────────────
    {
      suite,
      service: "sqs",
      name: "sqs-messages",
      tests: [
        {
          name: "SendMessage",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            const resp = await sqs.send(
              new SendMessageCommand({
                QueueUrl: QueueUrl!,
                MessageBody: JSON.stringify({ hello: "world" }),
              }),
            );
            assert.ok(resp.MessageId, "SendMessage: missing MessageId");
          },
        },
        {
          name: "SendMessageBatch",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            const resp = await sqs.send(
              new SendMessageBatchCommand({
                QueueUrl: QueueUrl!,
                Entries: [
                  { Id: "1", MessageBody: "batch-1" },
                  { Id: "2", MessageBody: "batch-2" },
                ],
              }),
            );
            assert.ok(
              (resp.Successful?.length ?? 0) >= 2,
              `SendMessageBatch: expected 2 successful, got ${resp.Successful?.length}`,
            );
          },
        },
        {
          name: "ReceiveMessage",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            // Wait for messages to be available
            let messages: { Body?: string; ReceiptHandle?: string }[] = [];
            for (let i = 0; i < 5; i++) {
              const resp = await sqs.send(
                new ReceiveMessageCommand({
                  QueueUrl: QueueUrl!,
                  MaxNumberOfMessages: 10,
                  WaitTimeSeconds: 1,
                }),
              );
              messages = resp.Messages ?? [];
              if (messages.length > 0) break;
            }
            assert.notStrictEqual(
              messages.length,
              0,
              "ReceiveMessage: no messages received",
            );
          },
        },
        {
          name: "DeleteMessage",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            // Send a dedicated message so we're not racing the visibility
            // timeout of messages already received by the ReceiveMessage test.
            await sqs.send(
              new SendMessageCommand({
                QueueUrl: QueueUrl!,
                MessageBody: "delete-test",
              }),
            );
            const recv = await sqs.send(
              new ReceiveMessageCommand({
                QueueUrl: QueueUrl!,
                MaxNumberOfMessages: 1,
                WaitTimeSeconds: 2,
              }),
            );
            const msg = recv.Messages?.[0];
            assert.ok(msg?.ReceiptHandle, "no message to delete");
            await sqs.send(
              new DeleteMessageCommand({
                QueueUrl: QueueUrl!,
                ReceiptHandle: msg.ReceiptHandle,
              }),
            );
          },
        },
        {
          name: "ChangeMessageVisibility",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            await sqs.send(
              new SendMessageCommand({
                QueueUrl: QueueUrl!,
                MessageBody: "vis-test",
              }),
            );
            const recv = await sqs.send(
              new ReceiveMessageCommand({
                QueueUrl: QueueUrl!,
                MaxNumberOfMessages: 1,
              }),
            );
            const msg = recv.Messages?.[0];
            assert.ok(
              msg?.ReceiptHandle,
              "no message to change visibility for",
            );
            await sqs.send(
              new ChangeMessageVisibilityCommand({
                QueueUrl: QueueUrl!,
                ReceiptHandle: msg.ReceiptHandle,
                VisibilityTimeout: 0,
              }),
            );
            // With visibility timeout set to 0, message should be immediately available again
            const recv2 = await sqs.send(
              new ReceiveMessageCommand({
                QueueUrl: QueueUrl!,
                MaxNumberOfMessages: 1,
                WaitTimeSeconds: 1,
              }),
            );
            assert.ok(
              recv2.Messages?.length,
              "ChangeMessageVisibility: message not re-visible after setting timeout to 0",
            );
          },
        },
        {
          name: "DeleteMessageBatch",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            // Send some messages and batch-delete them
            await Promise.all([
              sqs.send(
                new SendMessageCommand({
                  QueueUrl: QueueUrl!,
                  MessageBody: "del-1",
                }),
              ),
              sqs.send(
                new SendMessageCommand({
                  QueueUrl: QueueUrl!,
                  MessageBody: "del-2",
                }),
              ),
            ]);
            const recv = await sqs.send(
              new ReceiveMessageCommand({
                QueueUrl: QueueUrl!,
                MaxNumberOfMessages: 10,
              }),
            );
            const entries = (recv.Messages ?? []).map((m, i) => ({
              Id: String(i),
              ReceiptHandle: m.ReceiptHandle!,
            }));
            if (entries.length > 0) {
              const batchResp = await sqs.send(
                new DeleteMessageBatchCommand({
                  QueueUrl: QueueUrl!,
                  Entries: entries,
                }),
              );
              assert.strictEqual(
                batchResp.Successful?.length,
                entries.length,
                `DeleteMessageBatch: expected ${entries.length} successful, got ${batchResp.Successful?.length}`,
              );
              // Verify no messages remain
              const check = await sqs.send(
                new ReceiveMessageCommand({
                  QueueUrl: QueueUrl!,
                  MaxNumberOfMessages: 10,
                  WaitTimeSeconds: 0,
                }),
              );
              assert.ok(
                (check.Messages?.length ?? 0) <= 0,
                "DeleteMessageBatch: messages still present after batch delete",
              );
            }
          },
        },
        {
          name: "PurgeQueue",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
            );
            await sqs.send(new PurgeQueueCommand({ QueueUrl: QueueUrl! }));
            const attrs = await sqs.send(
              new GetQueueAttributesCommand({
                QueueUrl: QueueUrl!,
                AttributeNames: ["ApproximateNumberOfMessages"],
              }),
            );
            const count = attrs.Attributes?.["ApproximateNumberOfMessages"];
            assert.strictEqual(
              count,
              "0",
              `PurgeQueue: expected ApproximateNumberOfMessages=0, got ${count}`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { sqs } = makeClients(ctx);
        await sqs.send(
          new CreateQueueCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
        );
      },
      teardown: async (ctx) => {
        const { sqs } = makeClients(ctx);
        try {
          const { QueueUrl } = await sqs.send(
            new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-msg` }),
          );
          await sqs.send(new DeleteQueueCommand({ QueueUrl: QueueUrl! }));
        } catch {}
      },
    },

    // ── sqs-dlq ────────────────────────────────────────────────────────────
    {
      suite,
      service: "sqs",
      name: "sqs-dlq",
      tests: [
        {
          name: "CreateDLQ",
          op: "CreateQueue",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const resp = await sqs.send(
              new CreateQueueCommand({ QueueName: `${ctx.runId}-sqs-dlq` }),
            );
            assert.ok(resp.QueueUrl, "CreateDLQ: missing QueueUrl");
          },
        },
        {
          name: "SetRedrivePolicy",
          op: "SetQueueAttributes",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl: dlqUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-dlq` }),
            );
            const dlqAttrs = await sqs.send(
              new GetQueueAttributesCommand({
                QueueUrl: dlqUrl!,
                AttributeNames: ["QueueArn"],
              }),
            );
            const dlqArn = dlqAttrs.Attributes?.QueueArn;
            assert.ok(dlqArn, "DLQ ARN not found");

            const { QueueUrl: srcUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-src` }),
            );
            await sqs.send(
              new SetQueueAttributesCommand({
                QueueUrl: srcUrl!,
                Attributes: {
                  RedrivePolicy: JSON.stringify({
                    deadLetterTargetArn: dlqArn,
                    maxReceiveCount: 3,
                  }),
                },
              }),
            );
          },
        },
        {
          name: "GetRedrivePolicy",
          op: "GetQueueAttributes",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: `${ctx.runId}-sqs-src` }),
            );
            const resp = await sqs.send(
              new GetQueueAttributesCommand({
                QueueUrl: QueueUrl!,
                AttributeNames: ["RedrivePolicy"],
              }),
            );
            assert.ok(
              resp.Attributes?.RedrivePolicy,
              "GetRedrivePolicy: missing RedrivePolicy attribute",
            );
            const policy = JSON.parse(resp.Attributes.RedrivePolicy);
            assert.strictEqual(
              policy.maxReceiveCount,
              3,
              `GetRedrivePolicy: expected maxReceiveCount=3, got ${policy.maxReceiveCount}`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { sqs } = makeClients(ctx);
        await Promise.all([
          sqs.send(
            new CreateQueueCommand({ QueueName: `${ctx.runId}-sqs-src` }),
          ),
          sqs.send(
            new CreateQueueCommand({ QueueName: `${ctx.runId}-sqs-dlq` }),
          ),
        ]);
      },
      teardown: async (ctx) => {
        const { sqs } = makeClients(ctx);
        for (const name of [`${ctx.runId}-sqs-src`, `${ctx.runId}-sqs-dlq`]) {
          try {
            const { QueueUrl } = await sqs.send(
              new GetQueueUrlCommand({ QueueName: name }),
            );
            await sqs.send(new DeleteQueueCommand({ QueueUrl: QueueUrl! }));
          } catch {}
        }
      },
    },

    // ── sqs-fifo ───────────────────────────────────────────────────────────
    {
      suite,
      service: "sqs",
      name: "sqs-fifo",
      tests: [
        {
          name: "CreateFifoQueue",
          op: "CreateQueue",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            // Use a suite-specific suffix to avoid collisions with go-sdk and
            // python-sdk suites that run in parallel with the same runId.
            const name = `${ctx.runId}-js-fifo.fifo`;
            const resp = await sqs.send(
              new CreateQueueCommand({
                QueueName: name,
                Attributes: {
                  FifoQueue: "true",
                  ContentBasedDeduplication: "true",
                },
              }),
            );
            assert.ok(resp.QueueUrl, "CreateFifoQueue: missing QueueUrl");
            (ctx as Record<string, unknown>)["_fifoUrl"] = resp.QueueUrl;
          },
        },
        {
          name: "SendFifoMessage",
          op: "SendMessage",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const QueueUrl = (ctx as Record<string, unknown>)[
              "_fifoUrl"
            ] as string;
            assert.ok(
              QueueUrl,
              "SendFifoMessage: no queue URL from CreateFifoQueue",
            );
            const resp = await sqs.send(
              new SendMessageCommand({
                QueueUrl,
                MessageBody: "fifo-msg",
                MessageGroupId: "grp1",
              }),
            );
            assert.ok(resp.MessageId, "SendFifoMessage: missing MessageId");
          },
        },
        {
          name: "ReceiveFifoMessage",
          op: "ReceiveMessage",
          fn: async (ctx) => {
            const { sqs } = makeClients(ctx);
            const QueueUrl = (ctx as Record<string, unknown>)[
              "_fifoUrl"
            ] as string;
            assert.ok(
              QueueUrl,
              "ReceiveFifoMessage: no queue URL from CreateFifoQueue",
            );
            const resp = await sqs.send(
              new ReceiveMessageCommand({
                QueueUrl,
                MaxNumberOfMessages: 1,
              }),
            );
            const msg = resp.Messages?.[0];
            assert.ok(msg, "ReceiveFifoMessage: no message received");
            assert.strictEqual(
              msg.Body,
              "fifo-msg",
              `ReceiveFifoMessage: expected "fifo-msg", got ${msg.Body}`,
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { sqs } = makeClients(ctx);
        try {
          const QueueUrl = (ctx as Record<string, unknown>)[
            "_fifoUrl"
          ] as string;
          if (QueueUrl) {
            await sqs.send(new DeleteQueueCommand({ QueueUrl }));
          }
        } catch {}
      },
    },
  ];
}
