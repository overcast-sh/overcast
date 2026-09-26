import { ATHENA_TAB } from "@/components/ui/arn-routes"
import { trinoIdentifier } from "@/lib/sql-quote"

/**
 * Links from the Glue pages into Athena.
 *
 * TODO(#2072): the Athena workspace publishes `athenaEditorLink` in
 * `@/features/athena/links`, with this signature and URL contract. When it
 * lands, import it from there and delete the local copy below. Until then
 * `/athena` declares no search params, so both links here are typed the way
 * `ResolvedRoute` types a route to a page that does not yet.
 */

export interface AthenaEditorLinkParams {
  /** The data catalog; the editor's current one when omitted. */
  catalog?: string
  /** The database the query runs in; the editor's current one when omitted. */
  database?: string
  /** The SQL for the new query tab. It is opened, not run. */
  sql: string
}

/** `/athena?tab=editor&catalog=…&database=…&sql=…`: a new query tab holding `sql`, in that context. */
export function athenaEditorLink({ catalog, database, sql }: AthenaEditorLinkParams): {
  to: string
  search: Record<string, string | undefined>
} {
  return { to: "/athena", search: { tab: "editor", catalog, database, sql } }
}

/** The Athena workgroups tab, filtered to one workgroup: the link an ARN to it resolves to. */
export function athenaWorkGroupLink(name: string): {
  to: string
  search: Record<string, string | undefined>
} {
  return { to: "/athena", search: { tab: ATHENA_TAB.workgroups, q: name } }
}

/** The first rows of a table, as Athena's own *Preview table* writes it. */
export function previewSql(database: string, table: string, limit = 10): string {
  return `SELECT * FROM ${trinoIdentifier(database)}.${trinoIdentifier(table)} LIMIT ${limit};`
}
