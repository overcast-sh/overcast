import { searchLargeId, searchOneOf, searchText } from "@/lib/search-params"

/**
 * The S3 Tables routes' search params: which tab, the list filter, and the
 * selection within a tab, so every view of a bucket or table deep-links.
 */

export const BUCKET_TABS = ["tables", "maintenance", "policy", "encryption", "tags"] as const
export type BucketTab = (typeof BUCKET_TABS)[number]

export const TABLE_TABS = [
  "overview",
  "schema",
  "snapshots",
  "metadata",
  "policy",
  "maintenance",
] as const
export type TableTab = (typeof TABLE_TABS)[number]

export interface TableBucketsSearch {
  q?: string
  sort?: string
}

export interface TableBucketSearch {
  tab?: BucketTab
  /** The filter over the bucket's tables and namespaces; a namespace search result sets it. */
  q?: string
}

export interface TableSearch {
  tab?: TableTab
  /** A snapshot whose *Diff with previous* opens. */
  snapshot?: string
  /** The metadata file shown, by file name; the current one when absent. */
  version?: string
  /** The metadata file the shown one is diffed against. */
  compare?: string
}

export const validateTableBucketsSearch = (s: Record<string, unknown>): TableBucketsSearch => ({
  q: searchText(s.q),
  sort: searchText(s.sort),
})

export const validateTableBucketSearch = (s: Record<string, unknown>): TableBucketSearch => ({
  tab: searchOneOf(BUCKET_TABS, s.tab),
  q: searchText(s.q),
})

export const validateTableSearch = (s: Record<string, unknown>): TableSearch => ({
  tab: searchOneOf(TABLE_TABS, s.tab),
  snapshot: searchLargeId(s.snapshot),
  version: searchText(s.version),
  compare: searchText(s.compare),
})
