/**
 * Lays an ASL model out as a top-to-bottom flow diagram.
 *
 * Each scope — the top level, every Parallel branch, a Map's item processor —
 * is laid out on its own with dagre, innermost first. A Parallel or Map then
 * becomes one large node in its parent's layout, sized to hold its branches
 * side by side, so a split reads as a fork into lanes and a join back out
 * rather than as a tangle of long edges. Edges carry their own routed points,
 * in absolute coordinates, so the renderer never has to guess a path.
 */
import dagre from "@dagrejs/dagre"
import { ROOT_SCOPE, type AslModel, type TransitionKind } from "./asl"

export const STATE_WIDTH = 224
export const STATE_HEIGHT = 56
export const PILL_WIDTH = 88
export const PILL_HEIGHT = 30
export const CONTAINER_HEADER = 52
const CONTAINER_PAD_X = 20
const CONTAINER_PAD_BOTTOM = 18
const FORK_GAP = 34
const JOIN_GAP = 30
const BRANCH_GAP = 32
const RANK_SEP = 46
const NODE_SEP = 40

/** Pseudo-node ids for the diagram's own start and end markers. */
export const START_ID = "__start__"
export const END_ID = "__end__"

/** Where a branch's fork edge begins, and where its join edge ends. */
export const forkId = (scopeId: string) => `__fork__:${scopeId}`
export const joinId = (scopeId: string) => `__join__:${scopeId}`

export interface Point {
  x: number
  y: number
}

export type LayoutNodeKind = "state" | "container" | "start" | "end"

export interface LayoutNode {
  /** State name, or START_ID / END_ID. */
  id: string
  kind: LayoutNodeKind
  x: number
  y: number
  width: number
  height: number
  /** Nesting depth: 0 at the top level. */
  depth: number
  /** For containers: the rectangle of each branch lane, in absolute coordinates. */
  lanes?: Array<{ scopeId: string; x: number; y: number; width: number; height: number }>
}

export type LayoutEdgeKind = TransitionKind | "start" | "end" | "fork" | "join"

export interface LayoutEdge {
  id: string
  /** State name, START_ID, or forkId(scope). */
  from: string
  /** State name, END_ID, or joinId(scope). */
  to: string
  kind: LayoutEdgeKind
  label?: string
  points: Point[]
  labelPosition?: Point
  /** The real nodes the edge hangs off, for the renderer's bookkeeping. */
  sourceNode: string
  targetNode: string
  depth: number
}

export interface FlowLayout {
  nodes: LayoutNode[]
  edges: LayoutEdge[]
  width: number
  height: number
}

interface ScopeLayout {
  width: number
  height: number
  nodes: LayoutNode[]
  edges: LayoutEdge[]
}

const LABEL_HEIGHT = 18
const MAX_LABEL_CHARS = 34

/** Truncates an edge label to what fits on the canvas; the full text stays in the tooltip. */
export function shortLabel(label: string): string {
  return label.length > MAX_LABEL_CHARS ? `${label.slice(0, MAX_LABEL_CHARS - 1)}…` : label
}

function labelWidth(label: string): number {
  return Math.min(label.length, MAX_LABEL_CHARS) * 6.2 + 16
}

export function layoutModel(model: AslModel): FlowLayout {
  const root = layoutScope(model, ROOT_SCOPE, 0)
  return { nodes: root.nodes, edges: root.edges, width: root.width, height: root.height }
}

