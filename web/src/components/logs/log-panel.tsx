/**
 * A self-contained, data-connected log viewer: give it a `LogFilter` (a log
 * group, optionally narrowed to a stream and a time window) and it owns
 * fetching, filtering and auto-refresh, rendering through `LogViewer`'s table
 * mode.
 *
 * This is the piece the audit in #1440 found missing — every surface that
 * shows a slice of CloudWatch Logs (Lambda's monitor tab, a container's
 * output, an invocation's log window) had grown its own fetch-and-render
 * wiring around the same `FilterLogEvents` call, each with a slightly
 * different idea of the refresh interval, the time window, and whether a
 * filter box existed at all. `LogPanel` is the one place that wiring lives
 * now; a caller that needs bespoke paging or a filter's explicit
 * search-on-Enter semantics (the CloudWatch Logs stream page, the map's
 * live-tailed peek panel) still reaches for `LogViewer` directly — see the
 * per-call-site notes in `monitor-tab.tsx`.
 *
 * ## Pinned vs adjustable
 *
 * The embedding page and the panel's own UI both contribute to the
 * `LogFilter` this renders, and they must never disagree about a field both
 * could plausibly set. `pinned` is what the *page* knows and controls — the
 * log group always, sometimes a stream, sometimes (an invocation's replay
 * window) a fixed absolute time range the panel has no business letting the
 * user nudge. Whatever `pinned` does not name is the panel's own to offer:
 * the filter-pattern box, and — only when the page did not pin one — a
 * relative time-range select. A pinned field's control simply does not
 * render, so the two can never step on each other; see the `filter` merge
 * inside `LogPanel` below.
 */
import { useMemo, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Search, X } from "lucide-react"
import { LogViewer } from "@/components/logs/log-viewer"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { formatQuantity } from "@/lib/format"
import { logPanelQueryOptions } from "@/features/cloudwatch/logs/data"
import { compileFilterHighlighter } from "@/features/cloudwatch/logs/tail"
import {
  RELATIVE_LOG_WINDOW_LABELS,
  type LogFilter,
  type RelativeLogWindowToken,
} from "@/features/cloudwatch/logs/log-filter"
import { cn } from "@/lib/utils"

const RELATIVE_WINDOW_TOKENS: RelativeLogWindowToken[] = ["15m", "1h", "6h", "24h"]

export interface LogPanelProps {
  /**
   * The fields the embedding page controls outright — always `group`, and
   * whichever of `stream`/`streams`/`time`/`pattern`/`limit` the page's own
   * context already answers. `MonitorTab` pins just `{ group }`; a future
   * per-invocation panel would pin `{ group, stream, time: { kind:
   * "absolute", ... } }` and the time-range select disappears accordingly.
   */
  pinned: Partial<LogFilter> & { group: string }
  /**
   * Starting point for the fields the panel's own UI controls — a relative
   * `time` token to preselect, an initial `pattern`, a non-default `limit`.
   * Ignored for any field `pinned` already names.
   */
  defaults?: Partial<Pick<LogFilter, "time" | "pattern" | "limit">>
  className?: string
  emptyMessage?: string
  /**
   * Milliseconds between automatic refetches, or `false` to disable — the
   * cadence `MonitorTab`'s footer has long promised. Defaults to 5s. Applies
   * equally to a relative window (which slides forward with each refetch)
   * and a pinned absolute one (which re-polls the same fixed range — the
   * point for a still-running invocation).
   */
  refreshIntervalMs?: number | false
  showFilterInput?: boolean
  showModeToggle?: boolean
  defaultMode?: "table" | "plain"
}

const DEFAULT_LIMIT = 200

