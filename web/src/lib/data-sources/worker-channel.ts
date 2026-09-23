import { abortError, NotTabularError } from "./row-source"
import type { DataWorkerPort, FromWorker, ToWorker } from "./worker-protocol"

/**
 * Request and reply over a data worker port: each request gets an id, its
 * promise settles with the reply that echoes it, and an aborted signal both
 * rejects the caller at once and tells the worker to stop the work.
 *
 * Messages without an id (index progress, `changed`) go to `onEvent`.
 */

type Reply = Exclude<FromWorker, { type: "index" } | { type: "changed" } | { type: "rows-partial" }>
type WorkerEvent = Extract<FromWorker, { type: "index" } | { type: "changed" }>
type PartialRows = Extract<FromWorker, { type: "rows-partial" }>
type ReplyOf<K extends Reply["type"]> = Extract<FromWorker, { type: K }>

interface Pending {
  settle: (message: Reply) => void
  fail: (error: Error) => void
  partial?: (message: PartialRows) => void
}

export interface RequestOptions {
  signal?: AbortSignal
  /** Hears each `rows-partial` message sent ahead of the reply. */
  onPartial?: (message: PartialRows) => void
}

export class WorkerChannel {
  private next = 1
  private readonly pending = new Map<number, Pending>()
  private readonly stopListening: () => void
  private readonly port: DataWorkerPort

  constructor(port: DataWorkerPort, onEvent: (event: WorkerEvent) => void) {
    this.port = port
    this.stopListening = port.listen((message) => {
      if (message.type === "index" || message.type === "changed") return onEvent(message)
      const pending = this.pending.get(message.id)
      if (!pending) return
      if (message.type === "rows-partial") return pending.partial?.(message)
      this.pending.delete(message.id)
      pending.settle(message)
    })
  }

  /** Sends a request built around its id, and settles with its `expected` reply. */
  request<K extends Reply["type"]>(
    expected: K,
    build: (id: number) => ToWorker,
    { signal, onPartial }: RequestOptions = {},
  ): Promise<ReplyOf<K>> {
    const id = this.next++
    return new Promise<ReplyOf<K>>((resolve, reject) => {
      if (signal?.aborted) return reject(abortError())
      const onAbort = () => {
        if (!this.pending.delete(id)) return
        this.port.post({ type: "abort", id })
        reject(abortError())
      }
      signal?.addEventListener("abort", onAbort, { once: true })
      const done = () => signal?.removeEventListener("abort", onAbort)
      this.pending.set(id, {
        partial: onPartial,
        settle: (message) => {
          done()
          if (message.type === expected) resolve(message as ReplyOf<K>)
          else reject(replyError(message))
        },
        fail: (error) => {
          done()
          reject(error)
        },
      })
      this.port.post(build(id))
    })
  }

  /** A message nothing replies to. */
  send(message: ToWorker): void {
    this.port.post(message)
  }

  /** Rejects everything pending and terminates the worker. */
  close(): void {
    this.stopListening()
    for (const pending of this.pending.values()) pending.fail(abortError())
    this.pending.clear()
    this.port.terminate()
  }
}

function replyError(message: Reply): Error {
  if (message.type !== "error")
    return new Error(`Unexpected reply from the data worker: ${message.type}`)
  return message.notTabular ? new NotTabularError(message.message) : new Error(message.message)
}
