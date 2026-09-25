/**
 * groups/athena.ts — Athena control-plane compatibility test groups for the Node.js suite.
 *
 * Groups:
 *   athena-control — a workgroup with a result location, a query run in it, a
 *                    named query, a prepared statement and a data catalog
 *   athena-engine  — Hive DDL that writes the Glue Data Catalog, then a query
 *                    over a CSV table in S3 run on the engine
 */

import {
  BatchGetNamedQueryCommand,
  CreateDataCatalogCommand,
  CreateNamedQueryCommand,
  CreatePreparedStatementCommand,
  CreateWorkGroupCommand,
  DeleteDataCatalogCommand,
  DeleteWorkGroupCommand,
  GetDatabaseCommand,
  GetDataCatalogCommand,
  GetNamedQueryCommand,
  GetPreparedStatementCommand,
  GetQueryExecutionCommand,
  GetQueryResultsCommand,
  GetQueryRuntimeStatisticsCommand,
  GetTableMetadataCommand,
  GetWorkGroupCommand,
  InvalidRequestException,
  ListEngineVersionsCommand,
  ListQueryExecutionsCommand,
  ListWorkGroupsCommand,
  StartQueryExecutionCommand,
  UpdateWorkGroupCommand,
} from "@aws-sdk/client-athena";
import { CreateBucketCommand, DeleteBucketCommand, DeleteObjectCommand, ListObjectsV2Command, PutObjectCommand } from "@aws-sdk/client-s3";
import { DeleteDatabaseCommand } from "@aws-sdk/client-glue";
import { makeClients } from "../lib/clients.ts";
import type { TestContext, TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

const results = "s3://compat-athena-control/results/";
const statement = "compat_by_id";
const workGroup = (ctx: TestContext) => `${ctx.runId}-athena-control-wg`;
const catalog = (ctx: TestContext) => `${ctx.runId}-athena-control-catalog`;

function state(ctx: TestContext): Record<string, unknown> {
  return ctx as unknown as Record<string, unknown>;
}

async function expectInvalidRequest(what: string, call: () => Promise<unknown>): Promise<void> {
  await assert.rejects(call, (err: unknown) => {
    assert.ok(err instanceof InvalidRequestException, `${what}: want InvalidRequestException, got ${String(err)}`);
    return true;
  });
}

// athena-engine: the first query waits for the engine to be pulled and started.
const engineQueryWaitMs = 240_000;
const engineBucket = (ctx: TestContext) => `${ctx.runId}-athena-engine`;
// An identifier Hive DDL and Trino SQL both accept unquoted.
const engineDatabase = (ctx: TestContext) => `${ctx.runId.replaceAll("-", "_")}_athena_engine`;
const skipWithoutDocker =
  process.env.OVERCAST_COMPAT_SKIP_DOCKER === "1" ? "Docker not available (set OVERCAST_COMPAT_SKIP_DOCKER=0 to enable)" : false;

/** runQuery starts query and waits for it to succeed, returning its id. */
async function runQuery(ctx: TestContext, query: string): Promise<string> {
  const { athena } = makeClients(ctx);
  const out = await athena.send(
    new StartQueryExecutionCommand({
      QueryString: query,
      ResultConfiguration: { OutputLocation: `s3://${engineBucket(ctx)}/results/` },
    }),
  );
  const id = out.QueryExecutionId ?? "";
  const deadline = Date.now() + engineQueryWaitMs;
  for (;;) {
    const resp = await athena.send(new GetQueryExecutionCommand({ QueryExecutionId: id }));
    const status = resp.QueryExecution?.Status;
    if (status?.State === "SUCCEEDED") return id;
    if (status?.State === "FAILED" || status?.State === "CANCELLED") {
      throw new Error(`${query}: ${status.State}: ${status.StateChangeReason}`);
    }
    if (Date.now() > deadline) throw new Error(`${query}: still unfinished after ${engineQueryWaitMs}ms`);
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

export function makeAthenaGroups(suite: string): TestGroup[] {
  return [
    // ── athena-control ─────────────────────────────────────────────────────
    {
      suite,
      service: "athena",
      name: "athena-control",
      tests: [
        {
          name: "CreateWorkGroup",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(
              new CreateWorkGroupCommand({
                Name: workGroup(ctx),
                Configuration: { ResultConfiguration: { OutputLocation: results } },
              }),
            );
            const resp = await athena.send(new GetWorkGroupCommand({ WorkGroup: workGroup(ctx) }));
            assert.equal(resp.WorkGroup?.Name, workGroup(ctx), "GetWorkGroup: Name");
            assert.equal(resp.WorkGroup?.Configuration?.ResultConfiguration?.OutputLocation, results, "GetWorkGroup: OutputLocation");
          },
        },
        {
          name: "UpdateWorkGroup",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(
              new UpdateWorkGroupCommand({
                WorkGroup: workGroup(ctx),
                Description: "compat",
                ConfigurationUpdates: { EnforceWorkGroupConfiguration: true },
              }),
            );
            const resp = await athena.send(new GetWorkGroupCommand({ WorkGroup: workGroup(ctx) }));
            assert.equal(resp.WorkGroup?.Description, "compat", "GetWorkGroup: Description");
            assert.equal(resp.WorkGroup?.Configuration?.EnforceWorkGroupConfiguration, true, "GetWorkGroup: Enforce");
          },
        },
        {
          name: "ListWorkGroups",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const resp = await athena.send(new ListWorkGroupsCommand({}));
            const names = (resp.WorkGroups ?? []).map((w) => w.Name);
            assert.ok(names.includes(workGroup(ctx)), `ListWorkGroups: ${names} lacks ${workGroup(ctx)}`);
            assert.ok(names.includes("primary"), `ListWorkGroups: ${names} lacks primary`);
          },
        },
        {
          name: "StartQueryExecution",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const out = await athena.send(new StartQueryExecutionCommand({ QueryString: "SELECT 1", WorkGroup: workGroup(ctx) }));
            assert.ok(out.QueryExecutionId, "StartQueryExecution: QueryExecutionId");
            state(ctx).athenaQueryId = out.QueryExecutionId;
            const resp = await athena.send(new GetQueryExecutionCommand({ QueryExecutionId: out.QueryExecutionId }));
            assert.equal(resp.QueryExecution?.WorkGroup, workGroup(ctx), "GetQueryExecution: WorkGroup");
            assert.equal(
              resp.QueryExecution?.ResultConfiguration?.OutputLocation,
              `${results}${out.QueryExecutionId}.csv`,
              "GetQueryExecution: OutputLocation is the result object",
            );
            assert.ok(resp.QueryExecution?.Status?.State, "GetQueryExecution: State");
          },
        },
        {
          name: "ListQueryExecutions",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const resp = await athena.send(new ListQueryExecutionsCommand({ WorkGroup: workGroup(ctx) }));
            const id = state(ctx).athenaQueryId as string;
            assert.ok(resp.QueryExecutionIds?.includes(id), `ListQueryExecutions: lacks ${id}`);
          },
        },
        {
          name: "CreateNamedQuery",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const out = await athena.send(
              new CreateNamedQueryCommand({
                Name: "compat-query",
                Database: "compat_db",
                QueryString: "SELECT 1",
                WorkGroup: workGroup(ctx),
              }),
            );
            state(ctx).athenaNamedQueryId = out.NamedQueryId;
            const resp = await athena.send(new GetNamedQueryCommand({ NamedQueryId: out.NamedQueryId }));
            assert.equal(resp.NamedQuery?.Name, "compat-query", "GetNamedQuery: Name");
            assert.equal(resp.NamedQuery?.WorkGroup, workGroup(ctx), "GetNamedQuery: WorkGroup");
          },
        },
        {
          name: "BatchGetNamedQuery",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const id = state(ctx).athenaNamedQueryId as string;
            const resp = await athena.send(new BatchGetNamedQueryCommand({ NamedQueryIds: [id, "compat-missing-id"] }));
            assert.equal(resp.NamedQueries?.length, 1, "BatchGetNamedQuery: found");
            assert.equal(resp.NamedQueries?.[0]?.NamedQueryId, id, "BatchGetNamedQuery: id");
            assert.equal(resp.UnprocessedNamedQueryIds?.length, 1, "BatchGetNamedQuery: unprocessed");
          },
        },
        {
          name: "CreatePreparedStatement",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(
              new CreatePreparedStatementCommand({
                StatementName: statement,
                WorkGroup: workGroup(ctx),
                QueryStatement: "SELECT * FROM t WHERE id = ?",
              }),
            );
            const resp = await athena.send(new GetPreparedStatementCommand({ StatementName: statement, WorkGroup: workGroup(ctx) }));
            assert.equal(resp.PreparedStatement?.QueryStatement, "SELECT * FROM t WHERE id = ?", "GetPreparedStatement: QueryStatement");
          },
        },
        {
          name: "CreateDataCatalog",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(
              new CreateDataCatalogCommand({
                Name: catalog(ctx),
                Type: "HIVE",
                Parameters: { "metadata-function": "arn:aws:lambda:us-east-1:000000000000:function:compat-meta" },
              }),
            );
            const resp = await athena.send(new GetDataCatalogCommand({ Name: catalog(ctx) }));
            assert.equal(resp.DataCatalog?.Name, catalog(ctx), "GetDataCatalog: Name");
            assert.equal(resp.DataCatalog?.Type, "HIVE", "GetDataCatalog: Type");
          },
        },
        {
          name: "ListEngineVersions",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const resp = await athena.send(new ListEngineVersionsCommand({}));
            const selected = (resp.EngineVersions ?? []).map((v) => v.SelectedEngineVersion);
            assert.ok(selected.includes("AUTO"), `ListEngineVersions: ${selected} lacks AUTO`);
          },
        },
        {
          name: "DeleteDataCatalog",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(new DeleteDataCatalogCommand({ Name: catalog(ctx) }));
            await expectInvalidRequest("GetDataCatalog after DeleteDataCatalog", () =>
              athena.send(new GetDataCatalogCommand({ Name: catalog(ctx) })),
            );
          },
        },
        {
          name: "DeleteWorkGroup",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await athena.send(new DeleteWorkGroupCommand({ WorkGroup: workGroup(ctx), RecursiveDeleteOption: true }));
            await expectInvalidRequest("GetWorkGroup after DeleteWorkGroup", () =>
              athena.send(new GetWorkGroupCommand({ WorkGroup: workGroup(ctx) })),
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { athena } = makeClients(ctx);
        try {
          await athena.send(new DeleteDataCatalogCommand({ Name: catalog(ctx) }));
        } catch {}
        // RecursiveDeleteOption removes the named query and prepared statement with it.
        try {
          await athena.send(new DeleteWorkGroupCommand({ WorkGroup: workGroup(ctx), RecursiveDeleteOption: true }));
        } catch {}
      },
    },
    // ── athena-engine ──────────────────────────────────────────────────────
    {
      suite,
      service: "athena",
      name: "athena-engine",
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(new CreateBucketCommand({ Bucket: engineBucket(ctx) }));
      },
      tests: [
        {
          name: "CreateDatabaseStatement",
          op: "StartQueryExecution",
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            await runQuery(ctx, `CREATE DATABASE ${engineDatabase(ctx)}`);
            const resp = await athena.send(new GetDatabaseCommand({ CatalogName: "AwsDataCatalog", DatabaseName: engineDatabase(ctx) }));
            assert.equal(resp.Database?.Name, engineDatabase(ctx), "GetDatabase: Name");
          },
        },
        {
          name: "CreateExternalTableStatement",
          op: "StartQueryExecution",
          depends: ["CreateDatabaseStatement"],
          fn: async (ctx) => {
            const { athena, s3 } = makeClients(ctx);
            await s3.send(new PutObjectCommand({ Bucket: engineBucket(ctx), Key: "people/part-0.csv", Body: "1,alice\n2,bob\n" }));
            await runQuery(
              ctx,
              `CREATE EXTERNAL TABLE ${engineDatabase(ctx)}.people (id int, name string) ` +
                `ROW FORMAT DELIMITED FIELDS TERMINATED BY ',' LOCATION 's3://${engineBucket(ctx)}/people/'`,
            );
            const resp = await athena.send(
              new GetTableMetadataCommand({ CatalogName: "AwsDataCatalog", DatabaseName: engineDatabase(ctx), TableName: "people" }),
            );
            const cols = resp.TableMetadata?.Columns ?? [];
            assert.deepEqual(cols.map((c) => c.Name), ["id", "name"], "GetTableMetadata: columns");
            assert.equal(cols[1]?.Type, "string", "GetTableMetadata: name type");
          },
        },
        {
          name: "SelectFromTable",
          op: "GetQueryResults",
          depends: ["CreateExternalTableStatement"],
          skip: skipWithoutDocker,
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const id = await runQuery(ctx, `SELECT id, name FROM ${engineDatabase(ctx)}.people ORDER BY id`);
            state(ctx).athenaEngineQuery = id;
            const resp = await athena.send(new GetQueryResultsCommand({ QueryExecutionId: id }));
            const rows = (resp.ResultSet?.Rows ?? []).map((r) => (r.Data ?? []).map((d) => d.VarCharValue));
            assert.deepEqual(rows, [["id", "name"], ["1", "alice"], ["2", "bob"]], "GetQueryResults: the header then the rows");
            const types = (resp.ResultSet?.ResultSetMetadata?.ColumnInfo ?? []).map((c) => c.Type);
            assert.deepEqual(types, ["integer", "varchar"], "GetQueryResults: column types");
          },
        },
        {
          name: "GetQueryRuntimeStatistics",
          depends: ["SelectFromTable"],
          skip: skipWithoutDocker,
          fn: async (ctx) => {
            const { athena } = makeClients(ctx);
            const resp = await athena.send(
              new GetQueryRuntimeStatisticsCommand({ QueryExecutionId: state(ctx).athenaEngineQuery as string }),
            );
            assert.equal(resp.QueryRuntimeStatistics?.Rows?.OutputRows, 2, "GetQueryRuntimeStatistics: OutputRows");
            assert.ok(resp.QueryRuntimeStatistics?.Timeline, "GetQueryRuntimeStatistics: Timeline");
          },
        },
      ],
      teardown: async (ctx) => {
        const { glue, s3 } = makeClients(ctx);
        try {
          await glue.send(new DeleteDatabaseCommand({ Name: engineDatabase(ctx) }));
        } catch {}
        try {
          const listed = await s3.send(new ListObjectsV2Command({ Bucket: engineBucket(ctx) }));
          for (const o of listed.Contents ?? []) {
            await s3.send(new DeleteObjectCommand({ Bucket: engineBucket(ctx), Key: o.Key }));
          }
          await s3.send(new DeleteBucketCommand({ Bucket: engineBucket(ctx) }));
        } catch {}
      },
    },
  ];
}
