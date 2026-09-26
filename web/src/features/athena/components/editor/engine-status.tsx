import { Badge } from "@/components/ui/badge"
import { Advisory } from "@/components/ui/advisory"
import { BlinkingCursor } from "@/components/ui/skeleton"
import { useNow } from "@/hooks/use-now"
import type { AthenaEngineStatus } from "@/types"
import { engineChip } from "../../engine-chip"

const ENGINE_DOCS = "services/athena/limitations.md#the-engine"

/** The engine's state in the editor's toolbar: starting, ready, stopped or off. */
export function EngineStatusChip({ status }: { status: AthenaEngineStatus }) {
  const now = useNow(status.state === "starting", 500)
  const chip = engineChip(status, now)
  return (
    <span
      title={[chip.label, chip.detail].filter(Boolean).join(" · ")}
      className="inline-flex min-w-0 items-center gap-1.5"
    >
      <Badge variant={chip.tone} className="max-w-72 truncate">
        {/* Only the state is announced: the detail ticks while the engine starts. */}
        <span role="status">{chip.label}</span>
        {chip.detail && <span className="normal-case opacity-80">&nbsp;· {chip.detail}</span>}
      </Badge>
      {chip.busy && <BlinkingCursor />}
    </span>
  )
}

/**
 * Inert mode, said where it matters: above the editor, for as long as it
 * lasts. Queries still run — control-plane flows can be tested — but return
 * no rows.
 */
export function InertEngineAdvisory({ status }: { status: AthenaEngineStatus }) {
  const bySetting = status.engine === "inert"
  return (
    <Advisory title="The query engine is off: queries succeed with no rows" docsPath={ENGINE_DOCS}>
      {bySetting ? (
        <>
          <code className="font-mono">ATHENA_ENGINE=inert</code> is set. DDL still updates the Glue
          Data Catalog. Unset it and restart Overcast to run SQL on Trino.
        </>
      ) : (
        <>
          No Docker daemon is connected, so there is nowhere to run the engine. DDL still updates
          the Glue Data Catalog. Start Docker and restart Overcast to run SQL on Trino.
        </>
      )}
    </Advisory>
  )
}