function layoutScope(model: AslModel, scopeId: string, depth: number): ScopeLayout {
  const scope = model.scopes.get(scopeId)
  const isRoot = scopeId === ROOT_SCOPE
  const g = new dagre.graphlib.Graph({ multigraph: true })
  g.setGraph({
    rankdir: "TB",
    ranksep: RANK_SEP,
    nodesep: NODE_SEP,
    edgesep: 18,
    marginx: 0,
    marginy: 0,
  })
  g.setDefaultEdgeLabel(() => ({}))

  // The scope's entry and exit: visible Start/End markers at the top level,
  // and 1px anchors in a branch that the container's fork and join attach to.
  const entry = isRoot ? START_ID : forkId(scopeId)
  const exit = isRoot ? END_ID : joinId(scopeId)
  g.setNode(entry, isRoot ? { width: PILL_WIDTH, height: PILL_HEIGHT } : { width: 1, height: 1 })

  const children = new Map<string, ScopeLayout & { lanes: NonNullable<LayoutNode["lanes"]> }>()
  const stateNames = scope?.states ?? []
  for (const name of stateNames) {
    const state = model.states.get(name)
    if (!state) continue
    if (state.childScopes.length > 0) {
      const inner = layoutContainer(model, state.childScopes, depth + 1)
      children.set(name, inner)
      g.setNode(name, { width: inner.width, height: inner.height })
    } else {
      g.setNode(name, { width: STATE_WIDTH, height: STATE_HEIGHT })
    }
  }

  const hasTerminal = stateNames.some((n) => model.states.get(n)?.terminal)
  if (isRoot || hasTerminal) {
    g.setNode(exit, isRoot ? { width: PILL_WIDTH, height: PILL_HEIGHT } : { width: 1, height: 1 })
  }

  type Pending = { name: string; from: string; to: string; kind: LayoutEdgeKind; label?: string }
  const pending: Pending[] = []
  const addEdge = (edge: Omit<Pending, "name">) => {
    const name = `${edge.kind}:${pending.length}`
    pending.push({ ...edge, name })
    const labelled = edge.label && edge.kind !== "fork" && edge.kind !== "join"
    g.setEdge(
      edge.from,
      edge.to,
      labelled ? { width: labelWidth(edge.label ?? ""), height: LABEL_HEIGHT, labelpos: "c" } : {},
      name,
    )
  }

  if (scope?.startAt && g.hasNode(scope.startAt)) {
    addEdge({ from: entry, to: scope.startAt, kind: isRoot ? "start" : "fork" })
  }
  for (const name of stateNames) {
    const state = model.states.get(name)
    if (!state) continue
    for (const t of state.transitions) {
      // An edge to a state in another scope is a definition error; the issue
      // list reports it, and drawing it would cut across the lanes.
      if (!g.hasNode(t.to) || model.states.get(t.to)?.scopeId !== scopeId) continue
      addEdge({ from: name, to: t.to, kind: t.kind, label: t.label })
    }
    if (state.terminal && g.hasNode(exit))
      addEdge({ from: name, to: exit, kind: isRoot ? "end" : "join" })
  }

  dagre.layout(g)
  const graph = g.graph()
  const width = Math.max(graph.width ?? 0, isRoot ? PILL_WIDTH : 1)
  const height = Math.max(graph.height ?? 0, isRoot ? PILL_HEIGHT : 1)

  const nodes: LayoutNode[] = []
  const boxes = new Map<string, { x: number; y: number; width: number; height: number }>()
  for (const id of g.nodes()) {
    const n = g.node(id)
    const box = { x: n.x - n.width / 2, y: n.y - n.height / 2, width: n.width, height: n.height }
    boxes.set(id, box)
    if (id === entry || id === exit) {
      if (isRoot) nodes.push({ id, kind: id === START_ID ? "start" : "end", depth, ...box })
      continue
    }
    const inner = children.get(id)
    if (inner) {
      nodes.push({
        id,
        kind: "container",
        depth,
        ...box,
        lanes: inner.lanes.map((lane) => ({ ...lane, x: lane.x + box.x, y: lane.y + box.y })),
      })
      for (const child of inner.nodes) {
        nodes.push({
          ...child,
          x: child.x + box.x,
          y: child.y + box.y,
          lanes: child.lanes?.map((lane) => ({ ...lane, x: lane.x + box.x, y: lane.y + box.y })),
        })
      }
    } else {
      nodes.push({ id, kind: "state", depth, ...box })
    }
  }

  const edges: LayoutEdge[] = []
  for (const inner of children.entries()) {
    const box = boxes.get(inner[0])
    if (!box) continue
    for (const edge of inner[1].edges) {
      edges.push({
        ...edge,
        points: edge.points.map((p) => ({ x: p.x + box.x, y: p.y + box.y })),
        labelPosition: edge.labelPosition && {
          x: edge.labelPosition.x + box.x,
          y: edge.labelPosition.y + box.y,
        },
      })
    }
  }

  for (const edge of pending) {
    const routed = g.edge({ v: edge.from, w: edge.to, name: edge.name })
    const points = snapEndpoints(routed?.points ?? [], boxes.get(edge.from), boxes.get(edge.to))
    edges.push({
      id: `${scopeId}:${edge.name}:${edge.from}->${edge.to}`,
      from: edge.from,
      to: edge.to,
      kind: edge.kind,
      label: edge.label,
      points,
      labelPosition:
        edge.label && routed?.x !== undefined && routed.y !== undefined
          ? { x: routed.x, y: routed.y }
          : undefined,
      sourceNode: edge.from,
      targetNode: edge.to,
      depth,
    })
  }

  return { width, height, nodes, edges }
}

