/**
 * Which data-file preview an object gets, if any.
 *
 * - `csv`, `tsv`, `jsonl` — text, read through the 1 MiB window, shown as a
 *   table with a toggle back to the raw text;
 * - `parquet` — binary, read by Range: the footer, then the first row group;
 * - `avro` — binary and not decoded here; the dialog says so instead of
 *   offering nothing, since Iceberg's manifests and manifest lists are Avro and
 *   are exactly what a developer browsing a table's `metadata/` prefix opens;
 * - `iceberg-metadata` — an Iceberg table metadata file, which is JSON and
 *   keeps the JSON view with a summary above it.
 *
 * A content type that names one of these wins over the key's extension, the
 * way `formatPreviewText` already treats them; the extension decides when the
 * content type is generic, which is how most data files are uploaded
 * (`binary/octet-stream`, or `text/plain` for a CSV).
 */
export type DataPreviewKind = "csv" | "tsv" | "jsonl" | "parquet" | "avro" | "iceberg-metadata"

const BY_CONTENT_TYPE: Partial<Record<string, DataPreviewKind>> = {
  "text/csv": "csv",
  "application/csv": "csv",
  "text/tab-separated-values": "tsv",
  "application/x-ndjson": "jsonl",
  "application/ndjson": "jsonl",
  "application/jsonl": "jsonl",
  "application/x-jsonlines": "jsonl",
  "application/jsonlines": "jsonl",
  "application/vnd.apache.parquet": "parquet",
  "application/x-parquet": "parquet",
  "application/parquet": "parquet",
  "avro/binary": "avro",
  "application/avro": "avro",
  "application/x-avro": "avro",
  "application/vnd.apache.avro": "avro",
}

export function dataPreviewKind(contentType: string, key: string): DataPreviewKind | null {
  // Named first: `v3.metadata.json` is JSON by every other measure, and the
  // content type (`application/json`) would otherwise settle it as plain JSON.
  if (isIcebergMetadataKey(key)) return "iceberg-metadata"
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase()
  const byType = BY_CONTENT_TYPE[mediaType]
  if (byType) return byType
  const name = key.toLowerCase()
  if (/\.csv$/.test(name)) return "csv"
  if (/\.(tsv|tab)$/.test(name)) return "tsv"
  if (/\.(jsonl|ndjson)$/.test(name)) return "jsonl"
  if (/\.(parquet|parq|pqt)$/.test(name)) return "parquet"
  if (/\.avro$/.test(name)) return "avro"
  return null
}

/**
 * Iceberg writes table metadata as `<version>-<uuid>.metadata.json` (or
 * `v<N>.metadata.json` from the Hadoop catalog), optionally gzipped as
 * `.gz.metadata.json`, which is not text and is left alone.
 */
export function isIcebergMetadataKey(key: string): boolean {
  const name = key.slice(key.lastIndexOf("/") + 1).toLowerCase()
  return name.endsWith(".metadata.json") && !name.endsWith(".gz.metadata.json")
}

/** The kinds read as text through the preview window. */
export function isTextDataKind(kind: DataPreviewKind | null): boolean {
  return kind === "csv" || kind === "tsv" || kind === "jsonl" || kind === "iceberg-metadata"
}
