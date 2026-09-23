/**
 * The HTTP side of reading an object: the Range header every read sends, and
 * the one error a read that reached the server can fail with.
 */

/** A read the server refused: the status says why (403, 404, 416…). */
export class HttpReadError extends Error {
  readonly status: number

  constructor(status: number) {
    super(`Read failed: HTTP ${status}`)
    this.name = "HttpReadError"
    this.status = status
  }
}

/** `bytes=start-(end-1)` for `[start, end)`, or open-ended `bytes=start-` without an end. */
export function rangeHeader(start: number, end?: number): string {
  return end === undefined ? `bytes=${start}-` : `bytes=${start}-${end - 1}`
}

/** The response, when it carries the object's bytes; an `HttpReadError` otherwise. */
export function checkedResponse(response: Response): Response {
  if (!response.ok) throw new HttpReadError(response.status)
  return response
}