/**
 * Pins a forward edge to the bottom-centre of its source and the top-centre of
 * its target, which is what makes a top-to-bottom flow read cleanly. Dagre
 * aims ends at node centres, so on nodes of different widths they otherwise
 * land off-centre. Back edges (loops) keep dagre's own routing around the
 * nodes in between.
 */
function snapEndpoints(
  points: Point[],
  source: { x: number; y: number; width: number; height: number } | undefined,
  target: { x: number; y: number; width: number; height: number } | undefined,
): Point[] {
  if (points.length < 2 || !source || !target) return points
  const sourceBottom = source.y + source.height
  if (target.y < sourceBottom - 1) return points
  const out = points.map((p) => ({ ...p }))
  out[0] = { x: source.x + source.width / 2, y: sourceBottom }
  out[out.length - 1] = { x: target.x + target.width / 2, y: target.y }
  return out
}

/**
 * Lays out a Parallel's branches (or a Map's processor) side by side inside one
 * container box, and wires each lane to the container's fork point under its
 * header and its join point at the bottom.
 */
function layoutContainer(
  model: AslModel,
  scopeIds: string[],
  depth: number,
): ScopeLayout & { lanes: NonNullable<LayoutNode["lanes"]> } {
  const inner = scopeIds.map((id) => ({ id, layout: layoutScope(model, id, depth) }))
  const lanesWidth =
    inner.reduce((sum, b) => sum + b.layout.width, 0) + BRANCH_GAP * Math.max(0, inner.length - 1)
  const width = Math.max(STATE_WIDTH + CONTAINER_PAD_X * 2, lanesWidth + CONTAINER_PAD_X * 2)
  const lanesHeight = Math.max(0, ...inner.map((b) => b.layout.height))
  const laneTop = CONTAINER_HEADER + FORK_GAP
  const height = laneTop + lanesHeight + JOIN_GAP + CONTAINER_PAD_BOTTOM

  const fork: Point = { x: width / 2, y: CONTAINER_HEADER }
  const join: Point = { x: width / 2, y: height - CONTAINER_PAD_BOTTOM + 4 }
  const forkBus = CONTAINER_HEADER + FORK_GAP / 2
  const joinBus = laneTop + lanesHeight + JOIN_GAP / 2

  const nodes: LayoutNode[] = []
  const edges: LayoutEdge[] = []
  const lanes: NonNullable<LayoutNode["lanes"]> = []
  let x = (width - lanesWidth) / 2
  for (const { id, layout } of inner) {
    lanes.push({
      scopeId: id,
      x: x - 8,
      y: laneTop - 10,
      width: layout.width + 16,
      height: lanesHeight + 20,
    })
    for (const node of layout.nodes) {
      nodes.push({
        ...node,
        x: node.x + x,
        y: node.y + laneTop,
        lanes: node.lanes?.map((lane) => ({ ...lane, x: lane.x + x, y: lane.y + laneTop })),
      })
    }
    for (const edge of layout.edges) {
      let points = edge.points.map((p) => ({ x: p.x + x, y: p.y + laneTop }))
      // Extend the branch's own entry and exit edges out to the shared fork and
      // join, turning at a common "bus" height so sibling lanes line up.
      if (edge.kind === "fork" && edge.from === forkId(id) && points.length > 0) {
        const first = points[0]
        points = [fork, { x: fork.x, y: forkBus }, { x: first.x, y: forkBus }, ...points]
      }
      if (edge.kind === "join" && edge.to === joinId(id) && points.length > 0) {
        const last = points[points.length - 1]
        points = [...points, { x: last.x, y: joinBus }, { x: join.x, y: joinBus }, join]
      }
      edges.push({
        ...edge,
        points,
        labelPosition: edge.labelPosition && {
          x: edge.labelPosition.x + x,
          y: edge.labelPosition.y + laneTop,
        },
      })
    }
    x += layout.width + BRANCH_GAP
  }
  return { width, height, nodes, edges, lanes }
}

