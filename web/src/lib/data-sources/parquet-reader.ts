import type { AsyncBuffer, Compressors, FileMetaData, SchemaElement, SchemaTree } from "hyparquet"
import type { GridColumn } from "@/components/data-grid/row-source"
import { RangeScheduler } from "./range-scheduler"

/**
 * Random access into a Parquet object, for the data worker.
 *
 * Only this module imports `hyparquet`, and only with `import()`, so it lands
 * in the worker's own lazy chunk. The decompressors for codecs hyparquet lacks
 * (`hyparquet-compressors`) are a second chunk, fetched only when a row group
 * actually uses one of them.
 *
 * What is read, and when:
 *
 * 1. `openParquet` reads the footer — the last `FOOTER_FETCH_BYTES`, plus one
 *    more request if the metadata is longer — which gives the schema, the
 *    exact row count and every column chunk's byte range;
 * 2. `readParquetRows` reads rows `[start, end)` of **only the requested
 *    columns**: hyparquet plans the read from the footer and fetches just the
 *    row groups and column chunks it needs, so scrolling a 200-column file
 *    across its visible six columns fetches six column chunks, not 200.
 *
 * Every read is a ranged GET through a `RangeScheduler`: coalesced, capped at
 * four in flight, and behind a byte-capped cache of raw ranges — consecutive
 * 1,000-row blocks usually fall in the same column chunk, and without the
 * cache each block would fetch it again. The offset index, where the writer
 * left one, narrows a read to the pages holding the requested rows.
 */

/**
 * The footer's opening read. Most footers are a few KiB; a wide schema with
 * statistics on every column can run to hundreds, and costs a second read.
 */
export const FOOTER_FETCH_BYTES = 64 * 1024

/** Codecs `hyparquet` decodes on its own. */
const BUILT_IN_CODECS = new Set(["UNCOMPRESSED", "SNAPPY"])

/**
 * Codecs decoded through `hyparquet-compressors`. ZSTD is the one that
 * matters most: Iceberg writes ZSTD Parquet by default, and so does S3 Tables.
 * BROTLI is deliberately absent — its decoder carries a ~80 KB dictionary for
 * a codec almost no writer uses — and LZO has no decoder at all; both keep
 * the schema readable and name the codec when rows are asked for.
 */
const PLUGIN_CODECS = new Set(["GZIP", "ZSTD", "LZ4", "LZ4_RAW"])

async function loadCompressors(): Promise<Compressors> {
  const { decompressZstd, gunzip, decompressLz4, decompressLz4Raw } =
    await import("hyparquet-compressors")
  return {
    GZIP: (input, length) => gunzip(input, new Uint8Array(length)),
    ZSTD: (input) => decompressZstd(input),
    LZ4: decompressLz4,
    LZ4_RAW: decompressLz4Raw,
  }
}

/**
 * An `AsyncBuffer` over one object, reading through a `RangeScheduler` — so
 * the column chunks hyparquet asks for together are coalesced into as few
 * requests as their layout allows, at most four at a time, from a raw-byte
 * cache when they were read before.
 *
 * The length comes from the HeadObject the console has already made, so there
 * is no probe request.
 */
export function schedulerAsyncBuffer(scheduler: RangeScheduler, byteLength: number): AsyncBuffer {
  return {
    byteLength,
    async slice(start: number, end?: number): Promise<ArrayBuffer> {
      const bytes = await scheduler.read(start, end ?? byteLength)
      // A copy: the scheduler hands out views of a coalesced response, and
      // hyparquet wants an ArrayBuffer of its own.
      return bytes.slice().buffer
    },
  }
}

/** An `AsyncBuffer` straight over a URL — the scheduler with its defaults. */
export function rangeAsyncBuffer(
  url: string,
  byteLength: number,
  fetchImpl: typeof fetch = fetch,
): AsyncBuffer {
  return schedulerAsyncBuffer(new RangeScheduler(url, fetchImpl), byteLength)
}

export interface ParquetField {
  name: string
  type: string
  nullable: boolean
}

