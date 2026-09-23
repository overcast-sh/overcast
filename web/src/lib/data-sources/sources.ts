import {
  abortError,
  type GridColumn,
  type IndexingStatus,
  type RowBlock,
  type RowCount,
  type RowSource,
} from "@/components/data-grid/row-source"
import type { ParquetField } from "./parquet-reader"
import type { DataWorkerPort, FromWorker, TextKind, ToWorker } from "./worker-protocol"

/**
 * The row sources over files in S3 (or anywhere a ranged GET reaches): text
 * (CSV, TSV, JSON Lines), Parquet, and an in-memory source for small tables
 * such as a Parquet schema.
 *
 * The text and Parquet sources are thin: each owns one data worker, turns
 * `getRows` into a worker request, and keeps the few things the grid asks
 * about synchronously — the columns, the row count, the index offsets. The
 * worker does the fetching and parsing; `dispose()` terminates it.
 */

/** Rows per block — and per index point, for text. */
export const BLOCK_ROWS = 1000

/** Bytes indexed per run before the footer asks to continue. */
export const INDEX_BYTE_LIMIT = 512 * 1024 * 1024

/** Why an open failed, when it failed because the file is not the table it claims to be. */
export class NotTabularError extends Error {
  readonly notTabular = true
}

type Pending = {
  resolve: (message: FromWorker) => void
  reject: (error: Error) => void
  partial?: (message: Extract<FromWorker, { type: "rows-partial" }>) => void
}

/** Request/reply correlation over a data worker port. */
class Channel {
  private next = 1
  private readonly pending = new Map<number, Pending>()
  private readonly stop: () => void
  readonly port: DataWorkerPort

  constructor(port: DataWorkerPort, onUnsolicited: (message: FromWorker) => void) {
    this.port = port
    this.stop = port.listen((message) => {
      if (message.type === "index" || message.type === "changed") {
        onUnsolicited(message)
        return
      }
      const pending = this.pending.get(message.id)
      if (!pending) return
      if (message.type === "rows-partial") {
        pending.partial?.(message)
        return
      }
      this.pending.delete(message.id)
      if (message.type === "error") {
        pending.reject(
          message.code === "not-tabular"
            ? new NotTabularError(message.message)
            : new Error(message.message),
        )
      } else {
        pending.resolve(message)
      }
    })
  }

  request<T extends FromWorker>(
    build: (id: number) => ToWorker,
    signal?: AbortSignal,
    partial?: Pending["partial"],
  ): Promise<T> {
    const id = this.next++
    return new Promise<T>((resolve, reject) => {
      if (signal?.aborted) {
        reject(abortError())
        return
      }
      const onAbort = () => {
        if (!this.pending.delete(id)) return
        this.port.post({ type: "abort", id })
        reject(abortError())
      }
      signal?.addEventListener("abort", onAbort, { once: true })
      this.pending.set(id, {
        partial,
        resolve: (message) => {
          signal?.removeEventListener("abort", onAbort)
          resolve(message as T)
        },
        reject: (error) => {
          signal?.removeEventListener("abort", onAbort)
          reject(error)
        },
      })
      this.port.post(build(id))
    })
  }

  send(message: ToWorker): void {
    this.port.post(message)
  }

  close(): void {
    this.stop()
    for (const pending of this.pending.values()) pending.reject(abortError())
    this.pending.clear()
    this.port.terminate()
  }
}

abstract class BaseSource implements RowSource {
  abstract readonly columns: readonly GridColumn[]
  abstract readonly projects: boolean
  readonly blockSize = BLOCK_ROWS
  rowCount: RowCount = { value: 0, exact: false }
  indexing?: IndexingStatus
  changed = false
  private readonly listeners = new Set<() => void>()
  protected disposed = false

  abstract getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
  ): Promise<RowBlock>

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  protected markChanged(): void {
    if (this.changed) return
    this.changed = true
    this.notify()
  }

  protected notify(): void {
    for (const listener of this.listeners) listener()
  }

  dispose(): void {
    this.disposed = true
    this.listeners.clear()
  }
}

// ─── Text ────────────────────────────────────────────────────────────────────

