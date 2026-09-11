import { StartLiveTailCommand } from "@aws-sdk/client-cloudwatch-logs"
import { awsClients } from "@/services/aws-clients"
import { endpointResolver } from "@/services/discovery"

export interface TailedLogEvent {
  timestamp: number
  ingestionTime: number
  logStreamName: string
  message: string
}

export interface TailLogEventsOptions {
  groupIdentifier: string
  streamName?: string
  streamNames?: string[]
  streamNamePrefixes?: string[]
  filterPattern?: string
  signal?: AbortSignal
  /**
   * Called once per frame received from the session — including the empty
   * sessionUpdate frames the emulator writes every second as a heartbeat,
   * which the generator otherwise consumes invisibly (they yield no events).
   * This is what lets a caller tell a quiet-but-healthy session from a
   * connection that died without an error (machine sleep, NAT timeout: no
   * FIN ever arrives, so nothing rejects). Kept off React state by callers:
   * heartbeats arrive forever, so a per-frame state write would be a standing
   * once-a-second re-render.
   */
  onActivity?: () => void
  /**
   * Called once, when the session is open: StartLiveTail has returned its
   * stream. The emulator subscribes the session to the log-write bus before
   * it writes the initial-response frame the SDK parks on, so by the time
   * this fires every write from here on will be pushed — which makes it the
   * moment a caller can safely read the gap between what it last fetched and
   * now, knowing nothing can fall between the read and the session.
   */
  onOpen?: () => void
}

export function parseLogFilterTerms(pattern: string): string[] {
  const terms: string[] = []
  let remaining = pattern.trim()
  while (remaining) {
    if (remaining[0] === '"') {
      const end = remaining.indexOf('"', 1)
      if (end >= 0) {
        terms.push(remaining.substring(1, end))
        remaining = remaining.substring(end + 1).trim()
      } else {
        terms.push(remaining.substring(1))
        remaining = ""
      }
    } else {
      const idx = remaining.search(/[\s\t]/)
      if (idx >= 0) {
        terms.push(remaining.substring(0, idx))
        remaining = remaining.substring(idx).trim()
      } else {
        terms.push(remaining)
        remaining = ""
      }
    }
  }
  return terms
}

/**
 * One regex matching any of a filter pattern's terms, or null when the pattern
 * selects everything.
 *
 * Compiled once per pattern rather than per row: the viewers highlight matches
 * inside every visible row on every scroll frame, and building the same regex
 * there made the pattern's cost scale with rows rendered instead of with edits
 * to the filter box.
 *
 * The capturing group is what makes `String.split` interleave the matches, so
 * callers can take the odd indices as the hits. `split` never reads or writes
 * the regex's `lastIndex` — it clones with a sticky flag internally — so one
 * compiled instance is safe to share across rows.
 */
export function compileFilterHighlighter(pattern: string): RegExp | null {
  const terms = parseLogFilterTerms(pattern)
  if (terms.length === 0) return null
  const escaped = terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
  return new RegExp(`(${escaped.join("|")})`, "gi")
}

// ── Merging a stored page with a live session ──────────────────────────────

/** The fields a stored and a tailed log event have in common. */
export interface MergeableLogEvent {
  eventId?: string
  timestamp?: number
  logStreamName?: string
  message?: string
}

/**
 * Identity for a log event that survives pagination and re-sorting.
 *
 * The original of the object-identity pattern, now living in
 * [stable-row-key.ts](../../../lib/stable-row-key.ts) — log events are its
 * canonical "no natural key" case (duplicate lines a function really did log
 * twice must get distinct keys, which content-derived keys cannot promise).
 * Kept as a named export because "the key of a log event" is what call sites
 * mean; the shared module is how they all mean the same thing.
 */
export { stableRowKey as logEventKey } from "@/lib/stable-row-key"

/**
 * A short, URL-safe signature for one event's content — for deep links that
 * anchor a view on a specific event.
 *
 * `logEventKey` is identity *within one page's memory* and dies with it; a
 * link needs identity that survives a fresh fetch in another tab. Timestamp
 * plus a message hash is enough to pick an event out of its millisecond; two
 * truly identical events in the same millisecond are indistinguishable on
 * every field the emulator returns, so matching the first is not a loss.
 */
export function logEventSignature(event: MergeableLogEvent): string {
  const message = event.message ?? ""
  // djb2 — tiny, stable, and adequate for disambiguation, not for security.
  let hash = 5381
  for (let i = 0; i < message.length; i++) {
    hash = ((hash << 5) + hash + message.charCodeAt(i)) | 0
  }
  return (hash >>> 0).toString(36)
}

/**
 * Chronological order, with the tie-breaks that keep it total: two events in
 * the same millisecond fall back to ingestion order, then to the stream name,
 * so a sort is stable across refetches that shuffle their input.
 */
export function compareLogEvents(
  a: MergeableLogEvent & { ingestionTime?: number },
  b: MergeableLogEvent & { ingestionTime?: number },
): number {
  const timeDelta = (a.timestamp ?? 0) - (b.timestamp ?? 0)
  if (timeDelta !== 0) return timeDelta
  const ingestDelta = (a.ingestionTime ?? 0) - (b.ingestionTime ?? 0)
  if (ingestDelta !== 0) return ingestDelta
  return (a.logStreamName ?? "").localeCompare(b.logStreamName ?? "")
}

/**
 * Merges two already-sorted event lists into one sorted list.
 *
 * This is what keeps the viewer's "everything on screen" array cheap to
 * rebuild: the fetched page is sorted once when it arrives and the tail buffer
 * maintains its own order, so combining them is a linear walk rather than a
 * fresh sort of the world on every batch of live events.
 */
