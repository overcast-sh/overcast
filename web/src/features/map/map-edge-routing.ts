/**
 * map-edge-routing — keeps a connection off the nodes it is not connected to.
 *
 * The layout engine reserves a lane in every column an edge passes through
 * and reports those lanes as the edge's waypoints. Following them is enough
 * inside one level. Two kinds of edge get no such help and used to be drawn
 * as a straight Bézier from handle to handle, through whatever sat in
 * between:
 *
 *   - an edge between two groups (stacks, VPCs), which the engine only sees
 *     at the level where both ends are phantom boxes — the segment from the
 *     real node to the box boundary crosses that node's siblings;
 *   - an edge between regions, which no level ever sees.
 *
 * So after every node has an absolute rectangle, each edge's polyline is
 * checked segment by segment against an index of every leaf node. A clear
 * polyline is kept as is. A blocked one is re-routed with A* over a coarse
 * grid in which the nodes are walls, then pulled taut so the result is a few
 * straight legs rather than a staircase of grid cells. The renderer turns
 * the legs into one smooth curve.
 *
 * Plain data, no DOM: this runs inside the layout worker.
 */

export interface Pt {
  x: number
  y: number
}

export interface Rect {
  x: number
  y: number
  w: number
  h: number
}

/** Clearance (px) kept between a route and any node it passes. */
export const ROUTE_MARGIN = 20
/** A* grid resolution (px). Coarser is faster; finer squeezes through tighter gaps. */
export const ROUTE_CELL = 12
const CELL = ROUTE_CELL
/**
 * The smallest gap between two cards that a detour can pass through: both
 * cards' margins plus one grid cell for the route itself. A layout that
 * keeps its gaps at least this wide never walls an edge in.
 */
export const ROUTABLE_GAP = 2 * ROUTE_MARGIN + ROUTE_CELL
/**
 * Most cells one search may cover. A leg long enough to need more is searched
 * on a coarser grid instead — a detour around a box hundreds of pixels away
 * does not need 12px precision.
 */
const MAX_CELLS = 120_000
/** Extra cost per change of direction — fewer bends read better than shortest length. */
const TURN_COST = 6
/** How far (px) beyond the two endpoints' bounding box a detour may wander. */
const SEARCH_PAD = 360
/** A* gives up past this many expansions and the leg keeps its straight route. */
const MAX_EXPANSIONS = 150_000

/** Liang–Barsky: does the closed segment a→b enter the rectangle? */
export function segmentHitsRect(a: Pt, b: Pt, r: Rect): boolean {
  const dx = b.x - a.x
  const dy = b.y - a.y
  let t0 = 0
  let t1 = 1
  const p = [-dx, dx, -dy, dy]
  const q = [a.x - r.x, r.x + r.w - a.x, a.y - r.y, r.y + r.h - a.y]
  for (let i = 0; i < 4; i++) {
    if (p[i] === 0) {
      if (q[i] < 0) return false
      continue
    }
    const t = q[i] / p[i]
    if (p[i] < 0) {
      if (t > t1) return false
      if (t > t0) t0 = t
    } else {
      if (t < t0) return false
      if (t < t1) t1 = t
    }
  }
  return true
}

function orient(a: Pt, b: Pt, c: Pt): number {
  return Math.sign((b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x))
}

/** Proper intersection of segments a1→a2 and b1→b2 (touching endpoints do not count). */
export function segmentsCross(a1: Pt, a2: Pt, b1: Pt, b2: Pt): boolean {
  const o1 = orient(a1, a2, b1)
  const o2 = orient(a1, a2, b2)
  const o3 = orient(b1, b2, a1)
  const o4 = orient(b1, b2, a2)
  return o1 !== 0 && o2 !== 0 && o3 !== 0 && o4 !== 0 && o1 !== o2 && o3 !== o4
}

export interface Obstacle extends Rect {
  id: string
}

/**
 * Uniform-grid spatial hash over node rectangles (each inflated by `margin`,
 * ROUTE_MARGIN unless the caller says otherwise), so a segment test only
 * visits the buckets the segment spans.
 */
export class ObstacleIndex {
  private readonly buckets = new Map<number, Obstacle[]>()
  private readonly bucketSize: number
  readonly obstacles: Obstacle[]
  readonly bounds: Rect

