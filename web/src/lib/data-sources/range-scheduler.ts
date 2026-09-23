import { abortError } from "@/components/data-grid/row-source"

/**
 * Every ranged GET a data worker makes goes through one scheduler, which does
 * three things a naive `fetch` per read would not:
 *
 * - **Coalesces.** Reads queued together whose ranges are adjacent or nearly
 *   so (a gap under `gap`, 64 KB) become one request. A Parquet row group's
 *   column chunks usually sit side by side, so six visible columns are one
 *   request, not six.
 * - **Caps concurrency** at `maxInFlight` (4). Browsers allow about six
 *   HTTP/1.1 connections per host and the rest of the console needs some; the
 *   indexer's stream, when there is one, holds one of the four.
 * - **Caches raw bytes** — a bounded LRU of fetched ranges, so a column
 *   scrolled back into view is decoded again from memory rather than fetched
 *   again. A read inside any cached range is a hit.
 *
 * It also watches the object's `ETag`. The first response fixes it; a later
 * response that disagrees means the object was overwritten while open, and
 * `onChanged` fires so the grid can say *file changed — reload* instead of
 * stitching rows from two different files together.
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
}

interface Waiter {
  start: number
  end: number
  resolve: (bytes: Uint8Array) => void
  reject: (error: unknown) => void
  signal?: AbortSignal
  done: boolean
}

interface Request {
  start: number
  end: number
  waiters: Waiter[]
  controller: AbortController
}

export class RangeScheduler {
  readonly url: string
  private readonly fetchImpl: typeof fetch
  private readonly maxInFlight: number
  private readonly gap: number
  private readonly maxRequest: number
  private readonly cacheBytes: number
  private queue: Waiter[] = []
  private readonly inFlight = new Set<Request>()
  private reserved = 0
  private flushScheduled = false
  private readonly cache: { start: number; end: number; bytes: Uint8Array }[] = []
  private cached = 0
  /** Requests actually sent — for tests and for the measurements in the PR. */
  requests = 0
  etag: string | undefined
  onChanged: (() => void) | undefined

  constructor(url: string, fetchImpl: typeof fetch, options: SchedulerOptions = {}) {
    this.url = url
    this.fetchImpl = fetchImpl
    this.maxInFlight = options.maxInFlight ?? 4
    this.gap = options.gap ?? 64 * 1024
    this.maxRequest = options.maxRequest ?? 8 * 1024 * 1024
    this.cacheBytes = options.cacheBytes ?? 32 * 1024 * 1024
  }

  /** Bytes `[start, end)`. */
  read(start: number, end: number, signal?: AbortSignal): Promise<Uint8Array> {
    if (end <= start) return Promise.resolve(new Uint8Array(0))
    const hit = this.lookup(start, end)
    if (hit) return Promise.resolve(hit)
    if (signal?.aborted) return Promise.reject(abortError())
    return new Promise<Uint8Array>((resolve, reject) => {
      const waiter: Waiter = { start, end, resolve, reject, signal, done: false }
      signal?.addEventListener(
        "abort",
        () => {
          if (waiter.done) return
          waiter.done = true
          reject(abortError())
          this.queue = this.queue.filter((w) => w !== waiter)
          for (const request of this.inFlight) {
            if (request.waiters.includes(waiter) && request.waiters.every((w) => w.done)) {
              request.controller.abort()
            }
          }
        },
        { once: true },
      )
      this.queue.push(waiter)
      this.scheduleFlush()
    })
  }

  /** Holds one of the slots for a long-lived stream (the indexer's). */
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
    if (this.etag === undefined) this.etag = etag
    else if (etag !== this.etag) this.onChanged?.()
  }

  clear(): void {
    for (const request of this.inFlight) request.controller.abort()
    for (const waiter of this.queue) {
      waiter.done = true
      waiter.reject(abortError())
    }
    this.queue = []
    this.cache.length = 0
    this.cached = 0
  }

  private lookup(start: number, end: number): Uint8Array | undefined {
    for (let i = this.cache.length - 1; i >= 0; i--) {
      const entry = this.cache[i]
      if (entry.start <= start && entry.end >= end) {
        // Most recently used goes to the end.
        this.cache.splice(i, 1)
        this.cache.push(entry)
        return entry.bytes.subarray(start - entry.start, end - entry.start)
      }
    }
    return undefined
  }

  private remember(start: number, bytes: Uint8Array): void {
    if (bytes.length > this.cacheBytes / 4) return
    this.cache.push({ start, end: start + bytes.length, bytes })
    this.cached += bytes.length
    while (this.cached > this.cacheBytes && this.cache.length > 0) {
      const old = this.cache.shift()
      if (old) this.cached -= old.bytes.length
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
    this.queue = this.queue.filter((w) => !w.done)
    while (this.queue.length > 0 && this.inFlight.size + this.reserved < this.maxInFlight) {
      this.queue.sort((a, b) => a.start - b.start)
      const first = this.queue[0]
      const request: Request = {
        start: first.start,
        end: first.end,
        waiters: [first],
        controller: new AbortController(),
      }
      let i = 1
      for (; i < this.queue.length; i++) {
        const next = this.queue[i]
        if (next.start - request.end >= this.gap) break
        const end = Math.max(request.end, next.end)
        if (end - request.start > this.maxRequest) break
        request.end = end
        request.waiters.push(next)
      }
      this.queue = this.queue.slice(i)
      this.send(request)
    }
  }

  private send(request: Request): void {
    this.inFlight.add(request)
    this.requests++
    this.fetchImpl(this.url, {
      headers: { Range: `bytes=${request.start}-${request.end - 1}` },
      signal: request.controller.signal,
    })
      .then(async (res) => {
        if (!res.ok) {
          throw Object.assign(new Error(`Read failed: HTTP ${res.status}`), { status: res.status })
        }
        this.checkEtag(res)
        let bytes = new Uint8Array(await res.arrayBuffer())
        // A server that ignored Range sent the whole object.
        if (res.status === 200) bytes = bytes.subarray(request.start, request.end)
        this.remember(request.start, bytes)
        for (const waiter of request.waiters) {
          if (waiter.done) continue
          waiter.done = true
          waiter.resolve(bytes.subarray(waiter.start - request.start, waiter.end - request.start))
        }
      })
      .catch((error: unknown) => {
        for (const waiter of request.waiters) {
          if (waiter.done) continue
          waiter.done = true
          waiter.reject(error)
        }
      })
      .finally(() => {
        this.inFlight.delete(request)
        this.flush()
      })
  }
}
