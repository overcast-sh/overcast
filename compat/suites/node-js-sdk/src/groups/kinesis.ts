/**
 * groups/kinesis.ts — Kinesis Data Streams compat groups for node-js-sdk.
 *
 * Status: NOT implemented in Overcast. Tests expected to fail with 501.
 *
 * Groups:
 *   kinesis-records — PutRecord / PutRecords / GetRecords
 *   kinesis-shards  — shard management (split, merge, list)
 *
 * kinesis-streams resolves through its authored scenario
 * (compat/model/authored/kinesis-streams.json).
 */

import {
  CreateStreamCommand,
  DeleteStreamCommand,
  DescribeStreamSummaryCommand,
  PutRecordCommand,
  PutRecordsCommand,
  GetShardIteratorCommand,
  GetRecordsCommand,
  ListShardsCommand,
  SplitShardCommand,
  MergeShardsCommand,
  StreamStatus,
  ShardIteratorType,
} from "@aws-sdk/client-kinesis";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

/** Poll until the stream is ACTIVE or throw after maxAttempts. */
async function waitForActive(
  kinesis: ReturnType<typeof makeClients>["kinesis"],
  streamName: string,
  maxAttempts = 10,
): Promise<void> {
  for (let i = 0; i < maxAttempts; i++) {
    const resp = await kinesis.send(
      new DescribeStreamSummaryCommand({ StreamName: streamName }),
    );
    if (resp.StreamDescriptionSummary?.StreamStatus === StreamStatus.ACTIVE)
      return;
    await new Promise<void>((r) => globalThis.setTimeout(r, 300));
  }
  throw new Error(`stream ${streamName} did not become ACTIVE`);
}

