/**
 * AthenaEnginePanel — the Athena query engine on the Metrics & Health page:
 * its state, the memory it is given, how long it has been up and what it is
 * running, from /_overcast/health's `athenaEngine`. The state is the Athena
 * editor's own chip, so both pages say it the same way.
 *
 * Hidden when Athena is not enabled, which is when health carries no engine.
 */
import { useQuery } from "@tanstack/react-query"
import { EngineStatusChip } from "@/features/athena/components/editor/engine-status"
import { formatBytes, formatDuration } from "@/lib/format"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { healthQueryOptions } from "./data"
import { StatPill } from "./stat-pill"

export function AthenaEnginePanel() {
  const { data: health } = useQuery(healthQueryOptions())
  const engine = health?.athenaEngine
  if (!engine) return null

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className={cn(sectionLabel, "shrink-0 text-fg-muted")}>Athena engine</h2>
        {/* Why a starting or stopped engine is not a fault. */}
        <p className="text-xs text-fg-muted">
          Starts on the first query that needs it, and stops once it has been idle a while.
        </p>
      </div>
      <div className="flex flex-wrap items-stretch gap-2">
        <StatPill label="State" value={<EngineStatusChip status={engine} />} />
        {engine.engine === "trino" && (
          <>
            <StatPill label="Memory" value={formatBytes(engine.memoryBytes ?? 0)} />
            <StatPill
              label="Uptime"
              value={engine.state === "ready" ? formatDuration(engine.uptimeMillis) : "—"}
            />
            <StatPill label="Running queries" value={String(engine.runningQueries)} />
            {/* The tag says which engine; the pinned digest after it says nothing more to a reader. */}
            {engine.image && <StatPill label="Image" value={engine.image.split("@")[0]} />}
          </>
        )}
      </div>
    </div>
  )
}