  constructor(rects: Obstacle[], bucketSize = 200, margin = ROUTE_MARGIN) {
    this.bucketSize = bucketSize
    this.obstacles = rects.map((r) => ({
      id: r.id,
      x: r.x - margin,
      y: r.y - margin,
      w: r.w + margin * 2,
      h: r.h + margin * 2,
    }))
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const o of this.obstacles) {
      if (o.x < minX) minX = o.x
      if (o.y < minY) minY = o.y
      if (o.x + o.w > maxX) maxX = o.x + o.w
      if (o.y + o.h > maxY) maxY = o.y + o.h
      const bx0 = Math.floor(o.x / bucketSize)
      const bx1 = Math.floor((o.x + o.w) / bucketSize)
      const by0 = Math.floor(o.y / bucketSize)
      const by1 = Math.floor((o.y + o.h) / bucketSize)
      for (let bx = bx0; bx <= bx1; bx++) {
        for (let by = by0; by <= by1; by++) {
          const key = bx * 73856093 + by * 19349663
          const list = this.buckets.get(key)
          if (list) list.push(o)
          else this.buckets.set(key, [o])
        }
      }
    }
    this.bounds =
      this.obstacles.length === 0
        ? { x: 0, y: 0, w: 0, h: 0 }
        : { x: minX, y: minY, w: maxX - minX, h: maxY - minY }
  }

  /** Every obstacle whose bucket the segment a→b touches (may contain duplicates). */
  private candidates(a: Pt, b: Pt): Obstacle[] {
    const bs = this.bucketSize
    const bx0 = Math.floor(Math.min(a.x, b.x) / bs)
    const bx1 = Math.floor(Math.max(a.x, b.x) / bs)
    const by0 = Math.floor(Math.min(a.y, b.y) / bs)
    const by1 = Math.floor(Math.max(a.y, b.y) / bs)
    // A segment's bounding box is a fine over-approximation of the buckets it
    // crosses: routes are short compared with the canvas, so the box is small.
    const out: Obstacle[] = []
    for (let bx = bx0; bx <= bx1; bx++) {
      for (let by = by0; by <= by1; by++) {
        const list = this.buckets.get(bx * 73856093 + by * 19349663)
        if (list) out.push(...list)
      }
    }
    return out
  }

  /** True when the segment a→b crosses no obstacle other than the two ignored ids. */
  segmentClear(a: Pt, b: Pt, ignoreA: string, ignoreB: string): boolean {
    for (const o of this.candidates(a, b)) {
      if (o.id === ignoreA || o.id === ignoreB) continue
      if (segmentHitsRect(a, b, o)) return false
    }
    return true
  }

  /** Adds every obstacle the segment a→b enters, other than the two ignored ids, to `into`. */
  collectHits(a: Pt, b: Pt, ignoreA: string, ignoreB: string, into: Set<Obstacle>): void {
    for (const o of this.candidates(a, b)) {
      if (o.id === ignoreA || o.id === ignoreB || into.has(o)) continue
      if (segmentHitsRect(a, b, o)) into.add(o)
    }
  }

  /** True when the point lies inside any obstacle other than the ignored ids. */
  pointBlocked(p: Pt, ignoreA: string, ignoreB: string): boolean {
    for (const o of this.candidates(p, p)) {
      if (o.id === ignoreA || o.id === ignoreB) continue
      if (p.x >= o.x && p.x <= o.x + o.w && p.y >= o.y && p.y <= o.y + o.h) return true
    }
    return false
  }
}

/** True when every leg of the polyline is clear. */
export function polylineClear(pts: Pt[], index: ObstacleIndex, srcId: string, tgtId: string) {
  for (let i = 0; i + 1 < pts.length; i++) {
    if (!index.segmentClear(pts[i], pts[i + 1], srcId, tgtId)) return false
  }
  return true
}

// ─── A* over a cell grid ─────────────────────────────────────────────────────

/** Binary min-heap keyed on f-score, storing cell indices. */
class MinHeap {
  private readonly keys: number[] = []
  private readonly vals: number[] = []
  get size() {
    return this.vals.length
  }
  push(key: number, val: number) {
    const k = this.keys
    const v = this.vals
    k.push(key)
    v.push(val)
    let i = k.length - 1
    while (i > 0) {
      const p = (i - 1) >> 1
      if (k[p] <= k[i]) break
      ;[k[p], k[i]] = [k[i], k[p]]
      ;[v[p], v[i]] = [v[i], v[p]]
      i = p
    }
  }
  pop(): number {
    const k = this.keys
    const v = this.vals
    const top = v[0]
    const lastK = k.pop()!
    const lastV = v.pop()!
    if (k.length > 0) {
      k[0] = lastK
      v[0] = lastV
      let i = 0
      for (;;) {
        const l = i * 2 + 1
        const r = l + 1
        let m = i
        if (l < k.length && k[l] < k[m]) m = l
        if (r < k.length && k[r] < k[m]) m = r
        if (m === i) break
        ;[k[m], k[i]] = [k[i], k[m]]
        ;[v[m], v[i]] = [v[i], v[m]]
        i = m
      }
    }
    return top
  }
}

