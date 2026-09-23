import type { AsyncBuffer, FileMetaData } from "hyparquet"
import { isNumericColumn } from "@/components/data-grid/cell-format"
import { columnNames, parseDelimited, sniffDelimiter, type Delimiter } from "./delimited-parse"
import { uniformColumns } from "./jsonl-parse"
import { openParquet, readParquetRows, schedulerAsyncBuffer } from "./parquet-reader"
import { RangeScheduler } from "./range-scheduler"
import { RecordIndexer, type IndexerState } from "./record-indexer"
import type { FromWorker, TextHead, TextKind, ToWorker } from "./worker-protocol"

/**
 * Everything the data worker does, as a plain class, so the same code runs in
 * the real worker (`data.worker.ts`) and in-process in tests.
 *
 * **Text (CSV, TSV, JSON Lines).** Opening makes two requests at once, and
 * neither waits for the other:
 *
 * - a ranged read of the first 64 KB, parsed into the header and the first
 *   rows — that is the reply the preview is waiting for;
 * - a streamed read of the whole object through the `RecordIndexer`, whose
 *   offsets follow as unsolicited progress messages, a few a second.
 *
 * Any block is then one ranged read between two index offsets, parsed on its
 * own: offsets always fall on record boundaries, so a block never starts
 * inside a quoted field.
 *
 * **Parquet.** The footer is read once — a suffix range, then its exact length
 * if it is longer — and kept. Row reads name their columns, and each column is
 * sent back the moment it decodes.
 *
 * Every ranged read goes through one `RangeScheduler` (coalescing, four in
 * flight, a raw-byte cache, and the ETag check that raises `changed`).
 */

type Post = (message: FromWorker) => void

/** Progress messages at most this often; the grid's footer does not need more. */
const PROGRESS_MS = 120

/** Longest stretch of indexing before the worker yields to its message queue. */
const SLICE_MS = 12

/** The first read: enough for the header and a screenful of rows, and to sniff the delimiter. */
const HEAD_BYTES = 64 * 1024

class NotTabular extends Error {}

const UNCLOSED_QUOTE =
  "A quoted field is never closed, so the file cannot be split into rows reliably."

interface TextState {
  url: string
  size: number
  kind: TextKind
  every: number
  byteLimit: number
  mode: "background" | "on-demand"
  untilRows: number
  delimiter: Delimiter
  width: number
  keys: string[]
  indexer: RecordIndexer | null
  resume: IndexerState | null
  running: AbortController | null
  done: boolean
  /** Offsets already reported to the main thread. */
  reported: number
}

interface ParquetState {
  file: AsyncBuffer
  metadata: FileMetaData
  names: string[]
}

export class DataWorkerCore {
  private text: TextState | null = null
  private parquet: ParquetState | null = null
  private scheduler: RangeScheduler | null = null
  private readonly requests = new Map<number, AbortController>()
  private readonly post: Post
  private readonly fetchImpl: typeof fetch

  constructor(post: Post, fetchImpl: typeof fetch = (input, init) => fetch(input, init)) {
    this.post = post
    this.fetchImpl = fetchImpl
  }

  /** Requests sent so far — reported in the grid's measurements. */
  get requestCount(): number {
    return this.scheduler?.requests ?? 0
  }

  handle(message: ToWorker): void {
    switch (message.type) {
      case "abort":
        this.requests.get(message.id)?.abort()
        this.requests.delete(message.id)
        return
      case "open-text":
        void this.track(message.id, (signal) => this.openText(message, signal))
        return
      case "continue":
        this.continueIndex(message.untilRows)
        return
      case "read-text":
        void this.track(message.id, (signal) => this.readText(message, signal))
        return
      case "open-parquet":
        void this.track(message.id, (signal) => this.openParquetFile(message, signal))
        return
      case "read-parquet":
        void this.track(message.id, (signal) => this.readParquet(message, signal))
        return
    }
  }

  /** Stops everything — the worker is about to be terminated. */
  dispose(): void {
    this.text?.running?.abort()
    for (const controller of this.requests.values()) controller.abort()
    this.requests.clear()
    this.scheduler?.clear()
  }

