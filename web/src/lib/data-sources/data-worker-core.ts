import { ParquetFile } from "./parquet-file"
import { RangeScheduler } from "./range-scheduler"
import { NotTabularError } from "./row-source"
import { TextFile } from "./text-file"
import type { FromWorker, ToWorker } from "./worker-protocol"

/**
 * The data worker's message loop, as a plain class, so the same code runs in
 * the real worker (`data.worker.ts`) and in-process in tests.
 *
 * It only dispatches: a request opens or reads the one file this worker
 * holds (`TextFile` or `ParquetFile`), each request is tracked by id so an
 * `abort` can cancel it, and a failure goes back as an `error` reply saying
 * whether the file is simply not a table. Every ranged read goes through one
 * `RangeScheduler` — coalescing, four in flight, a raw-byte cache, and the
 * ETag check that raises `changed`.
 */

type Post = (message: FromWorker) => void

export class DataWorkerCore {
  private text: TextFile | null = null
  private parquet: ParquetFile | null = null
  private scheduler: RangeScheduler | null = null
  private readonly requests = new Map<number, AbortController>()
  private readonly post: Post
  private readonly fetchImpl: typeof fetch

  constructor(post: Post, fetchImpl: typeof fetch = (input, init) => fetch(input, init)) {
    this.post = post
    this.fetchImpl = fetchImpl
  }

  handle(message: ToWorker): void {
    switch (message.type) {
      case "abort":
        this.requests.get(message.id)?.abort()
        this.requests.delete(message.id)
        return
      case "continue":
        this.text?.resume(message.untilRows)
        return
      case "open-text":
        return this.track(message.id, async (signal) => {
          const scheduler = this.openScheduler(message.url)
          this.text = new TextFile({
            open: message,
            scheduler,
            fetchImpl: this.fetchImpl,
            onProgress: (progress) => this.post({ type: "index", ...progress }),
          })
          const head = await this.text.open(signal)
          return { type: "text-head", id: message.id, ...head }
        })
      case "read-text":
        return this.track(message.id, async (signal) => {
          const rows = await required(this.text).read(
            message.start,
            message.end,
            message.count,
            signal,
          )
          return { type: "rows", id: message.id, ...rows }
        })
      case "open-parquet":
        return this.track(message.id, async () => {
          const opened = await ParquetFile.open(this.openScheduler(message.url), message.size)
          this.parquet = opened.file
          return { type: "parquet-head", id: message.id, ...opened.head }
        })
      case "read-parquet":
        return this.track(message.id, async (signal) => {
          // Each column crosses the port once: as a partial the moment it
          // decodes, or in the reply if it never reported itself complete.
          const sent = new Set<number>()
          const { count, columns } = await required(this.parquet).read(
            message.start,
            message.end,
            message.columns,
            signal,
            (column, values) => {
              if (signal.aborted) return
              sent.add(column)
              const count = message.end - message.start
              this.post({ type: "rows-partial", id: message.id, column, count, values })
            },
          )
          const unsent = columns.map((values, column) => (sent.has(column) ? undefined : values))
          return { type: "rows", id: message.id, count, columns: unsent }
        })
    }
  }

  /** Stops everything — the worker is about to be terminated. */
  dispose(): void {
    this.text?.dispose()
    for (const controller of this.requests.values()) controller.abort()
    this.requests.clear()
    this.scheduler?.clear()
  }

  /**
   * Runs one request: its reply is posted unless it was aborted, and a
   * failure is posted as an `error` reply instead.
   */
  private track(id: number, work: (signal: AbortSignal) => Promise<FromWorker>): void {
    const controller = new AbortController()
    this.requests.set(id, controller)
    work(controller.signal)
      .then(
        (reply) => {
          if (!controller.signal.aborted) this.post(reply)
        },
        (error: unknown) => {
          if (!controller.signal.aborted) this.fail(id, error)
        },
      )
      .finally(() => this.requests.delete(id))
  }

  private fail(id: number, error: unknown): void {
    this.post({
      type: "error",
      id,
      message: error instanceof Error ? error.message : String(error),
      notTabular: error instanceof NotTabularError,
    })
  }

  private openScheduler(url: string): RangeScheduler {
    this.scheduler = new RangeScheduler(url, this.fetchImpl, {
      onChanged: () => this.post({ type: "changed" }),
    })
    return this.scheduler
  }
}

function required<F>(file: F | null): F {
  if (!file) throw new Error("No file is open.")
  return file
}
