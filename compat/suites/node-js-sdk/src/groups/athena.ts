/**
 * groups/athena.ts — Athena control-plane compatibility test groups for the Node.js suite.
 *
 * Groups:
 *   athena-control — a workgroup with a result location, a query run in it, a
 *                    named query, a prepared statement and a data catalog
 */

import {
  BatchGetNamedQueryCommand,
  CreateDataCatalogCommand,
  CreateNamedQueryCommand,
  CreatePreparedStatementCommand,
  CreateWorkGroupCommand,
  DeleteDataCatalogCommand,
  DeleteWorkGroupCommand,
  GetDataCatalogCommand,
  GetNamedQueryCommand,
  GetPreparedStatementCommand,
  GetQueryExecutionCommand,
  GetWorkGroupCommand,
  InvalidRequestException,
  ListEngineVersionsCommand,
  ListQueryExecutionsCommand,
  ListWorkGroupsCommand,
  StartQueryExecutionCommand,
  UpdateWorkGroupCommand,
} from "@aws-sdk/client-athena";
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
  ];
}
