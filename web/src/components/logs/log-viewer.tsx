import { memo, useCallback, useEffect, useRef, useState } from "react"
import { useVirtualizer } from "@tanstack/react-virtual"
import { useScrollTrigger } from "@/hooks/use-scroll-trigger"
import { cn } from "@/lib/utils"
import { describeLogEvent, formatLogTime, logLevelRowClass } from "@/lib/log-format"
import { nearViewport, useScrollSettled } from "@/hooks/use-scroll-settled"
import { CopyButton } from "@/components/ui/copy-button"
import { LogMessage } from "./log-message"

export interface LogViewerEvent {
  timestamp?: number
  message?: string
  ingestionTime?: number
  logStreamName?: string
}

interface LogViewerProps {
  events: LogViewerEvent[]
  loading?: boolean
  error?: string | null
  emptyMessage?: string
  hasMore?: boolean
  isFetchingMore?: boolean
  onLoadMore?: () => void
  defaultMode?: "table" | "plain"
  showModeToggle?: boolean
  className?: string
  /**
   * Pre-compiled filter matcher, for callers that own a search box — see
   * `compileFilterHighlighter` in `features/cloudwatch/logs/tail`. Null (the
   * default) draws every row unhighlighted, exactly what every caller before
   * `LogPanel` got.
   */
  filterMatcher?: RegExp | null
  /**
   * Keep the newest row in view as rows arrive, as a terminal does — for a
   * live tail. Scrolling up releases it until the reader scrolls back to
   * the bottom, so reading an older row is not fought over.
   */
  follow?: boolean
}

/**
 * A collapsed table row's height, exactly: `py-1` (8px) around one
 * `text-2xs leading-relaxed` line (18px), plus the 2px border. Collapsed
 * rows carry a fixed height and are never measured — the same trade the
 * flagship stream viewer makes in `log-events-viewer.tsx`'s collapse mode,
 * and for the same reason: skipping `measureElement` is what keeps a table
 * of one-line rows cheap.
 */
const COLLAPSED_ROW_HEIGHT = 28

/**
 * A rough pre-measurement estimate for an *expanded* table row — refined by
 * `measureElement` the moment it mounts, so accuracy only has to be good
 * enough to keep the initial scrollbar from jumping. Deliberately not shared
 * with `log-events-viewer.tsx`'s `estimateRowHeight`: that one accounts for
 * the stream viewer's own delta/line-number chrome, which this row doesn't
 * carry, and a shared function with two unrelated call sites is how the
 * helpers in `log-format.ts` drifted before consolidation.
 */
function estimateExpandedRowHeight(message: string): number {
  const lines = Math.max(1, Math.ceil(message.length / 100))
  return 12 + lines * 16
}

