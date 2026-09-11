/**
 * LogStreamPeek — right-side slide-in panel for peering into a Lambda
 * instance's log stream and trigger event.
 *
 * - Logs tab: loads existing events via GetLogEvents, then opens a
 *   StartLiveTail session on the instance's log group + stream and appends
 *   what it pushes. History pages backward through the top sentinel; when the
 *   live session dies, a forward token walk (see `useForwardLogPages`) takes
 *   over at the bottom so events written after the death stay reachable.
 *   Rows render through the same pipeline as the CloudWatch stream viewer
 *   (`LogMessage`: level tint and badge, ANSI colour, a system log record's
 *   summary line, Format/Syntax/Wrap/Collapse) and share its persisted
 *   display preferences, so the peek reads like a window onto the full view
 *   rather than a wall of raw text — with a client-side filter over what is
 *   loaded, and a link to the full view for everything it does not do.
 * - Trigger Event tab: pretty-prints the JSON payload that triggered the
 *   invocation (as recorded by the instance tracker).
 */

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import * as Dialog from "@radix-ui/react-dialog"
import { infiniteQueryOptions, useInfiniteQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useVirtualizer } from "@tanstack/react-virtual"
import { X, FileText, Zap, ExternalLink, Search } from "lucide-react"
import { LiveTailIndicator } from "@/components/logs/live-tail-indicator"
import { LogMessage } from "@/components/logs/log-message"
import { CopyButton } from "@/components/ui/copy-button"
import { formatCount } from "@/lib/format"
import { describeLogEvent, formatLogTime, logLevelRowClass } from "@/lib/log-format"
import { cn } from "@/lib/utils"
import { logs } from "@/services/api"
import type { LogEvent } from "@/types"
import { useScrollTrigger } from "@/hooks/use-scroll-trigger"
import {
  compileFilterHighlighter,
  dropTailedDuplicates,
  logEventKey,
} from "@/features/cloudwatch/logs/tail"
import { useForwardLogPages } from "@/features/cloudwatch/logs/use-forward-log-pages"
import { useLogTailBuffer } from "@/features/cloudwatch/logs/use-log-tail-buffer"
import { useLogViewPrefs } from "@/features/cloudwatch/logs/use-log-view-prefs"
import { TriggerEventViewer } from "./trigger-event-viewer"

type Tab = "logs" | "trigger"

/**
 * A log stream to display in the peek panel.
 * Lambda instances produce one by converting their fields;
 * log group nodes produce one from the selected stream.
 */
export interface LogStreamTarget {
  /** Primary display label — function name, group name, etc. */
  title: string
  /** Secondary label — instance short ID, stream name, etc. */
  subtitle: string
  logGroup: string
  logStream: string
  /** Optional JSON trigger event (Lambda only). */
  triggerEvent?: string
}

function logStreamPeekQueryOptions(target: LogStreamTarget | null, enabled: boolean) {
  return infiniteQueryOptions({
    queryKey: ["logs", target?.logGroup, target?.logStream],
    queryFn: ({ pageParam }: { pageParam: string | undefined }) =>
      logs.getEvents(target!.logGroup, target!.logStream, {
        nextToken: pageParam,
        limit: 200,
        ...(pageParam == null ? { startFromHead: false } : {}),
      }),
    initialPageParam: undefined as string | undefined,
    // Each page gives us a backward token to fetch the page before it.
    getNextPageParam: (lastPage, _allPages, lastPageParam) => {
      if (lastPage.events.length === 0) return undefined
      const token = lastPage.nextBackwardToken
      return !token || token === lastPageParam ? undefined : token
    },
    enabled,
    // Disable stale refetching — SSE handles live tail updates.
    staleTime: Infinity,
  })
}

interface LogStreamPeekProps {
  target: LogStreamTarget | null
  onClose: () => void
}

