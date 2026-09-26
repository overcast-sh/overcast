import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import type { GetQueryResultsOutput, QueryExecution } from "@aws-sdk/client-athena"
import { ChevronDown, Copy, Download, FolderOpen } from "lucide-react"
// The query result is the one deliberate exception to ResourceTable: a result
// set has arbitrary typed columns and can hold millions of rows, and it is not
// a resource list. It is the shared DataGrid over the Athena result source
// (docs/plans/data-lake-console.md, *Result grid*).
import { DataGrid } from "@/components/data-grid/data-grid"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/menu"
import { EmptyState } from "@/components/ui/primitives"
import { SkeletonRows } from "@/components/ui/skeleton"
import { useToast } from "@/components/ui/toast"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import type { RowSource } from "@/lib/data-sources/row-source"
import { parseS3Uri } from "@/lib/s3-uri"
import { s3 } from "@/services/api"
import { firstResultPageQueryOptions, runtimeStatisticsQueryOptions } from "../../data"
import { formatResult, readAllRows, RESULT_FORMATS, type ResultFormat } from "../../result-export"
import { useResultSource } from "../../use-result-source"
import { StatementOutcome } from "./run-status"

/**
 * A succeeded query's result: what a DDL or DML statement did, and the rows
 * in the shared `DataGrid` with the result's actions — copy, download the
 * real CSV Athena wrote, open it in S3, copy the execution id.
 */
export function QueryResult({ execution, inert }: { execution: QueryExecution; inert: boolean }) {
  const id = execution.QueryExecutionId ?? ""
  const firstPage = useQuery(firstResultPageQueryOptions(id))
  const runtime = useQuery(runtimeStatisticsQueryOptions(id))
  if (firstPage.error) {
    return <EmptyState title="Could not read the result" description={firstPage.error.message} />
  }
  if (!firstPage.data || runtime.isLoading) return <SkeletonRows rows={4} noun="result" />
  const updateCount = firstPage.data.UpdateCount
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <StatementOutcome execution={execution} updateCount={updateCount} />
      {updateCount === undefined && (
        <ResultGrid
          execution={execution}
          firstPage={firstPage.data}
          outputRows={runtime.data?.Rows?.OutputRows}
          inert={inert}
        />
      )}
    </div>
  )
}

function ResultGrid({
  execution,
  firstPage,
  outputRows,
  inert,
}: {
  execution: QueryExecution
  firstPage: GetQueryResultsOutput
  outputRows: number | undefined
  inert: boolean
}) {
  const { source, error } = useResultSource(execution, firstPage, outputRows)
  if (error) return <EmptyState title="Could not read the result" description={error.message} />
  if (!source) return <SkeletonRows rows={4} noun="rows" />
  return (
    <DataGrid
      source={source}
      label="Query result"
      className="min-h-40 flex-1 rounded-md border border-border"
      emptyMessage={inert ? "No rows: the query engine is off." : "The query returned no rows."}
      toolbarEnd={<ResultActions execution={execution} source={source} />}
    />
  )
}

function ResultActions({ execution, source }: { execution: QueryExecution; source: RowSource }) {
  const { copy } = useCopyToClipboard()
  const { toast } = useToast()
  const id = execution.QueryExecutionId ?? ""
  const location = parseS3Uri(execution.ResultConfiguration?.OutputLocation ?? "")
  // A result held in memory is copied whole; a large one is the CSV's to download.
  const inMemory = source.rowCount.exact && !source.indexing
  const copyAs = (format: ResultFormat) => {
    readAllRows(source, new AbortController().signal).then(
      (rows) => copy(formatResult(rows, format), { noun: `result as ${format.toUpperCase()}` }),
      (error: unknown) =>
        toast({
          title: "Could not copy the result",
          description: error instanceof Error ? error.message : String(error),
          variant: "danger",
        }),
    )
  }
  return (
    <>
      {inMemory && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="sm">
              <Copy aria-hidden className="size-3.5" />
              Copy
              <ChevronDown aria-hidden className="size-3" />
            </Button>
          </MenuTrigger>
          <MenuContent>
            {RESULT_FORMATS.map(({ format, label }) => (
              <MenuItem key={format} onSelect={() => copyAs(format)}>
                {label}
              </MenuItem>
            ))}
          </MenuContent>
        </Menu>
      )}
      {location && (
        <>
          <Button asChild variant="ghost" size="sm">
            <a href={s3.getObjectDownloadUrl(location.bucket, location.key)} download>
              <Download aria-hidden className="size-3.5" />
              Download {location.key.endsWith(".csv") ? "CSV" : "result"}
            </a>
          </Button>
          <Button asChild variant="ghost" size="sm">
            <Link
              to="/s3/$bucket/objects/$"
              params={{ bucket: location.bucket, _splat: location.key }}
            >
              <FolderOpen aria-hidden className="size-3.5" />
              Open in S3
            </Link>
          </Button>
        </>
      )}
      <CopyButton value={id} noun="execution id" />
    </>
  )
}
