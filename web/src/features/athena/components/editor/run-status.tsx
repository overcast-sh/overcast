import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import * as PopoverPrimitive from "@radix-ui/react-popover"
import type { QueryExecution, QueryRuntimeStatisticsRows } from "@aws-sdk/client-athena"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { BlinkingCursor } from "@/components/ui/skeleton"
import { useNow } from "@/hooks/use-now"
import { formatBytes, formatCount, formatDuration } from "@/lib/format"
import type { AthenaEngineStatus } from "@/types"
import { runtimeStatisticsQueryOptions } from "../../data"
import { ddlSummary } from "../../ddl-summary"
import { engineChip } from "../../engine-chip"
import { elapsedMs, isFinished, stateBadge } from "../../execution-state"
import { qualifiedName } from "../../sql-text"

/**
 * The line under the editor: the execution's state as AWS reports it, its
 * ticking elapsed time, the engine's start-up while the query waits on it,
 * and once it finishes the statistics strip — or, for DDL, what it did.
 */
export function RunStatus({
  execution,
  engine,
}: {
  execution: QueryExecution
  engine: AthenaEngineStatus | undefined
}) {
  const state = execution.Status?.State
  const finished = isFinished(state)
  const now = useNow(!finished, 250)
  const elapsed = elapsedMs(execution, now)
  const waitingOnEngine = !finished && engine && ["pulling", "starting"].includes(engine.state)

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-2xs text-fg-muted">
      <span className="inline-flex items-center gap-1.5">
        <Badge variant={stateBadge(state)}>{state ?? "SUBMITTED"}</Badge>
        {!finished && <BlinkingCursor />}
      </span>
      {elapsed !== null && <span className="tabular-nums">{formatDuration(elapsed)}</span>}
      {waitingOnEngine && <EngineProgress engine={engine} now={now} />}
      {state === "SUCCEEDED" && <StatisticsStrip execution={execution} />}
      {state === "CANCELLED" && <span>Stopped</span>}
    </div>
  )
}

function EngineProgress({ engine, now }: { engine: AthenaEngineStatus; now: number }) {
  const chip = engineChip(engine, now)
  return (
    <span className="text-accent">
      {chip.label}
      {chip.detail && ` · ${chip.detail}`}
    </span>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span>
      <span className="text-fg-subtle">{label} </span>
      <span className="text-fg tabular-nums">{value}</span>
    </span>
  )
}

function StatisticsStrip({ execution }: { execution: QueryExecution }) {
  const id = execution.QueryExecutionId ?? ""
  const { data: runtime } = useQuery(runtimeStatisticsQueryOptions(id))
  const stats = execution.Statistics
  const rows = runtime?.Rows?.OutputRows
  return (
    <>
      <Stat label="run" value={formatDuration(stats?.EngineExecutionTimeInMillis)} />
      <Stat label="scanned" value={formatBytes(stats?.DataScannedInBytes ?? 0)} />
      {rows !== undefined && <Stat label="rows" value={formatCount(rows)} />}
      {execution.StatementType && <Stat label="type" value={execution.StatementType} />}
      <StatisticsDetails stats={stats} rows={runtime?.Rows} />
    </>
  )
}

const TIMINGS: [string, keyof NonNullable<QueryExecution["Statistics"]>][] = [
  ["Queued", "QueryQueueTimeInMillis"],
  ["Pre-processing", "ServicePreProcessingTimeInMillis"],
  ["Planning", "QueryPlanningTimeInMillis"],
  ["Engine", "EngineExecutionTimeInMillis"],
  ["Post-processing", "ServiceProcessingTimeInMillis"],
  ["Total", "TotalExecutionTimeInMillis"],
]

/** *Details*: the whole `Statistics`, and the rows `GetQueryRuntimeStatistics` counted. */
function StatisticsDetails({
  stats,
  rows,
}: {
  stats: QueryExecution["Statistics"]
  rows: QueryRuntimeStatisticsRows | undefined
}) {
  return (
    <PopoverPrimitive.Root>
      <PopoverPrimitive.Trigger asChild>
        <Button variant="link" size="sm" className="text-2xs">
          Details
        </Button>
      </PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          align="start"
          sideOffset={6}
          collisionPadding={12}
          aria-label="Query statistics"
          className="@container z-50 w-80 rounded-card border border-border bg-bg-elevated p-3 shadow-xl"
        >
          <DefinitionList columns={2} layout="stacked">
            {TIMINGS.map(([label, key]) => (
              <Definition
                key={key}
                label={label}
                value={formatDuration(stats?.[key] as number | undefined)}
              />
            ))}
            <Definition label="Data scanned" value={formatBytes(stats?.DataScannedInBytes ?? 0)} />
            <Definition
              label="Rows out"
              value={rows?.OutputRows !== undefined ? formatCount(rows.OutputRows) : undefined}
            />
            <Definition
              label="Rows in"
              value={rows?.InputRows !== undefined ? formatCount(rows.InputRows) : undefined}
            />
            <Definition
              label="Bytes in"
              value={rows?.InputBytes !== undefined ? formatBytes(rows.InputBytes) : undefined}
            />
          </DefinitionList>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}

/**
 * What a finished DDL or DML statement did, in one line: *Created table
 * `sales.orders`*, linked to the table in Glue, or *3 rows affected*.
 */
export function StatementOutcome({
  execution,
  updateCount,
}: {
  execution: QueryExecution
  updateCount: number | undefined
}) {
  if (execution.Status?.State !== "SUCCEEDED") return null
  if (execution.StatementType === "DML" && updateCount !== undefined) {
    return <p className="font-mono text-xs text-fg">{formatCount(updateCount)} rows affected</p>
  }
  if (execution.StatementType !== "DDL") return null
  const summary = ddlSummary(
    execution.Query ?? "",
    execution.QueryExecutionContext?.Database ?? "default",
  )
  if (!summary) return null
  const label = summary.name
    ? qualifiedName(summary.database, summary.name)
    : qualifiedName(summary.database)
  // Only AwsDataCatalog's tables are Glue's, where the link goes.
  const inGlue = (execution.QueryExecutionContext?.Catalog ?? "AwsDataCatalog") === "AwsDataCatalog"
  const linked = inGlue && summary.verb !== "Dropped" && summary.kind !== "view"
  return (
    <p className="font-mono text-xs text-fg">
      {summary.verb} {summary.kind}{" "}
      {linked && summary.name ? (
        <Link
          to="/glue/$database/$table"
          params={{ database: summary.database, table: summary.name }}
          className="text-accent hover:underline"
        >
          {label}
        </Link>
      ) : linked ? (
        <Link
          to="/glue/$database"
          params={{ database: summary.database }}
          className="text-accent hover:underline"
        >
          {label}
        </Link>
      ) : (
        label
      )}
    </p>
  )
}
