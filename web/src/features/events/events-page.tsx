/**
 * EventsPage — live event stream viewer at /events.
 *
 * Features: multi-source filtering, free-text search, date-range filtering,
 * heartbeat toggle, pause/resume. Source and text/date
 * filters are all client-side — the SSE connection always receives everything.
 *
 * Source filtering is a deny-list (hiddenSources), not an allow-list: the
 * dropdown is populated from every source seen in the stream so far, merged
 * with the statically-known ones (for stable labels/ordering before any
 * events arrive). A source is visible unless the user explicitly hid it.
 * This "default allow" behaviour matters because the set of sources is not
 * closed — a service wired into the bus later, or a source id the UI has
 * never seen, must show up by default rather than being silently dropped by
 * a hardcoded enumeration. The only source hidden out of the box is
 * "request" (see DEFAULT_HIDDEN_SOURCES), matching the noise classification
 * the server's history buffer uses to decide what to evict first.
 */
import { useState, useCallback, useMemo, useRef, useEffect } from "react"
import { Activity, Heart, Pause, Play, Search, X } from "lucide-react"
import { useEventStream, type StreamEvent } from "@/hooks/use-event-stream"
import { EventConsole } from "@/components/ui/event-console"
import { PageHeader } from "@/components/ui/primitives"
import { Button } from "@/components/ui/button"
import { Tooltip } from "@/components/ui/tooltip"
import { Input } from "@/components/ui/input"
import { CheckboxFilterDropdown } from "@/components/ui/checkbox-filter-dropdown"
import { cn } from "@/lib/utils"
import { formatQuantity, toTitleCase } from "@/lib/format"
import { EventType, isNoiseEventType } from "@/services/event-types"
import { SERVICES } from "@/lib/service-registry"

// ─── Source labelling ────────────────────────────────────────────────────────

type SourceEntry = { id: string; label: string }

const SOURCE_LABELS: Record<string, string> = {
  request: "Requests",
  service: "Service errors",
  inbox: "Inbox",
}

/** Statically-known sources, derived from the EventType namespace keys. */
function buildKnownSources(): SourceEntry[] {
  const entries: SourceEntry[] = []
  for (const key of Object.keys(EventType)) {
    const explicitLabel = SOURCE_LABELS[key]
    if (explicitLabel) {
      entries.push({ id: key, label: explicitLabel })
      continue
    }

    if (key in SERVICES) {
      const svc = SERVICES[key as keyof typeof SERVICES]
      entries.push({ id: key, label: svc.label })
      continue
    }

    entries.push({ id: key, label: toTitleCase(key) })
  }
  return entries
}

const KNOWN_SOURCES = buildKnownSources()
const KNOWN_SOURCES_BY_ID = new Map(KNOWN_SOURCES.map((s) => [s.id, s]))

/** Label for any source id, known or not (falls back to title-casing it). */
function sourceLabel(id: string): string {
  return KNOWN_SOURCES_BY_ID.get(id)?.label ?? toTitleCase(id)
}

/**
 * Sources hidden by default. A source is included here only if every event
 * type it can emit is classified as noise (see isNoiseEventType) — today
 * that is exactly "request". Computed from the classification rather than
 * hardcoded so the UI default can't silently drift from the server's
 * history-buffer eviction policy as event types evolve.
 */
function computeDefaultHiddenSources(): Set<string> {
  const hidden = new Set<string>()
  for (const [key, group] of Object.entries(EventType)) {
    const types = Object.values(group as Record<string, string>)
    if (types.length > 0 && types.every((t) => isNoiseEventType(t))) {
      hidden.add(key)
    }
  }
  return hidden
}

const DEFAULT_HIDDEN_SOURCES = computeDefaultHiddenSources()

