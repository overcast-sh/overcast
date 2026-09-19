/**
 * map-layout-engine — lays out one level of the system map's hierarchy.
 *
 * A level is a set of items (resource cards, or the boxes standing in for a
 * nested stack or a VPC) and the edges among them. The engine is a
 * Sugiyama-style pipeline where every step is judged by the score in
 * map-layout-score.ts rather than by taste:
 *
 *   1. components — the level splits into connected components; each is laid
 *      out on its own and the blocks are packed, flows first, inventory
 *      (cards with no connections) last and grouped by service;
 *   2. cycle breaking — a greedy feedback arc set (Eades–Lin–Smyth); the
 *      reversed edges are drawn as explicit loops;
 *   3. layering — longest path from the sources, then every node is pulled
 *      as far towards its neighbours as its edges allow;
 *   4. ordering — median sweeps from several deterministic starts, each
 *      followed by adjacent-swap local search on the exact crossing count,
 *      then simulated annealing on the same move;
 *   5. coordinates — columns at RANK_GAP; rows by a priority pass that aligns
 *      each node with the median of its neighbours, then annealing over row
 *      shifts with an incremental objective built from the score's weights;
 *   6. routing — long edges follow the column-gap channels reserved for them,
 *      back edges take a loop above or below everything they span.
 *
 * Everything is index-based over typed arrays and allocation-free in the
 * annealing loops. The only randomness is a seeded generator whose seed is a
 * hash of the component's ids, so the same input always gives the same
 * layout (H5). The annealing budget is a deterministic function of the node
 * count, calibrated so the H6 time limits hold — never a wall clock, which
 * would make the output depend on the machine.
 */

import type { TopologyEdge, TopologyNode } from "@/types"
import { ROUTABLE_GAP, type Pt } from "./map-edge-routing"
import { NODE_GAP, NODE_HEIGHT, NODE_WIDTH, RANK_GAP } from "./map-layout-constants"
import { hashSeed, seededRandom } from "./map-layout-random"
import {
  ASPECT_TARGET_LOG2,
  BEND_ANGLE_DEG,
  GRAZE,
  W_ASPECT,
  W_BEND,
  W_COMPACT,
  W_LENGTH,
  W_MISALIGN,
  W_NEIGHBOUR,
} from "./map-layout-score"

// ─── Public types ────────────────────────────────────────────────────────────

export interface Positioned {
  node: TopologyNode
  x: number
  y: number
  w: number
  h: number
}

/** Edge id → waypoints strictly between the two handles, in the caller's coordinates. */
export type Routes = Map<string, Pt[]>

export interface SubgraphLayout {
  positioned: Positioned[]
  width: number
  height: number
  routes: Routes
}

export type SizeOverrides = Record<string, { width: number; height: number } | undefined>

/** Geometry of one level: gaps and default card size. */
export interface LevelParams {
  /** Horizontal gap between adjacent columns. */
  rankGap: number
  /** Vertical gap between cards stacked in one column. */
  nodeGap: number
  /** Vertical gap between a card and a long edge passing through its column. */
  edgeGap: number
  /** Vertical gap between two long-edge lanes passing through one column. */
  laneGap: number
  /** Gap between packed blocks, horizontally and between shelves. */
  blockGap: number
  /**
   * Horizontal pitch gap of the inventory grid. Inventory cards sit on a
   * fixed grid whatever their width, so the corridors between grid columns
   * line up from row to row and an edge from another region can pass.
   */
  inventoryGap: number
  /** Vertical gap between inventory rows. */
  inventoryRowGap: number
  defaultW: number
  defaultH: number
  /** How far a back edge runs out past the outermost column before turning. */
  loopStub: number
  /** Clearance between a back edge's lane and the cards it loops around. */
  loopLane: number
}

// Every gap a detour might have to thread through is at least ROUTABLE_GAP,
// so the obstacle pass can always find a way past a row of cards; the edge
// lanes, which are not obstacles, can be tighter.
export const FULL_LEVEL_PARAMS: LevelParams = {
  rankGap: RANK_GAP,
  nodeGap: NODE_GAP,
  edgeGap: 28,
  laneGap: 24,
  blockGap: NODE_GAP,
  inventoryGap: ROUTABLE_GAP,
  inventoryRowGap: 32,
  defaultW: NODE_WIDTH,
  defaultH: NODE_HEIGHT,
  loopStub: 20,
  loopLane: 24,
}

/**
 * The annealer's own parameters. Tuned by `pnpm layout:tune` against the
 * fixtures' total score; the values here are the best of the third run of
 * 2026-09-20, after cross-region edges got their corridors (see
 * docs/plans/map-layout-objective.md §8 for the table).
 */
export interface AnnealerTuning {
  /** Share of the iteration budget spent on column order (the rest on rows). */
  orderShare: number
  /** Starting and final temperature for the order phase, in crossings. */
  orderTemp0: number
  orderTemp1: number
  /** Starting and final temperature for the row phase, in score points. */
  rowTemp0: number
  rowTemp1: number
  /** Largest random row shift, in pixels. */
  maxShift: number
  /** Share of row moves that snap a node to a neighbour's row rather than shift it randomly. */
  alignShare: number
  /** Share of row moves that shift a whole column. */
  columnShare: number
}

export const ANNEALER_TUNING: AnnealerTuning = {
  orderShare: 0.306,
  orderTemp0: 0.645,
  orderTemp1: 0.01,
  rowTemp0: 4,
  rowTemp1: 0.081,
  maxShift: 86,
  alignShare: 0.318,
  columnShare: 0.216,
}

// ─── Time budget ─────────────────────────────────────────────────────────────

/**
 * H6's limits: milliseconds allowed for a whole layout at each node count.
 * The budget between the points is interpolated so quality never steps down
 * at a threshold.
 */
const TIME_BUDGET_MS: ReadonlyArray<readonly [nodes: number, ms: number]> = [
  [50, 30],
  [150, 120],
  [300, 400],
  [600, 900],
  [1000, 1800],
]
/**
 * How the H6 budget turns into annealing iterations. Calibrated, not
 * measured: the iteration count — and with it the output — must be a pure
 * function of the input, so a wall clock never decides when to stop.
 */
export const ENGINE_BUDGET = {
  /** Share of the time budget the annealer may spend; the rest is the deterministic pipeline and headroom. */
  annealTimeShare: 0.25,
  /**
   * Annealing iterations this engine gets through per millisecond on the
   * machine the budget was calibrated on: `pnpm layout:tune` measured about
   * 2000 on mixed-600 on a 2026 desktop under Node 24 (run 2026-09-20). Set
   * at half that because a dense graph (dense-dag: 44 edges on 24 nodes)
   * pays more per iteration than that average, and a slower CI runner has
   * to land inside H6 too.
   */
  iterationsPerMs: 1000,
}

/** Counters the tuner reads to calibrate ENGINE_BUDGET; not part of the layout. */
export const ENGINE_STATS = { iterations: 0 }

