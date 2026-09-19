/**
 * Presentation helpers shared by the Step Functions views.
 */

type BadgeVariant = "default" | "success" | "danger" | "warning" | "info"

/** Maps an execution status to the badge colour that reads correctly for it. */
export function executionStatusVariant(status: string | undefined): BadgeVariant {
  switch (status) {
    case "SUCCEEDED":
      return "success"
    case "RUNNING":
      return "info"
    case "FAILED":
    case "TIMED_OUT":
      return "danger"
    case "ABORTED":
      return "warning"
    default:
      return "default"
  }
}

/** Maps a history event type to a badge colour by what it means for the run. */
export function historyEventVariant(type: string | undefined): BadgeVariant {
  if (!type) return "default"
  if (type.endsWith("Failed") || type.endsWith("TimedOut") || type.endsWith("Aborted")) {
    return "danger"
  }
  if (type.endsWith("Succeeded")) return "success"
  if (type.endsWith("Started") || type.endsWith("Scheduled")) return "info"
  return "default"
}

export type EventCategory = "states" | "tasks" | "errors" | "flow"

/**
 * Sorts a history event type into what a reader filters by: a state moving,
 * a task call, Map/Parallel control flow, or something going wrong.
 */
export function eventCategory(type: string): EventCategory {
  if (/(Failed|TimedOut|Aborted)$/.test(type)) return "errors"
  if (/State(Entered|Exited)$/.test(type) || type.startsWith("Execution")) return "states"
  if (/^(Map|Parallel)/.test(type)) return "flow"
  return "tasks"
}

/** "TaskStateEntered" → "Task state entered", so a column of event types reads as words. */
export function humanizeEventType(type: string | undefined): string {
  if (!type) return ""
  const words = type.replace(/([a-z0-9])([A-Z])/g, "$1 $2").split(" ")
  return words.map((w, i) => (i === 0 ? w : w.toLowerCase())).join(" ")
}

/** Formats an SDK timestamp for a table cell, or an em dash when absent. */
export function formatTimestamp(value: Date | undefined): string {
  if (!value) return "—"
  return value.toLocaleString()
}

/** Pretty-prints a JSON payload string, leaving non-JSON text untouched. */
export function prettyJSON(value: string | undefined): string {
  if (!value) return ""
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

/**
 * Extracts the error and cause an event carries, whichever of the many typed
 * detail fields it arrived in. This is what surfaces an Overcast
 * "not supported" failure to the user instead of a bare FAILED.
 */
export function historyEventFailure(event: Record<string, unknown>): {
  error: string
  cause: string
} {
  for (const [key, value] of Object.entries(event)) {
    if (!key.endsWith("EventDetails") || typeof value !== "object" || value === null) continue
    const details = value as { error?: string; cause?: string }
    if (details.error || details.cause) {
      return { error: details.error ?? "", cause: details.cause ?? "" }
    }
  }
  return { error: "", cause: "" }
}