/**
 * Finds a detour from `from` to `to` around every obstacle except the two
 * endpoint nodes. Returns the waypoints strictly between the endpoints, or
 * null when no route exists inside the search window.
 */
function astar(from: Pt, to: Pt, index: ObstacleIndex, srcId: string, tgtId: string): Pt[] | null {
  // Search window: the endpoints' bounding box padded so a route can go
  // around a tall neighbour, clipped to where obstacles actually are.
  const b = index.bounds
  const minX = Math.max(Math.min(from.x, to.x) - SEARCH_PAD, b.x - SEARCH_PAD)
  const minY = Math.max(Math.min(from.y, to.y) - SEARCH_PAD, b.y - SEARCH_PAD)
  const maxX = Math.min(Math.max(from.x, to.x) + SEARCH_PAD, b.x + b.w + SEARCH_PAD)
  const maxY = Math.min(Math.max(from.y, to.y) + SEARCH_PAD, b.y + b.h + SEARCH_PAD)
  const cell = Math.max(CELL, Math.ceil(Math.sqrt(((maxX - minX) * (maxY - minY)) / MAX_CELLS)))
  const cols = Math.max(1, Math.ceil((maxX - minX) / cell))
  const rows = Math.max(1, Math.ceil((maxY - minY) / cell))
  const n = cols * rows

  const toCell = (p: Pt) => {
    const cx = Math.min(cols - 1, Math.max(0, Math.floor((p.x - minX) / cell)))
    const cy = Math.min(rows - 1, Math.max(0, Math.floor((p.y - minY) / cell)))
    return cy * cols + cx
  }
  const centre = (i: number): Pt => ({
    x: minX + (i % cols) * cell + cell / 2,
    y: minY + Math.floor(i / cols) * cell + cell / 2,
  })

  const start = toCell(from)
  const goal = toCell(to)
  if (start === goal) return []

  // Walkability is resolved lazily — most cells are never looked at.
  // 0 = unknown, 1 = open, 2 = wall.
  const walk = new Uint8Array(n)
  const isOpen = (i: number): boolean => {
    let w = walk[i]
    if (w === 0) {
      w = index.pointBlocked(centre(i), srcId, tgtId) ? 2 : 1
      walk[i] = w
    }
    return w === 1
  }

  const g = new Float32Array(n).fill(Infinity)
  const came = new Int32Array(n).fill(-1)
  const dir = new Int8Array(n).fill(-1)
  const closed = new Uint8Array(n)
  const heap = new MinHeap()
  const gx = goal % cols
  const gy = Math.floor(goal / cols)
  const h = (i: number) => Math.abs((i % cols) - gx) + Math.abs(Math.floor(i / cols) - gy)

  g[start] = 0
  heap.push(h(start), start)
  // dx, dy per direction: right, down, left, up.
  const DX = [1, 0, -1, 0]
  const DY = [0, 1, 0, -1]
  let expansions = 0

  while (heap.size > 0) {
    const cur = heap.pop()
    if (closed[cur]) continue
    if (cur === goal) break
    closed[cur] = 1
    if (++expansions > MAX_EXPANSIONS) return null
    const cx = cur % cols
    const cy = Math.floor(cur / cols)
    for (let d = 0; d < 4; d++) {
      const nx = cx + DX[d]
      const ny = cy + DY[d]
      if (nx < 0 || ny < 0 || nx >= cols || ny >= rows) continue
      const nb = ny * cols + nx
      if (closed[nb] || !isOpen(nb)) continue
      const turn = dir[cur] !== -1 && dir[cur] !== d ? TURN_COST : 0
      const ng = g[cur] + 1 + turn
      if (ng < g[nb]) {
        g[nb] = ng
        came[nb] = cur
        dir[nb] = d
        heap.push(ng + h(nb), nb)
      }
    }
  }
  if (came[goal] === -1) return null

  // Walk back, then pull the polyline taut: keep a waypoint only when the
  // straight line from the previous kept point to the one after it is blocked.
  const cells: number[] = []
  for (let c = goal; c !== -1; c = came[c]) cells.push(c)
  cells.reverse()
  const pts: Pt[] = cells.map(centre)
  pts[0] = from
  pts[pts.length - 1] = to

  const kept: Pt[] = [pts[0]]
  let i = 0
  while (i < pts.length - 1) {
    let j = i + 1
    while (j + 1 < pts.length && index.segmentClear(pts[i], pts[j + 1], srcId, tgtId)) j++
    kept.push(pts[j])
    i = j
  }
  return kept.slice(1, -1)
}

