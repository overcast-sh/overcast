import { checkedResponse, rangeHeader } from "./http-read"
import type { RangeScheduler } from "./range-scheduler"
import type { RecordIndexer } from "./record-indexer"

/**
 * One indexing run over a text object: a streamed GET, pushed through the
 * `RecordIndexer` a chunk at a time, from where the indexer stands (the top,
 * or where the last run stopped) until the object ends or the run is told to
 * stop. The stream holds one of the scheduler's four request slots for as
 * long as it runs.
 */

export type IndexRunOutcome =
  /** The object ended: the index is complete. */
  | "done"
  /** This run's byte budget is spent; another run continues from `indexer.bytes`. */
  | "paused-limit"
  /** On demand: the index covers the rows asked for, and waits to be asked for more. */
  | "on-demand"

export interface IndexRunOptions {
  fetchImpl: typeof fetch
  /** Holds the stream's slot and checks its ETag. */
  scheduler: RangeScheduler
  /**
   * The indexer to feed. A promise because a first run needs the delimiter
   * the head read decides; the stream is requested at once regardless, so
   * neither read waits for the other on the network.
   */
  indexer: Promise<RecordIndexer>
  /** The byte the run starts at: 0, or where the indexer stopped (`indexer.bytes`). */
  from: number
  /** The object's size. */
  size: number
  /** Bytes this run may read before it pauses. */
  byteLimit: number
  /** On demand: stop once the index covers this many rows. */
  untilRows?: number
  signal: AbortSignal
  /** Called a few times a second while the run reads, and not after it ends. */
  onProgress: () => void
}

/** Progress at most this often; the grid's footer does not need more. */
const PROGRESS_MS = 120

/** Longest stretch of scanning before the run yields to the worker's message queue. */
const SLICE_MS = 12

export async function runIndex(options: IndexRunOptions): Promise<IndexRunOutcome> {
  const { scheduler, signal } = options
  const release = scheduler.reserve()
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  try {
    const startAt = options.from
    const [fetched, indexer] = await Promise.all([
      options.fetchImpl(scheduler.url, {
        headers: startAt > 0 ? { Range: rangeHeader(startAt) } : {},
        signal,
      }),
      options.indexer,
    ])
    const response = checkedResponse(fetched)
    if (!response.body) throw new Error("The object arrived without a body to stream.")
    scheduler.checkEtag(response)
    reader = response.body.getReader()
    const outcome = await scan(reader, indexer, options, {
      startAt,
      // A server that ignored the Range header sends the object from the top.
      skip: startAt > 0 && response.status === 200 ? startAt : 0,
    })
    if (outcome === "done") indexer.finish()
    return outcome
  } finally {
    release()
    // A run that stopped early must not leave the response streaming into
    // nothing; cancelling a finished stream is a no-op.
    reader?.cancel().catch(() => {})
  }
}

async function scan(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  indexer: RecordIndexer,
  { size, byteLimit, untilRows, onProgress }: IndexRunOptions,
  { startAt, skip }: { startAt: number; skip: number },
): Promise<IndexRunOutcome> {
  let lastProgress = 0
  let sliceStart = Date.now()
  for (;;) {
    const { value, done } = await reader.read()
    if (done) return "done"
    const drop = Math.min(skip, value.length)
    skip -= drop
    if (drop === value.length) continue
    indexer.push(drop > 0 ? value.subarray(drop) : value)

    const now = Date.now()
    if (now - lastProgress >= PROGRESS_MS) {
      lastProgress = now
      onProgress()
    }
    // A local stream can arrive faster than it is scanned, and a stream read
    // resolves on the microtask queue: without a yield the worker would
    // answer no read, no abort and no first-screen request until the whole
    // file was indexed.
    if (now - sliceStart >= SLICE_MS) {
      await new Promise((resolve) => setTimeout(resolve, 0))
      sliceStart = Date.now()
    }
    if (indexer.bytes - startAt >= byteLimit && indexer.bytes < size) return "paused-limit"
    if (untilRows !== undefined && indexer.rows >= untilRows) return "on-demand"
  }
}
