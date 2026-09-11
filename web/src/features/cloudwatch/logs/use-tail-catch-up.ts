/**
 * The read that makes Tail honest about the gap.
 *
 * A live session pushes what is written *after* it opens. The viewer's page
 * was fetched some time before the user reached for Tail — minutes, often —
 * and everything logged in between belonged to neither source: not on the
 * page, never pushed. Ticking Tail looked like "show me everything from
 * here", and quietly skipped the stretch the user most likely cared about.
 *
 * So each time a session opens, this reads FilterLogEvents from the newest
 * event already on screen up to now, and hands the result to the viewer as a
 * third source beside the fetched pages and the live buffer. Ordering is what
 * makes it complete: the emulator subscribes a session before it answers the
 * StartLiveTail call (see `TailLogEventsOptions.onOpen`), so a read that
 * starts after the answer overlaps the session rather than leaving a seam,
 * and the overlap is reconciled by count downstream like every other overlap
 * between a stored page and a session.
 *
 * Keyed on the session identity, not the toggle: a session that dies and is
 * reopened catches up again from wherever the view had got to. Paged to the
 * end, because the gap can exceed one 10,000-event page.
 */

import { useCallback, useEffect, useRef, useState } from "react"
import { logs } from "@/services/api"
import type { FilteredLogEvent } from "@/types/logs"
import { compareLogEvents, dropTailedDuplicates, mergeSortedEvents } from "./tail"

export interface TailCatchUpOptions {
  /**
   * The open session's identity from `useLogTailBuffer`, or null while none
   * is open. The read runs once per distinct value.
   */
  openSession: string | null
  groupName: string
  streamName?: string
  filterPattern?: string
  /**
   * Where the read starts (inclusive): the newest event already on screen,
   * or the window's own start when nothing is; undefined reads from the
   * beginning of the stream. Called at the moment the session opens — a
   * function rather than a value, so the newest edge moving with every live
   * event never re-arms the read.
   */
  since: () => number | undefined
}

export interface TailCatchUp {
  /** Everything caught up so far, oldest first, across every session opened. */
  events: FilteredLogEvent[]
  /** A catch-up read is in flight. */
  loading: boolean
  /** Forget what was caught up — the view's identity changed. */
  reset: () => void
}

const NO_EVENTS: FilteredLogEvent[] = []

export function useTailCatchUp({
  openSession,
  groupName,
  streamName,
  filterPattern,
  since,
}: TailCatchUpOptions): TailCatchUp {
  const [events, setEvents] = useState<FilteredLogEvent[]>(NO_EVENTS)
  // The session whose read has finished (or failed). "Loading" is derived —
  // the open session is not yet the settled one — so opening a session
  // needs no state write of its own, and closing it clears the flag for free.
  const [settledSession, setSettledSession] = useState<string | null>(null)
  const loading = openSession != null && settledSession !== openSession

  // The latest `since` without making it an effect dependency: it closes
  // over the events on screen and would otherwise re-run the read on every
  // live arrival. Written from an effect, never during render.
  const sinceRef = useRef(since)
  useEffect(() => {
    sinceRef.current = since
  }, [since])

  useEffect(() => {
    if (openSession == null) return
    const startTime = sinceRef.current()
    // Closed at "now": the session was already open, so everything written
    // from here on is its to push — and a bounded window terminates, where
    // an open-ended read of a busy stream could page after the live edge
    // indefinitely. FilterLogEvents' endTime is inclusive; a write in this
    // very millisecond lands in both sources and reconciles by count.
    const endTime = Date.now()
    // Torn down with the session: a read still paging when the tail closes
    // (or the view's identity changes) must not land its pages afterwards.
    const teardown = new AbortController()
    const { signal } = teardown
    void (async () => {
      try {
        const fetched: FilteredLogEvent[] = []
        let nextToken: string | undefined
        do {
          const page = await logs.filterEvents(groupName, {
            filterPattern: filterPattern || undefined,
            startTime,
            endTime,
            ...(streamName ? { logStreamNames: [streamName] } : {}),
            nextToken,
          })
          if (signal.aborted) return
          fetched.push(...page.events)
          nextToken = page.nextToken
        } while (nextToken)
        if (fetched.length === 0) return
        fetched.sort(compareLogEvents)
        // A second session's read starts at the newest event the first one
        // caught, so consecutive reads overlap by that one event at most —
        // reconciled by count, the way every stored/live overlap is.
        setEvents((prev) => mergeSortedEvents(prev, dropTailedDuplicates(prev, fetched)))
      } catch (err) {
        // The session is open and pushing regardless; a failed catch-up
        // leaves the gap unfilled, which is exactly what tailing did before
        // this read existed. Worth a console line, not an error state.
        if (!signal.aborted) console.warn("live tail catch-up read failed", err)
      } finally {
        if (!signal.aborted) setSettledSession(openSession)
      }
    })()
    return () => teardown.abort()
    // The three identity fields are folded into `openSession` already (it is
    // the buffer's session key); they are listed so the closure's captures
    // are honest, not because they add re-runs.
  }, [openSession, groupName, streamName, filterPattern])

  const reset = useCallback(() => setEvents(NO_EVENTS), [])

  return { events, loading, reset }
}
