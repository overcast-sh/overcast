/**
 * LatestCommitPeek — an S3 Tables table's latest commit, from the map: what
 * the commit did, how many records it added and removed, and when. The
 * question a developer has right after a write, answered without leaving
 * the graph.
 *
 * No *Query with Athena* here: Athena cannot run a query in a table bucket's
 * catalog yet (#2183), and an action that can only fail is not offered.
 */

import { Link } from "@tanstack/react-router"
import { ExternalLink } from "lucide-react"
import { Advisory } from "@/components/ui/advisory"
import { DefinitionList, Definition } from "@/components/ui/definition-card"
import { EmptyState } from "@/components/ui/primitives"
import { formatCount, formatDate } from "@/lib/format"
import { parseS3Uri } from "@/lib/s3-uri"
import type { TopologyDataTable } from "@/types"
import { MapPeekPanel, peekActionClass } from "./map-peek-panel"
import { s3TablesTableRoute } from "./node-route"

interface LatestCommitPeekProps {
  bucket: string
  table: TopologyDataTable | null
  onClose: () => void
}

export function LatestCommitPeek({ bucket, table, onClose }: LatestCommitPeekProps) {
  const namespace = table?.namespace ?? ""
  const name = table?.name ?? ""
  const route = s3TablesTableRoute(bucket, table?.id ?? "")
  return (
    <MapPeekPanel
      open={table !== null}
      onClose={onClose}
      title={`${namespace}.${name}`}
      subtitle={`Table bucket ${bucket}`}
      actions={
        <Link to={route.to} params={route.params} className={peekActionClass}>
          <ExternalLink aria-hidden className="h-3.5 w-3.5" />
          Open table
        </Link>
      }
    >
      {table && <CommitBody table={table} />}
    </MapPeekPanel>
  )
}

function CommitBody({ table }: { table: TopologyDataTable }) {
  const commit = table.lastCommit
  const warehouse = table.location ? parseS3Uri(table.location)?.bucket : undefined
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
      {commit ? (
        <DefinitionList columns={3} layout="stacked">
          <Definition label="Operation" value={commit.operation} />
          <Definition label="Committed" value={formatDate(commit.committedAt)} />
          <Definition label="Records added" value={records(commit.addedRecords)} />
          <Definition label="Records removed" value={records(commit.deletedRecords)} />
          <Definition label="Snapshot" value={commit.snapshotId} copyable />
          <Definition
            label="Snapshots kept"
            value={table.snapshots !== undefined ? formatCount(table.snapshots) : undefined}
          />
        </DefinitionList>
      ) : (
        <EmptyState
          title="No commits yet"
          description="The table has no snapshot: nothing has been written to it since it was created."
        />
      )}
      {warehouse && (
        <Advisory
          tone="info"
          title="Its files are in a warehouse bucket S3 Tables manages"
          docsPath="services/s3tables.md"
          action={
            <Link
              to="/s3/$bucket"
              params={{ bucket: warehouse }}
              className="font-mono text-2xs text-accent uppercase hover:underline"
            >
              Open in S3
            </Link>
          }
        >
          <code className="font-mono">{warehouse}</code> is drawn as part of this table bucket
          rather than as an S3 bucket of its own.
        </Advisory>
      )}
    </div>
  )
}

function records(n: number | undefined): string | undefined {
  return n === undefined ? undefined : formatCount(n)
}