/** H6's time limit, in milliseconds, for a layout of `totalNodes` nodes. */
export function timeBudgetMs(totalNodes: number): number {
  const n = Math.max(totalNodes, 1)
  const first = TIME_BUDGET_MS[0]
  const last = TIME_BUDGET_MS[TIME_BUDGET_MS.length - 1]
  // Below the first point the budget is the first point's: a small map is
  // allowed the whole 30ms, and spends a fraction of it.
  if (n <= first[0]) return first[1]
  if (n >= last[0]) return (last[1] * n) / last[0]
  for (let i = 1; i < TIME_BUDGET_MS.length; i++) {
    const [n0, ms0] = TIME_BUDGET_MS[i - 1]
    const [n1, ms1] = TIME_BUDGET_MS[i]
    if (n <= n1) return ms0 + ((ms1 - ms0) * (n - n0)) / (n1 - n0)
  }
  return last[1]
}

/** Annealing iterations per virtual node for a layout of `totalNodes` nodes. */
export function annealingIterationsPerNode(totalNodes: number): number {
  return (
    (timeBudgetMs(totalNodes) * ENGINE_BUDGET.annealTimeShare * ENGINE_BUDGET.iterationsPerMs) /
    Math.max(totalNodes, 1)
  )
}

// ─── Component graph ─────────────────────────────────────────────────────────

const TAN_BEND = Math.tan((BEND_ANGLE_DEG * Math.PI) / 180)
/** Ordering sweeps per start before the search moves on. */
const MAX_SWEEP_ROUNDS = 8
/** Coordinate passes (down and up) of the priority placement. */
const COORD_PASSES = 3
/** Rounds of adjacent-swap local search per sweep. */
const MAX_TRANSPOSE_ROUNDS = 4

const byIdAsc = (a: { id: string }, b: { id: string }) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)

/**
 * A laid-out component. `w` and `h` are its packing extent: the cards plus
 * every route lane, with the edge gap around a lane, so whatever is packed
 * beside or below it stays clear of its edges. The cards' own bounding box
 * starts at (0, 0) or, when a lane runs above or left of them, inside it.
 */
interface ComponentResult {
  positioned: Positioned[]
  routes: Routes
  w: number
  h: number
}

/**
 * One connected component through the pipeline. Real nodes are 0..n-1 in id
 * order; dummies for long edges follow. Every array is indexed by that
 * virtual node index.
 */
class ComponentLayout {
  readonly n: number
  readonly nodes: TopologyNode[]
  readonly edges: TopologyEdge[]
  /** DAG direction of each edge after cycle breaking. */
  private readonly es: Int32Array
  private readonly et: Int32Array
  private readonly reversed: Uint8Array
  private readonly rng: () => number

  private N = 0
  private L = 0
  private w!: Float64Array
  private h!: Float64Array
  private layerOf!: Int32Array
  private isDummy!: Uint8Array
  private layers: Int32Array[] = []
  private pos!: Int32Array
  private outOff!: Int32Array
  private outAdj!: Int32Array
  private inOff!: Int32Array
  private inAdj!: Int32Array
  /** Real edges incident to each real node (CSR), for the alignment terms. */
  private edgeOff!: Int32Array
  private edgeAdj!: Int32Array
  private chainOff!: Int32Array
  private chain!: Int32Array
  private colX!: Float64Array
  private colW!: Float64Array
  private y!: Float64Array
  private bit!: Int32Array
  private scratch!: Int32Array
  private fscratch!: Float64Array
  private diagMax = 0
  private readonly size: (n: TopologyNode) => { width: number; height: number }
  private readonly params: LevelParams
  private readonly iterationsPerNode: number
  private readonly tuning: AnnealerTuning

  constructor(
    nodes: TopologyNode[],
    edges: TopologyEdge[],
    size: (n: TopologyNode) => { width: number; height: number },
    params: LevelParams,
    iterationsPerNode: number,
    tuning: AnnealerTuning,
  ) {
    this.size = size
    this.params = params
    this.iterationsPerNode = iterationsPerNode
    this.tuning = tuning
    this.nodes = [...nodes].sort(byIdAsc)
    this.edges = [...edges].sort(byIdAsc)
    this.n = this.nodes.length
    const index = new Map(this.nodes.map((n, i) => [n.id, i]))
    this.es = new Int32Array(this.edges.length)
    this.et = new Int32Array(this.edges.length)
    for (let e = 0; e < this.edges.length; e++) {
      this.es[e] = index.get(this.edges[e].source)!
      this.et[e] = index.get(this.edges[e].target)!
    }
    this.reversed = new Uint8Array(this.edges.length)
    this.rng = seededRandom(hashSeed(this.nodes.map((n) => n.id).join("\n")))
  }

  run(): ComponentResult {
    this.breakCycles()
    const layer = this.assignLayers()
    this.buildVirtualGraph(layer)
    this.order()
    this.placeRows()
    this.annealRows()
    return this.emit()
  }

  // ── 2. Cycle breaking: Eades–Lin–Smyth ──────────────────────────────────

  private breakCycles(): void {
    const n = this.n
    const m = this.edges.length
    const outdeg = new Int32Array(n)
    const indeg = new Int32Array(n)
    for (let e = 0; e < m; e++) {
      outdeg[this.es[e]]++
      indeg[this.et[e]]++
    }
    const removed = new Uint8Array(n)
    const head: number[] = []
    const tail: number[] = []
    let remaining = n
    const remove = (v: number) => {
      removed[v] = 1
      remaining--
      for (let e = 0; e < m; e++) {
        if (this.es[e] === v && !removed[this.et[e]]) indeg[this.et[e]]--
        if (this.et[e] === v && !removed[this.es[e]]) outdeg[this.es[e]]--
      }
    }
    while (remaining > 0) {
      let progress = true
      while (progress) {
        progress = false
        for (let v = 0; v < n; v++) {
          if (!removed[v] && outdeg[v] === 0) {
            tail.push(v)
            remove(v)
            progress = true
          }
        }
        for (let v = 0; v < n; v++) {
          if (!removed[v] && indeg[v] === 0) {
            head.push(v)
            remove(v)
            progress = true
          }
        }
      }
      if (remaining === 0) break
      // Ties go to the node with more edges leaving it, so a hub that
      // several cycles share leads them all rather than sitting in the
      // middle of one; then to the lowest index, for determinism.
      let best = -1
      let bestDelta = -Infinity
      let bestOut = -1
      for (let v = 0; v < n; v++) {
        if (removed[v]) continue
        const delta = outdeg[v] - indeg[v]
        if (delta > bestDelta || (delta === bestDelta && outdeg[v] > bestOut)) {
          bestDelta = delta
          bestOut = outdeg[v]
          best = v
        }
      }
      head.push(best)
      remove(best)
    }
    const rank = new Int32Array(n)
    let r = 0
    for (const v of head) rank[v] = r++
    for (let i = tail.length - 1; i >= 0; i--) rank[tail[i]] = r++
    for (let e = 0; e < m; e++) {
      if (rank[this.es[e]] > rank[this.et[e]]) {
        this.reversed[e] = 1
        const s = this.es[e]
        this.es[e] = this.et[e]
        this.et[e] = s
      }
    }
  }

