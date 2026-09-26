import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import type { Table } from "@aws-sdk/client-glue"
import { Play } from "lucide-react"
import { DataGrid } from "@/components/data-grid/data-grid"
import { Advisory } from "@/components/ui/advisory"
import { Button } from "@/components/ui/button"
import { EmptyState } from "@/components/ui/primitives"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { SkeletonRows } from "@/components/ui/skeleton"
import { engineStatusQueryOptions } from "@/features/athena/data"
import { memorySource } from "@/lib/data-sources/memory-source"
import { parseS3Uri } from "@/lib/s3-uri"
import { formatQuantity } from "@/lib/format"
import { PREVIEW_ROWS, queryFailure } from "../../athena-query"
import { gluePreviewQueryOptions } from "../../data"
import { tableLocation } from "../../table-format"
import { QueryInAthenaButton } from "../query-in-athena"
import { LocationObjects } from "./location-objects"
import { ResultLocationAdvisory } from "./result-location-advisory"

/**
 * The table's first rows. With the query engine running they come from
 * Athena, on request (*Preview* writes a result file, so it is not run
 * just by opening the tab). With the engine off a query would succeed with
 * no rows, which would misstate the table, so the tab lists the objects
 * under its location instead, each a click from the S3 preview.
 */
export function DataTab({ table }: { table: Table }) {
  const database = table.DatabaseName ?? ""
  const name = table.Name ?? ""
  const engine = useQuery(engineStatusQueryOptions())
  const location = tableLocation(table)

  if (engine.isLoading) return <SkeletonRows rows={4} noun="engine status" />
  if (engine.data?.state === "off") {
    return (
      <div className="flex flex-col gap-3">
        <Advisory title="The Athena query engine is off, so these are the table's files">
          {engine.data.reason} Open one to preview its rows.
        </Advisory>
        {location ? (
          <LocationObjects location={location} />
        ) : (
          <ResourceListCard>
            <EmptyState title="No location" description="This table names no S3 location." />
          </ResourceListCard>
        )}
      </div>
    )
  }
  return (
    <AthenaPreview
      database={database}
      table={name}
      bucket={location ? parseS3Uri(location)?.bucket : undefined}
    />
  )
}

interface AthenaPreviewProps {
  database: string
  table: string
  /** The table's bucket, where the advisory suggests results go. */
  bucket?: string
}

function AthenaPreview({ database, table, bucket }: AthenaPreviewProps) {
  const preview = useQuery(gluePreviewQueryOptions(database, table))
  const result = preview.data
  const source = useMemo(
    () => result && result.columns.length > 0 && memorySource(result.columns, result.rows),
    [result],
  )
  const failure = result && queryFailure(result.execution)
  const run = () => void preview.refetch()

  return (
    <div className="flex flex-col gap-3">
      <ResultLocationAdvisory suggestedBucket={bucket} />
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" busy={preview.isFetching} busyLabel="Running" onClick={run}>
          <Play className="h-3.5 w-3.5" />
          {result ? "Run preview again" : `Preview ${PREVIEW_ROWS} rows`}
        </Button>
        <QueryInAthenaButton database={database} table={table} />
        {result && !failure && (
          <span className="font-mono text-2xs text-fg-subtle">
            {formatQuantity(result.rows.length, "row")} · {result.execution.QueryExecutionId}
          </span>
        )}
      </div>
      {preview.error && (
        <p role="alert" className="text-xs text-danger">
          {preview.error.message}
        </p>
      )}
      {failure && (
        <p role="alert" className="text-xs text-danger">
          {failure}
        </p>
      )}
      {source ? (
        <DataGrid
          source={source}
          label={`First rows of ${database}.${table}`}
          className="h-96"
          emptyMessage="The table has no rows."
        />
      ) : (
        !preview.isFetching &&
        !preview.error &&
        !failure && (
          <ResourceListCard>
            <EmptyState
              icon={<Play className="h-8 w-8" />}
              title={result ? "No rows" : "Not previewed yet"}
              description={
                result
                  ? "The query returned no rows."
                  : `Runs SELECT * … LIMIT ${PREVIEW_ROWS} through Athena, in the primary workgroup.`
              }
            />
          </ResourceListCard>
        )
      )}
    </div>
  )
}