  private async track(id: number, work: (signal: AbortSignal) => Promise<void>): Promise<void> {
    const controller = new AbortController()
    this.requests.set(id, controller)
    try {
      await work(controller.signal)
    } catch (error) {
      if (!controller.signal.aborted) this.fail(id, error)
    } finally {
      this.requests.delete(id)
    }
  }

  private fail(id: number, error: unknown): void {
    const status = (error as { status?: number } | null)?.status
    this.post({
      type: "error",
      id,
      message: error instanceof Error ? error.message : String(error),
      code: error instanceof NotTabular ? "not-tabular" : status ? "http" : undefined,
    })
  }

  private schedulerFor(url: string): RangeScheduler {
    const scheduler = new RangeScheduler(url, this.fetchImpl)
    scheduler.onChanged = () => this.post({ type: "changed" })
    this.scheduler = scheduler
    return scheduler
  }

  // ─── Text: open, index, read ──────────────────────────────────────────────

  private async openText(
    message: Extract<ToWorker, { type: "open-text" }>,
    signal: AbortSignal,
  ): Promise<void> {
    const state: TextState = {
      url: message.url,
      size: message.size,
      kind: message.kind,
      every: message.every,
      byteLimit: message.byteLimit,
      mode: message.mode,
      untilRows: message.untilRows,
      delimiter: message.kind === "tsv" ? "\t" : ",",
      width: 0,
      keys: [],
      indexer: null,
      resume: null,
      running: null,
      done: false,
      reported: 0,
    }
    this.text = state
    const scheduler = this.schedulerFor(message.url)
    // Both at once: the first screen from a small range, the index from a stream.
    const head = scheduler.read(0, Math.min(HEAD_BYTES, message.size), signal)
    this.startIndex(state)
    try {
      const bytes = await head
      if (signal.aborted) return
      this.post({ type: "text-head", id: message.id, ...this.parseHead(state, bytes) })
    } catch (error) {
      // No head, no preview: the index has nothing to serve.
      state.running?.abort()
      throw error
    }
  }

  private startIndex(state: TextState): void {
    void this.runIndex(state).catch((error: unknown) => {
      if (state.running?.signal.aborted) return
      state.running = null
      this.progress(state, "error", error instanceof Error ? error.message : String(error))
    })
  }

  private continueIndex(untilRows?: number): void {
    const state = this.text
    if (!state || state.done || state.running) return
    if (untilRows !== undefined) state.untilRows = Math.max(state.untilRows, untilRows)
    this.startIndex(state)
  }

