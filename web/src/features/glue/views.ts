/**
 * The deep-linkable state of the Glue pages, kept out of the component files
 * so route files can validate search params against it.
 */

import { searchLargeId, searchText } from "@/lib/search-params"

export const TABLE_TABS = [
  "schema",
  "partitions",
  "data",
  "iceberg",
  "versions",
  "properties",
] as const

export type TableTab = (typeof TABLE_TABS)[number]

export function isTableTab(value: unknown): value is TableTab {
  return typeof value === "string" && (TABLE_TABS as readonly string[]).includes(value)
}

/**
 * The create-table wizard, open over the page: `?create=s3`, optionally
 * with the prefix to start from (`/glue/<database>?create=s3&location=…`
 * also names the database), so any page can link to it on a prefix.
 */
export interface CreateTableSearch {
  create?: "s3"
  /** `s3://bucket/prefix/` to start the wizard on. */
  location?: string
}

/** The Glue pages' list search params: the filter and the sort. */
export interface GlueListSearch extends CreateTableSearch {
  q?: string
  sort?: string
}

export interface GlueTableSearch {
  tab?: TableTab
  /** The Partitions tab's Glue `Expression`, sent to GetPartitions as written. */
  q?: string
  /** The two table versions the Versions tab compares. */
  from?: string
  to?: string
  /** The Iceberg tab's snapshot whose diff with the previous one is open. */
  snapshot?: string
  /** The Iceberg tab's metadata file, by file name; the current one when absent. */
  version?: string
  /** The metadata file `version` is diffed against. */
  compare?: string
}

export function validateListSearch(search: Record<string, unknown>): GlueListSearch {
  return {
    q: searchText(search.q),
    sort: searchText(search.sort),
    create: search.create === "s3" ? "s3" : undefined,
    location: searchText(search.location),
  }
}

export function validateTableSearch(search: Record<string, unknown>): GlueTableSearch {
  return {
    tab: isTableTab(search.tab) ? search.tab : undefined,
    q: searchText(search.q),
    from: searchText(search.from),
    to: searchText(search.to),
    snapshot: searchLargeId(search.snapshot),
    version: searchText(search.version),
    compare: searchText(search.compare),
  }
}