  // ── 3. Layering: longest path, then tightening ──────────────────────────

  private assignLayers(): Int32Array {
    const n = this.n
    const m = this.edges.length
    const layer = new Int32Array(n)
    const indeg = new Int32Array(n)
    for (let e = 0; e < m; e++) indeg[this.et[e]]++
    // Kahn's algorithm gives a topological order; longest path follows it.
    const order: number[] = []
    for (let v = 0; v < n; v++) if (indeg[v] === 0) order.push(v)
    for (let i = 0; i < order.length; i++) {
      const u = order[i]
      for (let e = 0; e < m; e++) {
        if (this.es[e] !== u) continue
        const v = this.et[e]
        if (layer[u] + 1 > layer[v]) layer[v] = layer[u] + 1
        if (--indeg[v] === 0) order.push(v)
      }
    }
    // Tightening: a node with more out-edges than in-edges shortens its
    // spans by moving right, up to the column before its nearest successor;
    // the mirror case moves left. A node with one neighbour ends up beside it.
    const outdeg = new Int32Array(n)
    const indegAll = new Int32Array(n)
    for (let e = 0; e < m; e++) {
      outdeg[this.es[e]]++
      indegAll[this.et[e]]++
    }
    for (let round = 0; round < n; round++) {
      let changed = false
      for (let v = 0; v < n; v++) {
        if (outdeg[v] > indegAll[v]) {
          let bound = Infinity
          for (let e = 0; e < m; e++) if (this.es[e] === v) bound = Math.min(bound, layer[this.et[e]] - 1)
          if (bound !== Infinity && bound > layer[v]) {
            layer[v] = bound
            changed = true
          }
        } else if (indegAll[v] > outdeg[v]) {
          let bound = -Infinity
          for (let e = 0; e < m; e++) if (this.et[e] === v) bound = Math.max(bound, layer[this.es[e]] + 1)
          if (bound !== -Infinity && bound < layer[v]) {
            layer[v] = bound
            changed = true
          }
        }
      }
      if (!changed) break
    }
    // Compress away empty columns.
    const used = [...new Set(layer)].sort((a, b) => a - b)
    const remap = new Map(used.map((l, i) => [l, i]))
    for (let v = 0; v < n; v++) layer[v] = remap.get(layer[v])!
    this.L = used.length
    return layer
  }

  // ── Virtual graph: dummies, segments, adjacency ──────────────────────────

  private buildVirtualGraph(layer: Int32Array): void {
    const n = this.n
    const m = this.edges.length
    let dummies = 0
    for (let e = 0; e < m; e++) dummies += Math.max(0, layer[this.et[e]] - layer[this.es[e]] - 1)
    const N = n + dummies
    this.N = N
    this.w = new Float64Array(N)
    this.h = new Float64Array(N)
    this.layerOf = new Int32Array(N)
    this.isDummy = new Uint8Array(N)
    for (let v = 0; v < n; v++) {
      const s = this.size(this.nodes[v])
      this.w[v] = s.width
      this.h[v] = s.height
      this.layerOf[v] = layer[v]
    }
    // Chains: every edge's virtual nodes from source to target.
    this.chainOff = new Int32Array(m + 1)
    for (let e = 0; e < m; e++) this.chainOff[e + 1] = this.chainOff[e] + layer[this.et[e]] - layer[this.es[e]] + 1
    this.chain = new Int32Array(this.chainOff[m])
    let next = n
    for (let e = 0; e < m; e++) {
      let k = this.chainOff[e]
      this.chain[k++] = this.es[e]
      for (let l = layer[this.es[e]] + 1; l < layer[this.et[e]]; l++) {
        this.layerOf[next] = l
        this.isDummy[next] = 1
        this.chain[k++] = next++
      }
      this.chain[k] = this.et[e]
    }
    // Segment adjacency in CSR form, both directions.
    const outCount = new Int32Array(N + 1)
    const inCount = new Int32Array(N + 1)
    for (let e = 0; e < m; e++) {
      for (let k = this.chainOff[e]; k + 1 < this.chainOff[e + 1]; k++) {
        outCount[this.chain[k] + 1]++
        inCount[this.chain[k + 1] + 1]++
      }
    }
    for (let v = 0; v < N; v++) {
      outCount[v + 1] += outCount[v]
      inCount[v + 1] += inCount[v]
    }
    this.outOff = outCount
    this.inOff = inCount
    this.outAdj = new Int32Array(outCount[N])
    this.inAdj = new Int32Array(inCount[N])
    const outFill = outCount.slice(0, N)
    const inFill = inCount.slice(0, N)
    for (let e = 0; e < m; e++) {
      for (let k = this.chainOff[e]; k + 1 < this.chainOff[e + 1]; k++) {
        const a = this.chain[k]
        const b = this.chain[k + 1]
        this.outAdj[outFill[a]++] = b
        this.inAdj[inFill[b]++] = a
      }
    }
    // Real edges per real node.
    const edgeCount = new Int32Array(n + 1)
    for (let e = 0; e < m; e++) {
      edgeCount[this.es[e] + 1]++
      edgeCount[this.et[e] + 1]++
    }
    for (let v = 0; v < n; v++) edgeCount[v + 1] += edgeCount[v]
    this.edgeOff = edgeCount
    this.edgeAdj = new Int32Array(edgeCount[n])
    const edgeFill = edgeCount.slice(0, n)
    for (let e = 0; e < m; e++) {
      this.edgeAdj[edgeFill[this.es[e]]++] = e
      this.edgeAdj[edgeFill[this.et[e]]++] = e
    }
    // Layers.
    const counts = new Int32Array(this.L)
    for (let v = 0; v < N; v++) counts[this.layerOf[v]]++
    this.layers = []
    let widest = 0
    for (let l = 0; l < this.L; l++) {
      this.layers.push(new Int32Array(counts[l]))
      widest = Math.max(widest, counts[l])
    }
    this.pos = new Int32Array(N)
    this.bit = new Int32Array(widest + 2)
    // Scratch holds one column's neighbour positions: bounded by the widest
    // column, or by a node's degree when parallel edges repeat a neighbour.
    let maxDegree = 0
    for (let v = 0; v < N; v++) {
      maxDegree = Math.max(maxDegree, outCount[v + 1] - outCount[v], inCount[v + 1] - inCount[v])
    }
    this.scratch = new Int32Array(Math.max(widest, maxDegree, 2))
    this.fscratch = new Float64Array(this.scratch.length)
    this.y = new Float64Array(N)
    // Column geometry.
    this.colW = new Float64Array(this.L)
    for (let v = 0; v < n; v++) this.colW[layer[v]] = Math.max(this.colW[layer[v]], this.w[v])
    this.colX = new Float64Array(this.L)
    for (let l = 1; l < this.L; l++) this.colX[l] = this.colX[l - 1] + this.colW[l - 1] + this.params.rankGap
    // A leg may cross a column diagonally, without turning, while it stays
    // this close to the row it is heading for: under BEND_ANGLE_DEG over the
    // narrowest column, and clear of the cards above and below that row.
    let narrowest = Infinity
    for (let l = 0; l < this.L; l++) narrowest = Math.min(narrowest, this.colW[l])
    this.diagMax = Math.max(0, Math.min(this.params.nodeGap - GRAZE, (this.params.rankGap + narrowest) * TAN_BEND))
  }