export const LogStreamPeek = memo(function LogStreamPeek({ target, onClose }: LogStreamPeekProps) {
  const visible = target !== null
  const [activeTab, setActiveTab] = useState<Tab>("logs")

  // Reset the active tab when the panel closes.
  const prevTargetKey = useRef<string | null>(null)
  const targetKey = target ? `${target.logGroup}::${target.logStream}` : null
  if (targetKey !== prevTargetKey.current) {
    prevTargetKey.current = targetKey
    if (!target && activeTab !== "logs") setActiveTab("logs")
  }

  // Infinite query — first page fetches the latest events (startFromHead: false);
  // subsequent pages fetch older events via nextBackwardToken.
  const logQuery = useInfiniteQuery(
    logStreamPeekQueryOptions(
      target,
      !!target && activeTab === "logs" && !!target.logGroup && !!target.logStream,
    ),
  )

  // The live session, its per-frame batching and its bounded buffer. Keyed on
  // the stream itself rather than the target object: the map builds a fresh
  // target on every peek click, so an object dependency would tear down a
  // perfectly good session only to open an identical one. The buffer resets
  // itself when the stream changes.
  const logGroup = target?.logGroup
  const logStream = target?.logStream
  const tail = useLogTailBuffer({
    enabled: !!logGroup && !!logStream && activeTab === "logs",
    groupIdentifier: logGroup,
    streamName: logStream,
  })

  // The live session covers everything newer than the first fetched page —
  // until it dies. From then on, a forward token walk off that first page
  // keeps events written after the death reachable; it only ever fetches
  // while the tail is dead and the user sits at the bottom (see LogsPane).
  const tailDead = tail.status === "error"
  const forward = useForwardLogPages({
    logGroup,
    logStream,
    startToken: logQuery.data?.pages[0]?.nextForwardToken,
  })

  // All historical events: pages are in reverse order (newest first page),
  // so reverse them to get chronological order; forward-fetched events are
  // strictly newer than the first page (the token walk starts at its end).
  const storedEvents = useMemo(() => {
    const historical = [...(logQuery.data?.pages ?? [])].reverse().flatMap((p) => p.events)
    return forward.events.length ? [...historical, ...forward.events] : historical
  }, [logQuery.data, forward.events])

  // A page fetched while the session was open — a forward page especially,
  // which covers the very span the dead session was pushing — can already
  // hold what the session pushed, so the live events are reconciled against
  // it rather than simply appended. GetLogEvents output events carry no
  // logStreamName while tailed events name their stream, so the count-based
  // reconciliation runs against a stream-stamped *view* of the stored events
  // (the peek is single-stream — every stored event belongs to `logStream`).
  // Only the view is copied; the rendered objects keep their identity, which
  // `logEventKey` and the virtualizer's row keys depend on.
  const logEvents = useMemo(() => {
    if (tail.events.length === 0) return storedEvents
    const dedupView = logStream
      ? storedEvents.map((e) => ({ ...e, logStreamName: logStream }))
      : storedEvents
    return [...storedEvents, ...dropTailedDuplicates(dedupView, tail.events)]
  }, [storedEvents, tail.events, logStream])

  return (
    <Dialog.Root
      open={visible}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <Dialog.Portal>
        {/* Backdrop — pointer-events-none so clicking another peek-able node on the
            canvas (e.g. a different log stream) reaches that node instead of being
            swallowed here. Outside-click-to-close is still handled below via
            onInteractOutside. */}
        <Dialog.Overlay className="pointer-events-none fixed inset-0 z-60" />

        {/* Slide-in panel. Wide enough for a pretty-printed document beside
            its timestamp; capped at the viewport so it never overflows a
            small window. */}
        <Dialog.Content
          aria-describedby={undefined}
          onEscapeKeyDown={onClose}
          onInteractOutside={(e) => {
            // Clicking another peek trigger (a lambda instance or log stream row)
            // should switch this panel to the new target, not close it — that
            // trigger's own onClick already calls onPeek with the new target.
            const target = e.detail.originalEvent.target as HTMLElement | null
            if (target?.closest("[data-peek-trigger]")) {
              e.preventDefault()
              return
            }
            onClose()
          }}
          className={cn(
            "fixed inset-y-0 right-0 z-70 flex w-[min(44rem,100vw)] flex-col border-l border-border bg-bg-elevated shadow-2xl",
            "transition-transform duration-300",
            "data-[state=closed]:translate-x-full data-[state=open]:translate-x-0",
          )}
        >
          <Dialog.Title className="sr-only">{target?.title ?? "Log stream"}</Dialog.Title>
          {target && (
            <>
              {/* Header */}
              <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold">{target.title}</p>
                  <p className="truncate font-mono text-xs text-fg-muted" title={target.subtitle}>
                    {target.subtitle}
                  </p>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  {target.logGroup && target.logStream && (
                    // Everything the peek does not do — time ranges, server-side
                    // search, deep links, export — lives one click away.
                    <Link
                      to="/cloudwatch/logs/stream"
                      search={{ groupName: target.logGroup, streamName: target.logStream }}
                      className="flex items-center gap-1 rounded px-2 py-1 font-mono text-2xs text-fg-muted uppercase hover:bg-fg-muted/15 hover:text-fg"
                      title="Open this stream in the CloudWatch Logs viewer"
                    >
                      <ExternalLink aria-hidden className="h-3.5 w-3.5" />
                      Open in Logs
                    </Link>
                  )}
                  <Dialog.Close asChild>
                    <button
                      type="button"
                      className="shrink-0 rounded p-1 text-fg-muted hover:bg-fg-muted/15 hover:text-fg"
                      aria-label="Close"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </Dialog.Close>
                </div>
              </div>

              {/* Tabs */}
              <div className="flex shrink-0 gap-0 border-b border-border">
                <TabButton
                  active={activeTab === "logs"}
                  onClick={() => setActiveTab("logs")}
                  icon={<FileText className="h-3.5 w-3.5" />}
                  label="Logs"
                  disabled={!target.logGroup || !target.logStream}
                  trailing={
                    tail.status !== "idle" ? (
                      <LiveTailIndicator status={tail.status} className="ml-0.5" />
                    ) : null
                  }
                />
                {target.triggerEvent && (
                  <TabButton
                    active={activeTab === "trigger"}
                    onClick={() => setActiveTab("trigger")}
                    icon={<Zap className="h-3.5 w-3.5" />}
                    label="Trigger Event"
                  />
                )}
              </div>

              {/* Body */}
              <div className="min-h-0 flex-1 overflow-hidden">
                {activeTab === "logs" && (
                  <LogsPane
                    key={`${target.logGroup}::${target.logStream}`}
                    logEvents={logEvents}
                    loading={logQuery.isLoading}
                    hasStream={Boolean(target.logGroup && target.logStream)}
                    hasMore={logQuery.hasNextPage}
                    loadingMore={logQuery.isFetchingNextPage}
                    onLoadMore={() => logQuery.fetchNextPage()}
                    tailOverflowed={tail.overflowed}
                    tailDead={tailDead}
                    hasNewer={tailDead && !forward.exhausted}
                    loadingNewer={forward.loading}
                    onLoadNewer={forward.loadNewer}
                  />
                )}
                {activeTab === "trigger" && <TriggerPane triggerEvent={target.triggerEvent} />}
              </div>
            </>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
})

function TabButton({
  active,
  onClick,
  icon,
  label,
  disabled,
  trailing,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  disabled?: boolean
  /** e.g. the live-session dot on the Logs tab. */
  trailing?: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "flex items-center gap-1.5 px-4 py-2 text-xs font-medium transition-colors",
        "-mb-px border-b-2",
        active ? "border-cat-9 text-cat-9" : "border-transparent text-fg-muted hover:text-fg",
        disabled && "cursor-not-allowed opacity-40",
      )}
    >
      {icon}
      {label}
      {trailing}
    </button>
  )
}

/** One of the peek's display toggles — the stream viewer's, at the peek's density. */
function PrefToggle({
  label,
  checked,
  onChange,
  title,
}: {
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
  title?: string
}) {
  return (
    <label
      className="flex cursor-pointer items-center gap-1 rounded border border-border px-1.5 py-1 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10"
      title={title}
    >
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="h-3 w-3 accent-accent"
      />
      {label}
    </label>
  )
}

function LogsPane({
  logEvents: allEvents,
  loading,
  hasStream,
  hasMore,
  loadingMore,
  onLoadMore,
  tailOverflowed = 0,
  tailDead,
  hasNewer,
  loadingNewer,
  onLoadNewer,
}: {
  logEvents: LogEvent[]
  loading: boolean
  hasStream: boolean
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  /** Live events the bounded tail buffer has dropped — shown, never silent. */
  tailOverflowed?: number
  /** The live session died; what is on screen has stopped moving on its own. */
  tailDead: boolean
  /** The tail is dead and the forward token walk has more to fetch. */
  hasNewer: boolean
  loadingNewer: boolean
  onLoadNewer: () => void
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const pinnedRef = useRef(true)
  const [hasUnread, setHasUnread] = useState(false)
  const initializedRef = useRef(false)
  const prevLenRef = useRef(0)
  const prependSnapshotRef = useRef<{
    itemCount: number
    /** Key of the first rendered row — the content the user was looking at. */
    anchorKey: React.Key | null
    /** How far the viewport top sat below that row's start. */
    anchorDelta: number
  } | null>(null)
  const skipUnreadRef = useRef(false)

  // The same persisted preferences as the stream viewer: how the user likes
  // logs rendered is one setting, not one per surface. Only the four that
  // change a row's rendering are offered here; sort, UTC and deltas stay
  // the full view's.
  const { prefs, setPref } = useLogViewPrefs()
  const { formatted, syntaxHighlight, wrapLines, collapsed: collapseMode } = prefs
  // Rows the user expanded out of collapse mode, by event key so a prepend
  // keeps the expansion on the same event. Session state, like the viewer's.
  const [expandedKeys, setExpandedKeys] = useState<ReadonlySet<number>>(() => new Set())

  // A client-side filter over what is loaded — a substring match on the
  // message as it reads (escape sequences stripped), case-insensitive. Not
  // FilterLogEvents: the peek's job is a quick look at this instance's
  // output, and "the ERROR lines out of what I can already see" is the
  // question it gets asked. Anything more is the full view's, one click away.
  const [filter, setFilter] = useState("")
  const filterMatcher = useMemo(() => compileFilterHighlighter(filter), [filter])
  const logEvents = useMemo(() => {
    const needle = filter.trim().toLowerCase()
    if (!needle) return allEvents
    return allEvents.filter((e) => describeLogEvent(e).plain.toLowerCase().includes(needle))
  }, [allEvents, filter])

  const virtualizer = useVirtualizer({
    count: logEvents.length,
    getScrollElement: () => scrollRef.current,
    // One unwrapped line at text-2xs leading-relaxed plus the row's padding;
    // wrapped and pretty-printed rows are corrected by measurement.
    estimateSize: () => 22,
    overscan: 20,
    // Keyed by event, not by index, so a measurement made for a row stays
    // with that row when older pages shift every index under it.
    getItemKey: (index) => logEventKey(logEvents[index]),
  })
  const totalSize = virtualizer.getTotalSize()
  // Rows mounted mid-scroll render their highlight plain and hydrate once the
  // scroll settles — the same lightening the full viewer does.
  const scrolling = virtualizer.isScrolling

  // When a row above the viewport is measured and turns out taller or shorter
  // than its estimate, shift the scroll position by the difference so what the
  // user is looking at does not move. An instance property rather than an
  // option in this version of the virtualizer (its docs mark it experimental),
  // hence the assignment; it is read on each measurement, so an effect is
  // early enough.
  useLayoutEffect(() => {
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) =>
      item.start < (instance.scrollOffset ?? 0)
  }, [virtualizer])

  const scrollToBottom = useCallback((behavior: ScrollBehavior = "auto") => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior })
    pinnedRef.current = true
    setHasUnread(false)
  }, [])

  const handleLoadMore = useCallback(() => {
    if (!hasMore || loadingMore) return
    // Anchor on the first rendered row's *key*, not on a pixel offset: the
    // prepended rows enter as height estimates, and an offset restore lands
    // the accumulated estimate error right in the viewport. The key survives
    // the prepend, so the restore can put that exact row back at the top and
    // leave the estimate error above the fold, where the virtualizer's own
    // scroll adjustment corrects it row by row.
    const firstItem = virtualizer.getVirtualItems().at(0)
    prependSnapshotRef.current = {
      itemCount: logEvents.length,
      anchorKey: firstItem?.key ?? null,
      anchorDelta: firstItem ? (virtualizer.scrollOffset ?? 0) - firstItem.start : 0,
    }
    onLoadMore()
  }, [hasMore, loadingMore, logEvents.length, onLoadMore, virtualizer])

  // Load older logs when the top sentinel enters view
  const topSentinelRef = useScrollTrigger({
    onTrigger: handleLoadMore,
    enabled: hasMore && !loadingMore,
    direction: "up",
    rootMargin: "120px",
  })

  // Load newer logs while the tail is dead (`hasNewer`) and the user sits at
  // the bottom — a live session owns the newest edge, so this only ever runs
  // for a dead one. The walk chains without any timer: each appended page
  // changes `logEvents` and lands back in this effect, so pages keep coming
  // until AWS's same-token end signal latches `exhausted` (flipping `hasNewer`
  // off) or the user scrolls away from the bottom. Not a polling loop — every
  // fetch is caused by a state change, and an idle dead tail costs nothing.
  useEffect(() => {
    if (!hasNewer || loadingNewer) return
    if (logEvents.length > 0 && !pinnedRef.current) return
    onLoadNewer()
  }, [hasNewer, loadingNewer, logEvents, onLoadNewer])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 8
    pinnedRef.current = atBottom
    if (atBottom) {
      setHasUnread(false)
      // Scrolling back to the bottom of a dead tail re-checks the newest
      // edge — the user-driven counterpart of the effect above (which cannot
      // see scrolling: pinnedRef is a ref precisely so scrolls don't render).
      if (hasNewer && !loadingNewer) onLoadNewer()
    }
  }, [hasNewer, loadingNewer, onLoadNewer])

  // The filter the arrival logic below last saw. A filter edit changes the
  // row count without any event arriving, and count growth is what that
  // logic reads as "new logs" — so an edit rebases its baseline instead of
  // scrolling the view or raising the unread pill.
  const seenFilterRef = useRef(filter)

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return

    if (seenFilterRef.current !== filter) {
      seenFilterRef.current = filter
      prevLenRef.current = logEvents.length
      return
    }
    if (logEvents.length === 0) return

    if (!initializedRef.current) {
      initializedRef.current = true
      prevLenRef.current = logEvents.length
      el.scrollTo({ top: el.scrollHeight, behavior: "instant" })
      pinnedRef.current = true
      return
    }

    if (prependSnapshotRef.current && !loadingMore) {
      const snapshot = prependSnapshotRef.current
      prependSnapshotRef.current = null
      if (logEvents.length > snapshot.itemCount && snapshot.anchorKey != null) {
        const anchorIndex = logEvents.findIndex(
          (event) => logEventKey(event) === snapshot.anchorKey,
        )
        if (anchorIndex >= 0) {
          const [offset] = virtualizer.getOffsetForIndex(anchorIndex, "start") ?? [null]
          if (offset != null) {
            virtualizer.scrollToOffset(offset + snapshot.anchorDelta)
          }
        }
        skipUnreadRef.current = true
      }
      prevLenRef.current = logEvents.length
      return
    }

    if (logEvents.length <= prevLenRef.current) return

    prevLenRef.current = logEvents.length
    if (skipUnreadRef.current) {
      skipUnreadRef.current = false
      return
    }

    if (pinnedRef.current) {
      el.scrollTo({ top: el.scrollHeight, behavior: "auto" })
      pinnedRef.current = true
    } else {
      const unreadTimer = window.setTimeout(() => setHasUnread(true), 0)
      return () => window.clearTimeout(unreadTimer)
    }
  }, [logEvents, loadingMore, virtualizer, filter])

  // Measurement refines row heights after the pin-to-bottom scroll above, so
  // the true bottom keeps moving for a frame or two; while pinned, follow it.
  useLayoutEffect(() => {
    if (!initializedRef.current || !pinnedRef.current || prependSnapshotRef.current) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [totalSize])

  // One handler for every row (the key rides the DOM, not a per-row closure):
  // in collapse mode a click expands just that row, and a second folds it.
  const handleRowToggle = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    if (window.getSelection()?.toString()) return
    if ((e.target as Element).closest("button, a")) return
    const key = Number(e.currentTarget.dataset.rowKey)
    if (!Number.isFinite(key)) return
    setExpandedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  if (!hasStream) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-fg-muted">
        No log stream attached to this instance yet.
      </div>
    )
  }
  if (loading && allEvents.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-fg-muted">
        Loading logs…
      </div>
    )
  }
  if (!loading && allEvents.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-fg-muted">
        No log events yet.
      </div>
    )
  }
  return (
    <div className="relative flex h-full flex-col overflow-hidden">
      {/* Toolbar: filter + display toggles */}
      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-border px-2 py-1.5">
        <div className="flex min-w-32 flex-1 items-center gap-1.5 rounded-md border border-border bg-bg-muted px-2">
          <Search aria-hidden className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setFilter("")
            }}
            placeholder="Filter loaded events"
            aria-label="Filter loaded events"
            data-1p-ignore
            data-lpignore="true"
            className="h-6 min-w-0 flex-1 bg-transparent font-mono text-2xs text-fg outline-none placeholder:text-fg-subtle"
          />
          {filter && (
            <button
              type="button"
              onClick={() => setFilter("")}
              aria-label="Clear filter"
              className="rounded p-0.5 text-fg-muted hover:text-fg"
            >
              <X aria-hidden className="h-3 w-3" />
            </button>
          )}
        </div>
        <PrefToggle
          label="Format"
          checked={formatted}
          onChange={(v) => setPref("formatted", v)}
          title="Pretty-print JSON documents"
        />
        <PrefToggle
          label="Syntax"
          checked={syntaxHighlight}
          onChange={(v) => setPref("syntaxHighlight", v)}
          title="Colour JSON documents"
        />
        <PrefToggle
          label="Wrap"
          checked={wrapLines}
          onChange={(v) => setPref("wrapLines", v)}
          title="Wrap long lines instead of scrolling sideways"
        />
        <PrefToggle
          label="Collapse"
          checked={collapseMode}
          onChange={(v) => setPref("collapsed", v)}
          title="Show every row as one line — click a row to expand it"
        />
        <span className="ml-auto font-mono text-2xs text-fg-muted tabular-nums">
          {filter
            ? `${formatCount(logEvents.length)} of ${formatCount(allEvents.length)}`
            : `${formatCount(allEvents.length)} event${allEvents.length === 1 ? "" : "s"}`}
        </span>
      </div>

      {logEvents.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-sm text-fg-muted">
          No loaded events match the filter.
        </div>
      ) : (
        <div
          ref={scrollRef}
          onScroll={onScroll}
          data-1p-ignore=""
          data-lpignore="true"
          data-form-type="other"
          className="min-h-0 flex-1 overflow-auto font-mono text-2xs leading-relaxed"
        >
          {/* Top sentinel — triggers loading older pages when scrolled into view */}
          <div ref={topSentinelRef} />
          {loadingMore && (
            <div className="py-2 text-center text-2xs text-fg-muted">Loading older logs…</div>
          )}
          {!loadingMore && !hasMore && (
            <div className="py-2 text-center text-2xs text-fg-muted">No earlier logs</div>
          )}
          {tailOverflowed > 0 && (
            <div className="py-1 text-center text-2xs text-warning">
              {formatCount(tailOverflowed)} older live events dropped — the stream is faster than
              the buffer
            </div>
          )}
          <div style={{ height: `${totalSize}px`, width: "100%", position: "relative" }}>
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const e = logEvents[virtualRow.index]
              const meta = describeLogEvent(e)
              const rowKey = logEventKey(e)
              const rowCollapsed = collapseMode && !expandedKeys.has(rowKey)
              return (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  data-row-key={rowKey}
                  ref={virtualizer.measureElement}
                  onClick={collapseMode ? handleRowToggle : undefined}
                  className={cn(
                    "group/row absolute top-0 left-0 flex w-full gap-2 border-b border-l-2 border-border-muted border-l-transparent px-2 py-0.5 hover:bg-fg-muted/5",
                    collapseMode && "cursor-pointer",
                    meta.level && logLevelRowClass[meta.level],
                  )}
                  style={{ transform: `translateY(${virtualRow.start}px)` }}
                >
                  <span className="shrink-0 pt-px font-mono text-fg-muted tabular-nums select-none">
                    {formatLogTime(e.timestamp)}
                  </span>
                  <div className="min-w-0 flex-1">
                    <LogMessage
                      message={e.message ?? ""}
                      summary={meta.summary}
                      formatted={formatted}
                      syntaxHighlight={syntaxHighlight}
                      wrapLines={wrapLines}
                      filterMatcher={filterMatcher}
                      level={meta.level}
                      collapsed={rowCollapsed}
                      defer={scrolling}
                    />
                  </div>
                  <CopyButton
                    value={meta.plain}
                    noun="log message"
                    tone="inline"
                    className="shrink-0 self-start p-0.5 text-fg-muted/40 opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg-muted"
                  />
                </div>
              )
            })}
          </div>
          {loadingNewer && (
            <div className="py-2 text-center text-2xs text-fg-muted">Loading newer logs…</div>
          )}
          {/* The bottom edge says what it is: a live session waiting, or one
              that died (in which case the forward walk is what keeps this
              edge honest, and reopening the peek is the recovery path). */}
          {!loadingNewer && !hasNewer && (
            <div className="py-2 text-center text-2xs text-fg-muted">
              {tailDead
                ? "Live tail disconnected — reopen to reconnect"
                : "Live — watching for new events"}
            </div>
          )}
        </div>
      )}

      {/* "New logs" pill — visible when scrolled up and new events arrive */}
      {hasUnread && (
        <button
          type="button"
          onClick={() => scrollToBottom("smooth")}
          className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full bg-accent px-3 py-1 font-mono text-2xs font-medium text-fg-on-accent shadow-lg hover:bg-accent-hover"
        >
          ↓ New logs
        </button>
      )}
    </div>
  )
}

function TriggerPane({ triggerEvent }: { triggerEvent: unknown }) {
  if (!triggerEvent) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-fg-muted">
        No trigger event recorded.
      </div>
    )
  }
  return (
    <div className="m-0 p-4 font-mono text-2xs leading-relaxed text-fg">
      <TriggerEventViewer event={triggerEvent} />
    </div>
  )
}
