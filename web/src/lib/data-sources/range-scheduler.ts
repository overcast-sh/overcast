import { abortError } from "./row-source"
import { checkedResponse, rangeHeader } from "./http-read"

/**
 * Every ranged GET a data worker makes goes through one scheduler, which does
 * three things a naive `fetch` per read would not:
 *
 * - **Coalesces.** Reads queued in the same turn whose ranges are adjacent or
 *   nearly so (a gap under `gap`, 64 KB) become one request. A Parquet row
 *   group's column chunks usually sit side by side, so six visible columns
 *   are one request, not six.
 * - **Caps concurrency** at `maxInFlight` (4). Browsers allow about six
 *   HTTP/1.1 connections per host and the rest of the console needs some; the
 *   indexer's stream, when there is one, holds one of the four.
 * - **Caches raw bytes** — a bounded LRU of fetched ranges, so a column
 *   scrolled back into view is decoded again from memory rather than fetched
 *   again. A read inside any cached range is a hit.
 *
 * It also watches the object's `ETag`. The first response fixes it; a later
 * one that disagrees means the object was overwritten while open, and so
 * does a 416 for a range that was inside the object when it was opened. The cached
 * bytes belong to the old object, so they are dropped, and `onChanged` fires
 * so the grid can say *file changed — reload* instead of stitching rows from
 * two different files together.
 *
 * A read whose signal aborts is dropped from its request; a request nobody is
 * waiting for any more is aborted.
 */

export interface SchedulerOptions {
  maxInFlight?: number
  /** Merge ranges separated by less than this. */
  gap?: number
  /** Never merge into a request larger than this. */
  maxRequest?: number
  cacheBytes?: number
  /** The object's ETag moved: it was overwritten while open. */
  onChanged?: () => void
}

interface Waiter {
  start: number
  end: number
  resolve: (bytes: Uint8Array) => void
  reject: (error: unknown) => void
  settled: boolean
  /** Stops listening to the caller's signal, so a settled read keeps nothing alive. */
  release: () => void
}

interface Request {
  start: number
  end: number
  waiters: Waiter[]
  controller: AbortController
}

export class RangeScheduler {
  /** Requests actually sent — the number the budgets and the PR quote. */
  requests = 0
  readonly url: string
  private readonly fetchImpl: typeof fetch
  private readonly maxInFlight: number
  private readonly gap: number
  private readonly maxRequest: number
  private readonly onChanged?: () => void
  private readonly cache: RangeCache
  private queue: Waiter[] = []
  private readonly inFlight = new Set<Request>()
  private reserved = 0
  private flushScheduled = false
  private etag: string | undefined

  constructor(url: string, fetchImpl: typeof fetch, options: SchedulerOptions = {}) {
    this.url = url
    this.fetchImpl = fetchImpl
    this.maxInFlight = options.maxInFlight ?? 4
    this.gap = options.gap ?? 64 * 1024
    this.maxRequest = options.maxRequest ?? 8 * 1024 * 1024
    this.cache = new RangeCache(options.cacheBytes ?? 32 * 1024 * 1024)
    this.onChanged = options.onChanged
  }

  /** Bytes `[start, end)`. */
  read(start: number, end: number, signal?: AbortSignal): Promise<Uint8Array> {
    if (end <= start) return Promise.resolve(new Uint8Array(0))
    const hit = this.cache.lookup(start, end)
    if (hit) return Promise.resolve(hit)
    if (signal?.aborted) return Promise.reject(abortError())
    return new Promise<Uint8Array>((resolve, reject) => {
      const onAbort = () => this.abandon(waiter)
      const waiter: Waiter = {
        start,
        end,
        resolve,
        reject,
        settled: false,
        release: () => signal?.removeEventListener("abort", onAbort),
      }
      signal?.addEventListener("abort", onAbort, { once: true })
      this.queue.push(waiter)
      this.scheduleFlush()
    })
  }

  /** Holds one of the slots for a long-lived stream (the indexer's). Returns its release. */
  reserve(): () => void {
    this.reserved++
    let released = false
    return () => {
      if (released) return
      released = true
      this.reserved--
      this.scheduleFlush()
    }
  }

  /** Checks a response's ETag against the object's, fixing it on first sight. */
  checkEtag(response: Response): void {
    const etag = response.headers.get("ETag") ?? undefined
    if (!etag) return
    if (this.etag === undefined) {
      this.etag = etag
    } else if (etag !== this.etag) {
      this.etag = etag
      this.objectChanged()
    }
  }

