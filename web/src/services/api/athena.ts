import {
  BatchGetNamedQueryCommand,
  BatchGetQueryExecutionCommand,
  CreateDataCatalogCommand,
  CreateNamedQueryCommand,
  CreateWorkGroupCommand,
  DeleteDataCatalogCommand,
  DeleteNamedQueryCommand,
  DeleteWorkGroupCommand,
  GetQueryExecutionCommand,
  GetQueryResultsCommand,
  GetQueryRuntimeStatisticsCommand,
  GetWorkGroupCommand,
  ListQueryExecutionsCommand,
  StartQueryExecutionCommand,
  StopQueryExecutionCommand,
  UpdateNamedQueryCommand,
  UpdateWorkGroupCommand,
  paginateListDataCatalogs,
  paginateListDatabases,
  paginateListNamedQueries,
  paginateListPreparedStatements,
  paginateListTableMetadata,
  paginateListWorkGroups,
  type CreateDataCatalogInput,
  type CreateNamedQueryInput,
  type CreateWorkGroupInput,
  type DataCatalogSummary,
  type Database,
  type GetQueryResultsOutput,
  type NamedQuery,
  type PreparedStatementSummary,
  type QueryExecution,
  type QueryRuntimeStatistics,
  type StartQueryExecutionInput,
  type TableMetadata,
  type UpdateNamedQueryInput,
  type UpdateWorkGroupInput,
  type WorkGroup,
  type WorkGroupSummary,
} from "@aws-sdk/client-athena"
import type { AthenaEngineStatus } from "@/types"
import { awsClients } from "../aws-clients"
import { endpointStore } from "../endpoint-store"
import { collectPages } from "./paginate"

/** BatchGetNamedQuery's and BatchGetQueryExecution's limit on ids per call. */
const BATCH_LIMIT = 50

/**
 * The most recent executions History reads per workgroup. `ListQueryExecutions`
 * returns newest first, so this is the latest few hundred, which is what a
 * developer scrolling History is after.
 */
export const HISTORY_LIMIT = 200

function chunks<T>(items: readonly T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size))
  return out
}

/** Runs `perWorkGroup` for every workgroup and concatenates the results. */
async function acrossWorkGroups<T>(perWorkGroup: (name: string) => Promise<T[]>): Promise<T[]> {
  const workGroups = await athena.listWorkGroups()
  const lists = await Promise.all(workGroups.map((wg) => perWorkGroup(wg.Name ?? "")))
  return lists.flat()
}