  /** Channel x to the right of column l — where long edges change rows. */
  private channelX(l: number): number {
    return this.colX[l] + this.colW[l] + this.params.rankGap / 2
  }

  // ── 4. Ordering ───────────────────────────────────────────────────────────

  private fillLayers(key: (v: number) => number): void {
    const fill = new Int32Array(this.L)
    const order = Array.from({ length: this.N }, (_, v) => v).sort((a, b) => key(a) - key(b) || a - b)
    for (const v of order) {
      const l = this.layerOf[v]
      this.pos[v] = fill[l]
      this.layers[l][fill[l]++] = v
    }
  }

  /** Crossings between segments of u and of v when u sits immediately before v. */
  private pairCrossings(u: number, v: number): number {
    let c = 0
    const pos = this.pos
    for (let i = this.outOff[u]; i < this.outOff[u + 1]; i++) {
      const pa = pos[this.outAdj[i]]
      for (let j = this.outOff[v]; j < this.outOff[v + 1]; j++) if (pa > pos[this.outAdj[j]]) c++
    }
    for (let i = this.inOff[u]; i < this.inOff[u + 1]; i++) {
      const pa = pos[this.inAdj[i]]
      for (let j = this.inOff[v]; j < this.inOff[v + 1]; j++) if (pa > pos[this.inAdj[j]]) c++
    }
    return c
  }

  /** Exact crossings between columns l and l+1: inversions of target positions, via a Fenwick tree. */
  private layerCrossings(l: number): number {
    const next = this.layers[l + 1].length
    const bit = this.bit
    bit.fill(0, 0, next + 2)
    let inserted = 0
    let crossings = 0
    const scratch = this.scratch
    for (const u of this.layers[l]) {
      let k = 0
      for (let i = this.outOff[u]; i < this.outOff[u + 1]; i++) scratch[k++] = this.pos[this.outAdj[i]]
      // Ascending within one source: two segments from the same node never cross.
      for (let i = 1; i < k; i++) {
        const x = scratch[i]
        let j = i - 1
        while (j >= 0 && scratch[j] > x) {
          scratch[j + 1] = scratch[j]
          j--
        }
        scratch[j + 1] = x
      }
      for (let i = 0; i < k; i++) {
        const p = scratch[i]
        let sum = 0
        for (let q = p + 1; q > 0; q -= q & -q) sum += bit[q]
        crossings += inserted - sum
        for (let q = p + 1; q <= next; q += q & -q) bit[q]++
        inserted++
      }
    }
    return crossings
  }

  private totalCrossings(): number {
    let c = 0
    for (let l = 0; l + 1 < this.L; l++) c += this.layerCrossings(l)
    return c
  }

  /** Reorders column l by the median position of each node's neighbours in the reference column. */
  private medianSweep(l: number, up: boolean): void {
    const layer = this.layers[l]
    const off = up ? this.outOff : this.inOff
    const adj = up ? this.outAdj : this.inAdj
    const measure = new Float64Array(layer.length)
    for (let i = 0; i < layer.length; i++) {
      const v = layer[i]
      let k = 0
      for (let j = off[v]; j < off[v + 1]; j++) this.scratch[k++] = this.pos[adj[j]]
      if (k === 0) {
        measure[i] = i
        continue
      }
      const s = this.scratch
      for (let a = 1; a < k; a++) {
        const x = s[a]
        let b = a - 1
        while (b >= 0 && s[b] > x) {
          s[b + 1] = s[b]
          b--
        }
        s[b + 1] = x
      }
      measure[i] = k % 2 === 1 ? s[(k - 1) / 2] : (s[k / 2 - 1] + s[k / 2]) / 2
    }
    const idx = Array.from(layer.keys()).sort((a, b) => measure[a] - measure[b] || a - b)
    const sorted = idx.map((i) => layer[i])
    for (let i = 0; i < sorted.length; i++) {
      layer[i] = sorted[i]
      this.pos[sorted[i]] = i
    }
  }

  /** Adjacent-swap local search over every column until no swap reduces crossings. */
  private transpose(): void {
    for (let round = 0; round < MAX_TRANSPOSE_ROUNDS; round++) {
      let improved = false
      for (let l = 0; l < this.L; l++) {
        const layer = this.layers[l]
        for (let i = 0; i + 1 < layer.length; i++) {
          const u = layer[i]
          const v = layer[i + 1]
          if (this.pairCrossings(v, u) < this.pairCrossings(u, v)) {
            layer[i] = v
            layer[i + 1] = u
            this.pos[v] = i
            this.pos[u] = i + 1
            improved = true
          }
        }
      }
      if (!improved) return
    }
  }

  private snapshotOrder(into: Int32Array): void {
    let k = 0
    for (const layer of this.layers) for (const v of layer) into[k++] = v
  }

  private restoreOrder(from: Int32Array): void {
    let k = 0
    for (const layer of this.layers) {
      for (let i = 0; i < layer.length; i++) {
        layer[i] = from[k++]
        this.pos[layer[i]] = i
      }
    }
  }

  private order(): void {
    const N = this.N
    const degree = new Int32Array(N)
    for (let v = 0; v < N; v++) degree[v] = this.outOff[v + 1] - this.outOff[v] + this.inOff[v + 1] - this.inOff[v]
    // Deterministic starts: id order, reverse, by degree either way. Dummies
    // sort after the real nodes of their column in every start.
    const dummyAfter = (v: number) => (this.isDummy[v] ? N : 0)
    const starts: Array<(v: number) => number> = [
      (v) => dummyAfter(v) + v,
      (v) => dummyAfter(v) + (N - v),
      (v) => dummyAfter(v) - degree[v] * N + v,
      (v) => dummyAfter(v) + degree[v] * N + v,
    ]
    const best = new Int32Array(N)
    let bestCrossings = Infinity
    for (const key of starts) {
      this.fillLayers(key)
      let stale = 0
      let localBest = Infinity
      for (let round = 0; round < MAX_SWEEP_ROUNDS; round++) {
        for (let l = 1; l < this.L; l++) this.medianSweep(l, false)
        this.transpose()
        for (let l = this.L - 2; l >= 0; l--) this.medianSweep(l, true)
        this.transpose()
        const c = this.totalCrossings()
        if (c < localBest) {
          localBest = c
          stale = 0
          if (c < bestCrossings) {
            bestCrossings = c
            this.snapshotOrder(best)
          }
        } else if (++stale >= 2) break
        if (c === 0) break
      }
      if (bestCrossings === 0) break
    }
    this.restoreOrder(best)
    if (bestCrossings > 0) this.annealOrder(bestCrossings, best)
  }

