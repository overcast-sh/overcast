import type { AslState } from "./asl"
import type { DiagramState } from "./diagram-state"
import type { NodeStatus } from "./execution-trace"

/**
 * The caption on a container's lane: "Branch 2", "Item processor" for an
 * inline Map, and "Child execution" for a distributed one, whose states run
 * in child executions rather than in this execution's history.
 */
export function laneLabel(container: AslState, index: number): string {
  if (container.type !== "Map") return `Branch ${index + 1}`
  return container.distributed ? "Child execution — per item" : "Item processor"
}

/**
 * The Start marker is reached as soon as anything ran; the End marker takes
 * the execution's final status once there is one.
 */
export function pillStatus(kind: "start" | "end", view: DiagramState): NodeStatus {
  if (kind === "start") return view.hasTrace ? "succeeded" : "idle"
  return view.executionStatus === "running" ? "idle" : view.executionStatus
}
