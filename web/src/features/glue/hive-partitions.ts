/**
 * Hive-style partitions in an S3 listing: `dt=2026-09-01/region=eu/part-0.csv`
 * under a table's prefix says the table is partitioned by `dt` and `region`,
 * and that this file is in the partition `('2026-09-01', 'eu')` — the layout
 * `MSCK REPAIR TABLE` and a Glue crawler both read.
 */

export interface ListedObject {
  key: string
  size: number
}

export interface DiscoveredPartition {
  /** One value per partition key, in key order. */
  values: string[]
  /** The partition's own prefix, `s3://bucket/table/dt=…/region=…/`. */
  location: string
}

export interface PartitionLayout {
  /** The partition keys, outermost first. Empty for an unpartitioned prefix. */
  keys: string[]
  partitions: DiscoveredPartition[]
}

const SEGMENT = /^([^=/]+)=(.*)$/

/**
 * Files Hive, Athena and Spark all skip: names starting `_` or `.`
 * (`_SUCCESS`, `.part-0.crc`, `_temporary/`), and the `$folder$` markers
 * older tools wrote. Anything else under the prefix is table data.
 */
function isHidden(relativeKey: string): boolean {
  return (
    relativeKey.endsWith("$folder$") ||
    relativeKey.split("/").some((part) => part.startsWith("_") || part.startsWith("."))
  )
}

/** Whether an object under the table's prefix is data a query would read. */
export function isDataObject(object: ListedObject, prefix: string): boolean {
  const relative = object.key.slice(prefix.length)
  return object.size > 0 && relative !== "" && !relative.endsWith("/") && !isHidden(relative)
}

interface Segment {
  key: string
  /** The value, with Hive's `%XX` escapes decoded. */
  value: string
  /** The folder as it is in the key, which is what the location must use. */
  folder: string
}

/** The leading `key=value` folders of a key relative to the table's prefix. */
function partitionSegments(relativeKey: string): Segment[] {
  const folders = relativeKey.split("/").slice(0, -1)
  const segments: Segment[] = []
  for (const folder of folders) {
    const match = SEGMENT.exec(folder)
    if (!match) break
    segments.push({ key: match[1], value: safeDecode(match[2]), folder })
  }
  return segments
}

/** Hive escapes `:`, `/`, `=` and friends in partition values as `%XX`. */
function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

/**
 * The partition keys and partitions under `prefix`, read from the data
 * objects' paths. The first data object decides the keys; an object whose
 * folders name different keys is not part of this layout and is left out,
 * as `MSCK REPAIR TABLE` leaves it out.
 */
export function discoverPartitions(
  bucket: string,
  prefix: string,
  objects: readonly ListedObject[],
): PartitionLayout {
  const data = objects.filter((o) => isDataObject(o, prefix))
  const first = data.at(0)
  if (!first) return { keys: [], partitions: [] }
  const keys = partitionSegments(first.key.slice(prefix.length)).map((s) => s.key)
  if (keys.length === 0) return { keys, partitions: [] }

  const partitions = new Map<string, DiscoveredPartition>()
  for (const object of data) {
    const segments = partitionSegments(object.key.slice(prefix.length)).slice(0, keys.length)
    if (segments.length !== keys.length || segments.some((s, i) => s.key !== keys[i])) continue
    const path = segments.map((s) => `${s.folder}/`).join("")
    if (!partitions.has(path)) {
      partitions.set(path, {
        values: segments.map((s) => s.value),
        location: `s3://${bucket}/${prefix}${path}`,
      })
    }
  }
  return { keys, partitions: [...partitions.values()] }
}