  /** Simulated annealing over adjacent swaps, judged by the exact crossing delta. */
  private annealOrder(startCrossings: number, best: Int32Array): void {
    const iterations = Math.round(this.iterationsPerNode * this.N * this.tuning.orderShare)
    if (iterations <= 0 || this.N < 2) return
    ENGINE_STATS.iterations += iterations
    const { orderTemp0, orderTemp1 } = this.tuning
    const cooling = Math.pow(orderTemp1 / orderTemp0, 1 / iterations)
    let temp = orderTemp0
    let current = startCrossings
    let bestCrossings = startCrossings
    const rng = this.rng
    for (let it = 0; it < iterations; it++, temp *= cooling) {
      const v = Math.floor(rng() * this.N)
      const layer = this.layers[this.layerOf[v]]
      if (layer.length < 2) continue
      const i = this.pos[v]
      const j = i + 1 < layer.length ? i + 1 : i - 1
      const a = layer[Math.min(i, j)]
      const b = layer[Math.max(i, j)]
      const delta = this.pairCrossings(b, a) - this.pairCrossings(a, b)
      if (delta > 0 && rng() >= Math.exp(-delta / temp)) continue
      const lo = Math.min(i, j)
      layer[lo] = b
      layer[lo + 1] = a
      this.pos[b] = lo
      this.pos[a] = lo + 1
      current += delta
      if (current < bestCrossings) {
        bestCrossings = current
        this.snapshotOrder(best)
        if (current === 0) break
      }
    }
    this.restoreOrder(best)
  }

  // ── 5. Rows: priority placement, then annealing ─────────────────────────

  private gapAfter(layer: Int32Array, i: number): number {
    const dummies = this.isDummy[layer[i]] + this.isDummy[layer[i + 1]]
    return dummies === 2 ? this.params.laneGap : dummies === 1 ? this.params.edgeGap : this.params.nodeGap
  }

  private centre(v: number): number {
    return this.y[v] + this.h[v] / 2
  }

  /** Stacks every column from the top, in order, as the starting point. */
  private stackColumns(): void {
    for (const layer of this.layers) {
      let cursor = 0
      for (let i = 0; i < layer.length; i++) {
        const v = layer[i]
        this.y[v] = cursor
        cursor += this.h[v] + (i + 1 < layer.length ? this.gapAfter(layer, i) : 0)
      }
    }
  }

  /**
   * Places column l so each node sits at the median row of its neighbours
   * in the reference column, highest priority first (dummies, then by
   * neighbour count), each clamped so the nodes already placed keep their
   * place and the ones still to come keep enough room.
   */
  private placeColumn(l: number, up: boolean): void {
    const layer = this.layers[l]
    const k = layer.length
    const off = up ? this.outOff : this.inOff
    const adj = up ? this.outAdj : this.inAdj
    const desired = new Float64Array(k)
    const priority = new Float64Array(k)
    for (let i = 0; i < k; i++) {
      const v = layer[i]
      let count = 0
      const s = this.fscratch
      for (let j = off[v]; j < off[v + 1]; j++) s[count++] = this.centre(adj[j])
      if (count === 0) {
        desired[i] = this.centre(v)
        priority[i] = -1
        continue
      }
      for (let a = 1; a < count; a++) {
        const x = s[a]
        let b = a - 1
        while (b >= 0 && s[b] > x) {
          s[b + 1] = s[b]
          b--
        }
        s[b + 1] = x
      }
      desired[i] = count % 2 === 1 ? s[(count - 1) / 2] : (s[count / 2 - 1] + s[count / 2]) / 2
      priority[i] = (this.isDummy[v] ? k + 1 : 0) + count
    }
    const order = Array.from({ length: k }, (_, i) => i).sort((a, b) => priority[b] - priority[a] || a - b)
    const placed = new Uint8Array(k)
    for (const i of order) {
      const v = layer[i]
      // Room the unplaced nodes between i and the nearest placed neighbour need.
      let lower = -Infinity
      let need = 0
      for (let j = i - 1; j >= 0; j--) {
        need += this.gapAfter(layer, j)
        if (placed[j]) {
          lower = this.y[layer[j]] + this.h[layer[j]] + need
          break
        }
        need += this.h[layer[j]]
      }
      let upper = Infinity
      need = 0
      for (let j = i + 1; j < k; j++) {
        need += this.gapAfter(layer, j - 1)
        if (placed[j]) {
          upper = this.y[layer[j]] - need - this.h[v]
          break
        }
        need += this.h[layer[j]]
      }
      const want = desired[i] - this.h[v] / 2
      this.y[v] = Math.max(lower, Math.min(upper, want))
      placed[i] = 1
    }
  }

  private placeRows(): void {
    this.stackColumns()
    for (let pass = 0; pass < COORD_PASSES; pass++) {
      for (let l = 1; l < this.L; l++) this.placeColumn(l, false)
      for (let l = this.L - 2; l >= 0; l--) this.placeColumn(l, true)
    }
  }

  /**
   * Bends a leg between consecutive chain nodes a→b costs, given the row
   * difference between them, mirroring how `routeChain` will draw it: none
   * while the leg can slope within diagMax; otherwise one turn where the leg
   * meets a handle directly and two where it has to run along a channel.
   */
  private legBends(a: number, b: number, dy: number): number {
    if (Math.abs(dy) <= this.diagMax) return 0
    const aDummy = this.isDummy[a] === 1
    const bDummy = this.isDummy[b] === 1
    if (aDummy && bDummy) return 2
    if (aDummy) return 1
    const flush = this.w[a] === this.colW[this.layerOf[a]]
    if (bDummy) return flush ? 1 : 2
    return flush ? 0 : 1
  }

  /** The row-dependent cost of the segment a→b. */
  private segmentCost(a: number, b: number): number {
    const dy = this.centre(b) - this.centre(a)
    return W_LENGTH * Math.hypot(this.params.rankGap, dy) + W_BEND * this.legBends(a, b, dy)
  }

  /** The row-dependent cost of a real edge: handle misalignment and card-to-card distance. */
  private edgeCost(e: number): number {
    const u = this.es[e]
    const v = this.et[e]
    const misalign = Math.abs(this.centre(u) - this.centre(v))
    const hgap = this.colX[this.layerOf[v]] - (this.colX[this.layerOf[u]] + this.w[u])
    const vgap = Math.max(0, this.y[v] - (this.y[u] + this.h[u]), this.y[u] - (this.y[v] + this.h[v]))
    return W_MISALIGN * misalign + W_NEIGHBOUR * Math.max(0, Math.hypot(hgap, vgap) - this.params.rankGap)
  }

