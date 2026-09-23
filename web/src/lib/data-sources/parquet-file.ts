import type { FileMetaData } from "hyparquet"
import {
  openParquet,
  readParquetRows,
  schedulerAsyncBuffer,
  type ParquetHead,
} from "./parquet-reader"
import type { RangeScheduler } from "./range-scheduler"
import type { ColumnarRows } from "./worker-protocol"

/**
 * A Parquet object open in the data worker. The footer is read once, on
 * open — a suffix range, then its exact length if it is longer — and kept;
 * every read after it names its columns, so only their chunks are fetched.
 */
export class ParquetFile {
  private readonly scheduler: RangeScheduler
  private readonly size: number
  private readonly metadata: FileMetaData
  private readonly names: string[]

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
   * `onColumn` hears each column the moment it decodes, so the grid paints it
   * without waiting for the slowest; `signal` aborts the chunks still coming.
   */
  async read(
    start: number,
    end: number,
    indices: readonly number[],
    signal: AbortSignal,
    onColumn: (index: number, values: unknown[]) => void,
  ): Promise<ColumnarRows> {
    const values = await readParquetRows(
      schedulerAsyncBuffer(this.scheduler, this.size, signal),
      this.metadata,
      start,
      end,
      indices.map((index) => this.names[index]),
      (position, decoded) => onColumn(indices[position], decoded),
    )
    const columns: (unknown[] | undefined)[] = []
    indices.forEach((index, position) => (columns[index] = values[position]))
    return { count: end - start, columns }
  }
}
