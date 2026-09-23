/**
 * groups/glue.ts — Glue Data Catalog compatibility test groups for the Node.js suite.
 *
 * Groups:
 *   glue-catalog — one partitioned table driven through its definition, an
 *                  optimistic-concurrency update, its versions and its partitions
 */

import {
  BatchCreatePartitionCommand,
  ConcurrentModificationException,
  CreateDatabaseCommand,
  CreateTableCommand,
  DeleteDatabaseCommand,
  DeletePartitionCommand,
  DeleteTableCommand,
  EntityNotFoundException,
  GetDatabaseCommand,
  GetPartitionCommand,
  GetPartitionsCommand,
  GetTableCommand,
  GetTableVersionsCommand,
  UpdateTableCommand,
  type TableInput,
} from "@aws-sdk/client-glue";
import { makeClients } from "../lib/clients.ts";
import type { TestContext, TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

// Glue folds names to lowercase; the run id already is, so these round-trip.
const database = (ctx: TestContext) => `${ctx.runId}-glue-catalog-db`;
const table = (ctx: TestContext) => `${ctx.runId}-glue-catalog-events`;

function tableInput(ctx: TestContext, parameters: Record<string, string>): TableInput {
  return {
    Name: table(ctx),
    TableType: "EXTERNAL_TABLE",
    Parameters: parameters,
    PartitionKeys: [
      { Name: "year", Type: "int" },
      { Name: "month", Type: "string" },
    ],
    StorageDescriptor: {
      Location: "s3://compat-glue-catalog/events/",
      Columns: [
        { Name: "id", Type: "bigint" },
        { Name: "payload", Type: "string" },
      ],
      SerdeInfo: { SerializationLibrary: "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe" },
    },
  };
}

function state(ctx: TestContext): Record<string, unknown> {
  return ctx as unknown as Record<string, unknown>;
}

async function expectNotFound(what: string, call: () => Promise<unknown>): Promise<void> {
  await assert.rejects(call, (err: unknown) => {
    assert.ok(err instanceof EntityNotFoundException, `${what}: want EntityNotFoundException, got ${String(err)}`);
    return true;
  });
}

export function makeGlueGroups(suite: string): TestGroup[] {
  return [
    // ── glue-catalog ───────────────────────────────────────────────────────
    {
      suite,
      service: "glue",
      name: "glue-catalog",
      tests: [
        {
          name: "CreateDatabase",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            await glue.send(new CreateDatabaseCommand({ DatabaseInput: { Name: database(ctx), Description: "compat" } }));
            const resp = await glue.send(new GetDatabaseCommand({ Name: database(ctx) }));
            assert.equal(resp.Database?.Name, database(ctx), "GetDatabase: Name");
            assert.equal(resp.Database?.Description, "compat", "GetDatabase: Description");
          },
        },
        {
          name: "CreateTable",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            await glue.send(
              new CreateTableCommand({ DatabaseName: database(ctx), TableInput: tableInput(ctx, { classification: "parquet" }) }),
            );
          },
        },
        {
          name: "GetTable",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const { Table: t } = await glue.send(new GetTableCommand({ DatabaseName: database(ctx), Name: table(ctx) }));
            assert.equal(t?.StorageDescriptor?.Location, "s3://compat-glue-catalog/events/", "GetTable: Location");
            assert.equal(t?.StorageDescriptor?.Columns?.length, 2, "GetTable: Columns");
            assert.equal(t?.PartitionKeys?.length, 2, "GetTable: PartitionKeys");
            assert.equal(t?.Parameters?.classification, "parquet", "GetTable: Parameters");
            assert.ok(t?.VersionId && t.CreateTime, "GetTable: missing VersionId or CreateTime");
            state(ctx)["_glueVersion"] = t.VersionId;
          },
        },
        {
          name: "UpdateTable",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const read = state(ctx)["_glueVersion"] as string;
            await glue.send(
              new UpdateTableCommand({
                DatabaseName: database(ctx),
                VersionId: read,
                TableInput: tableInput(ctx, { classification: "parquet", compat: "updated" }),
              }),
            );
            const { Table: t } = await glue.send(new GetTableCommand({ DatabaseName: database(ctx), Name: table(ctx) }));
            assert.equal(t?.Parameters?.compat, "updated", "UpdateTable: Parameters");
            assert.notEqual(t?.VersionId, read, "UpdateTable: VersionId did not move");
          },
        },
        {
          // A commit against the version the table had before UpdateTable is
          // refused, which is how Iceberg's Glue catalog detects a lost race.
          name: "UpdateTableStaleVersion",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            await assert.rejects(
              glue.send(
                new UpdateTableCommand({
                  DatabaseName: database(ctx),
                  VersionId: state(ctx)["_glueVersion"] as string,
                  TableInput: tableInput(ctx, { compat: "stale" }),
                }),
              ),
              (err: unknown) => {
                assert.ok(err instanceof ConcurrentModificationException, `stale UpdateTable: got ${String(err)}`);
                return true;
              },
            );
          },
        },
        {
          name: "GetTableVersions",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const resp = await glue.send(new GetTableVersionsCommand({ DatabaseName: database(ctx), TableName: table(ctx) }));
            const read = state(ctx)["_glueVersion"] as string;
            assert.ok(
              (resp.TableVersions ?? []).some((v) => v.VersionId === read),
              `GetTableVersions: the pre-update version ${read} is not listed`,
            );
          },
        },
        {
          name: "BatchCreatePartition",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const resp = await glue.send(
              new BatchCreatePartitionCommand({
                DatabaseName: database(ctx),
                TableName: table(ctx),
                PartitionInputList: [
                  ["2023", "12"],
                  ["2024", "01"],
                  ["2024", "02"],
                ].map(([y, m]) => ({
                  Values: [y, m],
                  StorageDescriptor: { Location: `s3://compat-glue-catalog/events/year=${y}/month=${m}/` },
                })),
              }),
            );
            assert.equal(resp.Errors?.length ?? 0, 0, "BatchCreatePartition: errors");
          },
        },
        {
          name: "GetPartitions",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const resp = await glue.send(
              new GetPartitionsCommand({
                DatabaseName: database(ctx),
                TableName: table(ctx),
                Expression: "year = 2024 AND month IN ('01', '02')",
              }),
            );
            const got = (resp.Partitions ?? []).map((p) => (p.Values ?? []).join("/")).sort();
            assert.deepEqual(got, ["2024/01", "2024/02"], "GetPartitions: matched");
          },
        },
        {
          name: "DeletePartition",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            const key = { DatabaseName: database(ctx), TableName: table(ctx), PartitionValues: ["2023", "12"] };
            await glue.send(new DeletePartitionCommand(key));
            await expectNotFound("GetPartition after DeletePartition", () => glue.send(new GetPartitionCommand(key)));
          },
        },
        {
          name: "DeleteTable",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            await glue.send(new DeleteTableCommand({ DatabaseName: database(ctx), Name: table(ctx) }));
            await expectNotFound("GetTable after DeleteTable", () =>
              glue.send(new GetTableCommand({ DatabaseName: database(ctx), Name: table(ctx) })),
            );
          },
        },
        {
          name: "DeleteDatabase",
          fn: async (ctx) => {
            const { glue } = makeClients(ctx);
            await glue.send(new DeleteDatabaseCommand({ Name: database(ctx) }));
            await expectNotFound("GetDatabase after DeleteDatabase", () =>
              glue.send(new GetDatabaseCommand({ Name: database(ctx) })),
            );
          },
        },
      ],
      teardown: async (ctx) => {
        // DeleteDatabase removes the table and its partitions with it.
        const { glue } = makeClients(ctx);
        try {
          await glue.send(new DeleteDatabaseCommand({ Name: database(ctx) }));
        } catch {}
      },
    },
  ];
}
