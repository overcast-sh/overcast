/**
 * The raw event history, made readable: grouped into the categories people
 * actually look for (state moves, task calls, errors, Map/Parallel control),
 * searchable, with each event's state and iteration named beside it and its
 * elapsed time from the start, so "+2.3 s" says more than a wall-clock time.
 */
import { useMemo, useState } from "react"
import { Crosshair, Search } from "lucide-react"
import type { HistoryEvent } from "@aws-sdk/client-sfn"
import { Input } from "@/components/ui/input"
import { ResourceTable } from "@/components/ui/resource-table"
import { CodeBlock } from "@/components/ui/primitives"
import { Tooltip } from "@/components/ui/tooltip"
import { formatPreciseTimeOfDay } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  describeIterationPath,
  eventDetails,
  formatDuration,
  type ExecutionTrace,
  type StateRun,
} from "../execution-trace"
import { eventCategory, historyEventFailure, type EventCategory } from "../format"
import { EventType } from "./event-type"
import { JsonPane } from "./json-pane"

type Category = "all" | EventCategory

const CATEGORIES: Array<{ id: Category; label: string }> = [
  { id: "all", label: "All" },
  { id: "states", label: "State transitions" },
  { id: "tasks", label: "Task calls" },
  { id: "flow", label: "Map & Parallel" },
  { id: "errors", label: "Errors" },
]

interface Props {
  events: HistoryEvent[]
  trace: ExecutionTrace
  isLoading: boolean
  onSelectRun: (run: StateRun) => void
}

export function ExecutionEvents({ events, trace, isLoading, onSelectRun }: Props) {
  const [category, setCategory] = useState<Category>("all")
  const [query, setQuery] = useState("")

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    return events.filter((event) => {
      const type = String(event.type ?? "")
      if (category !== "all" && eventCategory(type) !== category) return false
      if (!q) return true
      const run = runOf(trace, event)
      return (
        type.toLowerCase().includes(q) ||
        (run?.name.toLowerCase().includes(q) ?? false) ||
        JSON.stringify(eventDetails(event) ?? {})
          .toLowerCase()
          .includes(q)
      )
    })
  }, [events, category, query, trace])

  const counts = useMemo(() => {
    const out: Record<Category, number> = {
      all: events.length,
      states: 0,
      tasks: 0,
      errors: 0,
      flow: 0,
    }
    for (const e of events) out[eventCategory(String(e.type ?? ""))] += 1
    return out
  }, [events])

  const start = trace.start ?? 0

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div role="radiogroup" aria-label="Event category" className="flex flex-wrap gap-1">
          {CATEGORIES.map((c) => (
            <button
              key={c.id}
              type="button"
              role="radio"
              aria-checked={category === c.id}
              onClick={() => setCategory(c.id)}
              disabled={c.id !== "all" && counts[c.id] === 0}
              className={cn(
                "flex h-7 items-center gap-1.5 rounded-full border px-3 text-2xs font-medium transition-colors disabled:opacity-40",
                category === c.id
                  ? c.id === "errors"
                    ? "border-danger/40 bg-danger-muted text-danger"
                    : "border-accent/40 bg-accent-muted text-accent"
                  : "border-border text-fg-muted hover:bg-bg-muted hover:text-fg",
              )}
            >
              {c.label}
              <span className="font-mono tabular-nums opacity-70">{counts[c.id]}</span>
            </button>
          ))}
        </div>
        <div className="relative ml-auto w-full max-w-64">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search events, states, payloads…"
            aria-label="Search events"
            className="h-7 pl-8 text-xs"
          />
        </div>
      </div>

      <ResourceTable
        variant="embedded"
        query={{ data: rows, isLoading }}
        noun="events"
        emptyTitle="No history"
        emptyDescription="This execution recorded no events."
        isFiltered={category !== "all" || query !== ""}
        onClearFilter={() => {
          setCategory("all")
          setQuery("")
        }}
        rowKey={(event) => Number(event.id ?? 0)}
        rowClassName={(event) =>
          eventCategory(String(event.type ?? "")) === "errors" ? "bg-danger-muted/40" : undefined
        }
        columns={[
          {
            id: "id",
            header: "#",
            headerClassName: "w-12",
            cellClassName: "text-fg-subtle",
            sortValue: (event) => Number(event.id ?? 0),
            cell: (event) => Number(event.id ?? 0),
          },
          {
            header: "Event",
            cell: (event) => <EventType type={event.type} />,
          },
          {
            header: "State",
            interactive: true,
            cell: (event) => {
              const run = runOf(trace, event)
              if (!run) return <span className="text-fg-subtle">—</span>
              const where = describeIterationPath(
                trace.eventContext.get(Number(event.id))?.iterationPath ?? [],
              )
              return (
                <Tooltip content="Show this state in the diagram">
                  <button
                    type="button"
                    onClick={() => onSelectRun(run)}
                    className="group flex max-w-72 items-center gap-1.5 text-left"
                  >
                    <Crosshair className="h-3 w-3 shrink-0 text-fg-subtle group-hover:text-accent" />
                    <span className="truncate text-fg group-hover:text-accent group-hover:underline">
                      {run.name}
                    </span>
                    {where && <span className="truncate text-2xs text-fg-subtle">{where}</span>}
                  </button>
                </Tooltip>
              )
            },
          },
          {
            id: "elapsed",
            header: "Elapsed",
            cellClassName: "text-fg-muted tabular-nums",
            cell: (event) =>
              event.timestamp instanceof Date && start
                ? `+${formatDuration(event.timestamp.getTime() - start)}`
                : "—",
          },
          {
            id: "time",
            header: "Time",
            cellClassName: "text-fg-subtle tabular-nums",
            cell: (event) =>
              event.timestamp instanceof Date ? formatPreciseTimeOfDay(event.timestamp) : "—",
          },
        ]}
        expandedContent={(event) => <EventDetail event={event} />}
      />
    </div>
  )
}

function runOf(trace: ExecutionTrace, event: HistoryEvent): StateRun | undefined {
  const key = trace.eventContext.get(Number(event.id ?? 0))?.runKey
  return key ? trace.runsByKey.get(key) : undefined
}

/** The fields worth reading on their own, lifted out of the raw event. */
function EventDetail({ event }: { event: HistoryEvent }) {
  const failure = historyEventFailure(event as unknown as Record<string, unknown>)
  const details = eventDetails(event) as Record<string, unknown> | undefined
  const payloads = (["input", "output", "parameters"] as const).filter(
    (k) => typeof details?.[k] === "string",
  )
  return (
    <div className="flex flex-col gap-3">
      {failure.error && (
        <p className="text-xs text-danger">
          <span className="font-mono font-semibold">{failure.error}</span>
          {failure.cause ? ` — ${failure.cause}` : ""}
        </p>
      )}
      {payloads.length > 0 && (
        <div className={cn("grid gap-3", payloads.length > 1 && "md:grid-cols-2")}>
          {payloads.map((k) => (
            <JsonPane
              key={k}
              label={k[0].toUpperCase() + k.slice(1)}
              value={details?.[k] as string}
              bodyClassName="max-h-64"
            />
          ))}
        </div>
      )}
      <details className="text-xs">
        <summary className="cursor-pointer text-fg-muted hover:text-fg">Raw event</summary>
        <CodeBlock className="mt-2 max-h-64">{JSON.stringify(event, null, 2)}</CodeBlock>
      </details>
    </div>
  )
}
