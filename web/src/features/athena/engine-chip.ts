import type { BadgeProps } from "@/components/ui/badge"
import { formatDuration } from "@/lib/format"
import type { AthenaEngineStatus } from "@/types"

/**
 * The query engine's state as the editor's chip says it. The engine is
 * Overcast's, not AWS's — Athena has no such thing to show — so the chip is
 * the place a ten-second cold start reads as progress rather than a hang,
 * and inert mode reads as a setting rather than a broken query.
 */
export interface EngineChip {
  label: string
  /** A second clause: the step, its time, or why. */
  detail?: string
  tone: NonNullable<BadgeProps["variant"]>
  /** On its way somewhere: the chip shows the busy caret. */
  busy: boolean
}

/** Whether queries run inert: they succeed with no rows and DDL still reaches Glue. */
export function isInert(status: AthenaEngineStatus | undefined): boolean {
  return status?.state === "off"
}

export function engineChip(status: AthenaEngineStatus, now: number): EngineChip {
  switch (status.state) {
    case "off":
      return {
        label: "Engine off",
        detail: status.engine === "inert" ? "ATHENA_ENGINE=inert" : "no Docker",
        tone: "warning",
        busy: false,
      }
    case "probing":
      return { label: "Engine", detail: "looking for Docker", tone: "default", busy: true }
    case "stopped":
      return {
        label: "Engine stopped",
        detail: "starts on demand",
        tone: "default",
        busy: false,
      }
    case "pulling":
      return { label: "Starting engine", detail: "pulling image", tone: "accent", busy: true }
    case "starting": {
      const since = status.startedAt ? now - Date.parse(status.startedAt) : undefined
      return {
        label: "Starting engine",
        detail:
          since !== undefined && since >= 0 ? `starting · ${formatDuration(since)}` : "starting",
        tone: "accent",
        busy: true,
      }
    }
    case "ready":
      return {
        label: "Engine ready",
        detail: status.startMillis ? `started in ${formatDuration(status.startMillis)}` : undefined,
        tone: "success",
        busy: false,
      }
    case "failed":
      return { label: "Engine failed", detail: status.lastError, tone: "danger", busy: false }
  }
}