  /** The object is not the one opened: its cached bytes are the old one's. */
  private objectChanged(): void {
    this.cache.clear()
    this.onChanged?.()
  }

  /** Aborts everything and forgets every byte — the file is closing. */
  clear(): void {
    for (const request of this.inFlight) request.controller.abort()
    for (const waiter of this.queue) settle(waiter, () => waiter.reject(abortError()))
    this.queue = []
    this.cache.clear()
  }

  private abandon(waiter: Waiter): void {
    if (!settle(waiter, () => waiter.reject(abortError()))) return
    this.queue = this.queue.filter((w) => w !== waiter)
    for (const request of this.inFlight) {
      if (request.waiters.includes(waiter) && request.waiters.every((w) => w.settled)) {
        request.controller.abort()
      }
    }
  }

  private scheduleFlush(): void {
    if (this.flushScheduled) return
    this.flushScheduled = true
    // Reads issued in the same turn — a Parquet plan asks for every column
    // chunk at once — are collected before any is sent, so they can merge.
    setTimeout(() => {
      this.flushScheduled = false
      this.flush()
    }, 0)
  }

  private flush(): void {
    this.queue = this.queue.filter((w) => !w.settled).sort((a, b) => a.start - b.start)
    while (this.queue.length > 0 && this.inFlight.size + this.reserved < this.maxInFlight) {
      this.send(this.takeMergeable())
    }
  }

  /** The first queued read, and every one after it close enough to share its request. */
  private takeMergeable(): Request {
    const [first] = this.queue
    const request: Request = {
      start: first.start,
      end: first.end,
      waiters: [first],
      controller: new AbortController(),
    }
    let i = 1
    for (; i < this.queue.length; i++) {
      const next = this.queue[i]
      const end = Math.max(request.end, next.end)
      if (next.start - request.end >= this.gap || end - request.start > this.maxRequest) break
      request.end = end
      request.waiters.push(next)
    }
    this.queue = this.queue.slice(i)
    return request
  }

  private send(request: Request): void {
    this.inFlight.add(request)
    this.requests++
    this.fetchImpl(this.url, {
      headers: { Range: rangeHeader(request.start, request.end) },
      signal: request.controller.signal,
    })
      .then(async (response) => {
        // Every read is inside the size the object was opened at, so a range
        // the server calls unsatisfiable means the object shrank under us.
        if (response.status === 416) this.objectChanged()
        checkedResponse(response)
        this.checkEtag(response)
        let bytes = new Uint8Array(await response.arrayBuffer())
        // A server that ignored Range sent the whole object: copy the range
        // out, so the cache does not keep the rest of it alive.
        if (response.status === 200) bytes = bytes.slice(request.start, request.end)
        this.cache.remember(request.start, bytes)
        for (const waiter of request.waiters) {
          const from = waiter.start - request.start
          settle(waiter, () => waiter.resolve(bytes.subarray(from, waiter.end - request.start)))
        }
      })
      .catch((error: unknown) => {
        for (const waiter of request.waiters) settle(waiter, () => waiter.reject(error))
      })
      .finally(() => {
        this.inFlight.delete(request)
        this.flush()
      })
  }
}

/** Runs `outcome` once per waiter; false when the waiter had already settled. */
function settle(waiter: Waiter, outcome: () => void): boolean {
  if (waiter.settled) return false
  waiter.settled = true
  waiter.release()
  outcome()
  return true
}

/**
 * Fetched byte ranges, least recently used first, capped by bytes. A lookup
 * is a hit when any cached range contains the one asked for. One range may
 * take at most a quarter of the budget, so a single large read cannot flush
 * everything else. Linear in the entries, which the cap keeps to a few dozen.
 */
class RangeCache {
  private entries: { start: number; end: number; bytes: Uint8Array }[] = []
  private total = 0
  private readonly budget: number

  constructor(budget: number) {
    this.budget = budget
  }

  lookup(start: number, end: number): Uint8Array | undefined {
    const i = this.entries.findLastIndex((e) => e.start <= start && e.end >= end)
    if (i === -1) return undefined
    const [entry] = this.entries.splice(i, 1)
    this.entries.push(entry)
    return entry.bytes.subarray(start - entry.start, end - entry.start)
  }

  remember(start: number, bytes: Uint8Array): void {
    if (bytes.length > this.budget / 4) return
    this.entries.push({ start, end: start + bytes.length, bytes })
    this.total += bytes.length
    while (this.total > this.budget) {
      const oldest = this.entries.shift()
      if (!oldest) break
      this.total -= oldest.bytes.length
    }
  }

  clear(): void {
    this.entries = []
    this.total = 0
  }
}