export const athena = {
  // ─── Workgroups ────────────────────────────────────────────────────────

  listWorkGroups: (): Promise<WorkGroupSummary[]> =>
    collectPages(
      paginateListWorkGroups({ client: awsClients.athena() }, {}),
      (page) => page.WorkGroups,
    ),

  getWorkGroup: async (name: string): Promise<WorkGroup> => {
    const out = await awsClients.athena().send(new GetWorkGroupCommand({ WorkGroup: name }))
    return out.WorkGroup ?? { Name: name }
  },

  createWorkGroup: async (input: CreateWorkGroupInput): Promise<void> => {
    await awsClients.athena().send(new CreateWorkGroupCommand(input))
  },

  updateWorkGroup: async (input: UpdateWorkGroupInput): Promise<void> => {
    await awsClients.athena().send(new UpdateWorkGroupCommand(input))
  },

  /** Deletes the workgroup and, as the AWS console's delete does, its saved queries. */
  deleteWorkGroup: async (name: string): Promise<void> => {
    await awsClients
      .athena()
      .send(new DeleteWorkGroupCommand({ WorkGroup: name, RecursiveDeleteOption: true }))
  },

  listPreparedStatements: (workGroup: string): Promise<PreparedStatementSummary[]> =>
    collectPages(
      paginateListPreparedStatements({ client: awsClients.athena() }, { WorkGroup: workGroup }),
      (page) => page.PreparedStatements,
    ),

  // ─── Saved queries ─────────────────────────────────────────────────────

  /** The saved queries of one workgroup, in full. */
  listNamedQueries: async (workGroup: string): Promise<NamedQuery[]> => {
    const client = awsClients.athena()
    const ids = await collectPages(
      paginateListNamedQueries({ client }, { WorkGroup: workGroup }),
      (page) => page.NamedQueryIds,
    )
    const pages = await Promise.all(
      chunks(ids, BATCH_LIMIT).map((NamedQueryIds) =>
        client.send(new BatchGetNamedQueryCommand({ NamedQueryIds })),
      ),
    )
    return pages.flatMap((page) => page.NamedQueries ?? [])
  },

  /** The saved queries of every workgroup. */
  listAllNamedQueries: (): Promise<NamedQuery[]> => acrossWorkGroups(athena.listNamedQueries),

  createNamedQuery: async (input: CreateNamedQueryInput): Promise<string> => {
    const out = await awsClients.athena().send(new CreateNamedQueryCommand(input))
    return out.NamedQueryId ?? ""
  },

  updateNamedQuery: async (input: UpdateNamedQueryInput): Promise<void> => {
    await awsClients.athena().send(new UpdateNamedQueryCommand(input))
  },

  deleteNamedQuery: async (id: string): Promise<void> => {
    await awsClients.athena().send(new DeleteNamedQueryCommand({ NamedQueryId: id }))
  },

  // ─── Query executions ──────────────────────────────────────────────────

  startQueryExecution: async (input: StartQueryExecutionInput): Promise<string> => {
    const out = await awsClients.athena().send(new StartQueryExecutionCommand(input))
    return out.QueryExecutionId ?? ""
  },

  stopQueryExecution: async (id: string): Promise<void> => {
    await awsClients.athena().send(new StopQueryExecutionCommand({ QueryExecutionId: id }))
  },

  getQueryExecution: async (id: string): Promise<QueryExecution> => {
    const out = await awsClients
      .athena()
      .send(new GetQueryExecutionCommand({ QueryExecutionId: id }))
    return out.QueryExecution ?? { QueryExecutionId: id }
  },

  /** One page of a result, from `token` (none for the first). */
  getQueryResults: (
    id: string,
    token: string | undefined,
    signal?: AbortSignal,
  ): Promise<GetQueryResultsOutput> =>
    awsClients
      .athena()
      .send(new GetQueryResultsCommand({ QueryExecutionId: id, NextToken: token }), {
        abortSignal: signal,
      }),

  getQueryRuntimeStatistics: async (id: string): Promise<QueryRuntimeStatistics> => {
    const out = await awsClients
      .athena()
      .send(new GetQueryRuntimeStatisticsCommand({ QueryExecutionId: id }))
    return out.QueryRuntimeStatistics ?? {}
  },

  /** A workgroup's most recent executions, newest first, up to `HISTORY_LIMIT`. */
  listQueryExecutions: async (workGroup: string): Promise<QueryExecution[]> => {
    const client = awsClients.athena()
    const ids: string[] = []
    let token: string | undefined
    do {
      const page = await client.send(
        new ListQueryExecutionsCommand({ WorkGroup: workGroup, NextToken: token }),
      )
      ids.push(...(page.QueryExecutionIds ?? []))
      token = page.NextToken
    } while (token && ids.length < HISTORY_LIMIT)
    const pages = await Promise.all(
      chunks(ids.slice(0, HISTORY_LIMIT), BATCH_LIMIT).map((QueryExecutionIds) =>
        client.send(new BatchGetQueryExecutionCommand({ QueryExecutionIds })),
      ),
    )
    return pages.flatMap((page) => page.QueryExecutions ?? [])
  },

  /** The recent executions of every workgroup. */
  listAllQueryExecutions: (): Promise<QueryExecution[]> =>
    acrossWorkGroups(athena.listQueryExecutions),

  // ─── Data catalogs and their metadata ──────────────────────────────────

  listDataCatalogs: (): Promise<DataCatalogSummary[]> =>
    collectPages(
      paginateListDataCatalogs({ client: awsClients.athena() }, {}),
      (page) => page.DataCatalogsSummary,
    ),

  createDataCatalog: async (input: CreateDataCatalogInput): Promise<void> => {
    await awsClients.athena().send(new CreateDataCatalogCommand(input))
  },

  deleteDataCatalog: async (name: string): Promise<void> => {
    await awsClients.athena().send(new DeleteDataCatalogCommand({ Name: name }))
  },

  listDatabases: (catalog: string): Promise<Database[]> =>
    collectPages(
      paginateListDatabases({ client: awsClients.athena() }, { CatalogName: catalog }),
      (page) => page.DatabaseList,
    ),

  listTableMetadata: (catalog: string, database: string): Promise<TableMetadata[]> =>
    collectPages(
      paginateListTableMetadata(
        { client: awsClients.athena() },
        { CatalogName: catalog, DatabaseName: database },
      ),
      (page) => page.TableMetadataList,
    ),

  // ─── The emulator's query engine ───────────────────────────────────────

  /** `GET /_overcast/athena/engine`: emulator-only, so plain `fetch`. */
  getEngineStatus: async (): Promise<AthenaEngineStatus> => {
    const { baseUrl } = endpointStore.get()
    const res = await fetch(`${baseUrl}/_overcast/athena/engine`)
    if (!res.ok) throw new Error(`Engine status: HTTP ${res.status}`)
    return (await res.json()) as AthenaEngineStatus
  },
}
