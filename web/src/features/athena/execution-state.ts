import type { QueryExecution } from "@aws-sdk/client-athena"
import type { BadgeProps } from "@/components/ui/badge"

/**
 * A query execution's lifecycle as AWS reports it: `QUEUED` → `RUNNING` →
 * `SUCCEEDED`, `FAILED` or `CANCELLED`.
 */

const FINISHED = new Set<string>(["SUCCEEDED", "FAILED", "CANCELLED"])

/** Whether the execution has stopped for good. An unknown state has not. */
export function isFinished(state: string | undefined): boolean {
  return state !== undefined && FINISHED.has(state)
}

const STATE_BADGE: Partial<Record<string, NonNullable<BadgeProps["variant"]>>> = {
  QUEUED: "default",
  RUNNING: "accent",
  SUCCEEDED: "success",
  FAILED: "danger",
  CANCELLED: "warning",
}

export function stateBadge(state: string | undefined): NonNullable<BadgeProps["variant"]> {
  return (state === undefined ? undefined : STATE_BADGE[state]) ?? "default"
}

/**
 * How long the execution has taken, in ms: to its completion when it has
 * finished, else to `now`. Null before it has a submission time.
 */
export function elapsedMs(execution: QueryExecution | undefined, now: number): number | null {
  const submitted = execution?.Status?.SubmissionDateTime
  if (!submitted) return null
  const completed = execution.Status?.CompletionDateTime
  const end = completed ?? (isFinished(execution.Status?.State) ? submitted : new Date(now))
  return Math.max(end.getTime() - submitted.getTime(), 0)
}
