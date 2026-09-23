import type { FileMetaData } from "hyparquet"
import { LruCache } from "@/lib/lru-cache"
import {
  openParquet,
  readParquetRows,
  schedulerAsyncBuffer,
  type ParquetHead,
} from "./parquet-reader"
import type { RangeScheduler } from "./range-scheduler"
import { columnBytes } from "./value-bytes"
import type { ColumnarRows } from "./worker-protocol"

/**
 * A Parquet object open in the data worker. The footer is read once, on
 * open — a suffix range, then its exact length if it is longer — and kept;
 * every read after it names its columns, so only their chunks are fetched.
 *
 * **Files without a page index.** Most writers (pyarrow and Spark by
 * default) leave out the offset index that lets a reader fetch only the pages
 * holding the rows asked for. Then the smallest thing hyparquet can read is a
 * whole column chunk — a row group's worth of one column, often tens of MB —
 * and a grid reading 1,000 rows at a time would fetch and decode each chunk
 * again for every block in it. For those files, whole chunks are decoded once
 * and kept, byte-capped, and blocks are cut from them.
 */

/** Decoded column chunks kept for files without a page index. */
const DECODED_CHUNK_BYTES = 48 * 1024 * 1024

interface RowGroupSpan {
  start: number
  end: number
}

type OnColumn = (index: number, values: unknown[]) => void

export class ParquetFile {
  private readonly scheduler: RangeScheduler
  private readonly size: number
  private readonly metadata: FileMetaData
  private readonly names: string[]
  private readonly groups: RowGroupSpan[]
  /** Decoded chunks by `group:column`, only for a file without a page index. */
  private readonly chunks: LruCache<unknown[]> | null

  private constructor(
    scheduler: RangeScheduler,
    size: number,
    metadata: FileMetaData,
    names: string[],
  ) {
    this.scheduler = scheduler
    this.size = size
    this.metadata = metadata
    this.names = names
    let at = 0
    this.groups = metadata.row_groups.map((group) => {
      const start = at
      at += Number(group.num_rows)
      return { start, end: at }
    })
    const paged = metadata.row_groups.every((group) =>
      group.columns.every((chunk) => chunk.offset_index_offset !== undefined),
    )
    this.chunks = paged ? null : new LruCache(DECODED_CHUNK_BYTES, { maxEntryShare: 1 })
  }

  static async open(
    scheduler: RangeScheduler,
    size: number,
  ): Promise<{ file: ParquetFile; head: ParquetHead }> {
    const { head, metadata } = await openParquet(schedulerAsyncBuffer(scheduler, size))
    const names = head.columns.map((column) => column.name)
    return { file: new ParquetFile(scheduler, size, metadata, names), head }
  }

  /**
   * Rows `[start, end)` of the columns at `indices`, laid out by column index.
   * `onColumn` hears each column the moment it is ready, so the grid paints it
   * without waiting for the slowest; `signal` aborts the chunks still coming.
   */
  async read(
    start: number,
    end: number,
    indices: readonly number[],
    signal: AbortSignal,
    onColumn: OnColumn,
  ): Promise<ColumnarRows> {
    const values = this.chunks
      ? await this.readFromChunks(this.chunks, start, end, indices, signal, onColumn)
      : await this.decode(start, end, indices, signal, onColumn)
    const columns: (unknown[] | undefined)[] = []
    indices.forEach((index, position) => (columns[index] = values[position]))
    return { count: end - start, columns }
  }

  /** Rows `[start, end)` of each column in `indices`, straight from the file. */
  private decode(
    start: number,
    end: number,
    indices: readonly number[],
    signal: AbortSignal,
    onColumn?: OnColumn,
  ): Promise<unknown[][]> {
    return readParquetRows(
      schedulerAsyncBuffer(this.scheduler, this.size, signal),
      this.metadata,
      start,
      end,
      indices.map((index) => this.names[index]),
      onColumn && ((position, decoded) => onColumn(indices[position], decoded)),
    )
  }

  /** The same rows, cut from whole decoded chunks — each decoded once while it stays cached. */
  private async readFromChunks(
    chunks: LruCache<unknown[]>,
    start: number,
    end: number,
    indices: readonly number[],
    signal: AbortSignal,
    onColumn: OnColumn,
  ): Promise<unknown[][]> {
    const spans = this.groups
      .map((span, group) => ({ ...span, group }))
      .filter((span) => span.start < end && span.end > start)
    const pieces = indices.map(() => [] as unknown[][])
    for (const span of spans) {
      const key = (index: number) => `${span.group}:${index}`
      // Held for this span before anything is stored, so storing one chunk
      // cannot evict another this read is about to cut from.
      const held = new Map(indices.map((index) => [index, chunks.get(key(index))]))
      const missing = indices.filter((index) => held.get(index) === undefined)
      if (missing.length > 0) {
        const decoded = await this.decode(span.start, span.end, missing, signal)
        missing.forEach((index, i) => {
          held.set(index, decoded[i])
          // A chunk larger than the whole budget is not kept, and is decoded again next time.
          chunks.put(key(index), decoded[i], columnBytes(decoded[i]))
        })
      }
      const from = Math.max(start, span.start) - span.start
      const to = Math.min(end, span.end) - span.start
      indices.forEach((index, position) => {
        pieces[position].push((held.get(index) ?? []).slice(from, to))
      })
    }
    return pieces.map((parts, position) => {
      const values = parts.flat()
      onColumn(indices[position], values)
      return values
    })
  }
}
