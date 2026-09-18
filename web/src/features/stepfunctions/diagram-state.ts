/**
 * What every node and edge of the diagram should look like for a given
 * execution trace and iteration selection. Pure, so the live canvas and the
 * SVG/PNG exporter draw exactly the same picture.
 */
import type { AslModel } from "./asl"
import {
  summarizeNode,
  summarizeTransitions,
  type ExecutionTrace,
  type IterationSelection,
  type NodeStatus,
  type NodeSummary,
} from "./execution-trace"
import type { FlowLayout, LayoutEdge } from "./graph-layout"

/**
 * - plain: no execution on show — the definition only
 * - idle: an execution is on show and never took this edge
 * - taken / catch: taken normally / because the source failed
 * - active: taken, and leads into a state that is running now
 */
export type EdgeState = "plain" | "idle" | "taken" | "catch" | "active"

export interface EdgeView {
  state: EdgeState
  count: number
  /** Whether this edge carried the execution's newest move. */
  newest: boolean
}

export interface DiagramState {
  hasTrace: boolean
  summaries: Map<string, NodeSummary>
  /** Status of the execution as a whole, for the Start and End markers. */
  executionStatus: NodeStatus
  edges: Map<string, EdgeView>
  /** The event id of the newest move, which changes each time the execution moves on. */
  newestMove: number
  laneStatus: (scopeId: string) => NodeStatus
}

export function executionNodeStatus(trace: ExecutionTrace | undefined): NodeStatus {
  switch (trace?.status) {
    case "SUCCEEDED":
      return "succeeded"
    case "FAILED":
    case "TIMED_OUT":
      return "failed"
    case "ABORTED":
      return "aborted"
    case "RUNNING":
      return "running"
    default:
      return "idle"
  }
}

/**
 * Whether the same pair of states is also joined by a Catch edge. When a
 * state both has `Next: X` and a Catch to X, a move taken because of a failure
 * belongs on the Catch edge, not the Next edge.
 */
function hasCatchSibling(edges: LayoutEdge[], edge: LayoutEdge): boolean {
  return edges.some(
    (e) => e !== edge && e.from === edge.from && e.to === edge.to && e.kind === "catch",
  )
}

export function computeDiagramState(
  model: AslModel,
  layout: FlowLayout,
  trace: ExecutionTrace | undefined,
  selection: IterationSelection,
): DiagramState {
  const hasTrace = !!trace && trace.runs.length > 0
  const summaries = new Map<string, NodeSummary>()
  if (trace)
    for (const name of model.states.keys())
      summaries.set(name, summarizeNode(trace, name, selection))

  const moves = summarizeTransitions(trace, selection)
  const newestMove = trace ? Math.max(0, ...trace.transitions.map((t) => t.eventId)) : 0
  const statusOf = (id: string): NodeStatus => summaries.get(id)?.status ?? "idle"

  const edges = new Map<string, EdgeView>()
  for (const e of layout.edges) {
    const move = moves.get(`${e.from}->${e.to}`)
    if (!hasTrace) {
      edges.set(e.id, { state: "plain", count: 0, newest: false })
      continue
    }
    const count =
      e.kind === "catch"
        ? (move?.catchCount ?? 0)
        : (move?.count ?? 0) + (hasCatchSibling(layout.edges, e) ? 0 : (move?.catchCount ?? 0))
    let state: EdgeState
    if (count === 0) state = "idle"
    else if (e.kind === "catch") state = "catch"
    else state = statusOf(e.to) === "running" ? "active" : "taken"
    edges.set(e.id, { state, count, newest: count > 0 && move?.lastEventId === newestMove })
  }

  const laneStatus = (scopeId: string): NodeStatus => {
    const statuses = (model.scopes.get(scopeId)?.states ?? []).map(statusOf)
    for (const s of ["running", "failed", "aborted"] as const) if (statuses.includes(s)) return s
    if (statuses.some((s) => s === "succeeded" || s === "caught")) return "succeeded"
    return "idle"
  }

  return {
    hasTrace,
    summaries,
    executionStatus: executionNodeStatus(trace),
    edges,
    newestMove,
    laneStatus,
  }
}
