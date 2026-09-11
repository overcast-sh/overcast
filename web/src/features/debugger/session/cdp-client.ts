/**
 * A minimal CDP client over the debugger bridge WebSocket
 * (docs/plans/compute-debugger-console.md § 3.1, § 3.4).
 *
 * What it owns: request/response correlation by `id`, event fan-out, and the
 * connection's life — reconnecting with backoff when the bridge drops the
 * socket while a session is meant to be open, and stopping when the bridge
 * says there is nothing to connect to. Everything else (which domains to
 * enable, what a pause means) is `session.ts`'s.
 *
 * The transport is injected as a factory so tests drive a scripted socket;
 * production passes `(url) => new WebSocket(url)`. The bridge relays one CDP
 * JSON message per text frame, which is all this client reads.
 *
 * Close codes the bridge uses (§ 3.1):
 *   1011 `no container`   — nothing to relay to yet: stop, and let the session
 *                           retry once a container exists.
 *   1012 `service restart` — the container was replaced under the session:
 *                           reconnect, the session re-applies breakpoints.
 * Any other close while the client is wanted open is treated like 1012.
 */
import {
  type CdpCommands,
  type CdpEvent,
  type CdpEvents,
  type CdpIncomingFrame,
  type CdpMethod,
  type CdpRequestFrame,
  isResponseFrame,
} from "./cdp-protocol"

// ─── Transport ────────────────────────────────────────────────────────────

/** The subset of `WebSocket` the client uses; a fake implements the same four events. */
export interface BridgeSocket {
  send(data: string): void
  close(code?: number, reason?: string): void
  addEventListener(type: "open", listener: () => void): void
  addEventListener(type: "message", listener: (event: { data: unknown }) => void): void
  addEventListener(type: "close", listener: (event: { code: number; reason: string }) => void): void
  addEventListener(type: "error", listener: () => void): void
}

export type BridgeDial = (url: string) => BridgeSocket

/** The bridge's "nothing to relay to yet" close (§ 3.1); 1012 and every other drop reconnect. */
const CLOSE_NO_CONTAINER = 1011

// ─── Errors ───────────────────────────────────────────────────────────────

/** The protocol answered a command with an error frame. */
export class CdpError extends Error {
  readonly code: number
  readonly method: string
  constructor(method: string, code: number, message: string) {
    super(message)
    this.name = "CdpError"
    this.code = code
    this.method = method
  }
}

/** The socket went away before a command was answered. */
export class CdpClosedError extends Error {
  constructor(method: string) {
    super(`${method}: connection closed before the reply arrived`)
    this.name = "CdpClosedError"
  }
}

// ─── Client ───────────────────────────────────────────────────────────────

/**
 * Connection states, protocol-neutral so the session can pass them through:
 * `closed` (not wanted), `connecting` (first dial or a retry the session
 * asked for), `open`, `reconnecting` (dropped while wanted; a retry is
 * scheduled), `no-container` (the bridge closed with 1011; stopped until
 * `open()` is called again).
 */
export type CdpClientStatus = "closed" | "connecting" | "open" | "reconnecting" | "no-container"

export interface CdpClientOptions {
  url: string
  dial: BridgeDial
  /** Delays before each reconnect attempt; the last one repeats. */
  backoffMs?: readonly number[]
}

const DEFAULT_BACKOFF_MS = [500, 1_000, 2_000, 5_000] as const

type Pending = { method: string; resolve: (value: unknown) => void; reject: (err: Error) => void }
type EventHandler<E extends CdpEvent> = (params: CdpEvents[E]) => void

export class CdpClient {
  private readonly url: string
  private readonly dial: BridgeDial
  private readonly backoffMs: readonly number[]

  private socket: BridgeSocket | null = null
  private wanted = false
  private attempt = 0
  private nextId = 1
  private retryTimer: ReturnType<typeof setTimeout> | null = null
  private readonly pending = new Map<number, Pending>()
  private readonly eventHandlers = new Map<string, Set<(params: unknown) => void>>()
  private readonly statusHandlers = new Set<(status: CdpClientStatus) => void>()
  private readonly openHandlers = new Set<() => void>()
  private _status: CdpClientStatus = "closed"

  constructor({ url, dial, backoffMs = DEFAULT_BACKOFF_MS }: CdpClientOptions) {
    this.url = url
    this.dial = dial
    this.backoffMs = backoffMs
  }

  get status(): CdpClientStatus {
    return this._status
  }

  /** Dial the bridge. Also the way back from `no-container` once one exists. */
  open(): void {
    this.wanted = true
    this.clearRetry()
    if (this.socket) return
    this.attempt = 0
    this.setStatus("connecting")
    this.connect()
  }

