import type { AsyncBuffer, FileMetaData, SchemaElement, SchemaTree } from "hyparquet"
import { PREVIEW_COLUMN_LIMIT, PREVIEW_ROW_LIMIT, type PreviewTableModel } from "./preview-table"

/**
 * Parquet for the S3 preview, through `hyparquet`.
 *
 * Only this module imports the library, and only with `import()`, so it is its
 * own chunk and costs nothing until a Parquet object is opened. The type-only
 * import above is erased at build time.
 *
 * What is read, and in what order, is the point of the design:
 *
 * 1. the footer — the last `FOOTER_FETCH_BYTES`, plus one more request if the
 *    metadata is longer than that — which gives the schema, the row count and
 *    the row groups' byte ranges;
 * 2. the column chunks of the **first row group only**, for the first rows.
 *
 * Both through `rangeAsyncBuffer`, one HTTP Range request per slice, so a
 * 10 GB file costs the same two or three small requests as a 10 KB one. The
 * 1 MiB text window does not apply here; `MAX_ROW_GROUP_BYTES` is this path's
 * cap, and past it the schema is still shown.
 */

/**
 * The footer's opening read. Most footers are a few KiB; a wide schema with
 * statistics on every column can run to hundreds, and costs a second read.
 */
export const FOOTER_FETCH_BYTES = 64 * 1024

/**
 * A first row group bigger than this is not read: the schema is shown and the
 * rows are skipped with a note. Row groups are sized for scans (128 MiB is a
 * common writer default), and a preview must not download one to show 200 rows.
 */
export const MAX_ROW_GROUP_BYTES = 16 * 1024 * 1024

export interface ParquetField {
  name: string
  /** `INT64`, `STRING`, `DECIMAL(10,2)`, `TIMESTAMP(MICROS, UTC)`, `LIST<STRING>`, `STRUCT<city, zip>`. */
  type: string
  nullable: boolean
}

export interface ParquetPreview {
  fields: ParquetField[]
  numRows: number
  rowGroups: number
  createdBy?: string
  /** Compression codecs the file uses, e.g. `["SNAPPY"]`. */
  codecs: string[]
  /** The first rows; absent when they could not be read (see `rowsError`). */
  table?: PreviewTableModel
  /**
   * Why the rows are missing while the schema is not: a codec the reader does
   * not decode, a first row group past the byte cap, or a decode failure.
   */
  rowsError?: string
}

/** Codecs `hyparquet` decodes without a plug-in. */
const SUPPORTED_CODECS = new Set(["UNCOMPRESSED", "SNAPPY"])

/**
 * An `AsyncBuffer` over one S3 object: each `slice` is a ranged GET.
 *
 * The length comes from the HeadObject the dialog has already made, so there
 * is no probe request. A server that ignores Range and answers 200 with the
 * whole body is tolerated — the requested bytes are cut from it — so a
 * misbehaving proxy costs bandwidth rather than a wrong answer.
 */
export function rangeAsyncBuffer(
  url: string,
  byteLength: number,
  fetchImpl: typeof fetch = fetch,
): AsyncBuffer {
  return {
    byteLength,
    async slice(start: number, end?: number): Promise<ArrayBuffer> {
      const stop = end ?? byteLength
      if (stop <= start) return new ArrayBuffer(0)
      const res = await fetchImpl(url, { headers: { Range: `bytes=${start}-${stop - 1}` } })
      if (!res.ok) {
        throw Object.assign(new Error(`Preview failed: HTTP ${res.status}`), { status: res.status })
      }
      const body = await res.arrayBuffer()
      return res.status === 200 ? body.slice(start, stop) : body
    },
  }
}

/**
 * Reads the schema and the first rows. Throws only when the footer cannot be
 * read — not a Parquet file, or the object is unreachable; anything that goes
 * wrong after that leaves the schema standing and says why in `rowsError`.
 */
export async function readParquetPreview(
  file: AsyncBuffer,
  {
    maxRows = PREVIEW_ROW_LIMIT,
    maxRowGroupBytes = MAX_ROW_GROUP_BYTES,
    footerFetchBytes = FOOTER_FETCH_BYTES,
  } = {},
): Promise<ParquetPreview> {
  const { parquetMetadataAsync, parquetSchema, parquetReadObjects } = await import("hyparquet")
  const metadata = await parquetMetadataAsync(file, { initialFetchSize: footerFetchBytes })
  const tree = parquetSchema(metadata)
  const columns = tree.children
  const numRows = Number(metadata.num_rows)
  const codecs = [
    ...new Set(metadata.row_groups.flatMap((g) => g.columns.map((c) => c.meta_data?.codec ?? ""))),
  ].filter(Boolean)
  const preview: ParquetPreview = {
    fields: columns.map((c) => ({
      name: c.element.name,
      type: describeType(c),
      nullable: c.element.repetition_type !== "REQUIRED",
    })),
    numRows,
    rowGroups: metadata.row_groups.length,
    createdBy: metadata.created_by,
    codecs,
  }

  const first = metadata.row_groups.at(0)
  if (!first || numRows === 0) {
    preview.table = emptyTable(columns, numRows)
    return preview
  }
  const firstCodecs = new Set(first.columns.map((c) => c.meta_data?.codec ?? "UNCOMPRESSED"))
  const unsupported = [...firstCodecs].filter((c) => !SUPPORTED_CODECS.has(c))
  if (unsupported.length > 0) {
    preview.rowsError = `Rows are compressed with ${unsupported.join(", ")}, which the preview does not decode. The schema still reads, from the footer, which is never compressed.`
    return preview
  }
  const groupBytes = rowGroupBytes(metadata)
  if (groupBytes > maxRowGroupBytes) {
    preview.rowsError = `The first row group is ${formatMiB(groupBytes)}, over the preview's ${formatMiB(maxRowGroupBytes)} limit, so its rows are not read.`
    return preview
  }

  const shownColumns = columns.slice(0, PREVIEW_COLUMN_LIMIT)
  const rowEnd = Math.min(maxRows, Number(first.num_rows))
  try {
    const rows = await parquetReadObjects({
      file,
      metadata,
      rowStart: 0,
      rowEnd,
      columns: shownColumns.map((c) => c.element.name),
    })
    preview.table = {
      columns: shownColumns.map(previewColumn),
      // hyparquet leaves a null list or struct as `undefined`; in Parquet that
      // is a NULL like any other, so it is drawn as one.
      rows: rows.map((row) => shownColumns.map((c) => row[c.element.name] ?? null)),
      totalRows: numRows,
      totalIsEstimate: false,
      truncatedByBytes: false,
      hiddenColumns: columns.length - shownColumns.length,
    }
  } catch (error) {
    preview.rowsError = `The rows could not be decoded: ${error instanceof Error ? error.message : String(error)}`
  }
  return preview
}