// ─── Paths ────────────────────────────────────────────────────────────────────

/** Removes consecutive duplicate points so a path never has a zero-length segment. */
function dedupe(points: Point[]): Point[] {
  return points.filter(
    (p, i) => i === 0 || Math.hypot(p.x - points[i - 1].x, p.y - points[i - 1].y) > 0.5,
  )
}

/**
 * An SVG path through the points with rounded corners: straight runs, and a
 * quadratic curve of at most `radius` at each bend. Reads as a routed diagram
 * rather than as a spline, and still passes through every point dagre chose.
 */
export function roundedPath(input: Point[], radius = 12): string {
  const points = dedupe(input)
  if (points.length === 0) return ""
  if (points.length === 1) return `M${points[0].x},${points[0].y}`
  let d = `M${points[0].x},${points[0].y}`
  for (let i = 1; i < points.length - 1; i++) {
    const prev = points[i - 1]
    const curr = points[i]
    const next = points[i + 1]
    const inLen = Math.hypot(curr.x - prev.x, curr.y - prev.y)
    const outLen = Math.hypot(next.x - curr.x, next.y - curr.y)
    const r = Math.min(radius, inLen / 2, outLen / 2)
    const a = {
      x: curr.x - ((curr.x - prev.x) / inLen) * r,
      y: curr.y - ((curr.y - prev.y) / inLen) * r,
    }
    const b = {
      x: curr.x + ((next.x - curr.x) / outLen) * r,
      y: curr.y + ((next.y - curr.y) / outLen) * r,
    }
    d += ` L${a.x},${a.y} Q${curr.x},${curr.y} ${b.x},${b.y}`
  }
  const last = points[points.length - 1]
  d += ` L${last.x},${last.y}`
  return d
}

/** The arrowhead at the end of a path, as a closed triangle pointing along the final segment. */
export function arrowHead(input: Point[], size = 7): string {
  const points = dedupe(input)
  if (points.length < 2) return ""
  const tip = points[points.length - 1]
  const from = points[points.length - 2]
  const len = Math.hypot(tip.x - from.x, tip.y - from.y) || 1
  const ux = (tip.x - from.x) / len
  const uy = (tip.y - from.y) / len
  const baseX = tip.x - ux * size
  const baseY = tip.y - uy * size
  const half = size * 0.6
  return `M${tip.x},${tip.y} L${baseX - uy * half},${baseY + ux * half} L${baseX + uy * half},${baseY - ux * half} Z`
}