  /** Close for good: no reconnect, every pending command rejected. */
  close(): void {
    this.wanted = false
    this.clearRetry()
    const socket = this.socket
    this.socket = null
    this.rejectPending()
    socket?.close(1000, "session stopped")
    this.setStatus("closed")
  }

  /** Send a command and resolve with its typed result, or reject with `CdpError`. */
  send<M extends CdpMethod>(
    method: M,
    params?: CdpCommands[M]["params"],
  ): Promise<CdpCommands[M]["result"]> {
    const socket = this.socket
    if (!socket || this._status !== "open") {
      return Promise.reject(new CdpClosedError(method))
    }
    const id = this.nextId++
    const frame: CdpRequestFrame = { id, method, params }
    return new Promise((resolve, reject) => {
      this.pending.set(id, { method, resolve: resolve as (value: unknown) => void, reject })
      socket.send(JSON.stringify(frame))
    })
  }

  /** Subscribe to a protocol event; returns the unsubscribe. */
  on<E extends CdpEvent>(event: E, handler: EventHandler<E>): () => void {
    let handlers = this.eventHandlers.get(event)
    if (!handlers) {
      handlers = new Set()
      this.eventHandlers.set(event, handlers)
    }
    const wrapped = handler as (params: unknown) => void
    handlers.add(wrapped)
    return () => {
      handlers.delete(wrapped)
    }
  }

  /** Observe connection state changes. */
  onStatus(handler: (status: CdpClientStatus) => void): () => void {
    this.statusHandlers.add(handler)
    return () => {
      this.statusHandlers.delete(handler)
    }
  }

  /**
   * Fires on every successful open, first and reconnects alike — the hook
   * for re-enabling domains and re-applying breakpoints against a fresh
   * inspector.
   */
  onOpen(handler: () => void): () => void {
    this.openHandlers.add(handler)
    return () => {
      this.openHandlers.delete(handler)
    }
  }

  // ─── Internals ──────────────────────────────────────────────────────────

  private connect(): void {
    let socket: BridgeSocket
    try {
      socket = this.dial(this.url)
    } catch (err) {
      // A dial that throws synchronously (a malformed URL, a blocked
      // origin) is a close without an open; the same retry rule applies.
      this.socket = null
      this.onClosed({ code: 1006, reason: err instanceof Error ? err.message : String(err) })
      return
    }
    this.socket = socket
    socket.addEventListener("open", () => {
      if (this.socket !== socket) return
      this.attempt = 0
      this.setStatus("open")
      for (const handler of this.openHandlers) handler()
    })
    socket.addEventListener("message", (event) => {
      if (this.socket !== socket) return
      this.onMessage(event.data)
    })
    socket.addEventListener("close", (event) => {
      if (this.socket !== socket) return
      this.socket = null
      this.onClosed(event)
    })
    // An error is always followed by a close event, which carries the
    // decision; nothing to do here beyond not crashing on it.
    socket.addEventListener("error", () => {})
  }

  private onMessage(data: unknown): void {
    if (typeof data !== "string") return
    let frame: CdpIncomingFrame
    try {
      frame = JSON.parse(data) as CdpIncomingFrame
    } catch {
      return
    }
    if (isResponseFrame(frame)) {
      const pending = this.pending.get(frame.id)
      if (!pending) return
      this.pending.delete(frame.id)
      if (frame.error) {
        pending.reject(new CdpError(pending.method, frame.error.code, frame.error.message))
      } else {
        pending.resolve(frame.result ?? {})
      }
      return
    }
    const handlers = this.eventHandlers.get(frame.method)
    if (!handlers) return
    for (const handler of handlers) handler(frame.params ?? {})
  }

  private onClosed(event: { code: number; reason: string }): void {
    this.rejectPending()
    if (!this.wanted) {
      this.setStatus("closed")
      return
    }
    if (event.code === CLOSE_NO_CONTAINER) {
      this.setStatus("no-container")
      return
    }
    this.setStatus("reconnecting")
    const delay = this.backoffMs[Math.min(this.attempt, this.backoffMs.length - 1)]
    this.attempt += 1
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null
      if (this.wanted && !this.socket) this.connect()
    }, delay)
  }

  private rejectPending(): void {
    for (const [, pending] of this.pending) pending.reject(new CdpClosedError(pending.method))
    this.pending.clear()
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer)
      this.retryTimer = null
    }
  }

  private setStatus(status: CdpClientStatus): void {
    if (this._status === status) return
    this._status = status
    for (const handler of this.statusHandlers) handler(status)
  }
}

/** The production transport: the browser's own WebSocket. */
export const webSocketDial: BridgeDial = (url) => new WebSocket(url)
