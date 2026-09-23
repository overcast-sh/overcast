/**
 * Which data-file preview an object gets, if any.
 *
 * - `csv`, `tsv`, `jsonl` — text, streamed and indexed into the `DataGrid`,
 *   with a toggle back to the raw text (its opening window);
 * - `parquet` — binary, read by Range into the `DataGrid`: the footer, then
 *   only the rows and columns in view;
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

/** The kinds shown as rows in the `DataGrid`. */
export type TabularKind = "csv" | "tsv" | "jsonl" | "parquet"

interface KindTraits {
  /** The format's name, as the preview's badge shows it. */
  label: string
  /** Key extensions that name the kind, lowercase and without the dot. */
  extensions: readonly string[]
  /** Media types that name the kind, whatever the key says. */
  contentTypes: readonly string[]
  /** Shown as rows in the grid, which is what widens the dialog. */
  tabular: boolean
}

const KINDS: Record<DataPreviewKind, KindTraits> = {
  csv: {
    label: "CSV",
    extensions: ["csv"],
    contentTypes: ["text/csv", "application/csv"],
    tabular: true,
  },
  tsv: {
    label: "TSV",
    extensions: ["tsv", "tab"],
    contentTypes: ["text/tab-separated-values"],
    tabular: true,
  },
  jsonl: {
    label: "JSON Lines",
    extensions: ["jsonl", "ndjson"],
    contentTypes: [
      "application/x-ndjson",
      "application/ndjson",
      "application/jsonl",
      "application/x-jsonlines",
      "application/jsonlines",
    ],
    tabular: true,
  },
  parquet: {
    label: "Parquet",
    extensions: ["parquet", "parq", "pqt"],
    contentTypes: [
      "application/vnd.apache.parquet",
      "application/x-parquet",
      "application/parquet",
    ],
    tabular: true,
  },
  avro: {
    label: "Avro",
    extensions: ["avro"],
    contentTypes: [
      "avro/binary",
      "application/avro",
      "application/x-avro",
      "application/vnd.apache.avro",
    ],
    tabular: false,
  },
  // Named by its key alone (see isIcebergMetadataKey): by content type and
  // extension it is plain JSON.
  "iceberg-metadata": {
    label: "Iceberg metadata",
    extensions: [],
    contentTypes: [],
    tabular: false,
  },
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

/** The kinds shown as rows in the grid. */
export function isTabularKind(kind: DataPreviewKind | null): kind is TabularKind {
  return kind !== null && KINDS[kind].tabular
}

/** The format's name for a badge: "CSV", "JSON Lines". */
export function kindLabel(kind: DataPreviewKind): string {
  return KINDS[kind].label
}
