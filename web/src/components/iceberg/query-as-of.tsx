import { Link } from "@tanstack/react-router"
import { TextSearch } from "lucide-react"
import { RowAction } from "@/components/ui/resource-list-page"
import { athenaEditorLink } from "@/features/athena/links"
import type { IcebergSnapshot } from "./metadata"
import { snapshotQuerySql, type IcebergTableRef } from "./snapshot-sql"

/**
 * *Query as of this snapshot*: a new Athena query tab holding the table as it
 * was at the snapshot, in the table's own catalog and database. Not run —
 * the developer reads it and presses ⌘⏎.
 */
export function QueryAsOfSnapshot({
  table,
  snapshot,
}: {
  table: IcebergTableRef
  snapshot: IcebergSnapshot
}) {
  const link = athenaEditorLink({
    catalog: table.catalog,
    database: table.database,
    sql: snapshotQuerySql(table, snapshot.snapshotId),
  })
  return (
    <RowAction label="Query as of this snapshot" asChild>
      <Link {...link}>
        <TextSearch className="size-3.5" />
      </Link>
    </RowAction>
  )
}
