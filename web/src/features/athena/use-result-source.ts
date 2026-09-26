import type { GetQueryResultsOutput, QueryExecution } from "@aws-sdk/client-athena"
import { useRowSource, type RowSourceState } from "@/components/data-grid/use-row-source"
import { openAthenaResultSource, type AthenaResult } from "@/lib/data-sources/athena-result-source"
import type { RowSource } from "@/lib/data-sources/row-source"
import { createDataWorker } from "@/lib/data-sources/worker-port"
import { parseS3Uri } from "@/lib/s3-uri"
import { athena, s3 } from "@/services/api"
import { resultPage } from "./result-values"

/**
 * The row count that sends a result down the paged path whatever its size:
 * `openAthenaResultSource` pages anything at or under `PAGED_RESULT_ROWS`.
 */
const READ_BY_PAGE = 0

/**
 * A finished execution's rows as a `RowSource`, through the Athena result
 * adapter: a small result pages through `GetQueryResults` into memory, a
 * large one is read by range from the CSV at `OutputLocation`.
 *
 * Which one is decided by the row count, which comes from
 * `GetQueryRuntimeStatistics`:
 *
 * - a result that fits on the first page is read from that page, whatever
 *   its size;
 * - a DDL or utility result is always paged: its file is a `.txt` with no
 *   header, not a CSV — and so is a result with no `OutputLocation` to read;
 * - a `SELECT` with no runtime statistics takes the CSV path, right for any
 *   size.
 */
export function useResultSource(
  execution: QueryExecution,
  firstPage: GetQueryResultsOutput,
  outputRows: number | undefined,
): RowSourceState<RowSource> {
  const id = execution.QueryExecutionId ?? ""
  // Athena's DML results — a SELECT's — open with a header row of column names.
  const hasHeader = execution.StatementType === "DML"
  const location = parseS3Uri(execution.ResultConfiguration?.OutputLocation ?? "")
  const firstRows = firstPage.ResultSet?.Rows?.length ?? 0
  const rowCount = !firstPage.NextToken
    ? firstRows
    : hasHeader && location
      ? outputRows
      : READ_BY_PAGE
  return useRowSource(`${id}:${rowCount ?? "?"}`, async (signal) => {
    const byRange = location !== null && (rowCount === undefined || rowCount > firstRows)
    const result: AthenaResult = {
      rowCount,
      output: {
        url: location ? s3.getObjectDownloadUrl(location.bucket, location.key) : "",
        size: byRange
          ? (await s3.getObjectMetadata(location.bucket, location.key)).contentLength
          : 0,
      },
      readPage: async (token, pageSignal) => {
        const output = token ? await athena.getQueryResults(id, token, pageSignal) : firstPage
        return resultPage(output, hasHeader && token === undefined)
      },
    }
    return openAthenaResultSource(result, { spawnWorker: createDataWorker, signal })
  })
}