export function LogViewer({
  events,
  loading = false,
  error,
  emptyMessage = "No log events found",
  hasMore = false,
  isFetchingMore = false,
  onLoadMore,
  defaultMode = "plain",
  showModeToggle = true,
  className,
  filterMatcher = null,
  follow = false,
}: LogViewerProps) {
  const [mode, setMode] = useState<"table" | "plain">(defaultMode)
  const [formatted, setFormatted] = useState(false)
  const parentRef = useRef<HTMLDivElement>(null)

  // Rows the user expanded out of the table's collapsed default — index-keyed,
  // which is sound exactly because `LogViewer`'s events only ever grow by
  // appending (via `onLoadMore`) or get wholesale-replaced by a new filter:
  // unlike the flagship stream viewer, nothing here prepends or re-sorts, so
  // an index never changes out from under a row that already rendered.
  const [expandedIndices, setExpandedIndices] = useState<ReadonlySet<number>>(() => new Set())
  const toggleExpanded = useCallback((index: number) => {
    setExpandedIndices((prev) => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }, [])

  // A shrinking event list — a new filter, a clear, a narrower refetch — means
  // the indices on screen no longer name the rows they used to; holding the
  // old expansion set would "expand" whatever unrelated row now sits at that
  // index. Growth (append) keeps it: the rows that were expanded still are.
  const prevLengthRef = useRef(events.length)
  useEffect(() => {
    if (events.length < prevLengthRef.current) setExpandedIndices(new Set())
    prevLengthRef.current = events.length
  }, [events.length])

  const canLoadMore = Boolean(hasMore && onLoadMore)
  const sentinelRef = useScrollTrigger({
    onTrigger: () => {
      if (!onLoadMore || isFetchingMore || !hasMore) return
      onLoadMore()
    },
    enabled: canLoadMore && !isFetchingMore,
    direction: "down",
    rootMargin: "120px",
  })

  // Row content is derived per row, not per list: this used to map every event
  // on any change and — with Format ticked — `JSON.parse` all of them, so a
  // 10,000-event page paid 10,000 parses to show the ~30 rows on screen.
  // `describeLogEvent` caches the cheap part per event; the parse is row-local.
  const virtualizer = useVirtualizer({
    count: events.length,
    getScrollElement: () => parentRef.current,
    estimateSize: (index) => {
      if (mode !== "table") return 32
      if (!expandedIndices.has(index)) return COLLAPSED_ROW_HEIGHT
      return estimateExpandedRowHeight(String(events[index]?.message ?? ""))
    },
    // Cheap mid-scroll mounts (see `useScrollSettled`) make wide overscan
    // nearly free, and it is the difference between a fast fling showing rows
    // and showing blank track.
    overscan: 30,
  })

  // Collapsing or expanding a row changes its height without ever attaching
  // `measureElement` to it (collapsed rows skip measurement on purpose, the
  // same trade `log-events-viewer.tsx` makes) — so nothing observes the
  // change automatically. A flip between Table and Plain needs the same
  // nudge, since the estimate function itself switches behaviour underneath
  // rows the virtualizer already sized.
  useEffect(() => {
    virtualizer.measure()
  }, [virtualizer, mode, expandedIndices])

  // Following: stuck to the bottom until the reader scrolls away from it.
  const stuckRef = useRef(true)
  const onScroll = useCallback((e: React.UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget
    stuckRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }, [])
  useEffect(() => {
    if (!follow || !stuckRef.current || events.length === 0) return
    virtualizer.scrollToIndex(events.length - 1, { align: "end" })
  }, [follow, events.length, virtualizer])

  return (
    <div className={cn("flex min-h-0 flex-1 flex-col", className)}>
      {showModeToggle && (
        <div className="mb-2 flex items-center justify-end gap-1.5">
          <button
            type="button"
            onClick={() => setMode("plain")}
            className={cn(
              "rounded border px-2 py-1 font-mono text-2xs font-medium uppercase",
              mode === "plain"
                ? "border-accent/50 bg-accent-muted text-fg"
                : "border-border text-fg-muted hover:bg-fg-muted/10",
            )}
          >
            Plain
          </button>
          <button
            type="button"
            onClick={() => setMode("table")}
            className={cn(
              "rounded border px-2 py-1 font-mono text-2xs font-medium uppercase",
              mode === "table"
                ? "border-accent/50 bg-accent-muted text-fg"
                : "border-border text-fg-muted hover:bg-fg-muted/10",
            )}
          >
            Table
          </button>
          <label className="flex cursor-pointer items-center gap-1 rounded border border-border px-2 py-1 font-mono text-2xs font-medium text-fg-muted uppercase select-none hover:bg-fg-muted/10">
            <input
              type="checkbox"
              checked={formatted}
              onChange={(e) => setFormatted(e.target.checked)}
              className="h-3 w-3 accent-accent"
            />
            Format
          </label>
        </div>
      )}

      <div
        ref={parentRef}
        onScroll={follow ? onScroll : undefined}
        className="min-h-0 flex-1 overflow-auto rounded bg-bg-elevated p-2"
      >
        {loading && events.length === 0 && (
          <div className="py-4 text-center text-2xs text-fg-muted">Loading logs...</div>
        )}

        {!loading && error && (
          <div className="py-4 text-center text-2xs text-danger">{error}</div>
        )}

        {!loading && !error && events.length === 0 && (
          <div className="py-4 text-center text-2xs text-fg-muted">{emptyMessage}</div>
        )}

        {events.length > 0 && (
          <div
            style={{
              height: `${virtualizer.getTotalSize()}px`,
              width: "100%",
              position: "relative",
            }}
          >
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const rowCollapsed = mode === "table" && !expandedIndices.has(virtualRow.index)
              return (
                <div
                  key={virtualRow.key}
                  data-index={virtualRow.index}
                  // Collapsed table rows are fixed-height and never measured —
                  // the same trade `log-events-viewer.tsx`'s collapse mode
                  // makes, and for the same reason: it is what removes the
                  // measurement churn from a table of one-line rows.
                  ref={mode === "table" && rowCollapsed ? undefined : virtualizer.measureElement}
                  className="absolute top-0 left-0 w-full"
                  style={{
                    transform: `translateY(${virtualRow.start}px)`,
                    ...(rowCollapsed ? { height: `${COLLAPSED_ROW_HEIGHT}px` } : {}),
                  }}
                >
                  {mode === "table" ? (
                    <TableRow
                      event={events[virtualRow.index]}
                      formatted={formatted}
                      filterMatcher={filterMatcher}
                      collapsed={rowCollapsed}
                      onToggle={() => toggleExpanded(virtualRow.index)}
                      defer={virtualizer.isScrolling || !nearViewport(virtualRow, virtualizer)}
                    />
                  ) : (
                    <LogViewerRow
                      event={events[virtualRow.index]}
                      formatted={formatted}
                      filterMatcher={filterMatcher}
                      defer={virtualizer.isScrolling || !nearViewport(virtualRow, virtualizer)}
                    />
                  )}
                </div>
              )
            })}
          </div>
        )}

        {isFetchingMore && (
          <div className="pt-2 text-center text-2xs text-fg-muted">Loading more...</div>
        )}

        {!isFetchingMore && canLoadMore && (
          <div ref={sentinelRef} className="h-3" aria-hidden="true" />
        )}

        {!isFetchingMore && !hasMore && events.length > 0 && (
          <div className="pt-2 text-center text-2xs text-fg-muted">End of logs</div>
        )}
      </div>
    </div>
  )
}

