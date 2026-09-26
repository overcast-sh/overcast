import type { Column, Table } from "@aws-sdk/client-glue"
import { Columns3, Copy } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { SectionLabel } from "@/components/ui/primitives"
import { ResourceListCard } from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableColumn } from "@/components/ui/resource-table"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { tableDdl } from "../../table-ddl"
import { isIcebergTable } from "../../table-format"

interface SchemaRow {
  position: number
  column: Column
  partition: boolean
}

/** Iceberg writers record each field's id and whether it is optional on the Glue column. */
function icebergParam(column: Column, name: string): string | undefined {
  return column.Parameters?.[`iceberg.field.${name}`]
}

const BASE_COLUMNS: ResourceTableColumn<SchemaRow>[] = [
  {
    id: "position",
    header: "#",
    headerClassName: "w-10 text-right",
    cellClassName: "text-right tabular-nums text-fg-subtle",
    cell: (r) => r.position,
  },
  {
    id: "name",
    header: "Name",
    cell: (r) => (
      <span className="flex items-center gap-2">
        <span className="font-medium text-fg">{r.column.Name}</span>
        {r.partition && <Badge variant="accent">Partition</Badge>}
      </span>
    ),
  },
  { id: "type", header: "Type", cell: (r) => r.column.Type },
]

const ICEBERG_COLUMNS: ResourceTableColumn<SchemaRow>[] = [
  { id: "field-id", header: "Field id", cell: (r) => icebergParam(r.column, "id") ?? "—" },
  {
    id: "required",
    header: "Required",
    cell: (r) => {
      const optional = icebergParam(r.column, "optional")
      return optional === undefined ? "—" : optional === "false" ? "required" : "optional"
    },
  },
]

const COMMENT_COLUMN: ResourceTableColumn<SchemaRow> = {
  id: "comment",
  header: "Comment",
  prose: true,
  cell: (r) => r.column.Comment ?? <span className="text-fg-subtle">—</span>,
}

/** The columns, then the partition keys (marked), with the DDL that recreates the table. */
export function SchemaTab({ table }: { table: Table }) {
  const { copy } = useCopyToClipboard()
  const ddl = tableDdl(table)
  const columns = table.StorageDescriptor?.Columns ?? []
  const rows: SchemaRow[] = [
    ...columns.map((column) => ({ column, partition: false })),
    ...(table.PartitionKeys ?? []).map((column) => ({ column, partition: true })),
  ].map((row, i) => ({ ...row, position: i + 1 }))

  return (
    <ResourceListCard>
      <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-2.5">
        <SectionLabel>Columns</SectionLabel>
        {ddl && (
          <Button size="sm" variant="ghost" onClick={() => copy(ddl, { noun: "DDL" })}>
            <Copy className="h-3.5 w-3.5" />
            Copy DDL
          </Button>
        )}
      </div>
      <ResourceTable<SchemaRow>
        variant="embedded"
        query={{ data: rows, isLoading: false }}
        noun="columns"
        rowKey={(r) => `${r.partition ? "p" : "c"}:${r.column.Name ?? r.position}`}
        emptyIcon={Columns3}
        emptyTitle="No columns"
        emptyDescription="This table declares no columns."
        columns={[
          ...BASE_COLUMNS,
          ...(isIcebergTable(table) ? ICEBERG_COLUMNS : []),
          COMMENT_COLUMN,
        ]}
      />
    </ResourceListCard>
  )
}