function setsEqual(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  if (a.size !== b.size) return false
  for (const v of a) if (!b.has(v)) return false
  return true
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function eventMatchesText(ev: StreamEvent, lower: string): boolean {
  if (ev.type.toLowerCase().includes(lower)) return true
  if (ev.source.toLowerCase().includes(lower)) return true
  // Envelope-level, so pasting a request id narrows the console to everything
  // that one API call set off — the S3 write, the notification, the queue
  // send, the invoke — not just the request:Received row summarising the call.
  if (ev.requestId?.toLowerCase().includes(lower)) return true
  if (ev.type === "request:Received") {
    const p = ev.payload as Record<string, unknown> | null
    if (p) {
      for (const f of ["method", "path", "service", "operation", "requestId"]) {
        if (
          String(p[f] ?? "")
            .toLowerCase()
            .includes(lower)
        )
          return true
      }
      if (
        String(p.status ?? "")
          .toLowerCase()
          .includes(lower)
      )
        return true
    }
  }
  try {
    if (JSON.stringify(ev.payload).toLowerCase().includes(lower)) return true
  } catch {
    /* ignore */
  }
  return false
}

function topSources(
  events: readonly StreamEvent[],
  n: number,
): { id: string; label: string; count: number }[] {
  const counts = new Map<string, number>()
  for (const e of events) counts.set(e.source, (counts.get(e.source) ?? 0) + 1)
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1])
    .slice(0, n)
    .map(([id, count]) => ({ id, label: sourceLabel(id), count }))
}

// ─── Component ────────────────────────────────────────────────────────────────

