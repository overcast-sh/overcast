import { HttpReadError } from "./http-read"
import { isAbortError } from "./row-source"
import { RangeScheduler } from "./range-scheduler"

const KB = 1024

/** A server of `size` bytes where byte i is `i % 251`, holding responses until released. */
function server(size = 4 * 1024 * KB, { etags = ['"v1"'] } = {}) {
  const calls: { range: [number, number]; release: () => void; aborted: boolean }[] = []
  let hold = false
  let etagIndex = 0
  const fetchImpl = ((_url: string, init?: RequestInit) => {
    const [, a, b] = /bytes=(\d+)-(\d+)/.exec(new Headers(init?.headers).get("Range") ?? "") ?? []
    const start = Number(a)
    const end = Number(b) + 1
    return new Promise<Response>((resolve, reject) => {
      const call = {
        range: [start, end] as [number, number],
        aborted: false,
        release: () => {
          const body = Uint8Array.from({ length: end - start }, (_, i) => (start + i) % 251)
          const etag = etags[Math.min(etagIndex++, etags.length - 1)]
          resolve(new Response(body, { status: 206, headers: { ETag: etag } }))
        },
      }
      calls.push(call)
      init?.signal?.addEventListener("abort", () => {
        call.aborted = true
        reject(new DOMException("Aborted", "AbortError"))
      })
      if (!hold) call.release()
    })
  }) as typeof fetch
  return {
    fetchImpl,
    calls,
    size,
    holdResponses() {
      hold = true
    },
  }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

describe("RangeScheduler", () => {
  it("merges reads queued together whose ranges nearly touch", async () => {
    const s = server()
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    const reads = [
      scheduler.read(0, 10 * KB),
      scheduler.read(20 * KB, 30 * KB), // 10 KB gap: merged
      scheduler.read(40 * KB, 50 * KB),
      scheduler.read(500 * KB, 510 * KB), // 450 KB gap: its own request
    ]
    const out = await Promise.all(reads)
    expect(s.calls.map((c) => c.range)).toEqual([
      [0, 50 * KB],
      [500 * KB, 510 * KB],
    ])
    expect(out[1][0]).toBe((20 * KB) % 251)
    expect(out[3].length).toBe(10 * KB)
  })

  it("keeps at most four requests in flight", async () => {
    const s = server()
    s.holdResponses()
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    const reads = Array.from({ length: 10 }, (_, i) =>
      scheduler.read(i * 200 * KB, i * 200 * KB + KB),
    )
    await tick()
    expect(s.calls).toHaveLength(4)
    s.calls[0].release()
    await tick()
    await tick()
    expect(s.calls).toHaveLength(5)
    for (let round = 0; round < 10; round++) {
      for (const call of s.calls) call.release()
      await tick()
    }
    await Promise.all(reads)
    expect(s.calls).toHaveLength(10)
  })

  it("counts a reserved stream against the cap", async () => {
    const s = server()
    s.holdResponses()
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    const release = scheduler.reserve()
    for (let i = 0; i < 5; i++) void scheduler.read(i * 200 * KB, i * 200 * KB + KB)
    await tick()
    expect(s.calls).toHaveLength(3)
    release()
    await tick()
    expect(s.calls).toHaveLength(4)
  })

  it("aborts a request nobody is waiting for any more", async () => {
    const s = server()
    s.holdResponses()
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    const a = new AbortController()
    const b = new AbortController()
    const first = scheduler.read(0, KB, a.signal)
    const second = scheduler.read(2 * KB, 3 * KB, b.signal)
    await tick()
    expect(s.calls).toHaveLength(1)
    a.abort()
    expect(s.calls[0].aborted).toBe(false) // the other read still wants it
    b.abort()
    expect(s.calls[0].aborted).toBe(true)
    await expect(first).rejects.toSatisfy(isAbortError)
    await expect(second).rejects.toSatisfy(isAbortError)
  })

  it("serves a read inside a fetched range from its cache", async () => {
    const s = server()
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    await scheduler.read(0, 100 * KB)
    const inner = await scheduler.read(10 * KB, 20 * KB)
    expect(s.calls).toHaveLength(1)
    expect(inner[0]).toBe((10 * KB) % 251)
  })

  it("reports an object that changed under it", async () => {
    // Given: an object whose second response carries a new ETag
    const s = server(4 * 1024 * KB, { etags: ['"v1"', '"v2"'] })
    const changed = vi.fn()
    const scheduler = new RangeScheduler("/o", s.fetchImpl, { onChanged: changed })
    await scheduler.read(0, KB)
    expect(changed).not.toHaveBeenCalled()
    // When: a read sees the new ETag
    await scheduler.read(1024 * KB, 1025 * KB)
    // Then: the change is reported once
    expect(changed).toHaveBeenCalledOnce()
  })

  it("stops serving the old object's bytes once the ETag moves", async () => {
    // Given: a cached range of the first version
    const s = server(4 * 1024 * KB, { etags: ['"v1"', '"v2"'] })
    const scheduler = new RangeScheduler("/o", s.fetchImpl)
    await scheduler.read(0, 100 * KB)
    // When: another read reveals the object was overwritten
    await scheduler.read(1024 * KB, 1025 * KB)
    // Then: a read inside the old range goes back to the server
    await scheduler.read(10 * KB, 20 * KB)
    expect(s.calls).toHaveLength(3)
  })

  it("fails a read the server refuses with the status", async () => {
    // Given: a server that answers 403
    const refuse = (() => Promise.resolve(new Response(null, { status: 403 }))) as typeof fetch
    const scheduler = new RangeScheduler("/o", refuse)
    // When / Then: the read rejects with an HttpReadError naming it
    await expect(scheduler.read(0, KB)).rejects.toEqual(new HttpReadError(403))
  })
})
