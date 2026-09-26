/**
 * The `/athena` route's search params: the tab, the list filter, and the
 * editor deep link (`catalog`, `database`, `sql`). See `links.ts` for the
 * helper other pages use to build that link.
 */

import { ATHENA_TAB } from "@/components/ui/arn-routes"
import { searchText } from "@/lib/search-params"

/**
 * The page's tabs, in order. The list tabs' ids are `ATHENA_TAB`'s, which
 * ARN links and search results already open.
 */
export const ATHENA_TABS = [
  "editor",
  "history",
  ATHENA_TAB.savedQueries,
  ATHENA_TAB.workgroups,
  ATHENA_TAB.dataCatalogs,
] as const

export type AthenaTab = (typeof ATHENA_TABS)[number]

export interface AthenaSearch {
  /** The open tab; the editor when absent. */
  tab?: AthenaTab
  /** The filter on the History, Saved queries, Workgroups and Data catalogs tables. */
  q?: string
  /** Table sort, as `useSortSearchParam` reads it. */
  sort?: string
  /** One query execution, expanded in History. */
  execution?: string
  /** In Workgroups, the workgroup open in detail; in an editor link, the one the new tab runs in. */
  workgroup?: string
  /** Deep link into the editor: the query context and SQL for a new query tab. */
  catalog?: string
  database?: string
  sql?: string
}

function isAthenaTab(value: unknown): value is AthenaTab {
  return typeof value === "string" && (ATHENA_TABS as readonly string[]).includes(value)
}

export function validateAthenaSearch(search: Record<string, unknown>): AthenaSearch {
  return {
    tab: isAthenaTab(search.tab) ? search.tab : undefined,
    q: searchText(search.q),
    sort: searchText(search.sort),
    execution: searchText(search.execution),
    workgroup: searchText(search.workgroup),
    catalog: searchText(search.catalog),
    database: searchText(search.database),
    sql: searchText(search.sql),
  }
}
