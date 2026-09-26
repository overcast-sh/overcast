import {
  BatchGetNamedQueryCommand,
  GetQueryExecutionCommand,
  GetQueryResultsCommand,
  GetWorkGroupCommand,
  StartQueryExecutionCommand,
  paginateListNamedQueries,
  paginateListWorkGroups,
  type GetQueryResultsOutput,
  type NamedQuery,
  type QueryExecution,
  type StartQueryExecutionInput,
  type WorkGroup,
  type WorkGroupSummary,
} from "@aws-sdk/client-athena"
import type { AthenaEngineStatus } from "@/types"
import { awsClients } from "../aws-clients"
import { endpointStore } from "../endpoint-store"
import { collectPages } from "./paginate"

/** BatchGetNamedQuery's limit on ids per call. */
const NAMED_QUERY_BATCH = 50

export const athena = {
  listWorkGroups: (): Promise<WorkGroupSummary[]> =>
    collectPages(
      paginateListWorkGroups({ client: awsClients.athena() }, {}),
      (page) => page.WorkGroups,
    ),

  /** The saved queries of one workgroup, in full. */
  listNamedQueries: async (workGroup: string): Promise<NamedQuery[]> => {
    const client = awsClients.athena()
    const ids = await collectPages(
      paginateListNamedQueries({ client }, { WorkGroup: workGroup }),
      (page) => page.NamedQueryIds,
    )
    const batches: string[][] = []
    for (let i = 0; i < ids.length; i += NAMED_QUERY_BATCH) {
      batches.push(ids.slice(i, i + NAMED_QUERY_BATCH))
    }
    const pages = await Promise.all(
      batches.map((NamedQueryIds) => client.send(new BatchGetNamedQueryCommand({ NamedQueryIds }))),
    )
    return pages.flatMap((page) => page.NamedQueries ?? [])
  },

  /** The saved queries of every workgroup. */
  listAllNamedQueries: async (): Promise<NamedQuery[]> => {
    const workGroups = await athena.listWorkGroups()
    const perWorkGroup = await Promise.all(
      workGroups.map((wg) => athena.listNamedQueries(wg.Name ?? "")),
    )
    return perWorkGroup.flat()
  },

  getWorkGroup: async (name: string): Promise<WorkGroup> => {
    const out = await awsClients.athena().send(new GetWorkGroupCommand({ WorkGroup: name }))
    return out.WorkGroup ?? { Name: name }
  },

  // ─── Query executions ──────────────────────────────────────────────────

  startQueryExecution: async (input: StartQueryExecutionInput): Promise<string> => {
    const out = await awsClients.athena().send(new StartQueryExecutionCommand(input))
    return out.QueryExecutionId ?? ""
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

  // ─── The emulator's query engine ───────────────────────────────────────

  /** `GET /_overcast/athena/engine`: emulator-only, so plain `fetch`. */
  getEngineStatus: async (): Promise<AthenaEngineStatus> => {
    const { baseUrl } = endpointStore.get()
    const res = await fetch(`${baseUrl}/_overcast/athena/engine`)
    if (!res.ok) throw new Error(`Engine status: HTTP ${res.status}`)
    return (await res.json()) as AthenaEngineStatus
  },
}
