import type { DiagramState } from "./diagram-state"
import type { NodeStatus } from "./execution-trace"

/** The caption on a container's lane: "Branch 2", or "Item processor" for a Map. */
export function laneLabel(containerType: string, index: number): string {
  return containerType === "Map" ? "Item processor" : `Branch ${index + 1}`
}

/**
 * The Start marker is reached as soon as anything ran; the End marker takes
 * the execution's final status once there is one.
 */
export function pillStatus(kind: "start" | "end", view: DiagramState): NodeStatus {
  if (kind === "start") return view.hasTrace ? "succeeded" : "idle"
  return view.executionStatus === "running" ? "idle" : view.executionStatus
}