export function LogPanel({
  pinned,
  defaults,
  className,
  emptyMessage = "No log events found",
  refreshIntervalMs = 5_000,
  showFilterInput = true,
  showModeToggle = true,
  defaultMode = "table",
}: LogPanelProps) {
  const timePinned = pinned.time !== undefined
  const patternPinned = pinned.pattern !== undefined

  // The panel's own state covers exactly the fields `pinned` leaves open —
  // seeded from `defaults`, falling back to a plain "last hour" the first
  // time a page does not care to suggest anything more specific.
  const [timeToken, setTimeToken] = useState<RelativeLogWindowToken>(
    defaults?.time?.kind === "relative" ? defaults.time.token : "1h",
  )
  const [patternInput, setPatternInput] = useState(defaults?.pattern ?? "")
  // Debounced, not submit-on-Enter: unlike the flagship stream page (which
  // pages through up to 10,000 events across a manual Search click), this
  // panel already refetches on its own cadence, so a keystroke pays for
  // itself the same way the refresh timer does — a query, not a page-through.
  const debouncedPattern = useDebouncedValue(patternInput.trim(), 300)

  // The page's fields always win: spreading `pinned` last means a field it
  // names overrides whatever the panel's own state would have contributed,
  // and a field it does not name is simply absent from `pinned` and falls
  // through to the adjustable side. Two objects can never disagree about a
  // field only one of them is allowed to set.
  const filter: LogFilter = useMemo(
    () => ({
      time: timePinned ? pinned.time! : { kind: "relative", token: timeToken },
      pattern: patternPinned ? pinned.pattern : debouncedPattern || undefined,
      limit: defaults?.limit ?? DEFAULT_LIMIT,
      ...pinned,
    }),
    [pinned, timePinned, timeToken, patternPinned, debouncedPattern, defaults?.limit],
  )

  const { data, isLoading, isFetching, isError, error } = useQuery({
    ...logPanelQueryOptions(filter),
    enabled: Boolean(filter.group),
    refetchInterval: refreshIntervalMs,
  })

  const limit = filter.limit ?? DEFAULT_LIMIT
  const events = useMemo(() => (data?.events ?? []).slice(-limit), [data, limit])
  // Reuses the flagship stream viewer's own highlighter — same quoted-phrase
  // parsing (`parseLogFilterTerms`), so a filter that matches server-side
  // marks the same substrings here that it would there.
  const activePattern = filter.pattern ?? ""
  const filterMatcher = useMemo(() => compileFilterHighlighter(activePattern), [activePattern])

  const errorMessage = isError
    ? error instanceof Error
      ? error.message
      : "Failed to load log events"
    : null

  // Each half of the toolbar renders only when its field is the panel's own
  // to adjust — a pinned field's control simply does not exist, so the page
  // and the panel can never disagree about it.
  const showPatternInput = showFilterInput && !patternPinned
  const showTimeSelect = !timePinned
  const showToolbar = showPatternInput || showTimeSelect

  return (
    <div className={cn("flex min-h-0 flex-1 flex-col gap-2", className)}>
      {showToolbar && (
        <div className="flex items-center gap-2 rounded-md border border-border bg-bg-muted px-2 py-1.5">
          {showPatternInput && (
            <>
              <Search className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
              <Input
                data-1p-ignore
                data-lpignore="true"
                value={patternInput}
                onChange={(e) => setPatternInput(e.target.value)}
                placeholder='Filter — e.g. ERROR, "request failed"'
                className="h-6 border-0 bg-transparent px-1 text-xs shadow-none focus-visible:ring-0"
              />
              {patternInput && (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="h-6 px-1.5"
                  onClick={() => setPatternInput("")}
                >
                  <X className="h-3 w-3" />
                </Button>
              )}
            </>
          )}
          {/* A pinned time window is the page's call end to end — an
              invocation replay's fixed `[acquiredAt, releasedAt]`, say — so
              its control does not render at all rather than offering a
              choice that would silently be overridden. */}
          {showTimeSelect && (
            <select
              value={timeToken}
              onChange={(e) => setTimeToken(e.target.value as RelativeLogWindowToken)}
              className={cn(
                "h-6 shrink-0 rounded border border-border bg-transparent px-1.5 font-mono text-2xs text-fg-muted uppercase",
                showPatternInput && "ml-auto",
              )}
              aria-label="Time range"
            >
              {RELATIVE_WINDOW_TOKENS.map((token) => (
                <option key={token} value={token}>
                  {RELATIVE_LOG_WINDOW_LABELS[token]}
                </option>
              ))}
            </select>
          )}
        </div>
      )}

      <LogViewer
        events={events}
        loading={isLoading}
        error={errorMessage}
        emptyMessage={debouncedPattern ? "No events match this filter." : emptyMessage}
        defaultMode={defaultMode}
        showModeToggle={showModeToggle}
        filterMatcher={filterMatcher}
        className="min-h-0 flex-1"
      />

      {events.length > 0 && (
        <p className="text-right text-2xs text-fg-muted">
          Showing last {formatQuantity(events.length, "event")}
          {refreshIntervalMs
            ? ` · auto-refreshes every ${Math.round(refreshIntervalMs / 1000)}s`
            : null}
          {isFetching && !isLoading ? " · refreshing…" : null}
        </p>
      )}
    </div>
  )
}
