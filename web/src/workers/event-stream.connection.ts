/**
 * event-stream.connection — one SSE connection that reconnects on its own
 * schedule, and reports that schedule.
 *
 * Shared by both transports: the SharedWorker that normally owns the
 * connection, and the direct-EventSource fallback for browsers without one.
 * They had a copy each of the same backoff loop, which is exactly the kind of
 * duplication a reconnect policy should not have — two answers to "when is the
 * next attempt?" is one too many for a UI that counts down to it.
 *
 * ## Why the browser's own reconnect is not used
 *
 * An `EventSource` already retries a dropped connection by itself, and for a
 * network-level failure — the emulator stopped, which is the common case —
 * that is the path taken: `readyState` stays `CONNECTING` and the browser
 * reconnects on an interval it never exposes. Only a bad *response* (a non-200
 * or the wrong content type) closes it for good.
 *
 * That left the interesting failure invisible: nothing could say when the next
 * attempt was due, and "retry now" had nothing to pre-empt. So the connection
 * is closed on the first error either way and every attempt is scheduled here.
 * Nothing is lost by taking it over — the resume point travels as a query
 * parameter (see `withResumePoint`), which is what a reconnect the browser did
 * not manage for us needs anyway.
 */

import type { StreamEvent } from "@/types"
import { withResumePoint, DISCONNECTED, type ConnectionState } from "./event-stream.protocol"
import { formatQuantity } from "@/lib/format"

// ─── Backoff ───────────────────────────────────────────────────────────────

const BASE_DELAY_MS = 1_000
const MAX_DELAY_MS = 5_000

/**
 * Delay before the attempt that follows `failures` consecutive failures:
 * 1s, 2s, 4s, then 5s forever.
 *
 * The ceiling is low on purpose. This is a local emulator a developer restarts
 * by hand, so the cost of a wasted attempt is nil and the cost of a long
 * backoff is staring at a UI that has not noticed the emulator came back.
 */
export function retryDelayMs(failures: number): number {
  return Math.min(BASE_DELAY_MS * 2 ** failures, MAX_DELAY_MS)
}

// ─── EventSource seam ──────────────────────────────────────────────────────

/**
 * The slice of `EventSource` this module drives.
 *
 * jsdom has no `EventSource`, so the tests supply their own; a real
 * `EventSource` satisfies this structurally.
 */
export interface EventSourceLike {
  onopen: ((event: Event) => void) | null
  onerror: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent<string>) => void) | null
  close(): void
}

export interface ReconnectingStreamOptions {
  /** Stream URL, without a resume point — this class adds its own. */
  url: string
  /** Called for every well-formed frame, in arrival order. */
  onEvent: (event: StreamEvent) => void
  /** Called on every state change, including the initial attempt. */
  onState: (state: ConnectionState) => void
  /** Opens the underlying connection. Overridden in tests. */
  open?: (url: string) => EventSourceLike
}

// ─── ReconnectingStream ────────────────────────────────────────────────────

export class ReconnectingStream {
  readonly #url: string
  readonly #onEvent: (event: StreamEvent) => void
  readonly #onState: (state: ConnectionState) => void
  readonly #open: (url: string) => EventSourceLike

  #source: EventSourceLike | null = null
  #timer: ReturnType<typeof setTimeout> | null = null
  #state: ConnectionState = DISCONNECTED
  /** Last SSE id seen, replayed on reconnect — see `withResumePoint`. */
  #lastEventId: string | null = null

  constructor({ url, onEvent, onState, open }: ReconnectingStreamOptions) {
    this.#url = url
    this.#onEvent = onEvent
    this.#onState = onState
    this.#open = open ?? ((target) => new EventSource(target))
    this.#connect()
  }

  /** The current state, for a tab that subscribes midway through. */
  get state(): ConnectionState {
    return this.#state
  }

  /**
   * Brings the scheduled attempt forward to now. A no-op while an attempt is
   * already in flight, so an impatient click cannot stack connections.
   */
  retryNow(): void {
    if (this.#state.nextAttemptAt === null) return
    this.#connect()
  }

  /** Closes the connection and cancels any scheduled attempt, permanently. */
  close(): void {
    this.#clearTimer()
    this.#source?.close()
    this.#source = null
  }

  // ── Internals ────────────────────────────────────────────────────────────

  #connect(): void {
    this.#clearTimer()
    this.#source?.close()

    const source = this.#open(withResumePoint(this.#url, this.#lastEventId))
    this.#source = source
    this.#publish({ nextAttemptAt: null })

    /** Guards against a superseded connection reporting over a live one. */
    const isCurrent = () => source === this.#source

    source.onopen = () => {
      if (!isCurrent()) return
      const { attempt } = this.#state
      console.info(
        attempt > 0
          ? `[event-stream] reconnected after ${formatQuantity(attempt, "attempt")}`
          : "[event-stream] connected",
      )
      this.#publish({ connected: true, attempt: 0, lastConnectedAt: Date.now() })
    }

    source.onerror = () => {
      if (!isCurrent()) return
      source.close()
      this.#source = null
      this.#scheduleRetry()
    }

    source.onmessage = (event) => {
      if (event.lastEventId) this.#lastEventId = event.lastEventId
      if (!event.data) return
      try {
        this.#onEvent(JSON.parse(event.data) as StreamEvent)
      } catch {
        console.error("[event-stream] malformed SSE frame", event.data)
      }
    }
  }

  #scheduleRetry(): void {
    const delay = retryDelayMs(this.#state.attempt)
    const attempt = this.#state.attempt + 1
    console.warn(`[event-stream] connection lost, retrying in ${delay}ms (attempt ${attempt})`)

    this.#publish({ connected: false, attempt, nextAttemptAt: Date.now() + delay })
    this.#timer = setTimeout(() => {
      this.#timer = null
      this.#connect()
    }, delay)
  }

  #publish(change: Partial<ConnectionState>): void {
    this.#state = { ...this.#state, ...change }
    this.#onState(this.#state)
  }

  #clearTimer(): void {
    if (this.#timer === null) return
    clearTimeout(this.#timer)
    this.#timer = null
  }
}
