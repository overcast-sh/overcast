/**
 * AthenaWorkgroupNode — an Athena workgroup on the map: its latest queries,
 * the query engine's state while it cannot run one yet, and *Run query*.
 *
 * A query row runs through its states in order, each on screen for at least
 * a moment (see `runDisplay`), pulses while it runs, flashes if it fails, and
 * once its outcome has been seen settles back as a ghost — the SQS row model.
 */

import { memo, useEffect, useState } from "react"
import type { NodeProps } from "@xyflow/react"
import { Badge, type BadgeProps } from "@/components/ui/badge"
import { formatDuration } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { TopologyQueryRun } from "@/types"
import {
  areDataLakeNodePropsEqual,
  useDataLakeOverlay,
  type DataLakeNodeData,
} from "./data-lake-context"
import { RUN_ROWS } from "./data-lake-layout"
import { DataLakeCard } from "./data-lake-node"
import { runDisplay, type RunDisplay } from "./data-lake-overlay"
import { athenaExecutionRoute, nodeRoute, useNodeNavigation } from "./node-route"
import { RunQueryPopover } from "./run-query-popover"

/** The query engine's states that keep a query from running on it straight away. */
const ENGINE_CHIPS: Record<string, { label: string; variant: NonNullable<BadgeProps["variant"]> }> =
  {
    off: { label: "engine off", variant: "warning" },
    probing: { label: "engine starting", variant: "accent" },
    pulling: { label: "engine starting", variant: "accent" },
    starting: { label: "engine starting", variant: "accent" },
    failed: { label: "engine failed", variant: "danger" },
  }

export const AthenaWorkgroupNode = memo(function AthenaWorkgroupNode({ data }: NodeProps) {
  const nodeData = data as DataLakeNodeData
  const node = nodeData.topologyNode
  const runs = (node.recentQueries ?? []).slice(0, RUN_ROWS)
  const { runs: steps } = useDataLakeOverlay()
  const now = useRunClock(runs.map((r) => (at) => runDisplay(steps[r.id], r.state, at)))
  const chip = node.engineState ? ENGINE_CHIPS[node.engineState] : undefined

  return (
    <DataLakeCard
      data={nodeData}
      route={nodeRoute({ service: node.service, label: node.label })}
      subtitle={
        <>
          <span>Workgroup</span>
          {chip && (
            <Badge variant={chip.variant} className="px-1 py-0">
              {chip.label}
            </Badge>
          )}
        </>
      }
      action={
        <RunQueryPopover workGroup={node.label} lastRun={runs[0]} engineState={node.engineState} />
      }
    >
      {runs.length > 0 && (
        <div className="map-detail -mx-1.5 mt-1.5 flex flex-col">
          {runs.map((run) => (
            <QueryRunRow
              key={run.id}
              run={run}
              display={runDisplay(steps[run.id], run.state, now)}
              region={node.region}
            />
          ))}
        </div>
      )}
    </DataLakeCard>
  )
}, areDataLakeNodePropsEqual)

const STATE_DOT: Record<string, string> = {
  QUEUED: "bg-fg-subtle",
  RUNNING: "bg-accent animate-pulse",
  SUCCEEDED: "bg-success",
  FAILED: "bg-danger",
  CANCELLED: "bg-warning",
}

/** How long the fail flash takes (ms). */
const FAIL_FLASH_MS = 1_800

function QueryRunRow({
  run,
  display,
  region,
}: {
  run: TopologyQueryRun
  display: RunDisplay
  region: string
}) {
  const { open, openInNewTab } = useNodeNavigation(athenaExecutionRoute(run.id), region)
  const finished = display.state !== "QUEUED" && display.state !== "RUNNING"
  const settled = finished && !display.fresh
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        open()
      }}
      onMouseDown={openInNewTab}
      title={`${display.state.toLowerCase()} · ${run.query}`}
      className={cn(
        "flex h-6 w-full items-center gap-2 rounded px-1.5 text-left text-xs transition-opacity duration-500 hover:bg-bg-muted focus-visible:bg-bg-muted",
        settled && "opacity-50 hover:opacity-100",
      )}
      style={
        display.fresh && display.state === "FAILED"
          ? { animation: `overcastFailFlash ${FAIL_FLASH_MS}ms ease-out` }
          : undefined
      }
    >
      <span
        aria-label={display.state.toLowerCase()}
        className={cn("size-1.5 shrink-0 rounded-full", STATE_DOT[display.state] ?? "bg-fg-subtle")}
      />
      <span className="min-w-0 flex-1 truncate font-mono">{run.query}</span>
      <span className="shrink-0 text-2xs text-fg-subtle tabular-nums">
        {finished && run.completedAt
          ? formatDuration(run.completedAt - run.submittedAt)
          : display.state.toLowerCase()}
      </span>
    </button>
  )
}

/**
 * The time the rows are drawn at. It moves on when a row's display next
 * changes by itself — a dwelling state moving on, a fresh outcome settling —
 * so a row never waits on anything else to re-render. A state that arrives
 * in between is drawn at once (see `runDisplay`) and schedules its own next
 * change.
 */
function useRunClock(displays: ((now: number) => RunDisplay)[]): number {
  const [now, setNow] = useState(() => Date.now())
  const next = Math.min(
    ...displays.map((display) => display(now).nextChangeAt ?? Number.POSITIVE_INFINITY),
  )
  useEffect(() => {
    if (!Number.isFinite(next)) return
    const t = setTimeout(() => setNow(Date.now()), Math.max(0, next - Date.now()))
    return () => clearTimeout(t)
  }, [next])
  return now
}