export function makeKinesisGroups(suite: string): TestGroup[] {
  return [
    // ── kinesis-records ────────────────────────────────────────────────────
    {
      suite,
      service: "kinesis",
      name: "kinesis-records",
      setup: async (ctx) => {
        const { kinesis } = makeClients(ctx);
        await kinesis.send(
          new CreateStreamCommand({
            StreamName: `${ctx.runId}-rec`,
            ShardCount: 1,
          }),
        );
        await waitForActive(kinesis, `${ctx.runId}-rec`);
      },
      tests: [
        {
          name: "PutRecord",
          fn: async (ctx) => {
            const { kinesis } = makeClients(ctx);
            const resp = await kinesis.send(
              new PutRecordCommand({
                StreamName: `${ctx.runId}-rec`,
                Data: Buffer.from(JSON.stringify({ msg: "hello" })),
                PartitionKey: "pk1",
              }),
            );
            assert.ok(resp.SequenceNumber, "PutRecord: missing SequenceNumber");
            (ctx as Record<string, unknown>)["_shardId"] = resp.ShardId;
          },
        },
        {
          name: "PutRecords",
          fn: async (ctx) => {
            const { kinesis } = makeClients(ctx);
            const resp = await kinesis.send(
              new PutRecordsCommand({
                StreamName: `${ctx.runId}-rec`,
                Records: [
                  { Data: Buffer.from("record-1"), PartitionKey: "pk1" },
                  { Data: Buffer.from("record-2"), PartitionKey: "pk2" },
                  { Data: Buffer.from("record-3"), PartitionKey: "pk3" },
                ],
              }),
            );
            assert.strictEqual(
              resp.FailedRecordCount,
              0,
              `PutRecords: ${resp.FailedRecordCount} failed records`,
            );
          },
        },
        {
          name: "GetShardIterator",
          fn: async (ctx) => {
            const shardId =
              ((ctx as Record<string, unknown>)["_shardId"] as string) ??
              "shardId-000000000000";
            const { kinesis } = makeClients(ctx);
            const resp = await kinesis.send(
              new GetShardIteratorCommand({
                StreamName: `${ctx.runId}-rec`,
                ShardId: shardId,
                ShardIteratorType: ShardIteratorType.TRIM_HORIZON,
              }),
            );
            assert.ok(
              resp.ShardIterator,
              "GetShardIterator: missing ShardIterator",
            );
            (ctx as Record<string, unknown>)["_iterator"] = resp.ShardIterator;
          },
        },
        {
          name: "GetRecords",
          fn: async (ctx) => {
            const iterator = (ctx as Record<string, unknown>)[
              "_iterator"
            ] as string;
            assert.ok(iterator, "no ShardIterator from previous step");
            const { kinesis } = makeClients(ctx);
            const resp = await kinesis.send(
              new GetRecordsCommand({ ShardIterator: iterator, Limit: 10 }),
            );
            assert.notStrictEqual(
              resp.Records?.length ?? 0,
              0,
              "GetRecords: expected at least 1 record",
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { kinesis } = makeClients(ctx);
        try {
          await kinesis.send(
            new DeleteStreamCommand({ StreamName: `${ctx.runId}-rec` }),
          );
        } catch {}
      },
    },

    // ── kinesis-shards ─────────────────────────────────────────────────────
    {
      suite,
      service: "kinesis",
      name: "kinesis-shards",
      setup: async (ctx) => {
        const { kinesis } = makeClients(ctx);
        await kinesis.send(
          new CreateStreamCommand({
            StreamName: `${ctx.runId}-shrd`,
            ShardCount: 2,
          }),
        );
        await waitForActive(kinesis, `${ctx.runId}-shrd`);
      },
      tests: [
        {
          name: "ListShards",
          fn: async (ctx) => {
            const { kinesis } = makeClients(ctx);
            const resp = await kinesis.send(
              new ListShardsCommand({ StreamName: `${ctx.runId}-shrd` }),
            );
            assert.ok(
              (resp.Shards?.length ?? 0) >= 2,
              `ListShards: expected ≥2 shards, got ${resp.Shards?.length}`,
            );
            (ctx as Record<string, unknown>)["_shards"] = resp.Shards;
          },
        },
        {
          name: "SplitShard",
          fn: async (ctx) => {
            const shards = (ctx as Record<string, unknown>)[
              "_shards"
            ] as Array<{
              ShardId: string;
              HashKeyRange: { StartingHashKey: string; EndingHashKey: string };
            }>;
            assert.ok(shards?.length, "no shards");
            const { kinesis } = makeClients(ctx);
            const shard = shards[0];
            const start = BigInt(shard.HashKeyRange.StartingHashKey);
            const end = BigInt(shard.HashKeyRange.EndingHashKey);
            const mid = ((start + end) / 2n).toString();
            await kinesis.send(
              new SplitShardCommand({
                StreamName: `${ctx.runId}-shrd`,
                ShardToSplit: shard.ShardId,
                NewStartingHashKey: mid,
              }),
            );
            await waitForActive(kinesis, `${ctx.runId}-shrd`);
            const resp = await kinesis.send(
              new ListShardsCommand({ StreamName: `${ctx.runId}-shrd` }),
            );
            // After split, should have more shards than before (original 2 + 2 from split - 1 closed = 3 open, plus closed)
            const openShards = (resp.Shards ?? []).filter(
              (s) => !s.SequenceNumberRange?.EndingSequenceNumber,
            );
            assert.ok(
              openShards.length >= 3,
              `SplitShard: expected ≥3 open shards, got ${openShards.length}`,
            );
          },
        },
        {
          name: "MergeShards",
          fn: async (ctx) => {
            const { kinesis } = makeClients(ctx);
            // List open shards and merge the first two adjacent ones.
            const listResp = await kinesis.send(
              new ListShardsCommand({ StreamName: `${ctx.runId}-shrd` }),
            );
            const openShards = (listResp.Shards ?? []).filter(
              (s) => !s.SequenceNumberRange?.EndingSequenceNumber,
            );
            assert.ok(
              openShards.length >= 2,
              `MergeShards: need ≥2 open shards, got ${openShards.length}`,
            );
            await kinesis.send(
              new MergeShardsCommand({
                StreamName: `${ctx.runId}-shrd`,
                ShardToMerge: openShards[0].ShardId,
                AdjacentShardToMerge: openShards[1].ShardId,
              }),
            );
            await waitForActive(kinesis, `${ctx.runId}-shrd`);
            const afterResp = await kinesis.send(
              new ListShardsCommand({ StreamName: `${ctx.runId}-shrd` }),
            );
            const afterOpen = (afterResp.Shards ?? []).filter(
              (s) => !s.SequenceNumberRange?.EndingSequenceNumber,
            );
            assert.ok(
              afterOpen.length < openShards.length,
              `MergeShards: expected fewer open shards after merge, got ${afterOpen.length}`,
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { kinesis } = makeClients(ctx);
        try {
          await kinesis.send(
            new DeleteStreamCommand({ StreamName: `${ctx.runId}-shrd` }),
          );
        } catch {}
      },
    },
  ];
}
