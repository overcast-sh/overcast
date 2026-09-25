import {
  BatchGetNamedQueryCommand,
  GetQueryExecutionCommand,
  GetQueryResultsCommand,
  paginateListNamedQueries,
  paginateListWorkGroups,
  StartQueryExecutionCommand,
  type NamedQuery,
  type QueryExecution,
  type QueryExecutionContext,
  type WorkGroupSummary,
} from "@aws-sdk/client-athena"
import type { AthenaResultPage } from "@/lib/data-sources/athena-result-source"
import { abortError } from "@/lib/data-sources/row-source"
import type { DataColumn } from "@/lib/data-sources/row-source"
import { awsClients } from "../aws-clients"
import { collectPages } from "./paginate"

/** BatchGetNamedQuery's limit on ids per call. */
const NAMED_QUERY_BATCH = 50

/** How often a query the console is waiting on is polled. */
const QUERY_POLL_MS = 250

/** The states a query execution ends in. */
const FINISHED_STATES = new Set(["SUCCEEDED", "FAILED", "CANCELLED"])

/** Athena's numeric types, which the grid right-aligns. */
const NUMERIC_TYPES = /^(tinyint|smallint|integer|int|bigint|double|float|real|decimal)/i

export interface StartQueryInput {
  sql: string
  context?: QueryExecutionContext
  /** The workgroup; the service's own default, `primary`, when omitted. */
  workGroup?: string
}

/** Waits `ms`, or rejects as soon as `signal` aborts. */
function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms)
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(timer)
        reject(abortError())
      },
      { once: true },
    )
  })
}

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

  startQuery: async ({ sql, context, workGroup }: StartQueryInput): Promise<string> => {
    const res = await awsClients.athena().send(
      new StartQueryExecutionCommand({
        QueryString: sql,
        QueryExecutionContext: context,
        WorkGroup: workGroup,
      }),
    )
    return res.QueryExecutionId ?? ""
  },

  getQueryExecution: async (id: string): Promise<QueryExecution> => {
    const res = await awsClients
      .athena()
      .send(new GetQueryExecutionCommand({ QueryExecutionId: id }))
    return res.QueryExecution ?? { QueryExecutionId: id }
  },

  /**
   * Starts a query and resolves with its execution once it has finished,
   * whichever way it finished: a FAILED query is an answer, not an error.
   */
  runQuery: async (input: StartQueryInput, signal?: AbortSignal): Promise<QueryExecution> => {
    const id = await athena.startQuery(input)
    for (;;) {
      const execution = await athena.getQueryExecution(id)
      if (FINISHED_STATES.has(execution.Status?.State ?? "")) return execution
      await delay(QUERY_POLL_MS, signal)
    }
  },

  /**
   * One page of GetQueryResults as the grid reads it: typed columns and data
   * rows. A SELECT's first page opens with a header row, which `hasHeader`
   * drops. A NULL is `null`; every other value is the text Athena sent.
   */
  getResultPage: async (
    id: string,
    token: string | undefined,
    { hasHeader, signal }: { hasHeader: boolean; signal?: AbortSignal },
  ): Promise<AthenaResultPage> => {
    const res = await awsClients
      .athena()
      .send(new GetQueryResultsCommand({ QueryExecutionId: id, NextToken: token }), {
        abortSignal: signal,
      })
    const columns: DataColumn[] = (res.ResultSet?.ResultSetMetadata?.ColumnInfo ?? []).map((c) => ({
      name: c.Label ?? c.Name ?? "",
      type: c.Type,
      numeric: NUMERIC_TYPES.test(c.Type ?? ""),
      dateOnly: c.Type === "date",
    }))
    const rows = (res.ResultSet?.Rows ?? []).map((row) =>
      (row.Data ?? []).map((d) => d.VarCharValue ?? null),
    )
    const skip = hasHeader && token === undefined ? 1 : 0
    return { columns, rows: rows.slice(skip), nextToken: res.NextToken }
  },
}
