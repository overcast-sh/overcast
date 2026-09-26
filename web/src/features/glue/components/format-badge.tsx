import type { Table } from "@aws-sdk/client-glue"
import { Badge } from "@/components/ui/badge"
import { tableFormat } from "../table-format"

/** A table's format as a badge — `CSV`, `PARQUET`, `ICEBERG` — or an em dash when nothing says. */
export function FormatBadge({ table }: { table: Table }) {
  const format = tableFormat(table)
  if (!format) return <span className="text-fg-subtle">—</span>
  return <Badge variant={format === "ICEBERG" ? "accent" : "default"}>{format}</Badge>
}
