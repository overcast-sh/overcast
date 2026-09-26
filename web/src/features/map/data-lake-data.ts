/**
 * Query options for the map's data-lake peeks.
 *
 *   firstDataFileQueryOptions(location) -> [...s3Keys.objects(), bucket, prefix, "first-data-file"]
 *
 * Under `s3Keys.objects()`, so an object written or removed under the
 * location makes the answer stale, as it makes every listing stale.
 */

import { queryOptions } from "@tanstack/react-query"
import { s3Keys } from "@/features/s3/data"
import { dataPreviewKind, isTabularKind, type TabularKind } from "@/features/s3/preview-kind"
import { parseS3Uri } from "@/lib/s3-uri"
import { s3 } from "@/services/api"
import type { S3Object } from "@/types"

/** Keys listed before the search for a data file gives up. */
const SCAN_LIMIT = 5_000

/** A file the grid can show a table's rows from. */
export interface DataFile {
  bucket: string
  object: S3Object
  kind: TabularKind
}

/**
 * Whether a key under a table's location holds its rows: a CSV, TSV, JSON
 * Lines or Parquet file that is not Iceberg metadata, and not hidden the way
 * Hive and Spark hide their bookkeeping (`_SUCCESS`, `.part-0.crc`,
 * `_temporary/`).
 */
export function isDataFile(key: string, prefix: string): boolean {
  const rest = key.slice(prefix.length)
  const parts = rest.split("/")
  if (parts.some((p) => p.startsWith("_") || p.startsWith("."))) return false
  if (parts.includes("metadata")) return false
  return isTabularKind(dataPreviewKind("", key))
}

async function findFirstDataFile(bucket: string, prefix: string): Promise<DataFile | null> {
  let token: string | undefined
  let scanned = 0
  do {
    const page = await s3.listObjects(bucket, { prefix, delimiter: "", maxKeys: 1000, token })
    const hit = page.objects.find((o) => isDataFile(o.key, prefix))
    if (hit) return { bucket, object: hit, kind: dataPreviewKind("", hit.key) as TabularKind }
    scanned += page.objects.length
    token = page.isTruncated ? page.nextContinuationToken : undefined
  } while (token && scanned < SCAN_LIMIT)
  return null
}

/**
 * The first file under a table's location that holds rows, or null when there
 * is none — a table nothing has been written to yet.
 */
export function firstDataFileQueryOptions(location: string) {
  const parsed = parseS3Uri(location)
  const bucket = parsed?.bucket ?? ""
  const key = parsed?.key ?? ""
  const prefix = key && !key.endsWith("/") ? `${key}/` : key
  return queryOptions({
    queryKey: [...s3Keys.objects(), bucket, prefix, "first-data-file"] as const,
    queryFn: () => findFirstDataFile(bucket, prefix),
    enabled: bucket !== "",
  })
}