export function mergeSortedEvents<T extends MergeableLogEvent & { ingestionTime?: number }>(
  a: readonly T[],
  b: readonly T[],
): T[] {
  if (a.length === 0) return [...b]
  if (b.length === 0) return [...a]
  const merged: T[] = new Array<T>(a.length + b.length)
  let ai = 0
  let bi = 0
  let out = 0
  while (ai < a.length && bi < b.length) {
    merged[out++] = compareLogEvents(a[ai], b[bi]) <= 0 ? a[ai++] : b[bi++]
  }
  while (ai < a.length) merged[out++] = a[ai++]
  while (bi < b.length) merged[out++] = b[bi++]
  return merged
}

function mergeKey(event: MergeableLogEvent): string {
  // Real CloudWatch stamps every event with an eventId and both APIs return
  // it; the emulator does not, so identity otherwise falls back to the tuple
  // that makes two records the same line.
  if (event.eventId) return `id ${event.eventId}`
  return `${event.logStreamName ?? ""} ${event.timestamp ?? 0} ${event.message ?? ""}`
}

/**
 * Return the tailed events not already accounted for by `stored`.
 *
 * A live session and a FilterLogEvents/GetLogEvents page overlap: once an
 * event is on disk, any later read returns it, and the session that was open
 * when it was written has already pushed it. Rendering the concatenation shows
 * that event twice.
 *
 * Dropping is by occurrence count, not by presence, so a line a function
 * really did log twice within the same millisecond still shows twice — the
 * stored page holds two copies too, and only those two are cancelled out.
 */
export function dropTailedDuplicates<T extends MergeableLogEvent>(
  stored: readonly MergeableLogEvent[],
  tailed: T[],
): T[] {
  if (stored.length === 0 || tailed.length === 0) return tailed

  const unclaimed = new Map<string, number>()
  for (const event of stored) {
    const key = mergeKey(event)
    unclaimed.set(key, (unclaimed.get(key) ?? 0) + 1)
  }

  const kept: T[] = []
  for (const event of tailed) {
    const key = mergeKey(event)
    const count = unclaimed.get(key)
    if (count) {
      unclaimed.set(key, count - 1)
      continue
    }
    kept.push(event)
  }
  return kept.length === tailed.length ? tailed : kept
}

function logGroupIdentifierArn(identifier: string): string {
  if (identifier.startsWith("arn:")) return identifier
  const endpoint = endpointResolver.get()
  return `arn:aws:logs:${endpoint.region}:000000000000:log-group:${identifier}`
}

/** Race marker for "the caller aborted while we were parked on the stream". */
const ABORTED = Symbol("aborted")

export async function* tailLogEvents(opts: TailLogEventsOptions): AsyncGenerator<TailedLogEvent> {
  const signal = opts.signal
  if (signal?.aborted) return

  const logStreamNames = opts.streamNames ?? (opts.streamName ? [opts.streamName] : undefined)

  let response
  try {
    response = await awsClients.logs().send(
      new StartLiveTailCommand({
        logGroupIdentifiers: [logGroupIdentifierArn(opts.groupIdentifier)],
        logStreamNames,
        logStreamNamePrefixes: opts.streamNamePrefixes,
        logEventFilterPattern: opts.filterPattern || undefined,
      }),
      { abortSignal: signal },
    )
  } catch (err) {
    // Every teardown — a filter change, a navigation, StrictMode's remount —
    // races the request that opens the session, and the SDK rejects the send
    // when the abort wins. That is the ordinary path, not a failure: the
    // callers drive this generator from an effect body they cannot wrap in a
    // catch, so a rejection here surfaces as an unhandled rejection.
    if (signal?.aborted) return
    throw err
  }

  const stream = response.responseStream
  if (!stream) return

  // Consume the stream through an explicit iterator rather than `for await`,
  // so the generator's exit is its own rather than a consequence of the
  // transport closing. `abortSignal` does abort the underlying request — the
  // emulator sees the hang-up within a few milliseconds — but a `for await`
  // re-checks the signal only when the next frame arrives, which leaves the
  // exit at the mercy of how idle the stream happens to be.
  const iterator = stream[Symbol.asyncIterator]()

  try {
    // The abort may already have fired while the request was in flight, in
    // which case the session came back to nobody and no further `abort` event
    // is coming. Bail here and let `finally` hang it up.
    if (signal?.aborted) return
    // Past that check the session is ours and subscribed: say so.
    opts.onOpen?.()

    const aborted = new Promise<typeof ABORTED>((resolve) => {
      signal?.addEventListener("abort", () => resolve(ABORTED), { once: true })
    })

    for (;;) {
      const next = await Promise.race([iterator.next(), aborted])
      if (next === ABORTED || signal?.aborted || next.done) return
      // Every received frame is proof the connection is alive — the empty
      // heartbeat frames especially, since they are all a quiet stream sends.
      opts.onActivity?.()
      const results = next.value.sessionUpdate?.sessionResults ?? []
      for (const event of results) {
        yield {
          timestamp: event.timestamp ?? 0,
          ingestionTime: event.ingestionTime ?? event.timestamp ?? 0,
          logStreamName: event.logStreamName ?? "",
          message: event.message ?? "",
        }
        if (signal?.aborted) return
      }
    }
  } catch (err) {
    if (signal?.aborted) return
    throw err
  } finally {
    // Reached on the abort race, on a consumer that stops iterating, and on a
    // stream error alike — all of them want the session closed.
    await iterator.return?.()
  }
}
