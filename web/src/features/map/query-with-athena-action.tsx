import { Link } from "@tanstack/react-router"
import { Database } from "lucide-react"
import { athenaEditorLink } from "@/features/athena/links"
import { previewSql } from "@/features/glue/athena-link"
import { peekActionClass } from "./map-peek-panel"

interface QueryWithAthenaActionProps {
  /** The table's catalog; the editor's current one when omitted. */
  catalog?: string
  database: string
  table: string
}

/**
 * *Query with Athena* in a table peek's header: a new editor tab in the
 * table's catalog and database, holding a `SELECT` of its first rows, not run.
 */
export function QueryWithAthenaAction({ catalog, database, table }: QueryWithAthenaActionProps) {
  return (
    <Link
      {...athenaEditorLink({ catalog, database, sql: previewSql(database, table) })}
      className={peekActionClass}
    >
      <Database aria-hidden className="h-3.5 w-3.5" />
      Query with Athena
    </Link>
  )
}
