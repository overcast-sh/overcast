import { BaseSource, BLOCK_ROWS, sliceBlock } from "./base-source"
import type { DataColumn, RowBlock, RowSource } from "./row-source"
import { WorkerChannel } from "./worker-channel"
import type { DataWorkerPort, IndexProgress, TextKind } from "./worker-protocol"

/** Bytes indexed per run before the footer asks to continue. */
export const INDEX_BYTE_LIMIT = 512 * 1024 * 1024

/** On demand (`Save-Data`), how far ahead of the reader the index is taken. */
const ON_DEMAND_LEAD = BLOCK_ROWS * 5

export interface TextSourceOptions {
  url: string
  size: number
  kind: TextKind
  /** CSV: every value is quoted (Athena's result CSV), so an unquoted empty field is a NULL. */
  quotedValues?: boolean
  port: DataWorkerPort
  /** `Save-Data`: index only as far as the user scrolls. */
  saveData?: boolean
  byteLimit?: number
  signal?: AbortSignal
}

export interface TextRowSource extends RowSource {
  /** The delimiter the text itself chose, for CSV and TSV. */
  readonly delimiter?: string
}

/**
 * A CSV, TSV or JSON Lines object. Resolves as soon as the header and the
 * first rows have parsed; the rest of the file is indexed in the background
 * by the worker, and `rowCount` grows as it is.
 *
 * Rejects with `NotTabularError` when the file does not read as a table (an
 * unclosed quote, JSON Lines records of different shapes); the preview then
 * shows the raw text with the reason.
 */
export async function openTextSource(options: TextSourceOptions): Promise<TextRowSource> {
  const source = new TextSource(options)
  try {
    await source.open(options.signal)
  } catch (error) {
    source.dispose()
    throw error
  }
  return source
}

class TextSource extends BaseSource implements TextRowSource {
  columns: DataColumn[] = []
  delimiter?: string
  readonly projects = false
  /** `offsets[k]` is the byte where row `k * BLOCK_ROWS` starts. */
  private readonly offsets: number[] = []
  /** Byte just past the last indexed record. */
  private end = 0
  /** The rows parsed from the first 64 KB, served until the index covers them. */
  private first: RowBlock | null = null
  private readonly channel: WorkerChannel
  private readonly options: TextSourceOptions

  constructor(options: TextSourceOptions) {
    super()
    this.options = options
    this.channel = new WorkerChannel(options.port, (event) => {
      if (event.type === "index") this.onIndex(event)
      else this.markChanged()
    })
    this.indexing = {
      state: options.saveData ? "on-demand" : "running",
      rows: 0,
      bytes: 0,
      totalBytes: options.size,
    }
  }

  async open(signal?: AbortSignal): Promise<void> {
    const { url, size, kind, quotedValues, byteLimit = INDEX_BYTE_LIMIT, saveData } = this.options
    const head = await this.channel.request(
      "text-head",
      (id) => ({
        type: "open-text",
        id,
        url,
        size,
        kind,
        quotedValues,
        every: BLOCK_ROWS,
        byteLimit,
        mode: saveData ? "on-demand" : "background",
        untilRows: ON_DEMAND_LEAD,
      }),
      { signal },
    )
    this.columns = head.columns
    this.delimiter = head.delimiter
    this.first = { start: 0, ...head.first }
    // The first rows are on screen before the index has caught up with them.
    if (this.rowCount.value < head.first.count) {
      this.rowCount = { value: head.first.count, exact: false }
    }
  }

  async getRows(start: number, end: number, _cols: readonly number[], signal: AbortSignal) {
    if (this.first && end <= this.first.count) return sliceBlock(this.first, start, end)
    this.leadOnDemand(end)
    const block = Math.floor(start / BLOCK_ROWS)
    const from = this.offsets.at(block)
    const to = this.offsets.at(block + 1) ?? this.end
    if (from === undefined || to <= from) throw new Error(`Row ${start + 1} is not indexed yet.`)
    const reply = await this.channel.request(
      "rows",
      (id) => ({
        type: "read-text",
        id,
        start: from,
        end: to,
        count: Math.min(BLOCK_ROWS, this.rowCount.value - block * BLOCK_ROWS),
      }),
      { signal },
    )
    return sliceBlock({ start: block * BLOCK_ROWS, ...reply }, start, end)
  }

  continueIndexing(untilRows?: number): void {
    const status = this.indexing
    if (this.disposed || !status) return
    if (status.state === "paused-limit") {
      this.indexing = { ...status, state: this.options.saveData ? "on-demand" : "running" }
      this.notify()
    }
    this.channel.send({ type: "continue", untilRows })
  }

  dispose(): void {
    if (this.disposed) return
    super.dispose()
    this.channel.close()
  }

  /** On demand (`Save-Data`), reading near the end of the index asks for a few blocks more. */
  private leadOnDemand(end: number): void {
    const status = this.indexing
    if (status?.state === "on-demand" && end + BLOCK_ROWS >= status.rows) {
      this.continueIndexing(status.rows + ON_DEMAND_LEAD)
    }
  }

  private onIndex(progress: IndexProgress): void {
    if (this.disposed) return
    this.offsets.push(...progress.offsets)
    this.end = progress.end
    const { state, rows, bytes, totalBytes, error } = progress
    this.indexing = { state, rows, bytes, totalBytes, error }
    const exact = state === "done"
    this.rowCount = { value: exact ? rows : Math.max(rows, this.first?.count ?? 0), exact }
    this.notify()
  }
}