/**
 * One log line, Plain mode.
 *
 * Memoised and derived row-locally: the virtualizer re-renders its visible rows
 * on every scroll frame, so anything computed here is computed at 60 Hz unless
 * the row can bail out. `event` comes from a query cache that hands out stable
 * objects, and the remaining props are booleans, so an unchanged row re-renders
 * to identical output and leaves the DOM alone.
 */
const LogViewerRow = memo(function LogViewerRow({
  event,
  formatted,
  filterMatcher,
  defer,
}: {
  event: LogViewerEvent
  formatted: boolean
  filterMatcher: RegExp | null
  /** Mid-scroll and far-overscan rows defer their highlight spans — see `LogMessage`. */
  defer: boolean
}) {
  const { level, summary } = describeLogEvent(event)

  // The shared message pipeline, with this surface's choices: pretty JSON and
  // its highlighting arrive together behind the one Format toggle, the level
  // shows as the row tint below rather than as a badge.
  const body = (
    <LogMessage
      message={String(event.message ?? "")}
      summary={summary}
      formatted={formatted}
      syntaxHighlight={formatted}
      wrapLines
      filterMatcher={filterMatcher}
      level={level}
      hideLevel
      defer={defer}
      sizeClassName="text-2xs"
    />
  )

  return (
    <div
      className={cn(
        "flex gap-2 border-l-2 border-l-transparent py-0.5 font-mono text-2xs text-fg-subtle",
        level && logLevelRowClass[level],
      )}
    >
      <span className="shrink-0 font-mono text-fg-muted tabular-nums">
        {formatLogTime(event.timestamp)}
      </span>
      {body}
    </div>
  )
})