  /**
   * One indexing run: from the top, or from where the last run paused, until
   * the object ends, the run's byte budget is spent, or — on demand — enough
   * rows are indexed. The stream holds one of the scheduler's four slots.
   *
   * The first run holds back its opening bytes until it can sniff the
   * delimiter, because the indexer needs it: a quote opens a quoted field
   * only at the start of a field.
   */
  private async runIndex(state: TextState): Promise<void> {
    const controller = new AbortController()
    state.running = controller
    const scheduler = this.scheduler
    const release = scheduler?.reserve()
    const resume = state.resume
    const startAt = resume?.position ?? 0
    const headers: HeadersInit = startAt > 0 ? { Range: `bytes=${startAt}-` } : {}
    let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
    try {
      const res = await this.fetchImpl(state.url, { headers, signal: controller.signal })
      if (!res.ok || !res.body) {
        throw Object.assign(new Error(`Read failed: HTTP ${res.status}`), { status: res.status })
      }
      scheduler?.checkEtag(res)
      reader = res.body.getReader()
      // Offsets recorded by earlier runs are already on the main thread; this
      // run's indexer starts an empty list of its own.
      let indexer: RecordIndexer | null =
        state.indexer && resume ? new RecordIndexer(state.indexer.options, resume) : null
      if (indexer) state.indexer = indexer
      state.reported = 0
      const held: Uint8Array[] = []
      let heldBytes = 0
      // A server that ignored the Range header sends the object from the top.
      let skip = startAt > 0 && res.status === 200 ? startAt : 0
      let lastPost = 0
      let sliceStart = Date.now()
      let paused: "paused-limit" | "on-demand" | null = null

      const startIndexer = (): RecordIndexer => {
        const sample = new TextDecoder().decode(concat(held, heldBytes).subarray(0, HEAD_BYTES))
        if (state.kind !== "jsonl") {
          state.delimiter = sniffDelimiter(sample, state.kind === "tsv" ? "\t" : ",")
        }
        const created = new RecordIndexer({
          every: state.every,
          delimiter: state.kind === "jsonl" ? null : state.delimiter.charCodeAt(0),
          header: state.kind !== "jsonl",
        })
        state.indexer = created
        for (const chunk of held) created.push(chunk)
        held.length = 0
        return created
      }

      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        let chunk = value
        if (skip > 0) {
          const drop = Math.min(skip, chunk.length)
          chunk = chunk.subarray(drop)
          skip -= drop
          if (chunk.length === 0) continue
        }
        if (!indexer) {
          held.push(chunk)
          heldBytes += chunk.length
          if (heldBytes < HEAD_BYTES) continue
          indexer = startIndexer()
        } else {
          indexer.push(chunk)
        }
        const now = Date.now()
        if (now - lastPost >= PROGRESS_MS) {
          lastPost = now
          this.progress(state, "running")
        }
        // A local stream can arrive faster than it is scanned, and a stream
        // read resolves on the microtask queue: without a yield the worker
        // would answer no read, no abort and no first-screen request until
        // the whole file was indexed.
        if (now - sliceStart >= SLICE_MS) {
          await new Promise((resolve) => setTimeout(resolve, 0))
          sliceStart = Date.now()
        }
        if (indexer.bytes - startAt >= state.byteLimit && indexer.bytes < state.size) {
          paused = "paused-limit"
          break
        }
        if (state.mode === "on-demand" && indexer.rows >= state.untilRows) {
          paused = "on-demand"
          break
        }
      }
      indexer ??= startIndexer()
      if (paused) {
        state.resume = indexer.resumeState()
        state.running = null
        this.progress(state, paused)
        return
      }
      indexer.finish()
      state.done = true
      state.running = null
      this.progress(state, indexer.unterminatedQuote ? "error" : "done", indexer.unterminatedQuote ? UNCLOSED_QUOTE : undefined)
    } finally {
      release?.()
      // A run that stopped early — paused, or failed — must not leave the
      // response streaming into nothing. Cancelled rather than aborted, so
      // the signal still means only "the file was closed".
      if (reader) {
        if (state.done) reader.releaseLock()
        else reader.cancel().catch(() => {})
      }
    }
  }

  /** The header, the columns and the first rows, from the first 64 KB. */
  private parseHead(state: TextState, bytes: Uint8Array): TextHead {
    const truncated = bytes.length < state.size
    const text = new TextDecoder().decode(bytes)
    if (state.kind === "jsonl") {
      const records = parseJsonlLines(text, truncated, state.every)
      const keys = uniformColumns(records)
      if (!keys) {
        throw new NotTabular(
          records.length === 0
            ? "The file has no complete records in its first 64 KB."
            : "Records do not share the same fields, so they are shown as written.",
        )
      }
      state.keys = keys
      const columns = keys.map((key) => records.map((r) => (r as Record<string, unknown>)[key]))
      return {
        columns: keys.map((name, i) => ({
          name,
          numeric: isNumericColumn(columns[i], { numericText: false }),
        })),
        first: { count: records.length, columns },
      }
    }
    state.delimiter = sniffDelimiter(text, state.kind === "tsv" ? "\t" : ",")
    const parsed = parseDelimited(text, {
      delimiter: state.delimiter,
      maxRecords: state.every + 1,
      truncated,
    })
    if (parsed.malformed) throw new NotTabular(parsed.malformed)
    if (parsed.records.length === 0) {
      throw new NotTabular(
        truncated
          ? "The header is longer than 64 KB, so the file cannot be read as a table."
          : "The file has no rows.",
      )
    }
    const [header, ...body] = parsed.records
    const headerRecord = header.map((name) => name.replace(/^﻿/, ""))
    state.width = Math.max(headerRecord.length, ...body.map((r) => r.length))
    const names = columnNames(headerRecord, state.width)
    const columns = transpose(body, state.width)
    return {
      delimiter: state.delimiter,
      columns: names.map((name, i) => ({ name, numeric: isNumericColumn(columns[i]) })),
      first: { count: body.length, columns },
    }
  }

  private progress(
    state: TextState,
    phase: "running" | "paused-limit" | "on-demand" | "done" | "error",
    error?: string,
  ): void {
    const indexer = state.indexer
    if (!indexer) return
    const offsets = indexer.offsets.slice(state.reported)
    state.reported = indexer.offsets.length
    this.post({
      type: "index",
      state: phase,
      rows: indexer.rows,
      bytes: indexer.bytes,
      totalBytes: state.size,
      end: indexer.end,
      offsets,
      error,
    })
  }

  private async readText(
    message: Extract<ToWorker, { type: "read-text" }>,
    signal: AbortSignal,
  ): Promise<void> {
    const state = this.text
    const scheduler = this.scheduler
    if (!state || !scheduler) throw new Error("No file is open.")
    const bytes = await scheduler.read(message.start, message.end, signal)
    const text = new TextDecoder().decode(bytes)
    let columns: unknown[][]
    let count: number
    if (state.kind === "jsonl") {
      const records = parseJsonlLines(text, false, Infinity)
      count = records.length
      columns = state.keys.map((key) =>
        records.map((r) =>
          r !== null && typeof r === "object" ? (r as Record<string, unknown>)[key] : undefined,
        ),
      )
    } else {
      const parsed = parseDelimited(text, {
        delimiter: state.delimiter,
        maxRecords: message.count,
        truncated: false,
      })
      count = parsed.records.length
      columns = transpose(parsed.records, state.width)
    }
    if (!signal.aborted) this.post({ type: "rows", id: message.id, count, columns })
  }

  // ─── Parquet ─────────────────────────────────────────────────────────────────

  private async openParquetFile(
    message: Extract<ToWorker, { type: "open-parquet" }>,
    signal: AbortSignal,
  ): Promise<void> {
    const file = schedulerAsyncBuffer(this.schedulerFor(message.url), message.size)
    const { head, metadata } = await openParquet(file)
    this.parquet = { file, metadata, names: head.columns.map((c) => c.name) }
    if (!signal.aborted) this.post({ type: "parquet-head", id: message.id, ...head })
  }

  private async readParquet(
    message: Extract<ToWorker, { type: "read-parquet" }>,
    signal: AbortSignal,
  ): Promise<void> {
    const state = this.parquet
    if (!state) throw new Error("No file is open.")
    const names = message.columns.map((c) => state.names[c])
    const count = message.end - message.start
    const data = await readParquetRows(
      state.file,
      state.metadata,
      message.start,
      message.end,
      names,
      // Each column is on its way the moment it decodes.
      (i, values) => {
        if (!signal.aborted) {
          this.post({ type: "rows-partial", id: message.id, column: message.columns[i], count, values })
        }
      },
    )
    if (signal.aborted) return
    const columns: (unknown[] | undefined)[] = []
    message.columns.forEach((c, i) => {
      columns[c] = data[i]
    })
    this.post({ type: "rows", id: message.id, count, columns })
  }
}

