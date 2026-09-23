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

interface KindTraits {
  /** Key extensions that name the kind, lowercase and without the dot. */
  extensions: readonly string[]
  /** Media types that name the kind, whatever the key says. */
  contentTypes: readonly string[]
  /** `text` through the preview window, `range` by the file's own structure, `none` not read. */
  read: "text" | "range" | "none"
  /** Shown as a table of rows, which is what widens the dialog. */
  tabular: boolean
}

const KINDS: Record<DataPreviewKind, KindTraits> = {
  csv: {
    extensions: ["csv"],
    contentTypes: ["text/csv", "application/csv"],
    read: "text",
    tabular: true,
  },
  tsv: {
    extensions: ["tsv", "tab"],
    contentTypes: ["text/tab-separated-values"],
    read: "text",
    tabular: true,
  },
  jsonl: {
    extensions: ["jsonl", "ndjson"],
    contentTypes: [
      "application/x-ndjson",
      "application/ndjson",
      "application/jsonl",
      "application/x-jsonlines",
      "application/jsonlines",
    ],
    read: "text",
    tabular: true,
  },
  parquet: {
    extensions: ["parquet", "parq", "pqt"],
    contentTypes: [
      "application/vnd.apache.parquet",
      "application/x-parquet",
      "application/parquet",
    ],
    read: "range",
    tabular: true,
  },
  avro: {
    extensions: ["avro"],
    contentTypes: [
      "avro/binary",
      "application/avro",
      "application/x-avro",
      "application/vnd.apache.avro",
    ],
    read: "none",
    tabular: false,
  },
  // Named by its key alone (see isIcebergMetadataKey): by content type and
  // extension it is plain JSON.
  "iceberg-metadata": { extensions: [], contentTypes: [], read: "text", tabular: false },
}

function lookup(pick: (traits: KindTraits) => readonly string[]): Map<string, DataPreviewKind> {
  const entries = Object.entries(KINDS) as [DataPreviewKind, KindTraits][]
  return new Map(entries.flatMap(([kind, traits]) => pick(traits).map((name) => [name, kind])))
}

const BY_CONTENT_TYPE = lookup((traits) => traits.contentTypes)
const BY_EXTENSION = lookup((traits) => traits.extensions)

export function dataPreviewKind(contentType: string, key: string): DataPreviewKind | null {
  // Named first: `v3.metadata.json` is JSON by every other measure, and the
  // content type (`application/json`) would otherwise settle it as plain JSON.
  if (isIcebergMetadataKey(key)) return "iceberg-metadata"
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase()
  const extension = /\.([^./]+)$/.exec(key.toLowerCase())?.[1] ?? ""
  return BY_CONTENT_TYPE.get(mediaType) ?? BY_EXTENSION.get(extension) ?? null
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
  return kind !== null && KINDS[kind].read === "text"
}

/** The kinds shown as a table of rows. */
export function isTabularKind(kind: DataPreviewKind | null): boolean {
  return kind !== null && KINDS[kind].tabular
}
