/**
 * Objects served the way the console's download route serves them — full
 * GETs as a stream, ranged GETs as 206 — without a server, for the row-source
 * tests and budgets. Every request is recorded, so a test can say exactly
 * which bytes a scroll cost.
 */

export interface ByteSource {
  size: number
  /** Bytes `[start, end)`. */
  read(start: number, end: number): Uint8Array
}

export interface FakeRequest {
  range?: [number, number]
}

/** An object held in memory. */
export function bytesObject(content: string | Uint8Array): ByteSource {
  const bytes = typeof content === "string" ? new TextEncoder().encode(content) : content
  return { size: bytes.length, read: (start, end) => bytes.subarray(start, end) }
}

/**
 * A CSV of `rows` fixed-width rows, generated on demand: any byte range is
 * computed, never stored, so a five-million-row file costs no memory to serve.
 * Row `i` is `id,name,amount` with `id = i`.
 */
export function syntheticCsv(
  rows: number,
): ByteSource & { header: string; rowText(i: number): string } {
  const header = "id,name,amount\n"
  const rowText = (i: number) => {
    const id = String(i).padStart(9, "0")
    const name = String.fromCharCode(97 + (i % 26)).repeat(8)
    const amount = (10000 + ((i * 37) % 90000) / 100).toFixed(2)
    return `${id},${name},${amount}\n`
  }
  const width = rowText(0).length
  const encoder = new TextEncoder()
  const headerBytes = encoder.encode(header)
  const size = headerBytes.length + rows * width
  return {
    header,
    rowText,
    size,
    read(start, end) {
      end = Math.min(end, size)
      const out = new Uint8Array(Math.max(end - start, 0))
      let at = 0
      let pos = start
      if (pos < headerBytes.length) {
        const part = headerBytes.subarray(pos, Math.min(end, headerBytes.length))
        out.set(part, 0)
        at += part.length
        pos += part.length
      }
      if (pos < end) {
        // Whole rows at a time: generate the rows the range touches as one
        // string, encode once, and cut the range out of it.
        const firstRow = Math.floor((pos - headerBytes.length) / width)
        const lastRow = Math.ceil((end - headerBytes.length) / width)
        let text = ""
        for (let r = firstRow; r < lastRow; r++) text += rowText(r)
        const bytes = encoder.encode(text)
        const from = pos - headerBytes.length - firstRow * width
        out.set(bytes.subarray(from, from + (end - pos)), at)
      }
      return out
    },
  }
}

export interface FakeFetch {
  fetch: typeof fetch
  requests: FakeRequest[]
  /** Bytes sent in ranged responses (the full-object stream is counted separately). */
  rangedBytes(): number
  streamedBytes(): number
}

/**
 * A `fetch` over one object. A full GET streams it in `chunk`-byte pieces
 * (pulled lazily, so a cancelled stream stops generating); a Range GET
 * answers 206 with exactly those bytes. Every response carries `etag()`, so a
 * test can overwrite the object mid-read by changing what it returns.
 */
export function fakeFetch(
  object: ByteSource,
  { chunk = 256 * 1024, etag = () => '"v1"' }: { chunk?: number; etag?: () => string } = {},
): FakeFetch {
  const requests: FakeRequest[] = []
  let ranged = 0
  let streamed = 0
  const impl = (_input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const headers = new Headers(init?.headers)
    const range = /^bytes=(\d+)-(\d*)$/.exec(headers.get("Range") ?? "")
    const signal = init?.signal
    if (signal?.aborted) return Promise.reject(new DOMException("Aborted", "AbortError"))
    if (range) {
      const start = Number(range[1])
      const end = range[2] ? Number(range[2]) + 1 : object.size
      requests.push({ range: [start, end] })
      if (!range[2]) {
        // An open-ended range: a stream from `start`.
        return Promise.resolve(
          streamResponse(object, start, { chunk, etag: etag(), signal }, (n) => (streamed += n)),
        )
      }
      const body = object.read(start, Math.min(end, object.size))
      ranged += body.length
      return Promise.resolve(
        new Response(body.slice(), {
          status: 206,
          headers: { "Content-Range": `bytes ${start}-${end - 1}/${object.size}`, ETag: etag() },
        }),
      )
    }
    requests.push({})
    return Promise.resolve(
      streamResponse(object, 0, { chunk, etag: etag(), signal }, (n) => (streamed += n)),
    )
  }
  return {
    fetch: impl,
    requests,
    rangedBytes: () => ranged,
    streamedBytes: () => streamed,
  }
}

function streamResponse(
  object: ByteSource,
  from: number,
  { chunk, etag, signal }: { chunk: number; etag: string; signal?: AbortSignal | null },
  count: (n: number) => void,
): Response {
  let pos = from
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (signal?.aborted) {
        controller.error(new DOMException("Aborted", "AbortError"))
        return
      }
      if (pos >= object.size) {
        controller.close()
        return
      }
      const end = Math.min(pos + chunk, object.size)
      const bytes = object.read(pos, end).slice()
      count(bytes.length)
      pos = end
      controller.enqueue(bytes)
    },
  })
  return new Response(stream, { status: from > 0 ? 206 : 200, headers: { ETag: etag } })
}
