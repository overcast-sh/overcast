import type { BridgeDial, BridgeSocket } from "@/features/debugger/session/cdp-client"
import type { CdpEvent, CdpEvents } from "@/features/debugger/session/cdp-protocol"

/**
 * A scripted stand-in for the debugger bridge WebSocket, for every test that
 * drives a `CdpClient` or a `DebugSession` without a server: the test opens
 * it, answers the commands it sees, and emits protocol events in whatever
 * order the scenario needs. One fake, so the frame shapes live here once.
 */
export class FakeBridgeSocket implements BridgeSocket {
  readonly url: string
  /** Every frame the client sent, parsed, oldest first. */
  readonly sent: Array<{ id: number; method: string; params?: unknown }> = []
  /** Set when the client closed the socket itself. */
  closedBy: { code?: number; reason?: string } | null = null

  private readonly listeners = new Map<string, Array<(event: unknown) => void>>()

  constructor(url: string) {
    this.url = url
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data))
  }

  close(code?: number, reason?: string): void {
    this.closedBy = { code, reason }
  }

  addEventListener(type: string, listener: (event: never) => void): void {
    const list = this.listeners.get(type) ?? []
    list.push(listener as (event: unknown) => void)
    this.listeners.set(type, list)
  }

  // ─── Scripting ──────────────────────────────────────────────────────────

  /** The server accepted the upgrade. */
  open(): void {
    this.emit("open", undefined)
  }

  /** The bridge closed the socket with a code and reason (§ 3.1). */
  serverClose(code: number, reason = ""): void {
    this.emit("close", { code, reason })
  }

  /** Answer the request with `id` with a result. */
  respond(id: number, result: unknown = {}): void {
    this.emit("message", { data: JSON.stringify({ id, result }) })
  }

  /** Answer the request with `id` with a protocol error. */
  fail(id: number, code: number, message: string): void {
    this.emit("message", { data: JSON.stringify({ id, error: { code, message } }) })
  }

  /** Emit a protocol event. */
  event<E extends CdpEvent>(method: E, params: CdpEvents[E]): void {
    this.emit("message", { data: JSON.stringify({ method, params }) })
  }

  /** Deliver a raw text frame, for the malformed cases. */
  raw(data: unknown): void {
    this.emit("message", { data })
  }

  /** The most recent request, or the most recent one for `method`. */
  lastRequest(method?: string) {
    const frames = method ? this.sent.filter((f) => f.method === method) : this.sent
    const frame = frames.at(-1)
    if (!frame) throw new Error(`no request${method ? ` for ${method}` : ""} was sent`)
    return frame
  }

  /** Every request for `method`, oldest first. */
  requests(method: string) {
    return this.sent.filter((f) => f.method === method)
  }

  /** Answer every unanswered request so far with `{}`; the ids answered. */
  respondAll(result: unknown = {}): number[] {
    const ids = this.sent.filter((f) => !this.answered.has(f.id)).map((f) => f.id)
    for (const id of ids) this.respond(id, result)
    return ids
  }

  private readonly answered = new Set<number>()

  private emit(type: string, event: unknown): void {
    if (type === "message") {
      const data = (event as { data: unknown }).data
      if (typeof data === "string") {
        try {
          const frame = JSON.parse(data) as { id?: number }
          if (typeof frame.id === "number") this.answered.add(frame.id)
        } catch {
          // A deliberately malformed frame; nothing to track.
        }
      }
    }
    for (const listener of this.listeners.get(type) ?? []) listener(event)
  }
}

/**
 * A `BridgeDial` that records every socket it hands out, so a test can open,
 * script and close each connection attempt in turn.
 */
export function fakeBridge(): {
  dial: BridgeDial
  sockets: FakeBridgeSocket[]
  latest(): FakeBridgeSocket
} {
  const sockets: FakeBridgeSocket[] = []
  return {
    sockets,
    dial: (url) => {
      const socket = new FakeBridgeSocket(url)
      sockets.push(socket)
      return socket
    },
    latest() {
      const socket = sockets.at(-1)
      if (!socket) throw new Error("no socket has been dialled")
      return socket
    },
  }
}