export interface TextSourceOptions {
  url: string
  size: number
  kind: TextKind
  port: DataWorkerPort
  /** `Save-Data`: index only as far as the user scrolls. */
  saveData?: boolean
  byteLimit?: number
  signal?: AbortSignal
}

/**
 * A CSV, TSV or JSON Lines object. Resolves as soon as the header and the
 * first block have parsed — the rest of the file is indexed in the
 * background, and `rowCount` grows as it is.
 *
 * Rejects with `NotTabularError` when the file does not read as a table (an
 * unclosed quote, JSON Lines records of different shapes); the preview then
 * shows the raw text with the reason.
 */
export interface TextRowSource extends RowSource {
  /** The delimiter the text itself chose, for CSV and TSV. */
  readonly delimiter?: string
}

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
  columns: GridColumn[] = []
  delimiter?: string
  readonly projects = false
  /** `offsets[k]` is the byte where row `k * BLOCK_ROWS` starts. */
  private readonly offsets: number[] = []
  /** Byte just past the last indexed record. */
  private end = 0
  private first: RowBlock | null = null
  private readonly channel: Channel
  private readonly saveData: boolean
  private readonly options: TextSourceOptions

  constructor(options: TextSourceOptions) {
    super()
    this.options = options
    this.saveData = !!options.saveData
    this.channel = new Channel(options.port, (message) => {
      if (message.type === "index") this.onIndex(message)
      else if (message.type === "changed") this.markChanged()
    })
    this.indexing = {
      state: this.saveData ? "on-demand" : "running",
      rows: 0,
      bytes: 0,
      totalBytes: options.size,
    }
  }

  async open(signal?: AbortSignal): Promise<void> {
    const head = await this.channel.request<Extract<FromWorker, { type: "text-head" }>>(
      (id) => ({
        type: "open-text",
        id,
        url: this.options.url,
        size: this.options.size,
        kind: this.options.kind,
        every: BLOCK_ROWS,
        byteLimit: this.options.byteLimit ?? INDEX_BYTE_LIMIT,
        mode: this.saveData ? "on-demand" : "background",
        untilRows: BLOCK_ROWS * 3,
      }),
      signal,
    )
    this.columns = head.columns
    this.delimiter = head.delimiter
    this.first = { start: 0, count: head.first.count, columns: head.first.columns }
    // The first block is on screen before the index has caught up with it.
    if (this.rowCount.value < head.first.count) {
      this.rowCount = { value: head.first.count, exact: false }
    }
  }

  private onIndex(message: Extract<FromWorker, { type: "index" }>): void {
    if (this.disposed) return
    this.offsets.push(...message.offsets)
    this.end = message.end
    this.indexing = {
      state: message.state,
      rows: message.rows,
      bytes: message.bytes,
      totalBytes: message.totalBytes,
      error: message.error,
    }
    const exact = message.state === "done"
    this.rowCount = {
      value: Math.max(message.rows, exact ? 0 : (this.first?.count ?? 0)),
      exact,
    }
    this.notify()
  }

  async getRows(start: number, end: number, _cols: readonly number[], signal: AbortSignal) {
    const block = Math.floor(start / BLOCK_ROWS)
    if (block === 0 && this.first && end <= this.first.count) {
      return sliceBlock(this.first, start, end)
    }
    // On demand (Save-Data): reading near the end of what is indexed asks for
    // more, a few blocks at a time.
    const status = this.indexing
    if (status?.state === "on-demand" && end + BLOCK_ROWS >= status.rows) {
      this.continueIndexing(status.rows + BLOCK_ROWS * 5)
    }
    const from = this.offsets[block]
    const to = this.offsets[block + 1] ?? this.end
    if (from === undefined || to <= from) throw new Error(`Row ${start + 1} is not indexed yet.`)
    const count = Math.min(BLOCK_ROWS, this.rowCount.value - block * BLOCK_ROWS)
    const reply = await this.channel.request<Extract<FromWorker, { type: "rows" }>>(
      (id) => ({ type: "read-text", id, start: from, end: to, count }),
      signal,
    )
    return sliceBlock(
      { start: block * BLOCK_ROWS, count: reply.count, columns: reply.columns },
      start,
      end,
    )
  }

  continueIndexing(untilRows?: number): void {
    if (this.disposed) return
    const status = this.indexing
    if (status && status.state !== "running" && status.state !== "done") {
      this.indexing = { ...status, state: status.state === "on-demand" ? "on-demand" : "running" }
      this.notify()
    }
    this.channel.send({ type: "continue", id: 0, untilRows })
  }

  dispose(): void {
    if (this.disposed) return
    super.dispose()
    this.channel.close()
  }
}

