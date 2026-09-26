import type { ReactNode } from "react"
import type { Table } from "@aws-sdk/client-glue"
import { Table2 } from "lucide-react"
import { ResourceName, RowActions } from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { S3UriLink } from "@/components/ui/s3-uri-link"
import { formatDate } from "@/lib/format"
import { tableLocation } from "../table-format"
import { FormatBadge } from "./format-badge"
import { QueryInAthenaRowAction } from "./query-in-athena"

interface TablesTableProps {
  database: string
  query: { data?: Table[]; isLoading: boolean; error?: unknown }
  sort?: ResourceTableSort
  onSortChange: (next: ResourceTableSort | undefined) => void
  isFiltered: boolean
  onClearFilter: () => void
  onRowClick: (table: Table) => void
  emptyAction: ReactNode
}

function Muted() {
  return <span className="text-fg-subtle">—</span>
}

/** A database's tables: format, location, partitioning, width and freshness at a glance. */
export function TablesTable({ database, ...props }: TablesTableProps) {
  return (
    <ResourceTable<Table>
      {...props}
      noun="tables"
      rowKey={(t) => t.Name ?? ""}
      defaultSort={{ id: "name", desc: false }}
      emptyIcon={Table2}
      emptyTitle="No tables yet"
      emptyDescription="Create one over data already in S3: the wizard reads the schema from a sample file and finds Hive partitions."
      rowActions={(t) => (
        <RowActions>
          <QueryInAthenaRowAction database={database} table={t.Name} />
        </RowActions>
      )}
      columns={[
        {
          id: "name",
          header: "Name",
          sortValue: (t) => t.Name,
          cell: (t) => <ResourceName icon={Table2} name={t.Name} />,
        },
        { id: "format", header: "Format", cell: (t) => <FormatBadge table={t} /> },
        {
          id: "location",
          header: "Location",
          interactive: true,
          cell: (t) => {
            const location = tableLocation(t)
            return location ? <S3UriLink uri={location} /> : <Muted />
          },
        },
        {
          id: "partitioned",
          header: "Partitioned by",
          sortValue: (t) => t.PartitionKeys?.length ?? 0,
          cell: (t) =>
            t.PartitionKeys?.length ? t.PartitionKeys.map((k) => k.Name).join(", ") : <Muted />,
        },
        {
          id: "columns",
          header: "Columns",
          headerClassName: "text-right",
          cellClassName: "text-right tabular-nums",
          sortValue: (t) => t.StorageDescriptor?.Columns?.length ?? 0,
          cell: (t) => t.StorageDescriptor?.Columns?.length ?? 0,
        },
        {
          id: "updated",
          header: "Updated",
          sortValue: (t) => t.UpdateTime ?? t.CreateTime,
          cell: (t) => formatDate(t.UpdateTime ?? t.CreateTime),
        },
      ]}
    />
  )
}
