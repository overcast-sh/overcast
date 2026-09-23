import type { Delimiter } from "./delimited-parse"
import { UNCLOSED_QUOTE } from "./delimited-parse"
import { runIndex, type IndexRunOutcome } from "./index-run"
import type { RangeScheduler } from "./range-scheduler"
import { RecordIndexer } from "./record-indexer"
import type { IndexingState } from "./row-source"
import { delimitedHead, jsonlHead, textBlock, type TextLayout } from "./text-blocks"
import type { ColumnarRows, IndexProgress, OpenText, TextHead } from "./worker-protocol"

/**
 * A CSV, TSV or JSON Lines object open in the data worker.
 *
 * Opening makes two requests at once, and neither waits for the other:
 *
 * - a ranged read of the first 64 KB, parsed into the header and the first
 *   rows — the reply the preview is waiting for;
 * - a streamed read of the whole object through the `RecordIndexer`, whose
 *   offsets follow as progress messages a few times a second.
 *
 * Any block is then one ranged read between two index offsets, parsed on its
 * own: offsets fall on record boundaries, so a block never starts inside a
 * quoted field.
 *
 * Indexing stops at a byte limit per run (512 MB) and waits for `resume()`;
 * on demand (`Save-Data`) it stops as soon as it covers the rows asked for.
 */

/** The first read: enough for the header and a screenful of rows, and to sniff the delimiter. */
export const HEAD_BYTES = 64 * 1024

export interface TextFileOptions {
  open: OpenText
  scheduler: RangeScheduler
  fetchImpl: typeof fetch
  onProgress: (progress: IndexProgress) => void
}

export class TextFile {
  private readonly options: OpenText
  private readonly scheduler: RangeScheduler
  private readonly fetchImpl: typeof fetch
  private readonly onProgress: (progress: IndexProgress) => void
  private layout: TextLayout | null = null
  /** Settles once the head has decided the layout the indexer needs. */
  private readonly indexer: Promise<RecordIndexer>
  private resolveIndexer: (indexer: RecordIndexer) => void = () => {}
  private running: AbortController | null = null
  /** `Save-Data`: each run stops once the index covers `untilRows`. */
  private readonly onDemand: boolean
  private state: IndexingState
  private untilRows: number
  /** Offsets already reported to the main thread. */
  private reported = 0

  constructor({ open, scheduler, fetchImpl, onProgress }: TextFileOptions) {
    this.options = open
    this.scheduler = scheduler
    this.fetchImpl = fetchImpl
    this.onProgress = onProgress
    this.onDemand = open.mode === "on-demand"
    this.state = this.onDemand ? "on-demand" : "running"
    this.untilRows = open.untilRows
    this.indexer = new Promise((resolve) => (this.resolveIndexer = resolve))
  }

  /** The head: columns and first rows. The index starts streaming alongside it. */
  async open(signal: AbortSignal): Promise<TextHead> {
    const { size } = this.options
    const bytes = this.scheduler.read(0, Math.min(HEAD_BYTES, size), signal)
    this.startRun()
    try {
      const { head, layout } = this.parseHead(await bytes, size > HEAD_BYTES)
      this.layout = layout
      this.resolveIndexer(this.createIndexer(layout))
      return head
    } catch (error) {
      // No head, no preview: the index would have nothing to serve.
      this.running?.abort()
      throw error
    }
  }

  /** Rows between two index offsets. */
  async read(start: number, end: number, rows: number, signal: AbortSignal): Promise<ColumnarRows> {
    if (!this.layout) throw new Error("The file is not open.")
    const bytes = await this.scheduler.read(start, end, signal)
    return textBlock(new TextDecoder().decode(bytes), this.layout, rows)
  }

  /** Reads on: past the byte limit, or — on demand — to cover `untilRows`. */
  resume(untilRows?: number): void {
    if (untilRows !== undefined) this.untilRows = Math.max(this.untilRows, untilRows)
    if (this.running || this.state === "done" || this.state === "error") return
    this.state = this.onDemand ? "on-demand" : "running"
    this.startRun()
  }

  dispose(): void {
    this.running?.abort()
  }

  private parseHead(bytes: Uint8Array, truncated: boolean) {
    const text = new TextDecoder().decode(bytes)
    const { kind, every } = this.options
    const options = { rows: every, truncated }
    if (kind === "jsonl") return jsonlHead(text, options)
    return delimitedHead(text, kind === "tsv" ? "\t" : ",", options)
  }

  private createIndexer(layout: TextLayout): RecordIndexer {
    return new RecordIndexer({
      every: this.options.every,
      delimiter: layout.kind === "delimited" ? delimiterByte(layout.delimiter) : null,
      header: layout.kind === "delimited",
    })
  }

  private startRun(): void {
    const controller = new AbortController()
    this.running = controller
    void this.run(controller).catch((error: unknown) => {
      if (controller.signal.aborted) return
      this.running = null
      this.state = "error"
      void this.report(error instanceof Error ? error.message : String(error))
    })
  }

  private async run(controller: AbortController): Promise<void> {
    const from = this.layout ? (await this.indexer).bytes : 0
    const outcome = await runIndex({
      fetchImpl: this.fetchImpl,
      scheduler: this.scheduler,
      indexer: this.indexer,
      from,
      size: this.options.size,
      byteLimit: this.options.byteLimit,
      untilRows: this.onDemand ? this.untilRows : undefined,
      signal: controller.signal,
      onProgress: () => void this.report(),
    })
    this.running = null
    await this.finishRun(outcome)
  }

  private async finishRun(outcome: IndexRunOutcome): Promise<void> {
    const indexer = await this.indexer
    if (outcome === "done") {
      this.state = indexer.unterminatedQuote ? "error" : "done"
      return this.report(indexer.unterminatedQuote ? UNCLOSED_QUOTE : undefined)
    }
    this.state = outcome
    return this.report()
  }

  /** Posts the index's progress: the offsets recorded since the last report, and where it stands. */
  private async report(error?: string): Promise<void> {
    const indexer = await this.indexer
    const offsets = indexer.offsets.slice(this.reported)
    this.reported = indexer.offsets.length
    this.onProgress({
      state: this.state,
      rows: indexer.rows,
      bytes: indexer.bytes,
      totalBytes: this.options.size,
      end: indexer.end,
      offsets,
      error,
    })
  }
}

function delimiterByte(delimiter: Delimiter): number {
  return delimiter.charCodeAt(0)
}
