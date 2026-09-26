/**
 * Links into the Athena console that other pages build.
 *
 * The editor deep link is a contract shared with the Glue and S3 Tables
 * pages: `/athena?tab=editor&catalog=<name>&database=<name>&sql=<SQL>`
 * opens a new query tab holding the SQL, with that query context set, and
 * does not run it. `workgroup=<name>` optionally names the workgroup it runs
 * in, for SQL that belongs to one (a saved query, a past execution).
 */

import { linkOptions } from "@tanstack/react-router"

export interface AthenaEditorLinkParams {
  /** The data catalog, e.g. `AwsDataCatalog`. The editor's current one when omitted. */
  catalog?: string
  /** The database the query runs in. The editor's current one when omitted. */
  database?: string
  /** The workgroup the query runs in. The editor's current one when omitted. */
  workGroup?: string
  /** The SQL for the new query tab. */
  sql: string
}

/**
 * Link options for opening SQL in a new Athena query tab. Spread them into a
 * `<Link>` or pass them to `navigate()`:
 *
 * ```tsx
 * <Link {...athenaEditorLink({ database: "sales", sql: 'SELECT * FROM "orders" LIMIT 10' })}>
 *   Query with Athena
 * </Link>
 * ```
 */
export function athenaEditorLink({ catalog, database, workGroup, sql }: AthenaEditorLinkParams) {
  return linkOptions({
    to: "/athena",
    search: { tab: "editor", catalog, database, workgroup: workGroup, sql },
  })
}
