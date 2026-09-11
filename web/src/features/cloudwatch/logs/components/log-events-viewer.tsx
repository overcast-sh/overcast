import { memo, useState, useMemo, useRef, useCallback, useEffect, useLayoutEffect } from "react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { useVirtualizer } from "@tanstack/react-virtual"
import {
  AlertTriangle,
  ArrowDown,
  ArrowDownUp,
  ArrowLeft,
  Eraser,
  FileText,
  Link2,
  ListFilter,
  RefreshCw,
  Search,
  Undo2,
  X,
  Zap,
} from "lucide-react"
import { CopyButton } from "@/components/ui/copy-button"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { nearViewport, useScrollSettled } from "@/hooks/use-scroll-settled"
import {
  logsFilterInfiniteQueryOptions,
  logsStreamsQueryOptions,
} from "@/features/cloudwatch/logs/data"
import {
  TimeRangeFilter,
  type TimeRange,
} from "@/features/cloudwatch/logs/components/time-range-filter"
import { JumpToTimestamp } from "@/features/cloudwatch/logs/components/jump-to-timestamp"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PageHeader, Spinner, EmptyState } from "@/components/ui/primitives"
import { useEndpoint } from "@/hooks/use-endpoint"
import { isResourceNotFound } from "@/lib/aws-error"
import { cn } from "@/lib/utils"
import { describeLogEvent, formatLogDelta, formatLogTime, logLevelRowClass } from "@/lib/log-format"
import { LiveTailIndicator } from "@/components/logs/live-tail-indicator"
import { LogMessage } from "@/components/logs/log-message"
import type { FilteredLogEvent } from "@/types/logs"
import {
  compareLogEvents,
  compileFilterHighlighter,
  dropTailedDuplicates,
  logEventKey,
  logEventSignature,
  mergeSortedEvents,
} from "@/features/cloudwatch/logs/tail"
import { useLogTailBuffer } from "@/features/cloudwatch/logs/use-log-tail-buffer"
import { useTailCatchUp } from "@/features/cloudwatch/logs/use-tail-catch-up"
import { useLoadMoreAtEdge } from "@/hooks/use-load-more-at-edge"
import { useLogViewPrefs } from "@/features/cloudwatch/logs/use-log-view-prefs"
import { ExportMenu } from "@/features/cloudwatch/logs/components/export-menu"

// ── Row height estimation ──────────────────────────────────────────────────

/**
 * A collapsed row's height, exactly: `py-1.5` (12px) around one
 * `text-2xs leading-relaxed` line (18px). Collapsed rows carry a fixed
 * height and are never measured — a constant estimate with no measurement
 * churn is the point of the mode — so this must match what the CSS produces.
 */
const COLLAPSED_ROW_HEIGHT = 30

/** Estimate the row height for a log event based on message length and format state. */
function estimateRowHeight(msg: string, formatted: boolean): number {
  const baseHeight = 36 // padding + timestamp line
  if (formatted && (msg.trim().startsWith("{") || msg.trim().startsWith("["))) {
    return baseHeight + Math.ceil(msg.length / 48) * 18
  }
  // Plain: wrap estimation based on ~120 chars per line at typical widths
  const lines = Math.max(1, Math.ceil(msg.length / 120))
  return baseHeight + (lines - 1) * 18
}

/** Stable empty fallback — a fresh `[]` would re-run every memo keyed on it. */
const NO_EVENTS: FilteredLogEvent[] = []

/**
 * Each fetched page sorted once, cached on the page object: the query cache
 * hands out stable page identities, so a backward chunk prepending to the
 * pages array costs one sort of the chunk — never a re-sort of every page
 * already loaded. Cross-stream order is `compareLogEvents`' total order
 * (timestamp, ingestionTime, stream), the same one the tail merge uses.
 */
const pageSortCache = new WeakMap<object, FilteredLogEvent[]>()

function sortedPageEvents(page: { events: FilteredLogEvent[] }): FilteredLogEvent[] {
  let sorted = pageSortCache.get(page)
  if (!sorted) {
    sorted = [...page.events].sort(compareLogEvents)
    pageSortCache.set(page, sorted)
  }
  return sorted
}

/**
 * Absolute URL that reopens the stream view anchored on exactly this event —
 * the same `anchorTs` + `anchorSig` contract the search-hit deep links use,
 * so the link inherits the anchor machinery whole: lead-in window, paging to
 * the event, centring, the persistent mark. Null when the event names no
 * stream, since the stream route cannot open without one.
 */
function eventDeepLink(groupName: string, evt: FilteredLogEvent): string | null {
  if (!evt.logStreamName) return null
  const query = [
    ["groupName", groupName],
    ["streamName", evt.logStreamName],
    ["anchorTs", String(evt.timestamp ?? 0)],
    ["anchorSig", logEventSignature(evt)],
  ]
    .map(([key, value]) => `${key}=${encodeURIComponent(value)}`)
    .join("&")
  return `${window.location.origin}/cloudwatch/logs/stream?${query}`
}

// ── Main component ─────────────────────────────────────────────────────────

/** An event a deep link wants the view centred on — a search result, usually. */
export interface LogEventAnchor {
  timestamp: number
  /** `logEventSignature` of the event, to pick it out of its millisecond. */
  signature?: string
}

/**
 * How much history loads above an anchored event before it. The anchor lands
 * with its lead-up in view rather than at the very top of the list; scrolling
 * further back is the time-range filter's job.
 */
const ANCHOR_CONTEXT_BEFORE_MS = 15 * 60 * 1000

interface Props {
  groupName: string
  streamName?: string
  anchor?: LogEventAnchor
}

