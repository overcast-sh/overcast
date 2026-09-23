import { BaseSource } from "./base-source"
import type { ParquetField, ParquetHead } from "./parquet-reader"
import type { DataColumn, RowBlock, RowSource } from "./row-source"
import { WorkerChannel } from "./worker-channel"
import type { DataWorkerPort } from "./worker-protocol"

export interface ParquetSourceInfo {
  fields: ParquetField[]
  rowGroups: number
  codecs: string[]
  /** Why rows cannot be read while the schema can: a codec the console does not decode. */
  rowsError?: string
}

export interface ParquetRowSource extends RowSource {
  readonly info: ParquetSourceInfo
}

/**
 * A Parquet object. Resolves once the footer is read: the row count is exact
 * from the start, and every `getRows` reads only the requested columns, each
 * handed over (`onPartial`) the moment it decodes.
 */
export async function openParquetSource(options: {
  url: string
  size: number
  port: DataWorkerPort
  signal?: AbortSignal
}): Promise<ParquetRowSource> {
  const source = new ParquetSource(options.port)
  try {
    await source.open(options.url, options.size, options.signal)
  } catch (error) {
    source.dispose()
    throw error
  }
  return source
}

class ParquetSource extends BaseSource implements ParquetRowSource {
  columns: DataColumn[] = []
  readonly projects = true
  info: ParquetSourceInfo = { fields: [], rowGroups: 0, codecs: [] }
  private readonly channel: WorkerChannel

  constructor(port: DataWorkerPort) {
    super()
    this.channel = new WorkerChannel(port, (event) => {
      if (event.type === "changed") this.markChanged()
    })
  }

  async open(url: string, size: number, signal?: AbortSignal): Promise<void> {
    const head: ParquetHead = await this.channel.request(
      "parquet-head",
      (id) => ({ type: "open-parquet", id, url, size }),
      { signal },
    )
    this.columns = head.columns
    this.rowCount = { value: head.numRows, exact: true }
    const { fields, rowGroups, codecs, rowsError } = head
    this.info = { fields, rowGroups, codecs, rowsError }
  }

  async getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
    onPartial?: (block: RowBlock) => void,
  ): Promise<RowBlock> {
    if (this.info.rowsError) throw new Error(this.info.rowsError)
    const columns = cols.length > 0 ? [...cols] : this.columns.map((_, i) => i)
    const reply = await this.channel.request(
      "rows",
      (id) => ({ type: "read-parquet", id, start, end, columns }),
      {
        signal,
        onPartial:
          onPartial &&
          (({ column, count, values }) => {
            const partial: RowBlock["columns"] = []
            partial[column] = values
            onPartial({ start, count, columns: partial })
          }),
      },
    )
    return { start, ...reply }
  }

  dispose(): void {
    if (this.disposed) return
    super.dispose()
    this.channel.close()
  }
}