/**
 * Returns the interior waypoints an edge should follow from `from` (the
 * source's right-hand handle) to `to` (the target's left-hand handle).
 * `hint` is what the layout already knows (the engine's lane positions, or
 * nothing). Every leg of it that is already clear is kept as is, so a
 * well-laid-out edge never changes shape just because this pass ran; only a
 * leg that cuts through a box is replaced by a detour between its two ends.
 * Searching one leg rather than the whole edge keeps the grid small even
 * when the edge itself spans the canvas.
 */
export function routeEdge(
  from: Pt,
  to: Pt,
  hint: Pt[],
  index: ObstacleIndex,
  srcId: string,
  tgtId: string,
): Pt[] {
  const poly = [from, ...hint, to]
  const out: Pt[] = [from]
  let changed = false
  let i = 0
  while (i + 1 < poly.length) {
    const a = out[out.length - 1]
    // Skip over any waypoint that itself lies inside a box: the detour runs
    // from the last good point to the next one that is clear.
    let j = i + 1
    while (j < poly.length - 1 && index.pointBlocked(poly[j], srcId, tgtId)) j++
    const b = poly[j]
    if (!index.segmentClear(a, b, srcId, tgtId)) {
      const detour = astar(a, b, index, srcId, tgtId)
      if (detour) {
        out.push(...detour)
        changed = true
      }
    }
    if (j !== i + 1) changed = true
    out.push(b)
    i = j
  }
  return changed ? out.slice(1, -1) : hint
}

// ─── Rendering: waypoints → one smooth SVG path ──────────────────────────────

/**
 * Longest tangent (px) at an interior waypoint. Longer tangents round a turn
 * more; ROUTE_MARGIN is sized so the resulting bulge (about a quarter of the
 * tangent) stays clear of the cards the polyline was checked against.
 */
const TANGENT_MAX = 80
/**
 * A tangent is also capped at this fraction of the shorter adjacent leg, so a
 * curve through two close waypoints cannot overshoot the corner between them.
 */
const TANGENT_LEG_FRACTION = 0.6
/** Shortest horizontal run out of a handle so the curve never leaves a box at an angle. */
const HANDLE_RUN_MIN = 24

/**
 * One cubic Bézier per leg through every waypoint (Catmull-Rom tangents at
 * the interior points, horizontal at both handles). Returns the SVG path and
 * the point half-way along the polyline, where a label belongs.
 */
export function routedPath(pts: Pt[]): [string, number, number] {
  const n = pts.length
  const tangents: Pt[] = new Array(n)
  for (let i = 1; i < n - 1; i++) {
    const tx = (pts[i + 1].x - pts[i - 1].x) / 2
    const ty = (pts[i + 1].y - pts[i - 1].y) / 2
    const len = Math.hypot(tx, ty)
    const legIn = Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y)
    const legOut = Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
    const cap = Math.min(TANGENT_MAX, Math.min(legIn, legOut) * TANGENT_LEG_FRACTION)
    tangents[i] = len > cap ? { x: (tx / len) * cap, y: (ty / len) * cap } : { x: tx, y: ty }
  }
  const run = (a: Pt, b: Pt) =>
    Math.max(HANDLE_RUN_MIN, Math.min(TANGENT_MAX, Math.abs(b.x - a.x) / 2))
  tangents[0] = { x: run(pts[0], pts[1]), y: 0 }
  tangents[n - 1] = { x: run(pts[n - 2], pts[n - 1]), y: 0 }

  let d = `M ${pts[0].x} ${pts[0].y}`
  for (let i = 0; i < n - 1; i++) {
    const a = pts[i]
    const b = pts[i + 1]
    const c1x = a.x + tangents[i].x / 3
    const c1y = a.y + tangents[i].y / 3
    const c2x = b.x - tangents[i + 1].x / 3
    const c2y = b.y - tangents[i + 1].y / 3
    d += ` C ${c1x} ${c1y}, ${c2x} ${c2y}, ${b.x} ${b.y}`
  }

  // Label anchor: half-way along the polyline by arc length.
  let total = 0
  for (let i = 0; i < n - 1; i++) total += Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
  let remaining = total / 2
  for (let i = 0; i < n - 1; i++) {
    const seg = Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
    if (remaining <= seg || i === n - 2) {
      const t = seg === 0 ? 0 : remaining / seg
      return [d, pts[i].x + (pts[i + 1].x - pts[i].x) * t, pts[i].y + (pts[i + 1].y - pts[i].y) * t]
    }
    remaining -= seg
  }
  return [d, pts[0].x, pts[0].y]
}