  /** Everything in the objective that moving node v would change. */
  private localCost(v: number): number {
    let c = 0
    for (let i = this.outOff[v]; i < this.outOff[v + 1]; i++) c += this.segmentCost(v, this.outAdj[i])
    for (let i = this.inOff[v]; i < this.inOff[v + 1]; i++) c += this.segmentCost(this.inAdj[i], v)
    if (!this.isDummy[v]) for (let i = this.edgeOff[v]; i < this.edgeOff[v + 1]; i++) c += this.edgeCost(this.edgeAdj[i])
    return c
  }

  /** Everything in the objective that shifting whole column l would change. */
  private columnCost(l: number): number {
    let c = 0
    for (const v of this.layers[l]) {
      for (let i = this.outOff[v]; i < this.outOff[v + 1]; i++) c += this.segmentCost(v, this.outAdj[i])
      for (let i = this.inOff[v]; i < this.inOff[v + 1]; i++) c += this.segmentCost(this.inAdj[i], v)
      if (this.isDummy[v]) continue
      for (let i = this.edgeOff[v]; i < this.edgeOff[v + 1]; i++) {
        const e = this.edgeAdj[i]
        // An edge inside the column moves with it; count it once from its source.
        if (this.layerOf[this.es[e]] === l && this.layerOf[this.et[e]] === l && this.es[e] !== v) continue
        c += this.edgeCost(e)
      }
    }
    return c
  }

  /** Height of the bounding box of the real nodes. */
  private bboxHeight(): number {
    let top = Infinity
    let bottom = -Infinity
    for (let v = 0; v < this.n; v++) {
      top = Math.min(top, this.y[v])
      bottom = Math.max(bottom, this.y[v] + this.h[v])
    }
    return bottom - top
  }

  /** Vertical slack for node v: how far up (negative) and down it may move without touching its column neighbours. */
  private slack(v: number, out: Float64Array): void {
    const layer = this.layers[this.layerOf[v]]
    const i = this.pos[v]
    out[0] = i > 0 ? this.y[layer[i - 1]] + this.h[layer[i - 1]] + this.gapAfter(layer, i - 1) - this.y[v] : -Infinity
    out[1] = i + 1 < layer.length ? this.y[layer[i + 1]] - this.gapAfter(layer, i) - this.h[v] - this.y[v] : Infinity
  }

  /** Simulated annealing over row shifts with an incremental objective. */
  private annealRows(): void {
    const iterations = Math.round(this.iterationsPerNode * this.N * (1 - this.tuning.orderShare))
    if (iterations <= 0 || this.N < 2 || this.n < 2) return
    ENGINE_STATS.iterations += iterations
    const { rowTemp0, rowTemp1, maxShift, alignShare, columnShare } = this.tuning
    const cooling = Math.pow(rowTemp1 / rowTemp0, 1 / iterations)
    let temp = rowTemp0
    const rng = this.rng
    // Compactness, S10, as a function of the height alone since the width
    // is fixed by the columns: linear in it. (Aspect is left to the packer:
    // a component is rarely its level's whole shape, and pulling one taller
    // costs alignment for a term the packing then decides anyway.)
    let cardArea = 0
    for (let v = 0; v < this.n; v++) cardArea += this.w[v] * this.h[v]
    const width = this.colX[this.L - 1] + this.colW[this.L - 1]
    const shape = (height: number) => (W_COMPACT * this.n * width * height) / cardArea
    const slack = new Float64Array(2)
    let height = this.bboxHeight()
    let best = 0
    let current = 0
    const bestY = new Float64Array(this.y)
    for (let it = 0; it < iterations; it++, temp *= cooling) {
      const roll = rng()
      if (roll < columnShare && this.L > 1) {
        const l = Math.floor(rng() * this.L)
        const delta = (rng() * 2 - 1) * maxShift
        const before = this.columnCost(l)
        const layer = this.layers[l]
        for (const v of layer) this.y[v] += delta
        const newHeight = this.bboxHeight()
        const change = this.columnCost(l) - before + shape(newHeight) - shape(height)
        if (change > 0 && rng() >= Math.exp(-change / temp)) {
          for (const v of layer) this.y[v] -= delta
          continue
        }
        height = newHeight
        current += change
      } else {
        const v = Math.floor(rng() * this.N)
        this.slack(v, slack)
        let delta: number
        if (roll < columnShare + alignShare) {
          // Snap to a neighbour's row: the move that makes straight rows.
          const outs = this.outOff[v + 1] - this.outOff[v]
          const ins = this.inOff[v + 1] - this.inOff[v]
          if (outs + ins === 0) continue
          const pick = Math.floor(rng() * (outs + ins))
          const nb = pick < outs ? this.outAdj[this.outOff[v] + pick] : this.inAdj[this.inOff[v] + pick - outs]
          delta = this.centre(nb) - this.centre(v)
        } else {
          delta = (rng() * 2 - 1) * maxShift
        }
        delta = Math.max(slack[0], Math.min(slack[1], delta))
        if (delta === 0 || !Number.isFinite(delta)) continue
        const before = this.localCost(v)
        this.y[v] += delta
        const layer = this.layers[this.layerOf[v]]
        const atEdge = !this.isDummy[v] && (this.pos[v] === 0 || this.pos[v] === layer.length - 1)
        const newHeight = atEdge ? this.bboxHeight() : height
        const change = this.localCost(v) - before + shape(newHeight) - shape(height)
        if (change > 0 && rng() >= Math.exp(-change / temp)) {
          this.y[v] -= delta
          continue
        }
        height = newHeight
        current += change
      }
      if (current < best) {
        best = current
        bestY.set(this.y)
      }
    }
    this.y.set(bestY)
  }

  // ── 6. Routing and output ────────────────────────────────────────────────

  private handleOut(v: number): Pt {
    return { x: this.colX[this.layerOf[v]] + this.w[v], y: this.centre(v) }
  }

  private handleIn(v: number): Pt {
    return { x: this.colX[this.layerOf[v]], y: this.centre(v) }
  }

