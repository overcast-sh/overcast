/**
 * DataCatalogNode — a Glue database, S3 Tables table bucket or Glue's
 * federated s3tablescatalog on the map.
 *
 * A database or bucket lists its tables (by namespace in S3 Tables), and a
 * table's row opens the peek a developer reaches for first: a Glue table's
 * first rows, an S3 Tables table's latest commit (see data-lake-peeks.tsx).
 * The list fades at far zoom
 * like every node's detail, so the node reads as a box among boxes.
 */

import { memo } from "react"
import type { NodeProps } from "@xyflow/react"
import { Link } from "@tanstack/react-router"
import { Eye, GitCommitHorizontal } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { formatQuantity } from "@/lib/format"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import type { TopologyDataTable } from "@/types"
import {
  areDataLakeNodePropsEqual,
  useDataLakeOverlay,
  useDataLakePeek,
  type DataLakeNodeData,
} from "./data-lake-context"
import { DATA_MORE_H, DATA_SECTION_H, tableList } from "./data-lake-layout"
import { DataLakeCard } from "./data-lake-node"
import { nodeSuffix, rowKey } from "./data-lake-overlay"
import { DataTableRow, TickCount } from "./data-table-row"
import { SERVICE_THEME } from "./map-theme"
import { nodeRoute } from "./node-route"

export const DataCatalogNode = memo(function DataCatalogNode({ data }: NodeProps) {
  const nodeData = data as DataLakeNodeData
  const node = nodeData.topologyNode
  const isCatalog = node.glueResourceType === "catalog"
  const isBucket = node.service === "s3tables"
  const tables = node.tables ?? []
  const overlay = useDataLakeOverlay()
  const suffix = nodeSuffix(node.id)
  const list = tableList(tables, overlay.ghosts[suffix])
  const openPeek = useDataLakePeek()
  const route = nodeRoute({
    service: node.service,
    label: node.label,
    glueResourceType: node.glueResourceType,
  })
  const color = SERVICE_THEME[node.service]?.css ?? "currentColor"

  return (
    <DataLakeCard
      data={nodeData}
      route={route}
      subtitle={
        isCatalog ? (
          <span>Federated catalog · S3 Tables</span>
        ) : isBucket ? (
          // The rows say how many tables; the line says where their files are.
          <>
            <span className="shrink-0">Table bucket</span>
            <span aria-hidden>·</span>
            <WarehouseLabel />
          </>
        ) : (
          <>
            <span className="shrink-0">Database</span>
            <span aria-hidden>·</span>
            <span className="truncate">{formatQuantity(tables.length, "table")}</span>
          </>
        )
      }
    >
      {list.items.length > 0 && (
        <div className="map-detail -mx-1.5 mt-1.5 flex flex-col">
          {list.items.map((item) =>
            item.kind === "namespace" ? (
              <p
                key={`ns:${item.namespace}`}
                className={cn(sectionLabel, "flex items-end px-1.5 text-fg-subtle")}
                style={{ height: DATA_SECTION_H }}
              >
                {item.namespace}
              </p>
            ) : (
              <DataTableRow
                key={item.row.key}
                row={item.row}
                overlay={overlay.rows[rowKey(suffix, item.row.key)]}
                color={color}
                meta={
                  item.row.table && (
                    <TableFacts
                      table={item.row.table}
                      ticks={overlay.rows[rowKey(suffix, item.row.key)]?.ticks}
                    />
                  )
                }
                onPeek={() =>
                  openPeek({
                    kind: isBucket ? "commit" : "preview",
                    nodeId: node.id,
                    row: item.row.key,
                  })
                }
                peekLabel={isBucket ? "Latest commit" : "Preview first rows"}
                PeekIcon={isBucket ? GitCommitHorizontal : Eye}
              />
            ),
          )}
          {list.hidden > 0 && route && (
            <Link
              to={route.to}
              params={route.params}
              onClick={(e) => e.stopPropagation()}
              className="flex items-center px-1.5 text-2xs text-fg-muted hover:text-fg hover:underline"
              style={{ height: DATA_MORE_H }}
            >
              +{formatQuantity(list.hidden, "more table")}
            </Link>
          )}
        </div>
      )}
    </DataLakeCard>
  )
}, areDataLakeNodePropsEqual)

/** The right-hand facts of a table row: what it is, and how much of it there is. */
function TableFacts({ table, ticks }: { table: TopologyDataTable; ticks?: number }) {
  return (
    <>
      {table.format && (
        <Badge variant={table.format === "ICEBERG" ? "accent" : "default"} className="px-1 py-0">
          {table.format}
        </Badge>
      )}
      {table.partitions !== undefined && table.partitions > 0 && (
        <TickCount ticks={ticks}>{formatQuantity(table.partitions, "partition")}</TickCount>
      )}
      {table.snapshots !== undefined && (
        <span className="tabular-nums">{formatQuantity(table.snapshots, "snapshot")}</span>
      )}
    </>
  )
}

/**
 * A table bucket's *warehouse*: S3 keeps each table's files in its own
 * `--table-s3` bucket, which the map folds into this node rather than drawing
 * twice. Each table's peek links to its own.
 */
function WarehouseLabel() {
  return (
    <span
      className="rounded-control border border-border px-1 font-mono text-2xs text-fg-subtle"
      title="Each table's files are in its own managed --table-s3 bucket in S3, shown here rather than as S3 buckets. A table's latest-commit peek links to its warehouse."
    >
      warehouse
    </span>
  )
}
