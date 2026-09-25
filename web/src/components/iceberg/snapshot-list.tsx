import type { ReactNode } from "react"
import { History } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { CopyButton } from "@/components/ui/copy-button"
import { ResourceTable } from "@/components/ui/resource-table"
import { formatCount, formatDate } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { IcebergMetadata, IcebergSnapshot } from "./metadata"
import { commitChanges, parentOf } from "./snapshot-changes"
import { SnapshotDiff } from "./snapshot-diff"

/** The operations the spec defines, each in the tone of what it does to the data. */
const OPERATION_BADGE: Record<string, "success" | "warning" | "danger" | "default"> = {
  append: "success",
  overwrite: "warning",
  delete: "danger",
  replace: "default",
}

interface IcebergSnapshotListProps {
  metadata: IcebergMetadata
  /**
   * Actions a catalog adds to each snapshot — S3 Tables and Glue offer
   * *Query as of this snapshot*, each through its own Athena catalog.
   */
  snapshotActions?: (snapshot: IcebergSnapshot) => ReactNode
  /** A snapshot whose diff opens on first paint — the target of a deep link. */
  openSnapshotId?: string
}

/**
 * A table's snapshots, newest first: what each commit did, when, and to how
 * many records. Each row opens *Diff with previous*, the running totals
 * either side of the commit.
 */
export function IcebergSnapshotList({
  metadata,
  snapshotActions,
  openSnapshotId,
}: IcebergSnapshotListProps) {
  const { snapshots, currentSnapshotId, refs } = metadata
  // Branches and tags other than the `main` the "current" badge already stands for.
  const refsOf = (s: IcebergSnapshot) =>
    refs.filter(
      (ref) =>
        ref.snapshotId === s.snapshotId && !(ref.name === "main" && s.snapshotId === currentSnapshotId),
    )
  return (
    <ResourceTable
      variant="embedded"
      query={{ data: snapshots, isLoading: false }}
      noun="snapshots"
      emptyIcon={History}
      emptyTitle="No snapshots yet"
      emptyDescription="The table has a schema and no data. Its first commit — an INSERT, or PyIceberg's append — adds a snapshot here."
      rowKey={(s) => s.snapshotId}
      defaultSort={{ id: "committed", desc: true }}
      expandLabel="diff with previous"
      expandedContent={(s) => <SnapshotDiff snapshot={s} parent={parentOf(snapshots, s)} />}
      defaultExpanded={openSnapshotId ? (s) => s.snapshotId === openSnapshotId : undefined}
      rowActions={snapshotActions}
      columns={[
        {
          id: "snapshot",
          header: "Snapshot",
          interactive: true,
          cell: (s) => (
            <span className="inline-flex items-center gap-1.5">
              <span>{s.snapshotId}</span>
              <CopyButton value={s.snapshotId} noun="snapshot id" tone="inline" />
              {s.snapshotId === currentSnapshotId && <Badge variant="accent">current</Badge>}
              {refsOf(s).map((ref) => (
                <Badge key={ref.name} variant="outline" title={`${ref.type} ${ref.name}`}>
                  {ref.name}
                </Badge>
              ))}
            </span>
          ),
        },
        {
          header: "Operation",
          sortValue: (s) => s.operation,
          cell: (s) =>
            s.operation ? (
              <Badge variant={OPERATION_BADGE[s.operation] ?? "default"}>{s.operation}</Badge>
            ) : (
              <span className="text-fg-subtle">—</span>
            ),
        },
        {
          id: "committed",
          header: "Committed",
          sortValue: (s) => new Date(s.timestampMs),
          cellClassName: "text-fg-muted",
          cell: (s) => formatDate(s.timestampMs),
        },
        {
          header: "Changes",
          cell: (s) => <CommitChanges snapshot={s} />,
        },
        {
          header: "Records",
          cellClassName: "text-right tabular-nums",
          headerClassName: "text-right",
          sortValue: (s) => Number(s.summary["total-records"] ?? Number.NaN),
          cell: (s) =>
            s.summary["total-records"] === undefined
              ? "—"
              : formatCount(Number(s.summary["total-records"])),
        },
        {
          header: "Schema",
          cellClassName: "text-fg-muted",
          cell: (s) => (s.schemaId === undefined ? "—" : s.schemaId),
        },
      ]}
    />
  )
}

function CommitChanges({ snapshot }: { snapshot: IcebergSnapshot }) {
  const changes = commitChanges(snapshot)
  if (changes.length === 0) return <span className="text-fg-subtle">—</span>
  return (
    <span className="inline-flex flex-wrap gap-x-2">
      {changes.map((change) => (
        <span
          key={change.text}
          className={cn(change.tone === "added" ? "text-success" : "text-danger")}
        >
          {change.text}
        </span>
      ))}
    </span>
  )
}
