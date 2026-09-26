/**
 * A query execution's lifecycle as AWS reports it: `QUEUED` → `RUNNING` →
 * `SUCCEEDED`, `FAILED` or `CANCELLED`.
 */

const FINISHED = new Set<string>(["SUCCEEDED", "FAILED", "CANCELLED"])

/** Whether the execution has stopped for good. An unknown state has not. */
export function isFinished(state: string | undefined): boolean {
  return state !== undefined && FINISHED.has(state)
}