  /**
   * Waypoints for a forward edge: out of the source handle, along the
   * channel to the right of each column it passes, across each column on
   * its dummy's row, into the target handle. Legs that can slope within
   * diagMax skip their channel points, so an aligned edge is a straight
   * line; the rest turn where legBends said they would.
   */
  private routeChain(e: number): Pt[] {
    const pts: Pt[] = []
    const from = this.chainOff[e]
    const to = this.chainOff[e + 1] - 1
    const source = this.chain[from]
    const target = this.chain[to]
    const flush = this.w[source] === this.colW[this.layerOf[source]]
    const lSource = this.layerOf[source]
    // Leaving the source: a card narrower than its column first runs out to
    // the channel on its own row so the leg never crosses a sibling below.
    const firstDy = this.centre(this.chain[from + 1]) - this.centre(source)
    if (!flush && Math.abs(firstDy) > this.diagMax) pts.push({ x: this.channelX(lSource), y: this.centre(source) })
    for (let k = from + 1; k < to; k++) {
      const d = this.chain[k]
      const prev = this.chain[k - 1]
      const l = this.layerOf[d]
      const cy = this.centre(d)
      const dyIn = cy - this.centre(prev)
      // Entry channel point, unless the leg from the previous node slopes in.
      if (Math.abs(dyIn) > this.diagMax) pts.push({ x: this.channelX(l - 1), y: cy })
      const next = this.chain[k + 1]
      const dyOut = this.centre(next) - cy
      if (Math.abs(dyOut) > this.diagMax) pts.push({ x: this.channelX(l), y: cy })
    }
    return simplify([this.handleOut(source), ...pts, this.handleIn(target)])
  }

  /**
   * A back edge is drawn from the card on the right to the one on the left:
   * out and up (or down) to a lane clear of every card in the columns it
   * spans, across, and in. The nearer lane wins. A card flush with its
   * column leaves on a diagonal straight to the lane, which is one bend
   * fewer than a stub; a narrower card first runs out to the channel so the
   * diagonal cannot cut through a wider sibling.
   */
  private routeLoop(e: number): Pt[] {
    // After cycle breaking es→et is the DAG direction; the drawn edge runs the other way.
    const s = this.et[e]
    const t = this.es[e]
    const ls = this.layerOf[s]
    const lt = this.layerOf[t]
    const { loopStub, loopLane } = this.params
    const exitX = ls + 1 < this.L ? this.channelX(ls) : this.colX[ls] + this.colW[ls] + loopStub
    const entryX = lt > 0 ? this.channelX(lt - 1) : this.colX[lt] - loopStub
    const from = this.handleOut(s)
    const to = this.handleIn(t)
    // Walk outward from the pair until the lane clears every card it would cross.
    let above = Math.min(this.y[s], this.y[t]) - loopLane
    let below = Math.max(this.y[s] + this.h[s], this.y[t] + this.h[t]) + loopLane
    for (let l = lt; l <= ls; l++) {
      for (const v of this.layers[l]) {
        if (this.isDummy[v]) continue
        if (this.y[v] - loopLane < above && this.y[v] + this.h[v] + loopLane > above) above = this.y[v] - loopLane
        if (this.y[v] - loopLane < below && this.y[v] + this.h[v] + loopLane > below) below = this.y[v] + this.h[v] + loopLane
      }
    }
    // One more sweep: raising the lane can expose another card.
    for (let l = lt; l <= ls; l++) {
      for (const v of this.layers[l]) {
        if (this.isDummy[v]) continue
        if (this.y[v] - loopLane < above && this.y[v] + this.h[v] + loopLane > above) above = this.y[v] - loopLane
        if (this.y[v] - loopLane < below && this.y[v] + this.h[v] + loopLane > below) below = this.y[v] + this.h[v] + loopLane
      }
    }
    const mid = (from.y + to.y) / 2
    const lane = mid - above <= below - mid ? above : below
    const flush = this.w[s] === this.colW[ls]
    const pts: Pt[] = flush ? [] : [{ x: exitX, y: from.y }]
    pts.push({ x: exitX, y: lane }, { x: entryX, y: lane })
    return pts
  }

  private emit(): ComponentResult {
    const raw = new Map<number, Pt[]>()
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (let v = 0; v < this.n; v++) {
      minX = Math.min(minX, this.colX[this.layerOf[v]])
      minY = Math.min(minY, this.y[v])
      maxX = Math.max(maxX, this.colX[this.layerOf[v]] + this.w[v])
      maxY = Math.max(maxY, this.y[v] + this.h[v])
    }
    // A lane that runs past the cards is part of the extent: what gets
    // packed beside it must not sit on the edge.
    const margin = this.params.edgeGap
    for (let e = 0; e < this.edges.length; e++) {
      const pts = this.reversed[e] ? this.routeLoop(e) : this.routeChain(e)
      if (pts.length === 0) continue
      raw.set(e, pts)
      for (const p of pts) {
        minX = Math.min(minX, p.x)
        minY = Math.min(minY, p.y - margin)
        maxX = Math.max(maxX, p.x)
        maxY = Math.max(maxY, p.y + margin)
      }
    }
    const positioned: Positioned[] = []
    for (let v = 0; v < this.n; v++) {
      positioned.push({
        node: this.nodes[v],
        x: Math.round(this.colX[this.layerOf[v]] - minX),
        y: Math.round(this.y[v] - minY),
        w: this.w[v],
        h: this.h[v],
      })
    }
    const routes: Routes = new Map()
    for (const [e, pts] of raw) {
      routes.set(
        this.edges[e].id,
        pts.map((p) => ({ x: Math.round(p.x - minX), y: Math.round(p.y - minY) })),
      )
    }
    return { positioned, routes, w: Math.round(maxX - minX), h: Math.round(maxY - minY) }
  }
}

/** Drops repeated and collinear points; returns the interior waypoints only. */
function simplify(poly: Pt[]): Pt[] {
  const out: Pt[] = [poly[0]]
  for (let i = 1; i < poly.length; i++) {
    const p = poly[i]
    const last = out[out.length - 1]
    if (p.x === last.x && p.y === last.y) continue
    if (out.length >= 2) {
      const prev = out[out.length - 2]
      const cross = (last.x - prev.x) * (p.y - last.y) - (last.y - prev.y) * (p.x - last.x)
      const dot = (last.x - prev.x) * (p.x - last.x) + (last.y - prev.y) * (p.y - last.y)
      if (cross === 0 && dot > 0) {
        out[out.length - 1] = p
        continue
      }
    }
    out.push(p)
  }
  return out.slice(1, -1)
}

// ─── Level: components and packing ───────────────────────────────────────────

/** One packable unit: a laid-out connected component, or a lone card. */
interface Block {
  /** Smallest member id — a stable tie-breaker so the packing is deterministic. */
  key: string
  /** Lone cards are inventory; everything else is a flow. */
  lone: boolean
  w: number
  h: number
  positioned: Positioned[]
  routes: Routes
  service: string
  label: string
}

export function translateRoutes(routes: Routes, dx: number, dy: number, into: Routes): void {
  for (const [id, pts] of routes) {
    into.set(
      id,
      pts.map((p) => ({ x: p.x + dx, y: p.y + dy })),
    )
  }
}

/**
 * Shelf packing of blocks into rows no wider than `targetW`: the flows in
 * shelves, then the inventory on a grid whose pitch is the widest card plus
 * a routable gap.
 */
