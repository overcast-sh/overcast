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

/**
 * The router parses each search value as JSON first, so `q=42` arrives as a
 * number; it is read back as the text it was.
 */
function text(value: unknown): string | undefined {
  if (typeof value === "string") return value
  if (typeof value === "number" || typeof value === "boolean") return String(value)
  return undefined
}

/**
 * A snapshot id. The router's own links write it quoted, so it arrives as the
 * string it was. A bare id typed into the address bar has already been parsed
 * as a double, and one past 2^53 has been rounded to an id that does not
 * exist, so it is dropped rather than opening the wrong snapshot.
 */
function snapshotId(value: unknown): string | undefined {
  if (typeof value === "string") return value
  return typeof value === "number" && Number.isSafeInteger(value) ? String(value) : undefined
}

function oneOf<T extends string>(options: readonly T[], value: unknown): T | undefined {
  return (options as readonly unknown[]).includes(value) ? (value as T) : undefined
}

export const validateTableBucketsSearch = (s: Record<string, unknown>): TableBucketsSearch => ({
  q: text(s.q),
  sort: text(s.sort),
})

export const validateTableBucketSearch = (s: Record<string, unknown>): TableBucketSearch => ({
  tab: oneOf(BUCKET_TABS, s.tab),
  q: text(s.q),
})

export const validateTableSearch = (s: Record<string, unknown>): TableSearch => ({
  tab: oneOf(TABLE_TABS, s.tab),
  snapshot: snapshotId(s.snapshot),
  version: text(s.version),
  compare: text(s.compare),
})
