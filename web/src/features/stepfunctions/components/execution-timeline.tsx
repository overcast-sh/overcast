/**
 * The execution as a timeline: one bar per state run, on a shared time axis,
 * nested under the Parallel branch or Map iteration it ran in.
 *
 * The event table answers "what happened, in order"; this answers "where did
 * the time go" and "which branch was slow" at a glance. Map iterations fold
 * into collapsible groups so a thousand-item Map is one row until opened.
 */
import { useMemo, useState } from "react"
import { ChevronDown, ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import type { AslModel } from "../asl"
import { formatDuration, runDuration, type ExecutionTrace, type StateRun } from "../execution-trace"
import { STATUS_THEME, stateTypeTheme } from "../state-theme"

interface Props {
  model?: AslModel
  trace: ExecutionTrace
  now: number
  selectedState?: string
  /** Select a run's state, and narrow the diagram to the iteration it ran in. */
  onSelectRun: (run: StateRun) => void
}

type Row =
  | { kind: "run"; key: string; run: StateRun; depth: number }
  | {
      kind: "group"
      key: string
      label: string
      depth: number
      start: number
      end?: number
      status: StateRun["status"]
      count: number
      collapsed: boolean
    }

/** Groups this big start folded, so the first view of a large Map stays readable. */
const AUTO_COLLAPSE_OVER = 4
const MAX_CHILDREN_SHOWN = 200

function groupStatus(runs: StateRun[]): StateRun["status"] {
  if (runs.some((r) => r.status === "running")) return "running"
  if (runs.some((r) => r.status === "failed")) return "failed"
  if (runs.some((r) => r.status === "aborted")) return "aborted"
  if (runs.some((r) => r.status === "caught")) return "caught"
  return "succeeded"
}

export function ExecutionTimeline({ model, trace, now, selectedState, onSelectRun }: Props) {
  const [toggled, setToggled] = useState<Set<string>>(new Set())
  const start = trace.start ?? trace.runs.at(0)?.start ?? now
  const end = trace.end ?? now
  const span = Math.max(1, end - start)

  const rows = useMemo(() => {
    const children = new Map<string | undefined, StateRun[]>()
    for (const run of trace.runs) {
      const list = children.get(run.parentKey) ?? []
      list.push(run)
      children.set(run.parentKey, list)
    }
    const out: Row[] = []
    const isCollapsed = (key: string, auto: boolean) => (toggled.has(key) ? !auto : auto)

    const emit = (runs: StateRun[], depth: number) => {
      for (const run of runs.slice(0, MAX_CHILDREN_SHOWN)) {
        out.push({ kind: "run", key: run.key, run, depth })
        const inner = children.get(run.key)
        if (!inner?.length) continue
        // Group the container's children by Map iteration or Parallel branch.
        const groups = new Map<string, { label: string; runs: StateRun[]; order: number }>()
        for (const child of inner) {
          let groupKey: string
          let label: string
          let order: number
          if (run.type === "Map") {
            const frame = child.iterationPath.at(-1)
            order = frame?.index ?? 0
            groupKey = `${run.key}:i${order}`
            label = `Iteration #${order}`
          } else {
            const branch = model?.scopes.get(child.scopeId)?.branchIndex ?? 0
            order = branch
            groupKey = `${run.key}:b${branch}`
            label = `Branch ${branch + 1}`
          }
          const g = groups.get(groupKey) ?? { label, runs: [], order }
          g.runs.push(child)
          groups.set(groupKey, g)
        }
        const sorted = [...groups.entries()].sort((a, b) => a[1].order - b[1].order)
        const autoCollapse = run.type === "Map" && sorted.length > AUTO_COLLAPSE_OVER
        for (const [groupKey, g] of sorted.slice(0, MAX_CHILDREN_SHOWN)) {
          const collapsed = isCollapsed(groupKey, autoCollapse)
          const ends = g.runs.map((r) => r.end)
          out.push({
            kind: "group",
            key: groupKey,
            label: g.label,
            depth: depth + 1,
            start: Math.min(...g.runs.map((r) => r.start)),
            end: ends.some((e) => e === undefined) ? undefined : Math.max(...(ends as number[])),
            status: groupStatus(g.runs),
            count: g.runs.length,
            collapsed,
          })
          if (!collapsed) emit(g.runs, depth + 2)
        }
      }
    }
    emit(children.get(undefined) ?? [], 0)
    return out
  }, [trace, model, toggled])

  const toggle = (key: string) =>
    setToggled((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  if (trace.runs.length === 0) {
    return <p className="py-8 text-center text-sm text-fg-muted">No states have run yet.</p>
  }

  const ticks = [0, 0.25, 0.5, 0.75, 1]
  const barStyle = (from: number, to: number | undefined) => {
    const left = ((from - start) / span) * 100
    const width = (((to ?? now) - from) / span) * 100
    return { left: `${Math.max(0, left)}%`, width: `max(3px, ${Math.max(0, width)}%)` }
  }

  return (
    <div
      className="overflow-x-auto rounded-lg border border-border bg-bg-elevated"
      role="table"
      aria-label="Execution timeline"
    >
      <div className="min-w-160">
        <div
          role="row"
          className="sticky top-0 z-10 flex border-b border-border bg-bg-elevated text-2xs text-fg-subtle"
        >
          <div
            role="columnheader"
            className="w-72 shrink-0 px-3 py-2 font-medium tracking-wider uppercase"
          >
            State
          </div>
          <div role="columnheader" className="relative mr-20 flex-1 py-2">
            {ticks.map((t) => (
              <span
                key={t}
                className="absolute -translate-x-1/2 font-mono whitespace-nowrap tabular-nums first:translate-x-0 last:-translate-x-full"
                style={{ left: `${t * 100}%` }}
              >
                {formatDuration(span * t)}
              </span>
            ))}
          </div>
        </div>
        <div role="rowgroup">
          {rows.map((row) => {
            if (row.kind === "group") {
              const color = STATUS_THEME[row.status].color
              return (
                <div
                  role="row"
                  key={row.key}
                  className="flex items-center border-b border-border/50 bg-bg-muted/40 text-xs"
                >
                  <div
                    role="cell"
                    className="w-72 shrink-0 py-1 pr-2"
                    style={{ paddingLeft: 12 + row.depth * 14 }}
                  >
                    <button
                      type="button"
                      onClick={() => toggle(row.key)}
                      aria-expanded={!row.collapsed}
                      className="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left font-mono text-2xs text-fg-muted hover:bg-bg-muted hover:text-fg"
                    >
                      {row.collapsed ? (
                        <ChevronRight className="h-3.5 w-3.5" />
                      ) : (
                        <ChevronDown className="h-3.5 w-3.5" />
                      )}
                      {row.label}
                      <span className="ml-auto text-fg-subtle">
                        {row.count} state{row.count === 1 ? "" : "s"}
                      </span>
                    </button>
                  </div>
                  <div role="cell" className="relative mr-20 h-6 flex-1">
                    <div
                      className="absolute top-1/2 h-1.5 -translate-y-1/2 rounded-full"
                      style={{
                        ...barStyle(row.start, row.end),
                        background: `color-mix(in oklab, ${color} 55%, transparent)`,
                      }}
                    />
                  </div>
                </div>
              )
            }
            const { run } = row
            const theme = stateTypeTheme(run.type)
            const Icon = theme.icon
            const status = STATUS_THEME[run.status]
            const duration = formatDuration(runDuration(run, now))
            const selected = selectedState === run.name
            return (
              <div
                role="row"
                key={row.key}
                className={cn(
                  "group flex cursor-pointer items-center border-b border-border/50 text-xs transition-colors last:border-0 hover:bg-bg-muted/60",
                  selected && "bg-accent-muted/50",
                )}
                onClick={() => onSelectRun(run)}
              >
                <div
                  role="cell"
                  className="flex w-72 shrink-0 items-center gap-2 py-1.5 pr-2"
                  style={{ paddingLeft: 12 + row.depth * 14 }}
                >
                  <Icon className="h-3.5 w-3.5 shrink-0" style={{ color: theme.color }} />
                  <button
                    type="button"
                    className="min-w-0 truncate text-left font-medium text-fg group-hover:text-accent"
                    title={run.name}
                    onClick={(e) => {
                      e.stopPropagation()
                      onSelectRun(run)
                    }}
                  >
                    {run.name}
                  </button>
                  {run.attempts > 1 && (
                    <span
                      className="shrink-0 rounded-full bg-warning-muted px-1.5 font-mono text-2xs text-warning"
                      title="Attempts"
                    >
                      ×{run.attempts}
                    </span>
                  )}
                </div>
                <div
                  role="cell"
                  className="relative mr-20 h-7 flex-1"
                  title={`${run.name} — ${status.label}, ${duration}`}
                >
                  {ticks.slice(1, -1).map((t) => (
                    <span
                      key={t}
                      className="absolute inset-y-0 w-px bg-border/50"
                      style={{ left: `${t * 100}%` }}
                    />
                  ))}
                  <div
                    className={cn(
                      "absolute top-1/2 h-3.5 -translate-y-1/2 rounded-sm",
                      run.status === "running" && "oc-sfn-pulse",
                    )}
                    style={{ ...barStyle(run.start, run.end), background: status.color }}
                  />
                  <span
                    className="absolute top-1/2 ml-1.5 -translate-y-1/2 font-mono text-2xs whitespace-nowrap text-fg-muted tabular-nums"
                    style={{
                      left: `calc(${barStyle(run.start, run.end).left} + ${barStyle(run.start, run.end).width})`,
                    }}
                  >
                    {duration}
                  </span>
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