function packShelves(flows: Block[], inventory: Block[], targetW: number, params: LevelParams): SubgraphLayout {
  const positioned: Positioned[] = []
  const routes: Routes = new Map()
  let x = 0
  let y = 0
  let shelfH = 0
  const put = (b: Block) => {
    for (const p of b.positioned) positioned.push({ ...p, x: p.x + x, y: p.y + y })
    translateRoutes(b.routes, x, y, routes)
    shelfH = Math.max(shelfH, b.h)
  }
  for (const b of flows) {
    if (x > 0 && x + b.w > targetW) {
      y += shelfH + params.blockGap
      x = 0
      shelfH = 0
    }
    put(b)
    x += b.w + params.blockGap
  }
  if (flows.length > 0 && inventory.length > 0) {
    // The inventory starts on a fresh shelf: the flows above, the cards below.
    y += shelfH + params.blockGap
    x = 0
    shelfH = 0
  }
  let pitch = params.defaultW
  for (const b of inventory) pitch = Math.max(pitch, b.w)
  pitch += params.inventoryGap
  const columns = Math.max(1, Math.floor((targetW + params.inventoryGap) / pitch))
  let column = 0
  for (const b of inventory) {
    if (column === columns) {
      y += shelfH + params.inventoryRowGap
      column = 0
      shelfH = 0
    }
    x = column * pitch
    put(b)
    column++
  }
  // The level is the cards' bounding box — its container is that plus
  // padding — so a lane running above the first shelf is let into the
  // padding rather than counted as content.
  let left = Infinity
  let top = Infinity
  let right = 0
  let bottom = 0
  for (const p of positioned) {
    left = Math.min(left, p.x)
    top = Math.min(top, p.y)
  }
  for (const p of positioned) {
    p.x -= left
    p.y -= top
    right = Math.max(right, p.x + p.w)
    bottom = Math.max(bottom, p.y + p.h)
  }
  if (left !== 0 || top !== 0) translateRoutes(routes, -left, -top, routes)
  return { positioned, width: right, height: bottom, routes }
}

/**
 * What the score would charge this level for its shape alone: compactness
 * (S10) on every level, aspect (S11) on a region only — a box inside it is
 * free to be as long as its flow.
 */
function shapeCost(layout: SubgraphLayout, cardArea: number, count: number, region: boolean): number {
  if (count < 2) return 0
  const area = layout.width * layout.height
  const compact = W_COMPACT * (area / cardArea - 1) * count
  if (!region) return compact
  return compact + W_ASPECT * Math.abs(Math.log2(layout.width / layout.height) - ASPECT_TARGET_LOG2)
}

/** How many target widths the packer tries between the widest block and a tall column. */
const PACK_WIDTH_CANDIDATES = 12

/**
 * Packs the blocks of a level. The shelf packer is deterministic given a
 * target width, so the search is over the width: a dozen candidates from
 * the widest block up to a wide strip, keeping the one the score's shape
 * terms like best. `region` says whether this level is a region's, where
 * the aspect term applies.
 */
function packLevel(flows: Block[], inventory: Block[], params: LevelParams, region: boolean): SubgraphLayout {
  flows.sort((a, b) => b.w * b.h - a.w * a.h || b.h - a.h || (a.key < b.key ? -1 : 1))
  inventory.sort(
    (a, b) =>
      (a.service < b.service ? -1 : a.service > b.service ? 1 : 0) ||
      b.h - a.h ||
      (a.label < b.label ? -1 : a.label > b.label ? 1 : 0) ||
      (a.key < b.key ? -1 : 1),
  )
  const blocks = [...flows, ...inventory]
  let widest = 0
  let cardArea = 0
  let total = 0
  let count = 0
  for (const b of blocks) {
    widest = Math.max(widest, b.w)
    total += (b.w + params.blockGap) * (b.h + params.blockGap)
    for (const p of b.positioned) cardArea += p.w * p.h
    count += b.positioned.length
  }
  const upper = Math.max(widest, Math.sqrt(total * 2 ** ASPECT_TARGET_LOG2) * 2)
  let best: SubgraphLayout | undefined
  let bestCost = Infinity
  for (let i = 0; i <= PACK_WIDTH_CANDIDATES; i++) {
    const targetW = widest + ((upper - widest) * i) / PACK_WIDTH_CANDIDATES
    const laid = packShelves(flows, inventory, targetW, params)
    const cost = shapeCost(laid, cardArea, count, region)
    if (cost < bestCost) {
      bestCost = cost
      best = laid
    }
  }
  return best!
}

/**
 * Lays out one level of the hierarchy. `isBlock` marks lone items that lead
 * with the flows rather than being filed with the inventory — a stack or
 * VPC box has a whole flow inside even when nothing outside connects to it.
 * `region` says the level is a region's direct contents, the one level whose
 * shape the score judges for aspect.
 */
export function layoutLevel(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  sizeOverrides: SizeOverrides,
  params: LevelParams,
  iterationsPerNode: number,
  isBlock: (id: string) => boolean,
  region: boolean,
  tuning: AnnealerTuning = ANNEALER_TUNING,
): SubgraphLayout {
  if (nodes.length === 0) return { positioned: [], width: 0, height: 0, routes: new Map() }
  const size = (n: TopologyNode) => {
    const o = sizeOverrides[n.id]
    return { width: o?.width ?? params.defaultW, height: o?.height ?? params.defaultH }
  }

  // Connected components by union–find over the usable edges.
  const index = new Map<string, number>()
  nodes.forEach((n, i) => index.set(n.id, i))
  const parent = nodes.map((_, i) => i)
  const find = (i: number): number => {
    while (parent[i] !== i) {
      parent[i] = parent[parent[i]]
      i = parent[i]
    }
    return i
  }
  const usable: TopologyEdge[] = []
  for (const e of edges) {
    const a = index.get(e.source)
    const b = index.get(e.target)
    if (a === undefined || b === undefined || a === b) continue
    usable.push(e)
    parent[find(a)] = find(b)
  }
  const groups = new Map<number, TopologyNode[]>()
  nodes.forEach((n, i) => {
    const root = find(i)
    const list = groups.get(root)
    if (list) list.push(n)
    else groups.set(root, [n])
  })

  const flows: Block[] = []
  const inventory: Block[] = []
  for (const members of groups.values()) {
    const key = members.reduce((m, n) => (n.id < m ? n.id : m), members[0].id)
    if (members.length === 1 && !isBlock(members[0].id)) {
      const n = members[0]
      const { width: w, height: h } = size(n)
      inventory.push({
        key,
        lone: true,
        w,
        h,
        positioned: [{ node: n, x: 0, y: 0, w, h }],
        routes: new Map(),
        service: n.service,
        label: n.label,
      })
      continue
    }
    const ids = new Set(members.map((n) => n.id))
    const compEdges = usable.filter((e) => ids.has(e.source) && ids.has(e.target))
    const laid = new ComponentLayout(members, compEdges, size, params, iterationsPerNode, tuning).run()
    flows.push({ key, lone: false, service: "", label: "", ...laid })
  }
  return packLevel(flows, inventory, params, region)
}