export function EventsPage() {
  const [hiddenSources, setHiddenSources] = useState<Set<string>>(
    () => new Set(DEFAULT_HIDDEN_SOURCES),
  )
  const [showHeartbeats, setShowHeartbeats] = useState(false)
  const [paused, setPaused] = useState(false)
  const [frozenEvents, setFrozenEvents] = useState<StreamEvent[]>([])
  const [textFilter, setTextFilter] = useState("")
  const [dateFrom, setDateFrom] = useState("")
  const [dateTo, setDateTo] = useState("")
  const searchRef = useRef<HTMLInputElement>(null)

  const {
    events: rawEvents,
    connected,
    clear: streamClear,
  } = useEventStream({
    includeHeartbeats: showHeartbeats,
  })

  // Every source seen so far, merged with the statically-known ones (for
  // stable labels/ordering before any matching event has arrived). A source
  // that only shows up at runtime — a new service, or the "system" source
  // heartbeats carry — is added here automatically instead of being
  // silently unfilterable.
  const allSources = useMemo(() => {
    const merged = new Map<string, SourceEntry>(KNOWN_SOURCES_BY_ID)
    for (const e of rawEvents) {
      if (!merged.has(e.source)) {
        merged.set(e.source, { id: e.source, label: sourceLabel(e.source) })
      }
    }
    return Array.from(merged.values()).sort((a, b) => a.label.localeCompare(b.label))
  }, [rawEvents])

  // Client-side filtering chain. hiddenSources is a deny-list: anything not
  // explicitly hidden is shown, including sources the UI has never heard of.
  const events = useMemo(() => {
    let filtered = rawEvents.filter((e) => !hiddenSources.has(e.source))

    const lower = textFilter.toLowerCase().trim()
    if (lower) filtered = filtered.filter((e) => eventMatchesText(e, lower))

    if (dateFrom) {
      const from = new Date(dateFrom).getTime()
      if (!isNaN(from)) filtered = filtered.filter((e) => new Date(e.time).getTime() >= from)
    }
    if (dateTo) {
      const to = new Date(dateTo).getTime()
      if (!isNaN(to)) filtered = filtered.filter((e) => new Date(e.time).getTime() <= to)
    }
    return filtered
  }, [rawEvents, hiddenSources, textFilter, dateFrom, dateTo])

  const displayedEvents = paused ? frozenEvents : events

  const clear = useCallback(() => {
    streamClear()
    setFrozenEvents([])
    setPaused(false)
  }, [streamClear])

  // / to focus search.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "/" && !e.ctrlKey && !e.metaKey && document.activeElement === document.body) {
        e.preventDefault()
        searchRef.current?.focus()
      }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [])

  const toggleSource = useCallback((id: string) => {
    setHiddenSources((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const showAllSources = useCallback(() => setHiddenSources(new Set()), [])
  const hideAllSources = useCallback(
    () => setHiddenSources(new Set(allSources.map((s) => s.id))),
    [allSources],
  )

  const clearFilters = useCallback(() => {
    setHiddenSources(new Set(DEFAULT_HIDDEN_SOURCES))
    setTextFilter("")
    setDateFrom("")
    setDateTo("")
  }, [])

  const hasFilter =
    !setsEqual(hiddenSources, DEFAULT_HIDDEN_SOURCES) ||
    textFilter !== "" ||
    dateFrom !== "" ||
    dateTo !== ""
  const top5 = useMemo(() => topSources(rawEvents, 5), [rawEvents])

  return (
    <div className="flex w-full flex-col gap-3">
      <PageHeader
        title="Event Stream"
        description={
          connected ? (
            top5.length > 0 ? (
              <span className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                <span>{formatQuantity(rawEvents.length, "event")}</span>
                {top5.map((s) => (
                  <span key={s.id} className="text-fg-subtle">
                    <span className="font-medium text-fg-muted">{s.count}</span> {s.label}
                  </span>
                ))}
              </span>
            ) : (
              "Waiting for events…"
            )
          ) : (
            "Not connected"
          )
        }
      />

      {/* Filter bar */}
      <div className="flex flex-wrap items-center gap-2">
        {/* Free text search */}
        <div className="relative">
          <Search className="absolute top-1/2 left-2 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
          <Input
            ref={searchRef}
            placeholder={hasFilter ? "Filtered…" : "Filter events…"}
            value={textFilter}
            onChange={(e) => setTextFilter(e.target.value)}
            className="h-8 w-52 pr-7 pl-7 text-xs"
          />
          {textFilter && (
            <button
              className="absolute top-1/2 right-2 -translate-y-1/2 text-fg-subtle hover:text-fg"
              onClick={() => setTextFilter("")}
              aria-label="Clear text filter"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>

        {/* Source multi-select dropdown */}
        <CheckboxFilterDropdown
          items={allSources}
          model="hide"
          selected={hiddenSources}
          onToggle={toggleSource}
          onShowAll={showAllSources}
          onHideAll={hideAllSources}
          triggerLabel={(() => {
            const vc = allSources.length - hiddenSources.size
            if (vc <= 0) return "No sources"
            if (vc === allSources.length) return "All sources"
            return formatQuantity(vc, "source")
          })()}
        />

        {/* Date filters */}
        <Input
          type="datetime-local"
          value={dateFrom}
          onChange={(e) => setDateFrom(e.target.value)}
          className={cn("h-8 w-40 text-xs", dateFrom && "border-accent/40")}
          aria-label="Filter from date"
        />
        <Input
          type="datetime-local"
          value={dateTo}
          onChange={(e) => setDateTo(e.target.value)}
          className={cn("h-8 w-40 text-xs", dateTo && "border-accent/40")}
          aria-label="Filter to date"
        />

        <span className="mx-0.5 h-5 w-px bg-border" />

        {/* Pause */}
        <Button
          variant={paused ? "secondary" : "ghost"}
          size="sm"
          className="h-8 gap-1 text-xs"
          onClick={() => {
            if (!paused) setFrozenEvents(events)
            setPaused((v) => !v)
          }}
        >
          {paused ? (
            <Play className="h-3.5 w-3.5 text-success" />
          ) : (
            <Pause className="h-3.5 w-3.5" />
          )}
          {paused ? "Resume" : "Pause"}
        </Button>

        {/* Heartbeats — icon-only toggle: filled red when pings are shown,
            outlined when hidden; the label lives in the tooltip. */}
        <Tooltip content={showHeartbeats ? "Hide heartbeat pings" : "Show heartbeat pings"}>
          <Button
            variant="ghost"
            size="sm"
            className="h-8 text-xs"
            onClick={() => setShowHeartbeats((v) => !v)}
            aria-label={showHeartbeats ? "Hide heartbeat pings" : "Show heartbeat pings"}
            aria-pressed={showHeartbeats}
          >
            <Heart
              className={cn(
                "h-3.5 w-3.5",
                showHeartbeats ? "fill-danger text-danger" : "text-fg-muted",
              )}
            />
          </Button>
        </Tooltip>

        {/* Clear filters — always visible */}
        <Button
          variant="ghost"
          size="sm"
          className="h-8 gap-1 text-xs"
          disabled={!hasFilter}
          onClick={clearFilters}
        >
          <X className="h-3 w-3" />
          Clear
        </Button>
      </div>

      <EventConsole
        events={displayedEvents}
        connected={connected}
        onClear={clear}
        paused={paused}
      />
    </div>
  )
}

export { Activity as EventsIcon }