/**
 * One log line, Table mode — the log-stream table's row anatomy, reused: a
 * level badge column, a click-to-expand body (collapsed to one truncated
 * line by default, the same trade `log-events-viewer.tsx`'s collapse mode
 * makes), and a hover-revealed copy affordance. Deliberately not the same
 * component as the flagship stream viewer's row: that one carries page
 * concerns this viewer has no way to offer (permalinks, request-id filtering,
 * keyboard-cursor state) and inlining just its anatomy here, off the same
 * `LogMessage`/`LevelBadge` primitives, is what keeps the two from drifting
 * on the parts they *do* share without dragging page state into a generic
 * component. See the module docs on `LogEventsViewer` for the extraction
 * call.
 */
const TableRow = memo(function TableRow({
  event,
  formatted,
  filterMatcher,
  collapsed,
  onToggle,
  defer,
}: {
  event: LogViewerEvent
  formatted: boolean
  filterMatcher: RegExp | null
  collapsed: boolean
  onToggle: () => void
  defer: boolean
}) {
  const meta = describeLogEvent(event)

  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // A click that was really a text selection, or that landed on the
      // row's own copy button, is not an expand request.
      if (window.getSelection()?.toString()) return
      if ((e.target as Element).closest("button")) return
      onToggle()
    },
    [onToggle],
  )

  // The badge renders INLINE at the head of the message, by the shared
  // pipeline itself (no `hideLevel`) — exactly how the flagship stream
  // table does it. A dedicated badge column was tried first and is a trap:
  // any fixed width either overflows "ERROR" into the message or pads
  // every badge-less row (START/END/REPORT) with dead space, and the
  // message column's start stops lining up across rows either way.
  const body = (
    <LogMessage
      message={String(event.message ?? "")}
      summary={meta.summary}
      formatted={formatted}
      syntaxHighlight={formatted}
      wrapLines
      filterMatcher={filterMatcher}
      level={meta.level}
      collapsed={collapsed}
      defer={defer}
      sizeClassName="text-2xs"
    />
  )

  return (
    <div
      onClick={handleClick}
      className={cn(
        "group/row flex cursor-pointer border-b border-l-2 border-border-muted border-l-transparent",
        collapsed && "overflow-hidden",
        meta.level && logLevelRowClass[meta.level],
      )}
    >
      {/* px-1 on both sides: the left edge is the level-tint border, and
          text flush against a 2px colored bar reads as a glyph. */}
      <div className="w-20 shrink-0 px-1 py-1 font-mono text-2xs text-fg-muted tabular-nums">
        {formatLogTime(event.timestamp)}
      </div>
      <div className="min-w-0 flex-1 px-1 py-1">{body}</div>
      <RowCopyAction plain={meta.plain} defer={defer} />
    </div>
  )
})

/**
 * The hover-revealed copy-message affordance. A component of its own, not
 * inline JSX, for the reason `log-events-viewer.tsx`'s `RowActions` gives:
 * buttons are exactly what password-manager and accessibility-tree walkers
 * inspect, and a mid-scroll row is better off without them until the scroll
 * settles.
 */
const RowCopyAction = memo(function RowCopyAction({
  plain,
  defer,
}: {
  plain: string
  defer: boolean
}) {
  const settled = useScrollSettled(defer)
  return (
    <div className="flex w-6 shrink-0 items-start justify-end pt-1 pr-1">
      {settled && (
        <CopyButton
          value={plain}
          noun="log message"
          tone="inline"
          className="p-0.5 text-fg-muted/40 opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg-muted"
        />
      )}
    </div>
  )
})