/** `[start, end)` of a block, which may begin before `start`. */
function sliceBlock(block: RowBlock, start: number, end: number): RowBlock {
  if (block.start === start && block.start + block.count <= end) return block
  const from = start - block.start
  const to = Math.min(end, block.start + block.count) - block.start
  return {
    start,
    count: Math.max(to - from, 0),
    columns: block.columns.map((c) => (c ? Array.prototype.slice.call(c, from, to) : c)),
  }
}

// ─── Parquet ─────────────────────────────────────────────────────────────────

export interface ParquetSourceInfo {
  fields: ParquetField[]
  rowGroups: number
  codecs: string[]
  rowsError?: string
}

export interface ParquetSource extends RowSource {
  readonly info: ParquetSourceInfo
}

/**
 * A Parquet object. Resolves once the footer is read: the row count is exact
 * from the start, and every `getRows` reads only the requested columns.
 */
export async function openParquetSource(options: {
  url: string
  size: number
  port: DataWorkerPort
  signal?: AbortSignal
}): Promise<ParquetSource> {
  let source: ParquetRowSource | null = null
  const channel = new Channel(options.port, (message) => {
    if (message.type === "changed") source?.noteChanged()
  })
  try {
    const head = await channel.request<Extract<FromWorker, { type: "parquet-head" }>>(
      (id) => ({ type: "open-parquet", id, url: options.url, size: options.size }),
      options.signal,
    )
    source = new ParquetRowSource(channel, head)
    return source
  } catch (error) {
    channel.close()
    throw error
  }
}

class ParquetRowSource extends BaseSource implements ParquetSource {
  readonly columns: GridColumn[]
  readonly projects = true
  readonly info: ParquetSourceInfo
  private readonly channel: Channel

  constructor(channel: Channel, head: Extract<FromWorker, { type: "parquet-head" }>) {
    super()
    this.channel = channel
    this.columns = head.columns
    this.rowCount = { value: head.numRows, exact: true }
    this.info = {
      fields: head.fields,
      rowGroups: head.rowGroups,
      codecs: head.codecs,
      rowsError: head.rowsError,
    }
  }

  /** The worker saw the ETag move. */
  noteChanged(): void {
    this.markChanged()
  }

  async getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
    onPartial?: (block: RowBlock) => void,
  ) {
    if (this.info.rowsError) throw new Error(this.info.rowsError)
    const columns = cols.length > 0 ? [...cols] : this.columns.map((_, i) => i)
    const reply = await this.channel.request<Extract<FromWorker, { type: "rows" }>>(
      (id) => ({ type: "read-parquet", id, start, end, columns }),
      signal,
      onPartial
        ? (partial) => {
            const blockColumns: (unknown[] | undefined)[] = []
            blockColumns[partial.column] = partial.values
            onPartial({ start, count: partial.count, columns: blockColumns })
          }
        : undefined,
    )
    return { start, count: reply.count, columns: reply.columns }
  }

  dispose(): void {
    if (this.disposed) return
    super.dispose()
    this.channel.close()
  }
}

// ─── In memory ───────────────────────────────────────────────────────────────

/** Rows already in memory — a schema, a small result. Exact, instant, nothing to dispose. */
export function memorySource(columns: GridColumn[], rows: readonly unknown[][]): RowSource {
  const source = new (class extends BaseSource {
    readonly columns = columns
    readonly projects = false
    getRows(start: number, end: number): Promise<RowBlock> {
      const slice = rows.slice(start, end)
      return Promise.resolve({
        start,
        count: slice.length,
        columns: columns.map((_, c) => slice.map((row) => row[c])),
      })
    }
  })()
  source.rowCount = { value: rows.length, exact: true }
  return source
}
