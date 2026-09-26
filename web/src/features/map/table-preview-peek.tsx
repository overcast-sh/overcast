/**
 * TablePreviewPeek — a Glue table's first rows, from the map.
 *
 * The rows come from the first data file under the table's location, read
 * straight from S3 by the same grid the S3 preview uses. That needs no query
 * engine, writes no result file, and works with the engine off; the advisory
 * says so, since it is not what Athena would return for a partitioned or
 * Iceberg table, and *Query with Athena* is one click away for that.
 */

import { Link } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { Database, ExternalLink } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { EmptyState } from "@/components/ui/primitives"
import { SkeletonRows } from "@/components/ui/skeleton"
import { athenaEditorLink } from "@/features/athena/links"
import { DataFilePreview } from "@/features/s3/components/data-file-preview"
import { previewSql } from "@/features/glue/athena-link"
import type { TopologyDataTable } from "@/types"
import { firstDataFileQueryOptions } from "./data-lake-data"
import { MapPeekPanel, peekActionClass } from "./map-peek-panel"
import { glueTableRoute } from "./node-route"

interface TablePreviewPeekProps {
  database: string
  table: TopologyDataTable | null
  onClose: () => void
}

export function TablePreviewPeek({ database, table, onClose }: TablePreviewPeekProps) {
  const name = table?.name ?? ""
  const route = glueTableRoute(database, name)
  return (
    <MapPeekPanel
      open={table !== null}
      onClose={onClose}
      title={`${database}.${name}`}
      subtitle={table?.location || "No location"}
      actions={
        <>
          <Link
            {...athenaEditorLink({ database, sql: previewSql(database, name) })}
            className={peekActionClass}
          >
            <Database aria-hidden className="h-3.5 w-3.5" />
            Query with Athena
          </Link>
          <Link to={route.to} params={route.params} className={peekActionClass}>
            <ExternalLink aria-hidden className="h-3.5 w-3.5" />
            Open table
          </Link>
        </>
      }
    >
      {table && <PreviewBody table={table} />}
    </MapPeekPanel>
  )
}

function PreviewBody({ table }: { table: TopologyDataTable }) {
  const location = table.location ?? ""
  const file = useQuery(firstDataFileQueryOptions(location))

  if (!location) {
    return (
      <EmptyState
        title="No location"
        description="This table names no S3 location, so there are no files to read rows from."
      />
    )
  }
  if (file.isPending) return <SkeletonRows rows={6} noun="table rows" className="p-4" />
  if (file.error) {
    return (
      <p role="alert" className="p-4 text-xs text-danger">
        Could not list {location}: {file.error.message}
      </p>
    )
  }
  if (!file.data) {
    return (
      <EmptyState
        title="No data files yet"
        description={`Nothing under ${location} holds rows yet: write a CSV, JSON Lines or Parquet file there.`}
      />
    )
  }
  const { bucket, object, kind } = file.data
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
      <Advisory tone="info" title="Read from the table's first data file, not through Athena">
        These are the rows of <code className="font-mono">{object.key}</code>, straight from S3:
        partition columns and Iceberg deletes are not applied. Query with Athena for the table as a
        query sees it.
      </Advisory>
      <DataFilePreview
        kind={kind}
        bucket={bucket}
        objectKey={object.key}
        size={object.size}
        etag={object.etag}
        gridClassName="h-[60vh]"
      />
    </div>
  )
}
