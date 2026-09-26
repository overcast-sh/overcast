import type { QueryExecution, QueryExecutionContext } from "@aws-sdk/client-athena"
import type { DataColumn } from "@/lib/data-sources/row-source"
import { abortError } from "@/lib/data-sources/row-source"
import { athena } from "@/services/api"

/**
 * The two Athena queries the Glue pages run on a developer's behalf —
 * *Preview* and *Discover partitions* — started and waited on here, since
 * the page shows only the outcome. The editor (#2072) is where a query is
 * watched as it runs.
 */

/** How often a query the page is waiting on is polled. */
const POLL_MS = 250

const FINISHED = new Set(["SUCCEEDED", "FAILED", "CANCELLED"])

/** Athena's numeric types, which the grid right-aligns. */
const NUMERIC_TYPES = /^(tinyint|smallint|integer|int|bigint|double|float|real|decimal)/i

/** Rows *Preview* reads: a first look, like the console's own preview. */
export const PREVIEW_ROWS = 100

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

/**
 * Starts `sql` and resolves with its execution once it has finished, however
 * it finished: a FAILED query is an answer for the page to show, not an error.
 */
export async function runQuery(
  sql: string,
  context: QueryExecutionContext,
  signal?: AbortSignal,
): Promise<QueryExecution> {
  const id = await athena.startQueryExecution({ QueryString: sql, QueryExecutionContext: context })
  for (;;) {
    const execution = await athena.getQueryExecution(id)
    if (FINISHED.has(execution.Status?.State ?? "")) return execution
    await delay(POLL_MS, signal)
  }
}

export interface QueryRows {
  execution: QueryExecution
  columns: DataColumn[]
  rows: (string | null)[][]
}

/**
 * Runs a `SELECT` and reads its first page of rows as grid columns and
 * values — a NULL as `null`, everything else the text Athena sent. The
 * header row a SELECT's first page opens with is dropped.
 */
export async function selectRows(
  sql: string,
  context: QueryExecutionContext,
  signal?: AbortSignal,
): Promise<QueryRows> {
  const execution = await runQuery(sql, context, signal)
  if (execution.Status?.State !== "SUCCEEDED") return { execution, columns: [], rows: [] }
  const page = await athena.getQueryResults(execution.QueryExecutionId ?? "", undefined, signal)
  const columns: DataColumn[] = (page.ResultSet?.ResultSetMetadata?.ColumnInfo ?? []).map((c) => ({
    name: c.Label ?? c.Name ?? "",
    type: c.Type,
    numeric: NUMERIC_TYPES.test(c.Type ?? ""),
    dateOnly: c.Type === "date",
  }))
  const rows = (page.ResultSet?.Rows ?? []).map((row) =>
    (row.Data ?? []).map((d) => d.VarCharValue ?? null),
  )
  return { execution, columns, rows: rows.slice(1) }
}

/** Why a finished query did not succeed, in Athena's own words. */
export function queryFailure(execution: QueryExecution): string | undefined {
  if (execution.Status?.State === "SUCCEEDED") return undefined
  return (
    execution.Status?.AthenaError?.ErrorMessage ??
    execution.Status?.StateChangeReason ??
    `The query ended ${execution.Status?.State ?? "without a state"}.`
  )
}