function emptyTable(columns: SchemaTree[], numRows: number): PreviewTableModel {
  const shown = columns.slice(0, PREVIEW_COLUMN_LIMIT)
  return {
    columns: shown.map(previewColumn),
    rows: [],
    totalRows: numRows,
    totalIsEstimate: false,
    truncatedByBytes: false,
    hiddenColumns: columns.length - shown.length,
  }
}

/**
 * The first row group's size on disk. `total_compressed_size` is optional in
 * the format, so it falls back to the sum of the column chunks.
 */
function rowGroupBytes(metadata: FileMetaData): number {
  const group = metadata.row_groups[0]
  if (group.total_compressed_size !== undefined) return Number(group.total_compressed_size)
  return group.columns.reduce((sum, c) => sum + Number(c.meta_data?.total_compressed_size ?? 0), 0)
}

function formatMiB(bytes: number): string {
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MiB`
}

function previewColumn(node: SchemaTree) {
  const e = node.element
  const logical = e.logical_type
  const decimal = logical?.type === "DECIMAL" || e.converted_type === "DECIMAL"
  const temporal =
    logical?.type === "DATE" ||
    logical?.type === "TIME" ||
    logical?.type === "TIMESTAMP" ||
    /^(DATE|TIME_|TIMESTAMP_)/.test(e.converted_type ?? "")
  const numeric =
    node.children.length === 0 &&
    !temporal &&
    (decimal || ["INT32", "INT64", "FLOAT", "DOUBLE"].includes(e.type ?? ""))
  return {
    name: e.name,
    type: describeType(node),
    numeric,
    scale: decimal ? (logical?.type === "DECIMAL" ? logical.scale : (e.scale ?? 0)) : undefined,
    dateOnly: logical?.type === "DATE" || e.converted_type === "DATE",
  }
}

/**
 * A column's type the way a developer would say it: the logical type where
 * there is one (`STRING`, `DECIMAL(10,2)`, `TIMESTAMP(MICROS, UTC)`), the
 * physical type otherwise, and nested types by their shape.
 */
export function describeType(node: SchemaTree): string {
  const e = node.element
  if (node.children.length > 0) {
    if (isList(e)) return `LIST<${describeType(listElement(node))}>`
    if (e.logical_type?.type === "MAP" || e.converted_type === "MAP") {
      const kv = node.children.at(0)
      const k = kv?.children.at(0)
      const v = kv?.children.at(1)
      return `MAP<${k ? describeType(k) : "?"}, ${v ? describeType(v) : "?"}>`
    }
    return `STRUCT<${node.children.map((c) => c.element.name).join(", ")}>`
  }
  return scalarType(e)
}

function isList(e: SchemaElement): boolean {
  return e.logical_type?.type === "LIST" || e.converted_type === "LIST"
}

/** The three-level LIST's element (`list.element`), or the two-level legacy one. */
function listElement(node: SchemaTree): SchemaTree {
  const repeated = node.children.at(0)
  if (!repeated) return node
  return repeated.children.length === 1 ? repeated.children[0] : repeated
}

function scalarType(e: SchemaElement): string {
  const logical = e.logical_type
  if (logical) {
    switch (logical.type) {
      case "DECIMAL":
        return `DECIMAL(${logical.precision},${logical.scale})`
      case "TIMESTAMP":
      case "TIME":
        return `${logical.type}(${logical.unit}${logical.isAdjustedToUTC ? ", UTC" : ""})`
      case "INTEGER":
        return `${logical.isSigned ? "INT" : "UINT"}${logical.bitWidth}`
      default:
        return logical.type
    }
  }
  switch (e.converted_type) {
    case undefined:
      return e.type ?? "UNKNOWN"
    case "UTF8":
      return "STRING"
    case "DECIMAL":
      return `DECIMAL(${e.precision ?? "?"},${e.scale ?? 0})`
    case "TIMESTAMP_MILLIS":
      return "TIMESTAMP(MILLIS)"
    case "TIMESTAMP_MICROS":
      return "TIMESTAMP(MICROS)"
    default:
      return e.converted_type
  }
}