/** Records to columns, padding short records with empty strings. */
function transpose(records: readonly string[][], width: number): string[][] {
  const columns: string[][] = Array.from({ length: width }, () => new Array<string>(records.length))
  for (let r = 0; r < records.length; r++) {
    const record = records[r]
    for (let c = 0; c < width; c++) columns[c][r] = record[c] ?? ""
  }
  return columns
}

/**
 * One JSON value per non-blank line, up to `max`. The last line of a
 * truncated read is dropped — the read cut through it — and any other line
 * that is not JSON makes the block unreadable.
 */
function parseJsonlLines(text: string, truncated: boolean, max: number): unknown[] {
  const out: unknown[] = []
  let start = text.charCodeAt(0) === 0xfeff ? 1 : 0
  let line = 0
  while (start < text.length && out.length < max) {
    let end = text.indexOf("\n", start)
    if (end === -1) {
      if (truncated) break
      end = text.length
    }
    line++
    const source = text.slice(start, end).trim()
    if (source !== "") {
      try {
        out.push(JSON.parse(source))
      } catch {
        throw new NotTabular(`Line ${line} is not valid JSON.`)
      }
    }
    start = end + 1
  }
  return out
}

function concat(chunks: Uint8Array[], length: number): Uint8Array {
  const out = new Uint8Array(length)
  let at = 0
  for (const chunk of chunks) {
    out.set(chunk, at)
    at += chunk.length
  }
  return out
}