export interface ParquetHead {
  columns: GridColumn[]
  /** The schema, for the Schema view: name, type and nullability per column. */
  fields: ParquetField[]
  numRows: number
  rowGroups: number
  /** Compression codecs the file uses, e.g. `["ZSTD"]`. */
  codecs: string[]
  /**
   * Why rows cannot be read while the schema can: a codec the reader does not
   * decode. Undefined when rows read.
   */
  rowsError?: string
  createdBy?: string
}

export interface OpenParquet {
  head: ParquetHead
  metadata: FileMetaData
}

/** Reads the footer. Throws when it cannot: not a Parquet file, or unreachable. */
export async function openParquet(
  file: AsyncBuffer,
  { footerFetchBytes = FOOTER_FETCH_BYTES } = {},
): Promise<OpenParquet> {
  const { parquetMetadataAsync, parquetSchema } = await import("hyparquet")
  // A suffix read of the last 64 KiB holds most footers whole; a longer one
  // costs exactly one more read, of exactly its length.
  const metadata = await parquetMetadataAsync(file, { initialFetchSize: footerFetchBytes })
  const tree = parquetSchema(metadata)
  const codecs = [
    ...new Set(metadata.row_groups.flatMap((g) => g.columns.map((c) => c.meta_data?.codec ?? ""))),
  ].filter(Boolean)
  const unsupported = codecs.filter((c) => !BUILT_IN_CODECS.has(c) && !PLUGIN_CODECS.has(c))
  return {
    metadata,
    head: {
      columns: tree.children.map(gridColumn),
      fields: tree.children.map((c) => ({
        name: c.element.name,
        type: describeType(c),
        nullable: c.element.repetition_type !== "REQUIRED",
      })),
      numRows: Number(metadata.num_rows),
      rowGroups: metadata.row_groups.length,
      codecs,
      createdBy: metadata.created_by,
      rowsError:
        unsupported.length > 0
          ? `Rows are compressed with ${unsupported.join(", ")}, which the console does not decode. The schema still reads, from the footer, which is never compressed.`
          : undefined,
    },
  }
}

/**
 * Rows `[start, end)` of the named columns, one array per column in `names`
 * order. `onColumn` is called as each column's values are decoded, so the
 * grid can paint a column the moment it arrives rather than when the slowest
 * one does. A null list or struct comes back from hyparquet as `undefined`;
 * in Parquet that is a NULL like any other, so it is returned as `null`.
 */
export async function readParquetRows(
  file: AsyncBuffer,
  metadata: FileMetaData,
  start: number,
  end: number,
  names: readonly string[],
  onColumn?: (index: number, values: unknown[]) => void,
): Promise<unknown[][]> {
  const { parquetRead } = await import("hyparquet")
  const needsPlugin = metadata.row_groups.some((g) =>
    g.columns.some((c) => PLUGIN_CODECS.has(c.meta_data?.codec ?? "")),
  )
  const count = end - start
  const out = names.map(() => new Array<unknown>(count).fill(null))
  const filled = names.map(() => 0)
  const position = new Map(names.map((name, i) => [name, i]))
  await parquetRead({
    file,
    metadata,
    rowStart: start,
    rowEnd: end,
    columns: [...names],
    useOffsetIndex: true,
    compressors: needsPlugin ? await loadCompressors() : undefined,
    // Chunks can hold rows outside the range asked for; only the overlap is kept.
    onChunk: ({ columnName, columnData, rowStart }) => {
      const i = position.get(columnName)
      if (i === undefined) return
      const from = Math.max(start, rowStart)
      const to = Math.min(end, rowStart + columnData.length)
      for (let row = from; row < to; row++) {
        out[i][row - start] = columnData[row - rowStart] ?? null
      }
      filled[i] += Math.max(to - from, 0)
      if (filled[i] >= count) onColumn?.(i, out[i])
    },
  })
  return out
}

/** A schema column as the grid describes it: its type, alignment and formatting hints. */
export function gridColumn(node: SchemaTree): GridColumn {
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
