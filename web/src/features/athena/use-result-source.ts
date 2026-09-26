import type { GetQueryResultsOutput, QueryExecution } from "@aws-sdk/client-athena"
import { useRowSource, type RowSourceState } from "@/components/data-grid/use-row-source"
import { openAthenaResultSource, type AthenaResult } from "@/lib/data-sources/athena-result-source"
import { createDataWorker } from "@/lib/data-sources/worker-port"
import { parseS3Uri } from "@/lib/s3-uri"
import { athena, s3 } from "@/services/api"
import { resultPage } from "./result-values"

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
 *   header, not a CSV;
 * - a `SELECT` with no runtime statistics takes the CSV path, right for any
 *   size.
 */
export function useResultSource(
  execution: QueryExecution,
  firstPage: GetQueryResultsOutput,
  outputRows: number | undefined,
): RowSourceState<Awaited<ReturnType<typeof openAthenaResultSource>>> {
  const id = execution.QueryExecutionId ?? ""
  const selects = execution.StatementType === "DML"
  const firstRows = firstPage.ResultSet?.Rows?.length ?? 0
  const rowCount = !firstPage.NextToken ? firstRows : selects ? outputRows : 0
  return useRowSource(`${id}:${rowCount ?? "?"}`, async (signal) => {
    const location = parseS3Uri(execution.ResultConfiguration?.OutputLocation ?? "")
    const url = location ? s3.getObjectDownloadUrl(location.bucket, location.key) : ""
    const size =
      location && (rowCount === undefined || rowCount > firstRows)
        ? (await s3.getObjectMetadata(location.bucket, location.key)).contentLength
        : 0
    const result: AthenaResult = {
      rowCount,
      output: { url, size },
      readPage: async (token, pageSignal) => {
        const output = token ? await athena.getQueryResults(id, token, pageSignal) : firstPage
        return resultPage(output, selects && token === undefined)
      },
    }
    return openAthenaResultSource(result, { spawnWorker: createDataWorker, signal })
  })
}