export function LogEventsViewer({ groupName, streamName, anchor }: Props) {
  const navigate = useNavigate()
  const [filterInput, setFilterInput] = useState("")
  const [activeFilter, setActiveFilter] = useState("")
  // The user's time filter, kept apart from the anchor's lead-in window: an
  // explicit start here is a hard floor backward expansion must not cross,
  // while the lead-in is system-chosen and free to widen.
  const [timeRange, setTimeRange] = useState<TimeRange>({})
  const [timeRangeTouched, setTimeRangeTouched] = useState(false)
  // Display preferences persist across visits (one localStorage key, read once
  // at mount); Tail and the cleared-through cut are session state and do not.
  const { prefs, setPref } = useLogViewPrefs()
  const { displayMode, formatted, syntaxHighlight, wrapLines, sortAsc, utc, showDeltas } = prefs
  const collapseMode = prefs.collapsed
  const [tailMode, setTailMode] = useState(false)
  // Rows the user expanded out of collapse mode — by the same per-event key
  // the virtualizer uses, so a sort flip or a prepend keeps the expansion on
  // the same event. Session state: a revisit starts collapsed again.
  const [expandedKeys, setExpandedKeys] = useState<ReadonlySet<number>>(() => new Set())
  // The keyboard cursor: one `logEventKey`, so it survives sort flips and
  // prepends the way the expansion set does — and so moving it re-renders
  // only the row wrappers, never the memoised row content (LogMessage takes
  // no cursor-related props at all). Session state, like the expansion set.
  const [focusedKey, setFocusedKey] = useState<number | null>(null)
  // Clearing the buffer hides everything on screen without stopping the tail.
  // The live events are simply dropped; the fetched ones are still in the
  // query cache, so they are held back by timestamp instead — a marker that
  // survives the refetches that would otherwise put them straight back.
  const [clearedThrough, setClearedThrough] = useState<number | null>(null)

  const parentRef = useRef<HTMLDivElement>(null)
  const pinnedToLatestRef = useRef(true)
  const previousEventCountRef = useRef(0)

  // A jump navigates to the same route with a new anchorTs, which re-renders
  // this mounted component rather than remounting it — so an anchor change has
  // to hand the anchor machinery a clean slate itself.
  const anchorKey = anchor ? `${anchor.timestamp}:${anchor.signature ?? ""}` : null
  const [settledAnchorKey, setSettledAnchorKey] = useState<string | null>(null)
  const [prevAnchorKey, setPrevAnchorKey] = useState(anchorKey)
  if (anchorKey !== prevAnchorKey) {
    setPrevAnchorKey(anchorKey)
    setTimeRangeTouched(false)
  }

  // The window the query actually searches. Untouched by the user, an anchor
  // opens its 15-minute lead-in; once the user drives the time filter, their
  // choice wins outright.
  const userStartTime = timeRangeTouched ? timeRange.startTime : undefined
  const anchorLeadStart =
    anchor && !timeRangeTouched ? anchor.timestamp - ANCHOR_CONTEXT_BEFORE_MS : undefined
  const windowStart = userStartTime ?? anchorLeadStart
  const windowEnd = timeRangeTouched ? timeRange.endTime : undefined

  // Backward expansion applies exactly when the window's start is
  // system-chosen: a user-set start is a hard floor, and no start at all means
  // the window already reaches the beginning of history.
  const expandable = anchorLeadStart != null

  // The floor hint for expansion — min firstEventTimestamp over the streams in
  // view: one DescribeLogStreams instead of probing history with empty range
  // scans. Best-effort in real AWS, so it schedules the final chunk rather
  // than clipping it (see logsFilterInfiniteQueryOptions).
  const streamsQuery = useQuery({ ...logsStreamsQueryOptions(groupName), enabled: expandable })
  const streamsData = streamsQuery.data
  const historyFloor = useMemo(() => {
    if (!expandable || !streamsData) return undefined
    const inView = streamName
      ? streamsData.filter((s) => s.logStreamName === streamName)
      : streamsData
    const known = inView
      .map((s) => s.firstEventTimestamp)
      .filter((t): t is number => t != null && t > 0)
    return known.length > 0 ? Math.min(...known) : undefined
  }, [expandable, streamsData, streamName])

  // Paged, both ways: FilterLogEvents caps a page at 10,000 events, so one
  // page read as "the results" silently truncated everything past the cap.
  // Forward pages advance through the time window on nextTokens; previous
  // pages are backward time-window chunks (the API pages forward only, so
  // "older" means widening the window's start).
  const {
    data,
    error,
    isError,
    isLoading,
    isFetching,
    refetch,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
    hasPreviousPage,
    isFetchingPreviousPage,
    fetchPreviousPage,
  } = useInfiniteQuery({
    ...logsFilterInfiniteQueryOptions(
      groupName,
      {
        filterPattern: activeFilter || undefined,
        startTime: windowStart,
        endTime: windowEnd,
        ...(streamName ? { logStreamNames: [streamName] } : {}),
      },
      { enabled: expandable, floor: historyFloor },
    ),
  })

  // The buffer resets itself when the group, stream or filter changes; the
  // time range is not part of the session's identity, so a range change has to
  // reset by hand along with the cleared-through cut.
  const tail = useLogTailBuffer({
    enabled: tailMode,
    groupIdentifier: groupName,
    streamName,
    filterPattern: activeFilter,
  })
  // Ticking Tail must also pull in what was logged between the page fetch and
  // the session opening — a session only pushes what is written after it
  // opens, and the page is as old as the visit. Once per opened session, a
  // FilterLogEvents read runs from the newest event on screen up to now and
  // lands as a third source beside the pages and the live buffer (see
  // use-tail-catch-up.ts for why the ordering leaves no seam).
  const newestOnScreenRef = useRef<number | undefined>(undefined)
  const catchUp = useTailCatchUp({
    // Held back until the page is in: with nothing on screen the read would
    // start from the beginning of the window — the very read in flight.
    openSession: isLoading ? null : tail.openSession,
    groupName,
    streamName,
    filterPattern: activeFilter,
    // The newest event on screen from any source, else the cleared-through
    // cut (everything before it is hidden anyway), else the window's start.
    since: () => newestOnScreenRef.current ?? clearedThrough ?? windowStart,
  })
  const clearTail = tail.clear
  const resetCatchUp = catchUp.reset
  useEffect(() => {
    clearTail()
    resetCatchUp()
    setClearedThrough(null)
  }, [clearTail, resetCatchUp, groupName, streamName, activeFilter, windowStart, windowEnd])

  // Ascending is the canonical order and the other direction is its mirror.
  // Each page sorts once on first sight (cached on the page object itself, so
  // a backward prepend never re-sorts what was already loaded) and the pages
  // merge linearly — never a fresh sort of the world per prepend or per batch
  // of live events, which is what this replaced.
  const ascendingFetched = useMemo(() => {
    const pages = data?.pages ?? []
    if (pages.length === 0) return NO_EVENTS
    return pages.map(sortedPageEvents).reduce((merged, page) => mergeSortedEvents(merged, page))
  }, [data])
  // The catch-up read starts at the newest fetched event (inclusive), so it
  // overlaps the pages by at least that one — reconciled by count, like the
  // live buffer below. From here on "stored" means pages plus catch-up.
  const ascendingStored = useMemo(
    () =>
      catchUp.events.length === 0
        ? ascendingFetched
        : mergeSortedEvents(
            ascendingFetched,
            dropTailedDuplicates(ascendingFetched, catchUp.events),
          ),
    [ascendingFetched, catchUp.events],
  )
  const visibleFetched = useMemo(
    () =>
      clearedThrough == null
        ? ascendingStored
        : ascendingStored.filter((evt) => (evt.timestamp ?? 0) > clearedThrough),
    [ascendingStored, clearedThrough],
  )
  const clearedCount = ascendingStored.length - visibleFetched.length

  // A refetch while tailing re-reads events the open session has already
  // pushed, so the two sources are reconciled rather than concatenated.
  // Clearing the tail buffer on every refetch would do it too, but at the cost
  // of dropping whatever arrived after the refetch's snapshot was taken.
  //
  // Reconciled against the same list it is concatenated with, so what is on
  // screen is what was counted. Clear empties the tail buffer as it hides the
  // fetched rows, so the two lists never disagree about an event it hid.
  const visibleTailed = useMemo(
    () => dropTailedDuplicates(visibleFetched, tail.events),
    [visibleFetched, tail.events],
  )
  const ascending = useMemo(
    () => mergeSortedEvents(visibleFetched, visibleTailed),
    [visibleFetched, visibleTailed],
  )
  const events = useMemo(
    () => (sortAsc ? ascending : [...ascending].reverse()),
    [ascending, sortAsc],
  )
  // Where the next catch-up read starts. A ref written from an effect: the
  // newest edge moves with every live event, and as state it would re-run
  // the read's effect on each one.
  useEffect(() => {
    newestOnScreenRef.current = ascending.at(-1)?.timestamp
  }, [ascending])

  // Row metadata is derived per event and cached on the event itself, so a
  // tail that has accumulated history never re-reads it: only the event that
  // just arrived is new work. See `describeLogEvent`.
  const virtualizer = useVirtualizer({
    count: events.length,
    getScrollElement: () => parentRef.current,
    // Collapsed rows are FIXED-height — a constant, not an estimate: they skip
    // the measurement pass entirely (no `measureElement` ref), which is what
    // makes collapse mode cheap. Expanded rows still estimate and measure.
    estimateSize: (index) => {
      const evt = events[index]
      if (collapseMode && !expandedKeys.has(logEventKey(evt))) return COLLAPSED_ROW_HEIGHT
      return estimateRowHeight(describeLogEvent(evt).plain, formatted)
    },
    // Wider than the old 15 on purpose: rows mount as a handful of nodes while
    // scrolling (see `useScrollSettled`), so overscan headroom is nearly free
    // and it is what keeps fast flings showing rows instead of blank track.
    overscan: 30,
    // Keyed by event, not by index: a sort-order flip or a newly fetched page
    // shifts every index, and index keys would remount every row with it.
    getItemKey: (index) => logEventKey(events[index]),
  })

  // Mid-scroll, rows mount light: no syntax-highlight spans, no action
  // buttons — the things every document observer (extension form-walkers,
  // the accessibility tree) pays for per node. Each row hydrates itself once
  // the scroll settles AND it sits near the viewport (`nearViewport` — far
  // overscan rows hydrating all at once is what froze a Format-mode stream
  // of pretty-printed documents). The flag's flips already re-render this
  // component via the virtualizer's onChange.
  const scrolling = virtualizer.isScrolling

  // Entering or leaving collapse mode — and expanding or collapsing one row —
  // invalidates sizes the virtualizer measured under the previous rendering,
  // and a row that is now collapsed will never re-measure (by design). Drop
  // the measurement cache so collapsed rows take the constant and expanded
  // rows re-measure on mount. One-off per toggle, not a per-frame cost.
  useEffect(() => {
    virtualizer.measure()
  }, [virtualizer, collapseMode, expandedKeys])

  // When a row above the viewport is measured and turns out taller or shorter
  // than its estimate, shift the scroll position by the difference so what the
  // user is looking at does not move — essential once backward chunks put
  // estimated rows above the fold. An instance property rather than an option
  // in this version of the virtualizer (its docs mark it experimental), hence
  // the assignment; it is read on each measurement, so an effect is early
  // enough. Same pattern as the log peek's prepend anchoring.
  useLayoutEffect(() => {
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) =>
      item.start < (instance.scrollOffset ?? 0)
  }, [virtualizer])

  // Fetch the following page as the user nears the edge where it will attach —
  // the bottom under Oldest-first, the top under Newest-first. The fetched
  // pages advance toward newer events either way. The token-keyed re-arming
  // this viewer pioneered now lives in the shared hook (see its module docs
  // for the stall race it prevents).
  const virtualItems = virtualizer.getVirtualItems()
  const nextPageToken = data?.pages.at(-1)?.nextToken
  useLoadMoreAtEdge({
    firstIndex: virtualItems[0]?.index,
    lastIndex: virtualItems.at(-1)?.index,
    count: events.length,
    edge: sortAsc ? "end" : "start",
    threshold: 25,
    nextPageToken,
    isFetchingNextPage,
    fetchNextPage,
  })

  /** Compiled once per filter, not once per row per scroll frame. */
  const filterMatcher = useMemo(() => compileFilterHighlighter(activeFilter), [activeFilter])

  // ── Anchored arrival ─────────────────────────────────────────────────────
  // A search result deep-links here with an event to centre on; a
  // jump-to-timestamp arrives the same way, minus the signature. Find the
  // event, scroll to it once, and keep an exact match visibly marked; if it
  // sits beyond the loaded pages, keep paging until it turns up — the anchor
  // is what the user came for, not the top of the window. Without a signature
  // and without an event on the exact millisecond, the first event at-or-after
  // the timestamp is centred instead — but only an exact match wears the
  // persistent mark, because "this is the one" must never be claimed of a
  // neighbour.
  const anchorMatch = useMemo(() => {
    if (!anchor) return { ascendingIndex: -1, exact: false }
    const exactIndex = ascending.findIndex(
      (evt) =>
        evt.timestamp === anchor.timestamp &&
        (!anchor.signature || logEventSignature(evt) === anchor.signature),
    )
    if (exactIndex >= 0) return { ascendingIndex: exactIndex, exact: true }
    if (anchor.signature) return { ascendingIndex: -1, exact: false }
    // Loaded pages are a prefix of the window's ascending order, so the first
    // loaded event at-or-after the timestamp is the first one overall.
    const nearestIndex = ascending.findIndex((evt) => (evt.timestamp ?? 0) >= anchor.timestamp)
    return { ascendingIndex: nearestIndex, exact: false }
  }, [ascending, anchor])
  const anchorIndex =
    anchorMatch.ascendingIndex < 0
      ? -1
      : sortAsc
        ? anchorMatch.ascendingIndex
        : events.length - 1 - anchorMatch.ascendingIndex
  const anchorExact = anchorMatch.exact

  const pendingAnchorKey = anchorKey != null && settledAnchorKey !== anchorKey ? anchorKey : null
  const anchorPending = pendingAnchorKey != null
  useEffect(() => {
    if (pendingAnchorKey == null) return
    if (anchorIndex < 0) {
      if (hasNextPage && !isFetchingNextPage) {
        void fetchNextPage()
      } else if (!hasNextPage && !isLoading) {
        // Paged to the end and never found it — settle so the rest of the
        // view (backward expansion included) stops waiting on the anchor.
        setSettledAnchorKey(pendingAnchorKey)
      }
      return
    }
    setSettledAnchorKey(pendingAnchorKey)
    pinnedToLatestRef.current = false
    virtualizer.scrollToIndex(anchorIndex, { align: "center" })
    // Estimated row heights put the first landing near, not on, centre;
    // repeat once after this frame's measurements settle.
    const settle = requestAnimationFrame(() =>
      virtualizer.scrollToIndex(anchorIndex, { align: "center" }),
    )
    return () => cancelAnimationFrame(settle)
  }, [
    pendingAnchorKey,
    anchorIndex,
    hasNextPage,
    isFetchingNextPage,
    isLoading,
    fetchNextPage,
    virtualizer,
  ])

  // ── Backward expansion ───────────────────────────────────────────────────
  // Load older events as the user nears the oldest edge of loaded data — the
  // top under Oldest-first, the bottom under Newest-first. Same re-arming
  // trick as the forward edge (see use-load-more-at-edge), keyed on the
  // oldest page's param (a fresh chunk object per backward page) — but
  // deliberately NOT the shared hook: the prepend snapshot below must be
  // captured atomically with scheduling the fetch, and modelling a
  // before-fetch callback in the hook would cost more than this duplication.
  // The isFetchingPreviousPage guard keeps exactly one backward window in
  // flight. Held back while an anchored arrival is still paging toward its
  // event, so the two never compete.
  const oldestPageParam = data?.pageParams[0]
  const nearOldestEdge = sortAsc
    ? (virtualItems[0]?.index ?? Number.MAX_SAFE_INTEGER) <= 25
    : (virtualItems.at(-1)?.index ?? 0) >= events.length - 25
  const prependSnapshotRef = useRef<{
    eventCount: number
    /** Key of the first rendered row — the content the user was looking at. */
    anchorRowKey: React.Key | null
    /** How far the viewport top sat below that row's start. */
    anchorDelta: number
  } | null>(null)
  useEffect(() => {
    if (!hasPreviousPage || isFetchingPreviousPage || !nearOldestEdge || anchorPending) return
    // Under Oldest-first the prepend grows the list above the viewport, so
    // anchor on the first rendered row's *key*, not a pixel offset: the new
    // rows enter as height estimates, and an offset restore would land the
    // accumulated estimate error right in the viewport. The key survives the
    // prepend, so the restore puts that exact row back where it was and
    // leaves the estimate error above the fold, where the virtualizer's own
    // scroll adjustment corrects it row by row. (Under Newest-first the older
    // rows attach below the viewport and nothing on screen moves.)
    if (sortAsc) {
      const firstItem = virtualizer.getVirtualItems().at(0)
      prependSnapshotRef.current = {
        eventCount: events.length,
        anchorRowKey: firstItem?.key ?? null,
        anchorDelta: firstItem ? (virtualizer.scrollOffset ?? 0) - firstItem.start : 0,
      }
    }
    void fetchPreviousPage({ cancelRefetch: false })
  }, [
    oldestPageParam,
    hasPreviousPage,
    isFetchingPreviousPage,
    nearOldestEdge,
    anchorPending,
    sortAsc,
    events.length,
    fetchPreviousPage,
    virtualizer,
  ])

  useLayoutEffect(() => {
    const snapshot = prependSnapshotRef.current
    if (!snapshot || isFetchingPreviousPage) return
    prependSnapshotRef.current = null
    if (events.length <= snapshot.eventCount || snapshot.anchorRowKey == null) return
    const restoreIndex = events.findIndex((evt) => logEventKey(evt) === snapshot.anchorRowKey)
    if (restoreIndex < 0) return
    const [offset] = virtualizer.getOffsetForIndex(restoreIndex, "start") ?? [null]
    if (offset != null) virtualizer.scrollToOffset(offset + snapshot.anchorDelta)
  }, [events, isFetchingPreviousPage, virtualizer])

  // Scroll-to-bottom
  const [showScrollBottom, setShowScrollBottom] = useState(false)
  const handleScrollCheck = useCallback(() => {
    const el = parentRef.current
    if (!el) return
    const nearLatest = sortAsc
      ? el.scrollHeight - el.scrollTop - el.clientHeight < 80
      : el.scrollTop < 80
    pinnedToLatestRef.current = nearLatest
    setShowScrollBottom(!nearLatest && events.length > 20)
  }, [events.length, sortAsc])

  const scrollToBottom = useCallback(() => {
    virtualizer.scrollToIndex(sortAsc ? events.length - 1 : 0, { align: sortAsc ? "end" : "start" })
    pinnedToLatestRef.current = true
    setShowScrollBottom(false)
  }, [virtualizer, events.length, sortAsc])

  /**
   * Empty the view, leaving the tail running so only events from here on show.
   *
   * The cut is taken from the newest timestamp on screen rather than the wall
   * clock — the emulator stamps the events, and its clock is the only one that
   * decides which side of the line a record falls on.
   */
  const clearBuffer = useCallback(() => {
    setClearedThrough(
      events.reduce((newest, evt) => Math.max(newest, evt.timestamp ?? 0), clearedThrough ?? 0),
    )
    clearTail()
    pinnedToLatestRef.current = true
    setShowScrollBottom(false)
  }, [events, clearedThrough, clearTail])

  const restoreCleared = useCallback(() => setClearedThrough(null), [])

  const toggleExpandedKey = useCallback((key: number) => {
    setExpandedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  // One handler for every row (the key rides the DOM, not a per-row closure):
  // in collapse mode a click expands just that row to the full current
  // rendering, and a second click folds it back to its single line.
  const handleRowToggle = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // A click that was really a text selection, or that landed on the row's
      // own controls (Copy, links), is not an expand request.
      if (window.getSelection()?.toString()) return
      if ((e.target as Element).closest("button, a")) return
      const key = Number(e.currentTarget.dataset.rowKey)
      if (!Number.isFinite(key)) return
      toggleExpandedKey(key)
    },
    [toggleExpandedKey],
  )

  // Click-to-filter on a request id: the id rides the button's dataset (one
  // handler, no per-row closures) and lands in the filter QUOTED — the
  // phrase-match form FilterLogEvents already defines, exactly what typing it
  // would do. No new query semantics; the server does the matching.
  const handleRequestIdFilter = useCallback((e: React.MouseEvent<HTMLButtonElement>) => {
    e.stopPropagation()
    const id = e.currentTarget.dataset.requestId
    if (!id) return
    const quoted = `"${id}"`
    setFilterInput(quoted)
    setActiveFilter(quoted)
  }, [])

  // ── Keyboard navigation ──────────────────────────────────────────────────
  // Scoped to the scroll container (which is focusable), never the document:
  // the filter input keeps its keys, and Ctrl/Cmd+F keeps its document-level
  // shortcut because modified keys pass straight through.
  const { copy: copyPlainMessage } = useCopyToClipboard()
  const handleListKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return
    // Typing surfaces own their keys outright, wherever one ends up inside
    // the list.
    if ((e.target as Element).closest("input, textarea, select, [contenteditable]")) return
    if (events.length === 0) return

    const { key } = e
    if (key === "ArrowDown" || key === "j" || key === "ArrowUp" || key === "k") {
      e.preventDefault()
      const delta = key === "ArrowDown" || key === "j" ? 1 : -1
      const currentIndex =
        focusedKey == null ? -1 : events.findIndex((evt) => logEventKey(evt) === focusedKey)
      // No cursor yet (or its event left the list): start at the first
      // rendered row — what the user is looking at — not at index 0.
      const nextIndex =
        currentIndex < 0
          ? (virtualItems[0]?.index ?? 0)
          : Math.max(0, Math.min(events.length - 1, currentIndex + delta))
      setFocusedKey(logEventKey(events[nextIndex]))
      // A cursor the user is steering must not be yanked away by the next
      // tail arrival's scroll-to-latest; reaching the newest edge re-pins.
      pinnedToLatestRef.current = false
      // `auto`: scroll only if the row is out of view, never a recentring —
      // and via the virtualizer, so a cursor past the rendered window works.
      virtualizer.scrollToIndex(nextIndex, { align: "auto" })
      return
    }
    if (key === "Enter") {
      if (!collapseMode || focusedKey == null) return
      e.preventDefault()
      toggleExpandedKey(focusedKey)
      return
    }
    if (key === "c") {
      if (focusedKey == null) return
      const evt = events.find((candidate) => logEventKey(candidate) === focusedKey)
      if (!evt) return
      // The same value the row's copy button yields — the plain message.
      copyPlainMessage(describeLogEvent(evt).plain, { noun: "log message" })
    }
  }

  useLayoutEffect(() => {
    if (events.length <= previousEventCountRef.current) {
      previousEventCountRef.current = events.length
      return
    }

    previousEventCountRef.current = events.length
    if (!tailMode || !pinnedToLatestRef.current) return
    virtualizer.scrollToIndex(sortAsc ? events.length - 1 : 0, { align: sortAsc ? "end" : "start" })
  }, [events.length, sortAsc, tailMode, virtualizer])

  const handleSearch = () => setActiveFilter(filterInput)
  const handleClear = () => {
    setFilterInput("")
    setActiveFilter("")
  }

  // Jump-to-timestamp rides the anchor machinery: same route, new anchorTs,
  // no signature — so the view repositions on the first event at-or-after the
  // chosen time with exactly the fetches an anchored arrival makes.
  const handleJump = useCallback(
    (timestamp: number) => {
      if (streamName) {
        void navigate({
          to: "/cloudwatch/logs/stream",
          search: { groupName, streamName, anchorTs: timestamp },
        })
      } else {
        void navigate({
          to: "/cloudwatch/logs/events",
          search: { groupName, anchorTs: timestamp },
        })
      }
    },
    [navigate, groupName, streamName],
  )

  // Keyboard shortcut: Ctrl/Cmd+F focuses filter, Escape clears
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "f") {
        e.preventDefault()
        const input = document.querySelector<HTMLInputElement>("[data-log-filter]")
        input?.focus()
      }
    }
    document.addEventListener("keydown", handler)
    return () => document.removeEventListener("keydown", handler)
  }, [])

  const title = streamName ?? groupName
  const description = streamName ? (
    <>
      Log group:{" "}
      <Link
        to="/cloudwatch/logs/$groupName"
        params={{ groupName }}
        className="font-mono text-accent hover:underline"
      >
        {groupName}
      </Link>
    </>
  ) : (
    "All streams in this log group"
  )
  // A ResourceNotFoundException is an answer, not an absence: the group (or
  // stream) does not exist in the region the console points at. Real AWS's
  // console shows a not-found error here, and rendering the "no events yet"
  // empty state instead reads as "your app never logged" — the wrong-region
  // trap. Which resource is missing comes from the server's message, because
  // a stream URL can 404 on either: the group as a whole, or just the stream.
  const { region } = useEndpoint()
  const errorMessage = error instanceof Error ? error.message : undefined
  const notFound = isResourceNotFound(error)
  const notFoundState =
    streamName != null && /log stream/i.test(errorMessage ?? "")
      ? {
          title: "Log stream not found",
          description: `Log group "${groupName}" has no stream named "${streamName}" in ${region}.`,
        }
      : {
          title: "Log group not found",
          description: `No log group named "${groupName}" exists in ${region} — it may live in a different region. Check the region selector.`,
        }

  // An emptied buffer is not an empty log group, and saying so would read as a
  // bug in whatever the user is debugging.
  const emptyState =
    clearedThrough !== null
      ? {
          title: "Buffer cleared",
          description: tailMode
            ? "Waiting for new events — anything from before the clear is hidden."
            : clearedCount > 0
              ? "Turn on Tail to watch for new events, or show the earlier ones again."
              : "Turn on Tail to watch for new events.",
        }
      : activeFilter
        ? { title: "No matching events", description: "Try a different filter pattern." }
        : { title: "No log events", description: "This stream has no events yet." }

  // The oldest edge must say what it is, symmetric with "End of logs" at the
  // newest: empty space beyond the first row reads the same whether history
  // is exhausted, an older window is still loading, or the time filter cut it
  // off. It sits at the top under Oldest-first and at the bottom under
  // Newest-first — wherever the oldest edge actually is — and keeps a
  // constant height so its text changing never nudges the rows mid-restore.
  const oldestEdgeMarker = (
    <div className="flex h-7 shrink-0 items-center justify-center text-center font-mono text-2xs text-fg-muted">
      {isFetchingPreviousPage
        ? "Loading older events…"
        : hasPreviousPage
          ? // More history is reachable; nearing this edge loads it.
            null
          : userStartTime != null
            ? "Start of range — the time filter begins here; widen it to search earlier events"
            : "Start of logs"}
    </div>
  )

  return (
    <div className="flex h-full w-full flex-col gap-3">
      <PageHeader
        title={title}
        description={description}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                navigate({
                  to: "/cloudwatch/logs/group",
                  search: { groupName },
                })
              }
            >
              <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
              {streamName ? "Back to Streams" : "Back to Group"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              // Refresh means "reload this window of logs" — honouring a cut
              // taken before it would hand back an empty screen.
              onClick={() => {
                restoreCleared()
                void refetch()
              }}
              disabled={isFetching}
            >
              <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
            </Button>
          </div>
        }
      />

      {/* Toolbar: filter + toggles */}
      <div className="flex flex-wrap items-center gap-2">
        {/* Filter bar */}
        <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-border bg-bg-muted px-3 py-2">
          <Search className="h-4 w-4 shrink-0 text-fg-muted" />
          <Input
            data-log-filter
            // A log filter is never a credential field. Password managers scan
            // the page's inputs on every DOM change, and this view mutates a
            // lot; opting out keeps their field analysis off the hot path.
            data-1p-ignore
            data-lpignore="true"
            className="h-7 border-0 bg-transparent px-1 shadow-none focus-visible:ring-0"
            placeholder='Filter — e.g. ERROR, "request failed", ERROR timeout'
            value={filterInput}
            onChange={(e) => setFilterInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSearch()
              if (e.key === "Escape") handleClear()
            }}
          />
          {(filterInput || activeFilter) && (
            <Button size="sm" variant="ghost" onClick={handleClear} className="h-7 px-2">
              <X className="h-3.5 w-3.5" />
            </Button>
          )}
          <div className="mx-0.5 h-5 w-px bg-border" />
          <TimeRangeFilter
            value={timeRange}
            utc={utc}
            onChange={(range) => {
              setTimeRange(range)
              setTimeRangeTouched(true)
            }}
          />
          <JumpToTimestamp onJump={handleJump} />
          <Button
            size="sm"
            onClick={handleSearch}
            disabled={isFetching || !filterInput.trim()}
            className="h-7"
          >
            {isFetching ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
            Search
          </Button>
          {activeFilter && (
            <span className="ml-1 shrink-0 text-xs text-fg-muted">
              {events.length}
              {hasNextPage ? "+" : ""} result{events.length !== 1 || hasNextPage ? "s" : ""}
            </span>
          )}
        </div>

        {/* View toggles */}
        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            type="button"
            size="sm"
            variant={displayMode === "table" ? "default" : "ghost"}
            onClick={() => setPref("displayMode", "table")}
            className="h-7 px-2 text-2xs uppercase"
          >
            Table
          </Button>
          <Button
            type="button"
            size="sm"
            variant={displayMode === "plain" ? "default" : "ghost"}
            onClick={() => setPref("displayMode", "plain")}
            className="h-7 px-2 text-2xs uppercase"
          >
            Plaintext
          </Button>
          <label className="flex cursor-pointer items-center gap-1.5 rounded border border-border px-2 py-1.5 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10">
            <input
              type="checkbox"
              checked={formatted}
              onChange={(e) => setPref("formatted", e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Format
          </label>
          <label className="flex cursor-pointer items-center gap-1.5 rounded border border-border px-2 py-1.5 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10">
            <input
              type="checkbox"
              checked={syntaxHighlight}
              onChange={(e) => setPref("syntaxHighlight", e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Syntax
          </label>
          <label className="flex cursor-pointer items-center gap-1.5 rounded border border-border px-2 py-1.5 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10">
            <input
              type="checkbox"
              checked={wrapLines}
              onChange={(e) => setPref("wrapLines", e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Wrap
          </label>
          <label
            className="flex cursor-pointer items-center gap-1.5 rounded border border-border px-2 py-1.5 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10"
            title="Show every row as one line — click a row to expand it"
          >
            <input
              type="checkbox"
              checked={collapseMode}
              onChange={(e) => setPref("collapsed", e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Collapse
          </label>
          <Button
            type="button"
            size="sm"
            variant={utc ? "default" : "ghost"}
            onClick={() => setPref("utc", !utc)}
            className="h-7 px-2 text-2xs uppercase"
            title={
              utc
                ? "Timestamps are shown in UTC — switch to local time"
                : "Timestamps are shown in local time — switch to UTC"
            }
          >
            UTC
          </Button>
          <label
            className="flex cursor-pointer items-center gap-1.5 rounded border border-border px-2 py-1.5 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10"
            title="Show the time elapsed since the previous event beside each timestamp"
          >
            <input
              type="checkbox"
              checked={showDeltas}
              onChange={(e) => setPref("showDeltas", e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Deltas
          </label>
          <Button
            type="button"
            size="sm"
            variant={tailMode ? "default" : "ghost"}
            onClick={() => setTailMode((v) => !v)}
            className="h-7 px-2 text-2xs uppercase"
            title={
              tail.status === "error"
                ? "The live tail session ended unexpectedly — toggle to reconnect"
                : "Live tail streams new events as they arrive"
            }
          >
            {tail.status === "idle" ? (
              <Zap className="mr-1 h-3 w-3" />
            ) : (
              <LiveTailIndicator status={tail.status} className="mr-1.5" />
            )}
            Tail
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={clearBuffer}
            disabled={events.length === 0}
            className="h-7 px-2 text-2xs uppercase"
            title="Clear the events on screen — the tail keeps running, so only newer events appear"
          >
            <Eraser className="mr-1 h-3 w-3" />
            Clear
          </Button>
          {clearedCount > 0 && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={restoreCleared}
              className="h-7 px-2 text-2xs uppercase"
              title="Bring the cleared events back"
            >
              <Undo2 className="mr-1 h-3 w-3" />
              Show {clearedCount.toLocaleString()} earlier
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => setPref("sortAsc", !sortAsc)}
            className="h-7 px-2 text-2xs uppercase"
          >
            <ArrowDownUp className="mr-1 h-3 w-3" />
            {sortAsc ? "Oldest" : "Newest"}
          </Button>
          <ExportMenu events={events} hasMore={!!hasNextPage} baseName={streamName ?? groupName} />
          <span
            className="ml-1 font-mono text-2xs text-fg-muted tabular-nums"
            // "+" while a nextToken remains: the server caps a page at 10,000
            // events, and a bare count would read as the whole result set.
            title={
              hasNextPage
                ? "More events are available — scroll to the newest to load them"
                : undefined
            }
          >
            {events.length.toLocaleString()}
            {hasNextPage ? "+" : ""} event{events.length !== 1 || hasNextPage ? "s" : ""}
          </span>
          {tail.overflowed > 0 && (
            // The AWS console shows "% displayed" when Live Tail samples; the
            // same honesty here — a capped buffer must say it dropped, not
            // quietly show a window and let it read as everything.
            <span
              className="font-mono text-2xs text-warning tabular-nums"
              title="The live tail buffer is full, so the oldest events were dropped from view. Narrow the filter to keep up with the stream."
            >
              {tail.overflowed.toLocaleString()} dropped
            </span>
          )}
        </div>
      </div>

      {/* Log content */}
      {isLoading ? (
        <div className="flex justify-center py-16">
          <Spinner className="h-6 w-6" />
        </div>
      ) : notFound ? (
        <EmptyState
          icon={<AlertTriangle className="h-10 w-10" />}
          title={notFoundState.title}
          description={notFoundState.description}
        />
      ) : isError && events.length === 0 ? (
        // Any other failure with nothing on screen: an error rendered as the
        // ordinary empty state would read as an empty stream. With rows
        // already loaded the rows stay — a failed refetch is a toolbar
        // concern, not a reason to blank the screen.
        <EmptyState
          icon={<AlertTriangle className="h-10 w-10" />}
          title="Failed to load log events"
          description={errorMessage ?? "Please try again."}
        />
      ) : events.length === 0 ? (
        <EmptyState
          icon={<FileText className="h-10 w-10" />}
          title={emptyState.title}
          description={emptyState.description}
        />
      ) : (
        <div className="relative min-h-0 flex-1">
          {displayMode === "table" && (
            <div className="flex border-b border-border bg-bg-elevated px-1 py-1.5 text-2xs font-medium text-fg-muted">
              <div className="w-10 shrink-0 px-1 text-center">#</div>
              <div className={cn("shrink-0 px-1", showDeltas ? "w-36" : "w-20")}>Time</div>
              {!streamName && <div className="w-44 shrink-0 px-1">Stream</div>}
              <div className="min-w-0 flex-1 px-1">Message</div>
            </div>
          )}

          {/* Virtualized rows. Focusable for keyboard row navigation; the
              cursor is announced through aria-activedescendant so focus (and
              the scroll position) stays on the container itself. */}
          <div
            ref={parentRef}
            tabIndex={0}
            role="listbox"
            aria-label="Log events"
            aria-activedescendant={focusedKey != null ? `log-event-${focusedKey}` : undefined}
            // Advisory opt-outs for password managers and autofill scanners,
            // on the whole scroller: log rows are never fillable, and these
            // walkers were 43% of a profiled main thread (§3e of the plan
            // doc). Documented per-field; whether a given walker prunes the
            // subtree is its call — free to declare, measured by re-tracing.
            data-1p-ignore=""
            data-lpignore="true"
            data-form-type="other"
            onKeyDown={handleListKeyDown}
            className="min-h-0 flex-1 overflow-auto focus-visible:ring-1 focus-visible:ring-accent focus-visible:outline-none focus-visible:ring-inset"
            onScroll={handleScrollCheck}
            style={{ height: "calc(100vh - 280px)" }}
          >
            {sortAsc && oldestEdgeMarker}
            <div
              style={{
                height: `${virtualizer.getTotalSize()}px`,
                width: "100%",
                position: "relative",
              }}
            >
              {virtualItems.map((virtualRow) => {
                const evt = events[virtualRow.index]
                const meta = describeLogEvent(evt)
                // Exact matches only: a jump's nearest-match neighbour gets
                // centred, never marked as the event the link meant.
                const isAnchor = anchorExact && anchorIndex >= 0 && virtualRow.index === anchorIndex
                const rowKey = logEventKey(evt)
                const rowCollapsed = collapseMode && !expandedKeys.has(rowKey)
                const rowDefer = scrolling || !nearViewport(virtualRow, virtualizer)
                const isFocused = focusedKey != null && focusedKey === rowKey
                const deepLink = eventDeepLink(groupName, evt)
                // The chronological predecessor, by index in the display
                // order: the row above under Oldest-first, the row below
                // under Newest-first. O(1) per row, derived from the same
                // array the row came from — never a second pass or a stored
                // parallel list.
                const prevIndex = sortAsc ? virtualRow.index - 1 : virtualRow.index + 1
                const prevTimestamp =
                  prevIndex >= 0 && prevIndex < events.length
                    ? events[prevIndex].timestamp
                    : undefined
                return (
                  <div
                    key={virtualRow.key}
                    data-index={virtualRow.index}
                    // A stable id per event, for aria-activedescendant.
                    id={`log-event-${rowKey}`}
                    role="option"
                    aria-selected={isFocused}
                    // The event's own key, straight from `logEventKey`:
                    // `virtualRow.key` is the same value in production, but
                    // the expansion set must match what `estimateSize` checks
                    // even where the virtualizer is mocked.
                    data-row-key={rowKey}
                    data-anchored={isAnchor || undefined}
                    data-focused={isFocused || undefined}
                    // Collapsed rows are fixed-height and never measured —
                    // skipping `measureElement` is what removes the
                    // measurement churn in collapse mode. Expanded rows keep
                    // the dynamic pipeline.
                    ref={rowCollapsed ? undefined : virtualizer.measureElement}
                    onClick={collapseMode ? handleRowToggle : undefined}
                    className={cn(
                      "group/row absolute top-0 left-0 flex w-full border-b border-l-2 border-border-muted border-l-transparent",
                      collapseMode && cn("cursor-pointer", rowCollapsed && "overflow-hidden"),
                      // The keyboard cursor: an inset ring, distinct from the
                      // anchor's filled left-edge marking below.
                      isFocused && "ring-1 ring-accent/70 ring-inset",
                      // The anchored row is the one the deep link came for; its
                      // marking outranks the level tint.
                      isAnchor
                        ? "border-l-accent bg-accent-muted"
                        : meta.level && logLevelRowClass[meta.level],
                    )}
                    style={{
                      transform: `translateY(${virtualRow.start}px)`,
                      ...(rowCollapsed ? { height: `${COLLAPSED_ROW_HEIGHT}px` } : {}),
                    }}
                  >
                    {displayMode === "table" ? (
                      <>
                        <div className="flex w-10 shrink-0 items-start justify-center pt-1.5 font-mono text-2xs text-fg-muted/40 tabular-nums select-none">
                          {virtualRow.index + 1}
                        </div>
                        <div
                          className={cn(
                            "flex shrink-0 items-start gap-1.5 px-1 pt-1.5 font-mono text-2xs text-fg-muted tabular-nums",
                            showDeltas ? "w-36" : "w-20",
                          )}
                        >
                          {formatLogTime(evt.timestamp, utc)}
                          {/* The gap to the previous event — display-only
                              annotation beside the real timestamp, never in
                              place of it. */}
                          {showDeltas && evt.timestamp != null && prevTimestamp != null && (
                            <span className="text-fg-muted/60">
                              {formatLogDelta(evt.timestamp - prevTimestamp)}
                            </span>
                          )}
                        </div>
                        {!streamName && (
                          // Truncated, not just narrow: a Lambda stream name is
                          // wider than `w-44` at this size and the column has no
                          // overflow of its own, so it used to spill across the
                          // message — over the level badge first.
                          <div
                            className="w-44 shrink-0 truncate px-1 pt-1.5 font-mono text-2xs text-fg-muted"
                            title={evt.logStreamName}
                          >
                            {evt.logStreamName}
                          </div>
                        )}
                        <div className="min-w-0 flex-1 px-1 py-1.5">
                          <LogMessage
                            message={evt.message ?? ""}
                            summary={meta.summary}
                            formatted={formatted}
                            syntaxHighlight={syntaxHighlight}
                            wrapLines={wrapLines}
                            filterMatcher={filterMatcher}
                            level={meta.level}
                            collapsed={rowCollapsed}
                            defer={rowDefer}
                          />
                        </div>
                      </>
                    ) : (
                      <div className="min-w-0 flex-1 px-2 py-1.5">
                        <LogMessage
                          prefix={`${formatLogTime(evt.timestamp, utc)}${evt.logStreamName ? ` ${evt.logStreamName}` : ""}`}
                          message={evt.message ?? ""}
                          summary={meta.summary}
                          formatted={formatted}
                          syntaxHighlight={syntaxHighlight}
                          wrapLines={wrapLines}
                          filterMatcher={filterMatcher}
                          level={meta.level}
                          hideLevel
                          collapsed={rowCollapsed}
                          defer={rowDefer}
                        />
                      </div>
                    )}
                    <RowActions
                      requestId={meta.requestId}
                      deepLink={deepLink}
                      plain={meta.plain}
                      onRequestIdFilter={handleRequestIdFilter}
                      defer={rowDefer}
                    />
                  </div>
                )
              })}
            </div>

            {/* The list's edge must say what it is: empty space below the last
                row reads the same whether everything is loaded, a page is
                still coming, or a tail is quietly waiting. The newest-edge
                marker is ascending only — under Newest-first the bottom is
                the oldest edge of the loaded window, so it carries the
                oldest-edge marker there instead (the mirrored reasoning). */}
            {sortAsc ? (
              <div className="py-2 text-center font-mono text-2xs text-fg-muted">
                {isFetchingNextPage
                  ? "Loading more events…"
                  : hasNextPage
                    ? null
                    : tailMode
                      ? tail.status === "error"
                        ? "Live tail disconnected — toggle Tail to reconnect"
                        : catchUp.loading
                          ? "Live tail — catching up on events since the last fetch…"
                          : "Live tail — watching for new events"
                      : "End of logs"}
              </div>
            ) : (
              oldestEdgeMarker
            )}
          </div>

          {/* Scroll to bottom FAB */}
          {showScrollBottom && (
            <button
              type="button"
              onClick={scrollToBottom}
              className="absolute right-4 bottom-4 z-10 flex items-center gap-1 rounded-full border border-border bg-bg-elevated px-3 py-1.5 font-mono text-2xs font-medium text-fg-muted shadow-lg transition-colors hover:border-accent hover:text-accent"
            >
              <ArrowDown className="h-3 w-3" />
              Latest
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// ── Row actions ──────────────────────────────────────────────────────────────

/**
 * The hover-revealed cluster on a row's right edge — request-id filter, copy
 * link, copy message.
 *
 * A component, not inline JSX, because the buttons are deliberately absent
 * from rows mounted mid-scroll: buttons are precisely what form-detection
 * walkers inspect (the Firefox trace behind this put 37% of the main thread
 * in 1Password's MutationObserver and 6% in Firefox's own autofill scanner,
 * fed by row churn), and the cluster is invisible until hover anyway. The
 * wrapper always renders so the row's layout never depends on settling; the
 * buttons hydrate on the first idle frame and then stay for the row's life.
 *
 * Memoised so scroll-flag flips re-render it to identical output: every prop
 * is a primitive or a `useCallback` handler.
 */
const RowActions = memo(function RowActions({
  requestId,
  deepLink,
  plain,
  onRequestIdFilter,
  defer,
}: {
  requestId: string | null
  deepLink: string | null
  plain: string
  onRequestIdFilter: (e: React.MouseEvent<HTMLButtonElement>) => void
  defer: boolean
}) {
  const settled = useScrollSettled(defer)
  return (
    <div className="flex w-16 shrink-0 items-start justify-end gap-0.5 pt-1.5 pr-1">
      {settled && (
        <>
          {requestId != null && (
            <button
              type="button"
              data-request-id={requestId}
              onClick={onRequestIdFilter}
              aria-label={`Filter to request ID ${requestId}`}
              // What it really does — a quoted-term search, the
              // same filter typing the id would run.
              title={`Filter to request ID ${requestId} — searches for it as a quoted term`}
              className="cursor-pointer p-0.5 text-fg-muted/40 opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg-muted"
            >
              <ListFilter aria-hidden className="h-3.5 w-3.5" />
            </button>
          )}
          {deepLink != null && (
            <CopyButton
              value={deepLink}
              noun="link to this event"
              tone="inline"
              icon={<Link2 aria-hidden className="h-3.5 w-3.5" />}
              className="p-0.5 text-fg-muted/40 opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg-muted"
            />
          )}
          <CopyButton
            value={plain}
            noun="log message"
            tone="inline"
            className="p-0.5 text-fg-muted/40 opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg-muted"
          />
        </>
      )}
    </div>
  )
})

// ── Log message cell ───────────────────────────────────────────────────────

// `LogMessage` and `LevelBadge` live in @/components/logs/log-message — the
// same pipeline renders the generic LogViewer's rows and the log-group search
// results, and copies of it had already started to drift once before.
