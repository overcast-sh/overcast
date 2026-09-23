/**
 * groups/cloudwatch-logs.ts — CloudWatch Logs test groups for Node.js suite.
 *
 * Groups:
 *   logs-events  — PutLogEvents, GetLogEvents, FilterLogEvents (implemented)
 *
 * logs-groups is not here: it is a ported group, resolved from
 * compat/model/authored/logs-groups.json by the scenario backend (#1116).
 */

import {
  CreateLogGroupCommand,
  DeleteLogGroupCommand,
  CreateLogStreamCommand,
  DeleteLogStreamCommand,
  DescribeLogStreamsCommand,
  PutLogEventsCommand,
  GetLogEventsCommand,
  FilterLogEventsCommand,
} from "@aws-sdk/client-cloudwatch-logs";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

export function makeCloudWatchLogsGroups(suite: string): TestGroup[] {
  return [
    // ── logs-events ────────────────────────────────────────────────────────
    {
      suite,
      service: "cloudwatch-logs",
      name: "logs-events",
      tests: [
        {
          name: "PutLogEvents",
          fn: async (ctx) => {
            const { logs } = makeClients(ctx);
            const groupName = `/overcast/${ctx.runId}/events`;
            const streamName = "stream-1";
            const resp = await logs.send(
              new PutLogEventsCommand({
                logGroupName: groupName,
                logStreamName: streamName,
                logEvents: [
                  { timestamp: Date.now() - 2000, message: "first log event" },
                  { timestamp: Date.now() - 1000, message: "second log event" },
                  {
                    timestamp: Date.now(),
                    message: JSON.stringify({
                      level: "info",
                      msg: "structured",
                    }),
                  },
                ],
              }),
            );
            assert.ok(
              !resp.rejectedLogEventsInfo,
              "PutLogEvents: events rejected as too old",
            );
          },
        },
        {
          name: "GetLogEvents",
          fn: async (ctx) => {
            const { logs } = makeClients(ctx);
            const groupName = `/overcast/${ctx.runId}/events`;
            const streamName = "stream-1";
            const resp = await logs.send(
              new GetLogEventsCommand({
                logGroupName: groupName,
                logStreamName: streamName,
              }),
            );
            assert.ok(
              (resp.events?.length ?? 0) >= 3,
              `GetLogEvents: expected >=3 events, got ${resp.events?.length}`,
            );
          },
        },
        {
          name: "FilterLogEvents",
          fn: async (ctx) => {
            const { logs } = makeClients(ctx);
            const groupName = `/overcast/${ctx.runId}/events`;
            const resp = await logs.send(
              new FilterLogEventsCommand({
                logGroupName: groupName,
                filterPattern: "structured",
              }),
            );
            assert.notStrictEqual(
              resp.events?.length ?? 0,
              0,
              "FilterLogEvents: expected at least one matching event",
            );
          },
        },
        {
          name: "DescribeLogStreams",
          fn: async (ctx) => {
            const { logs } = makeClients(ctx);
            const groupName = `/overcast/${ctx.runId}/events`;
            const resp = await logs.send(
              new DescribeLogStreamsCommand({ logGroupName: groupName }),
            );
            assert.ok(
              resp.logStreams?.some((s) => s.logStreamName === "stream-1"),
              "DescribeLogStreams: stream-1 not found",
            );
          },
        },
        {
          name: "DeleteLogStream",
          fn: async (ctx) => {
            const { logs } = makeClients(ctx);
            const groupName = `/overcast/${ctx.runId}/events`;
            await logs.send(
              new DeleteLogStreamCommand({
                logGroupName: groupName,
                logStreamName: "stream-1",
              }),
            );
            const resp = await logs.send(
              new DescribeLogStreamsCommand({ logGroupName: groupName }),
            );
            assert.notStrictEqual(
              resp.logStreams?.some((s) => s.logStreamName, "stream-1"),
              "DeleteLogStream: stream-1 still present after delete",
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { logs } = makeClients(ctx);
        const groupName = `/overcast/${ctx.runId}/events`;
        await logs.send(new CreateLogGroupCommand({ logGroupName: groupName }));
        await logs.send(
          new CreateLogStreamCommand({
            logGroupName: groupName,
            logStreamName: "stream-1",
          }),
        );
      },
      teardown: async (ctx) => {
        const { logs } = makeClients(ctx);
        const groupName = `/overcast/${ctx.runId}/events`;
        try {
          await logs.send(
            new DeleteLogGroupCommand({ logGroupName: groupName }),
          );
        } catch {}
      },
    },
  ];
}
