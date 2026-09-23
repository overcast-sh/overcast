import { http, HttpResponse } from "msw"
import { server } from "@/test/server"

/**
 * Serves `content` at the console's object download route the way the BFF
 * does: a full GET whole, a Range GET as 206 with `Content-Range`, both with
 * an ETag. The data previews read through it for real — their row sources
 * and their (in-process) worker — so a test drives them end to end. Returns
 * the object's size, for the metadata the preview opens with.
 */
export function serveObject(content: string | Uint8Array, etag = '"v1"'): number {
  const bytes = typeof content === "string" ? new TextEncoder().encode(content) : content
  server.use(
    http.get(/\/api\/s3\/buckets\/[^/]+\/objects\/.+\/download/, ({ request }) => {
      const range = /^bytes=(\d+)-(\d*)$/.exec(request.headers.get("Range") ?? "")
      if (!range) {
        return new HttpResponse(bytes.slice(), { status: 200, headers: { ETag: etag } })
      }
      const start = Number(range[1])
      const end = range[2] ? Math.min(Number(range[2]) + 1, bytes.length) : bytes.length
      return new HttpResponse(bytes.slice(start, end), {
        status: 206,
        headers: { ETag: etag, "Content-Range": `bytes ${start}-${end - 1}/${bytes.length}` },
      })
    }),
  )
  return bytes.length
}
