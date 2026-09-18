/**
 * The deep-linkable views of the Step Functions pages, kept out of the
 * component files so route files can validate search params against them.
 */

export type ExecutionTab = "timeline" | "events" | "io" | "definition"

const EXECUTION_TABS: readonly string[] = ["timeline", "events", "io", "definition"]

export function isExecutionTab(value: unknown): value is ExecutionTab {
  return typeof value === "string" && EXECUTION_TABS.includes(value)
}

export type StateMachineTab = "executions" | "diagram" | "details"

export function isStateMachineTab(value: unknown): value is StateMachineTab {
  return value === "executions" || value === "diagram" || value === "details"
}
