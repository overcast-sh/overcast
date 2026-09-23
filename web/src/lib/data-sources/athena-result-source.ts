import { memorySource } from "./memory-source"
import type { DataColumn, RowSource } from "./row-source"
import { openTextSource } from "./text-source"
import type { DataWorkerPort } from "./worker-protocol"

/**
 * The row source for an Athena query result — the adapter the Athena result
 * grid (#2072) plugs into, so it is the same `DataGrid` as the S3 previews.
 *
 * Two ways to read a result, and the size decides:
 *
 * - **Small results** page through `GetQueryResults`, which is typed and
 *   needs no second request per page, into memory.
 * - **Large results** read the result CSV Athena writes to `OutputLocation`,
 *   through the CSV source: `GetQueryResults` only pages forward by
 *   `NextToken`, and the CSV can be read by Range anywhere, so a jump to row
 *   four million costs one request instead of four thousand.
 *
 * A result whose size is not known (no `Statistics`) takes the CSV path,
 * which is right for any size.
 */

/** Largest result read through `GetQueryResults`: a few pages of 1,000 rows. */
export const PAGED_RESULT_ROWS = 5000

export interface AthenaResultPage {
  /** From `ResultSetMetadata.ColumnInfo`, typed. */
  columns: DataColumn[]
  /** Data rows only: the caller drops the header row the first page carries. */
  rows: unknown[][]
  nextToken?: string
}

export interface AthenaResult {
  /** Rows in the result, from `QueryExecution.Statistics` when it has them. */
  rowCount?: number
  /** One page of `GetQueryResults`, from `token` (none for the first). */
  readPage(token: string | undefined, signal?: AbortSignal): Promise<AthenaResultPage>
  /** The result CSV at `OutputLocation`: a URL a ranged GET reaches, and its size. */
  output: { url: string; size: number }
}

export async function openAthenaResultSource(
  result: AthenaResult,
  {
    spawnWorker,
    signal,
  }: {
    /** A data worker, spawned only for the CSV path. */
    spawnWorker: () => DataWorkerPort
    signal?: AbortSignal
  },
): Promise<RowSource> {
  if (result.rowCount === undefined || result.rowCount > PAGED_RESULT_ROWS) {
    return openTextSource({ ...result.output, kind: "csv", port: spawnWorker(), signal })
  }
  let page = await result.readPage(undefined, signal)
  const { columns } = page
  const rows = [...page.rows]
  while (page.nextToken) {
    page = await result.readPage(page.nextToken, signal)
    rows.push(...page.rows)
  }
  return memorySource(columns, rows)
}
