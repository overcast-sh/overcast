/**
 * map-layout-score — what the system map considers a good layout, as a number.
 *
 * docs/plans/map-layout-objective.md states the rules; this module is their
 * one implementation. A layout that breaks a hard rule (H1–H4) is invalid and
 * scores Infinity, with every breach named in `violations`. A valid layout's
 * score is the weighted sum of the soft rules S1–S13; lower is better. The
 * weights are the named constants below and nowhere else: changing one is a
 * deliberate, reviewable change to what the map considers good. (H5,
 * determinism, and H6, the time budget, are properties of the algorithm and
 * are checked by the tests and the benchmark, not by a score.)
 *
 * The score works on the polylines the renderer is given (source right
 * handle → waypoints → target left handle), never on the smoothed curve, so it
 * is independent of how the curve is drawn. Edge terms are measured over the
 * whole canvas in absolute coordinates — a crossing is a crossing whichever
 * boxes the two edges live in — while the arrangement terms (S8, S10, S12)
 * are measured per container level, over that container's direct children,
 * and summed. Aspect (S11) is the exception: it is measured over a region's
 * direct children only. A stack whose contents are a chain should be a long
 * row, and a VPC with two instances a short one; what should be roughly 2:1
 * is the region a reader pans around, not each box inside it.
 *
 * Plain data, no DOM: the layout engine imports the weights from here to
 * drive its own incremental objective, so this module must stay importable
 * from the layout worker.
 */

import type { Node } from "@xyflow/react"
import type { TopologyEdge, TopologyNode } from "@/types"
import { CONTAINER_PADDING, isContainerType, RANK_GAP } from "./map-layout-constants"
import {
  ObstacleIndex,
  segmentHitsRect,
  segmentsCross,
  type Obstacle,
  type Pt,
  type Rect,
} from "./map-edge-routing"
import type { MapLayout } from "./map-layout"

// ─── Hard-rule thresholds ────────────────────────────────────────────────────

/** H1: the least space between any two cards on the axis that separates them. */
export const MIN_GAP = 24
/** H3: a card's rectangle is inflated by this much before asking whether an edge enters it. */
export const EDGE_CLEARANCE = 8
/** H4: a forward edge's target handle is at least this far right of its source handle. */
export const MIN_FORWARD = 24
/**
 * Positions are rounded to whole pixels as they are carried up the hierarchy,
 * so a containment or size check tolerates this much before calling it a
 * breach.
 */
const ROUNDING_TOLERANCE = 1

// ─── Soft-rule thresholds ────────────────────────────────────────────────────

/** S3: a direction change smaller than this, in degrees, is a wobble, not a bend. */
export const BEND_ANGLE_DEG = 15
/** S6: the band outside EDGE_CLEARANCE within which an edge is grazing a card. */
export const GRAZE = 16
/** S7: how much room an edge label's midpoint wants from any card or other label. */
export const LABEL_ROOM = 28
/** S11: target width ÷ height of a level, as a log2 — 1 is 2:1, wider than tall. */
export const ASPECT_TARGET_LOG2 = 1

// ─── Weights ─────────────────────────────────────────────────────────────────
//
// One edge crossing outweighs a lot of extra length: crossings are what make
// a diagram unreadable, length only makes it slow to read.

/** S1: per proper intersection of two edges that share no endpoint. */
export const W_CROSSING = 40
/** S2: per pixel of polyline beyond RANK_GAP. */
export const W_LENGTH = 0.03
/** S3: per direction change of more than BEND_ANGLE_DEG at a waypoint. */
export const W_BEND = 6
/** S4: per edge whose target is left of its source. */
export const W_BACK_EDGE = 30
/** S5: per pixel of vertical distance between an edge's two handles. */
export const W_MISALIGN = 0.05
/** S6: per (edge, card) pair where the edge passes within GRAZE of a card it is not attached to. */
export const W_GRAZE = 10
/** S7: per labelled edge whose midpoint sits within LABEL_ROOM of a card or another label. */
export const W_LABEL = 15
/** S8: per change of service, in reading order of the unconnected cards, beyond the minimum. */
export const W_SERVICE_CHANGE = 12
/** S9: per pixel of gap between two connected cards beyond RANK_GAP. */
export const W_NEIGHBOUR = 0.02
/** S10: per unit of (bounding box area ÷ card area − 1) × card count, per level. */
export const W_COMPACT = 2
/** S11: per unit of |log2(width ÷ height) − ASPECT_TARGET_LOG2|, per region. */
export const W_ASPECT = 25
/** S12: per (connected card, unconnected card) pair where the connected one starts lower. */
export const W_INVENTORY_ORDER = 8
/** S13: when the active region is not the first region box. */
export const W_REGION_ORDER = 200

export const SOFT_RULES = [
  "S1",
  "S2",
  "S3",
  "S4",
  "S5",
  "S6",
  "S7",
  "S8",
  "S9",
  "S10",
  "S11",
  "S12",
  "S13",
] as const
export type SoftRuleId = (typeof SOFT_RULES)[number]

export interface LayoutScore {
  /** Weighted sum of the soft terms, or Infinity when any hard rule is broken. */
  total: number
  /** The weighted sum regardless of validity — what an invalid layout would have scored. */
  soft: number
  /** Weighted contribution of each soft rule. */
  terms: Record<SoftRuleId, number>
  /** The raw quantity each soft rule measured (crossings, pixels, pairs …). */
  counts: Record<SoftRuleId, number>
  /** Every hard-rule breach, as `<rule>:<what>`. Empty for a valid layout. */
  violations: string[]
}

export type SizeOverrides = Record<string, { width: number; height: number } | undefined>

// ─── Geometry helpers shared with the engine ────────────────────────────────

/** Absolute rectangle of every node (containers included), resolved through the parent chain. */
export function absoluteRects(nodes: Node[]): Map<string, Rect> {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const cache = new Map<string, Pt>()
  const origin = (n: Node): Pt => {
    const hit = cache.get(n.id)
    if (hit) return hit
    let o: Pt = { x: n.position.x, y: n.position.y }
    if (n.parentId) {
      const parent = byId.get(n.parentId)
      if (parent) {
        const po = origin(parent)
        o = { x: po.x + o.x, y: po.y + o.y }
      }
    }
    cache.set(n.id, o)
    return o
  }
  const rects = new Map<string, Rect>()
  for (const n of nodes) {
    const o = origin(n)
    rects.set(n.id, { x: o.x, y: o.y, w: n.width ?? 0, h: n.height ?? 0 })
  }
  return rects
}

export function polylineLength(pts: Pt[]): number {
  let length = 0
  for (let i = 0; i + 1 < pts.length; i++) {
    length += Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
  }
  return length
}

const BEND_COS = Math.cos((BEND_ANGLE_DEG * Math.PI) / 180)

/** True when the direction from a→b to b→c changes by more than BEND_ANGLE_DEG. */
export function isBend(a: Pt, b: Pt, c: Pt): boolean {
  const ux = b.x - a.x
  const uy = b.y - a.y
  const vx = c.x - b.x
  const vy = c.y - b.y
  const lu = Math.hypot(ux, uy)
  const lv = Math.hypot(vx, vy)
  if (lu === 0 || lv === 0) return false
  return (ux * vx + uy * vy) / (lu * lv) < BEND_COS
}

/** Direction changes of more than BEND_ANGLE_DEG at the interior points of a polyline. */
export function bendCount(pts: Pt[]): number {
  let bends = 0
  for (let i = 1; i + 1 < pts.length; i++) if (isBend(pts[i - 1], pts[i], pts[i + 1])) bends++
  return bends
}

/** Shortest distance between two rectangles (0 when they touch or overlap). */
export function rectGap(a: Rect, b: Rect): number {
  const dx = Math.max(0, b.x - (a.x + a.w), a.x - (b.x + b.w))
  const dy = Math.max(0, b.y - (a.y + a.h), a.y - (b.y + b.h))
  return Math.hypot(dx, dy)
}

/** Distance from a point to a rectangle (0 inside). */
function pointRectDistance(p: Pt, r: Rect): number {
  const dx = Math.max(r.x - p.x, 0, p.x - (r.x + r.w))
  const dy = Math.max(r.y - p.y, 0, p.y - (r.y + r.h))
  return Math.hypot(dx, dy)
}

/** The point half-way along a polyline by arc length — where the renderer puts a label. */
export function polylineMidpoint(pts: Pt[]): Pt {
  let remaining = polylineLength(pts) / 2
  for (let i = 0; i + 1 < pts.length; i++) {
    const seg = Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
    if (remaining <= seg || i === pts.length - 2) {
      const t = seg === 0 ? 0 : remaining / seg
      return { x: pts[i].x + (pts[i + 1].x - pts[i].x) * t, y: pts[i].y + (pts[i + 1].y - pts[i].y) * t }
    }
    remaining -= seg
  }
  return pts[0]
}

/** Source right-handle → waypoints → target left-handle, as the renderer draws it. */
export function edgePolyline(s: Rect, t: Rect, route: Pt[] | undefined): Pt[] {
  return [{ x: s.x + s.w, y: s.y + s.h / 2 }, ...(route ?? []), { x: t.x, y: t.y + t.h / 2 }]
}

// ─── The score ───────────────────────────────────────────────────────────────

interface EdgeGeometry {
  edge: TopologyEdge
  pts: Pt[]
  s: Rect
  t: Rect
  minX: number
  maxX: number
  minY: number
  maxY: number
}

function zeroTerms(): Record<SoftRuleId, number> {
  const out = {} as Record<SoftRuleId, number>
  for (const id of SOFT_RULES) out[id] = 0
  return out
}

/**
 * Scores a layout against the objective. `sizes` are the per-node overrides
 * the layout was asked to honour; a card drawn at any other size is a breach.
 * `activeRegion` is the region that must come first (S13); when omitted the
 * region order is not judged.
 */
export function scoreLayout(
  layout: MapLayout,
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  sizes: SizeOverrides = {},
  activeRegion?: string,
): LayoutScore {
  const counts = zeroTerms()
  const violations: string[] = []
  const rects = absoluteRects(layout.nodes)
  const cards: Array<{ id: string; rect: Rect }> = []
  const cardRects = new Map<string, Rect>()
  for (const n of layout.nodes) {
    if (isContainerType(n.type)) continue
    const rect = rects.get(n.id)!
    cards.push({ id: n.id, rect })
    cardRects.set(n.id, rect)
    const size = sizes[n.id]
    if (
      size &&
      (Math.abs(size.width - rect.w) > ROUNDING_TOLERANCE ||
        Math.abs(size.height - rect.h) > ROUNDING_TOLERANCE)
    ) {
      violations.push(`size:${n.id}`)
    }
  }
  const serviceOf = new Map(nodes.map((n) => [n.id, n.service]))

  // ── H1: cards keep their distance ──────────────────────────────────────
  for (let i = 0; i < cards.length; i++) {
    const a = cards[i].rect
    for (let j = i + 1; j < cards.length; j++) {
      const b = cards[j].rect
      const dx = Math.max(b.x - (a.x + a.w), a.x - (b.x + b.w))
      const dy = Math.max(b.y - (a.y + a.h), a.y - (b.y + b.h))
      if (Math.max(dx, dy) < MIN_GAP - ROUNDING_TOLERANCE) {
        violations.push(`H1:${cards[i].id}|${cards[j].id}`)
      }
    }
  }

  // ── Degrees, for S8 and S12 ────────────────────────────────────────────
  const degree = new Map<string, number>()
  for (const e of edges) {
    degree.set(e.source, (degree.get(e.source) ?? 0) + 1)
    degree.set(e.target, (degree.get(e.target) ?? 0) + 1)
  }

  // ── Per-level terms: H2, S8, S10, S11, S12 ──────────────────────────────
  const children = new Map<string, Node[]>()
  for (const n of layout.nodes) {
    if (!n.parentId) continue
    const list = children.get(n.parentId)
    if (list) list.push(n)
    else children.set(n.parentId, [n])
  }
  for (const container of layout.nodes) {
    const pad = CONTAINER_PADDING[container.type ?? ""]
    if (!pad) continue
    const box = rects.get(container.id)!
    const items = children.get(container.id) ?? []
    if (items.length === 0) continue
    const inner: Rect = {
      x: box.x + pad.x,
      y: box.y + pad.top,
      w: box.w - pad.x * 2,
      h: box.h - pad.top - pad.bottom,
    }
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    let cardArea = 0
    for (const item of items) {
      const r = rects.get(item.id)!
      if (
        r.x < inner.x - ROUNDING_TOLERANCE ||
        r.y < inner.y - ROUNDING_TOLERANCE ||
        r.x + r.w > inner.x + inner.w + ROUNDING_TOLERANCE ||
        r.y + r.h > inner.y + inner.h + ROUNDING_TOLERANCE
      ) {
        violations.push(`H2:${item.id} outside ${container.id}`)
      }
      minX = Math.min(minX, r.x)
      minY = Math.min(minY, r.y)
      maxX = Math.max(maxX, r.x + r.w)
      maxY = Math.max(maxY, r.y + r.h)
      cardArea += r.w * r.h
    }
    // The box is its contents plus padding — no slack that would let a
    // layout look compact by hiding empty space in a larger container.
    if (
      Math.abs(inner.w - (maxX - minX)) > ROUNDING_TOLERANCE * 2 ||
      Math.abs(inner.h - (maxY - minY)) > ROUNDING_TOLERANCE * 2
    ) {
      violations.push(`H2:${container.id} is not its contents plus padding`)
    }
    for (let i = 0; i < items.length; i++) {
      const a = rects.get(items[i].id)!
      for (let j = i + 1; j < items.length; j++) {
        const b = rects.get(items[j].id)!
        if (a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h) {
          violations.push(`H2:${items[i].id} overlaps ${items[j].id}`)
        }
      }
    }

    // S8: unconnected cards in reading order.
    const loose = items
      .filter((n) => !isContainerType(n.type) && !degree.has(n.id))
      .map((n) => ({ rect: rects.get(n.id)!, service: serviceOf.get(n.id) ?? "" }))
      .sort((a, b) => a.rect.y - b.rect.y || a.rect.x - b.rect.x)
    const services = new Set(loose.map((l) => l.service))
    let changes = 0
    for (let i = 1; i < loose.length; i++) if (loose[i].service !== loose[i - 1].service) changes++
    counts.S8 += Math.max(0, changes - Math.max(0, services.size - 1))

    // S10 and S11 measure the arrangement; one item has no arrangement.
    // S11 judges regions only — a stack's shape is its flow's shape.
    if (items.length >= 2) {
      const boxArea = (maxX - minX) * (maxY - minY)
      counts.S10 += (boxArea / cardArea - 1) * items.length
      if (container.type === "regionGroup") {
        counts.S11 += Math.abs(Math.log2((maxX - minX) / (maxY - minY)) - ASPECT_TARGET_LOG2)
      }
    }

    // S12: every flow (a connected card, or a box with a flow inside) above the inventory.
    const flowTops: number[] = []
    const looseTops: number[] = []
    for (const item of items) {
      const top = rects.get(item.id)!.y
      if (isContainerType(item.type) || degree.has(item.id)) flowTops.push(top)
      else looseTops.push(top)
    }
    for (const f of flowTops) for (const l of looseTops) if (f > l) counts.S12++
  }

  // ── S13: the active region leads ───────────────────────────────────────
  if (activeRegion) {
    const regions = layout.nodes
      .filter((n) => n.type === "regionGroup")
      .sort((a, b) => a.position.y - b.position.y)
    if (regions.length > 0 && regions[0].data.region !== activeRegion) counts.S13 = 1
  }

  // ── Edge terms: H3, H4, S1–S7, S9 ───────────────────────────────────────
  const geometry: EdgeGeometry[] = []
  for (const edge of edges) {
    if (edge.type === "nested-stack") continue
    const s = cardRects.get(edge.source)
    const t = cardRects.get(edge.target)
    if (!s || !t) continue
    const pts = edgePolyline(s, t, layout.routes[edge.id])
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const p of pts) {
      minX = Math.min(minX, p.x)
      minY = Math.min(minY, p.y)
      maxX = Math.max(maxX, p.x)
      maxY = Math.max(maxY, p.y)
    }
    geometry.push({ edge, pts, s, t, minX, maxX, minY, maxY })
  }

  const grazeIndex = new ObstacleIndex(
    cards.map((c) => ({ id: c.id, ...c.rect })),
    200,
    GRAZE,
  )
  const hits = new Set<Obstacle>()
  const labelMidpoints: Pt[] = []
  for (const g of geometry) {
    const { edge, pts, s, t } = g
    const from = pts[0]
    const to = pts[pts.length - 1]

    // H4 / S4: forward, or an explicit loop whose lane runs outside both
    // cards' rows — above or below the pair, or in the corridor between two
    // region boxes when the edge crosses regions.
    if (to.x < from.x + MIN_FORWARD) {
      counts.S4++
      const outside = (p: Pt, r: Rect) => p.y < r.y || p.y > r.y + r.h
      const looped = pts.slice(1, -1).some((p) => outside(p, s) && outside(p, t))
      if (!looped) violations.push(`H4:${edge.id}`)
    }

    counts.S2 += Math.max(0, polylineLength(pts) - RANK_GAP)
    counts.S3 += bendCount(pts)
    counts.S5 += Math.abs(from.y - to.y)
    counts.S9 += Math.max(0, rectGap(s, t) - RANK_GAP)

    // H3 / S6: cards the polyline enters (inflated by EDGE_CLEARANCE) or grazes (by GRAZE).
    hits.clear()
    for (let i = 0; i + 1 < pts.length; i++) {
      grazeIndex.collectHits(pts[i], pts[i + 1], edge.source, edge.target, hits)
    }
    for (const o of hits) {
      const card: Rect = {
        x: o.x + GRAZE - EDGE_CLEARANCE,
        y: o.y + GRAZE - EDGE_CLEARANCE,
        w: o.w - (GRAZE - EDGE_CLEARANCE) * 2,
        h: o.h - (GRAZE - EDGE_CLEARANCE) * 2,
      }
      let enters = false
      for (let i = 0; i + 1 < pts.length && !enters; i++) {
        enters = segmentHitsRect(pts[i], pts[i + 1], card)
      }
      if (enters) violations.push(`H3:${edge.id} enters ${o.id}`)
      else counts.S6++
    }

    if (edge.label) labelMidpoints.push(polylineMidpoint(pts))
  }

  // S7: label room, against cards and against each other.
  for (let i = 0; i < labelMidpoints.length; i++) {
    const m = labelMidpoints[i]
    for (const c of cards) if (pointRectDistance(m, c.rect) < LABEL_ROOM) counts.S7++
    for (let j = i + 1; j < labelMidpoints.length; j++) {
      if (Math.hypot(m.x - labelMidpoints[j].x, m.y - labelMidpoints[j].y) < LABEL_ROOM) counts.S7++
    }
  }

  // S1: crossings, over pairs whose bounding boxes overlap.
  geometry.sort((a, b) => a.minX - b.minX)
  for (let i = 0; i < geometry.length; i++) {
    const a = geometry[i]
    for (let j = i + 1; j < geometry.length && geometry[j].minX <= a.maxX; j++) {
      const b = geometry[j]
      if (b.minY > a.maxY || b.maxY < a.minY) continue
      if (
        a.edge.source === b.edge.source ||
        a.edge.source === b.edge.target ||
        a.edge.target === b.edge.source ||
        a.edge.target === b.edge.target
      ) {
        continue
      }
      for (let p = 0; p + 1 < a.pts.length; p++) {
        for (let q = 0; q + 1 < b.pts.length; q++) {
          if (segmentsCross(a.pts[p], a.pts[p + 1], b.pts[q], b.pts[q + 1])) counts.S1++
        }
      }
    }
  }

  const terms: Record<SoftRuleId, number> = {
    S1: counts.S1 * W_CROSSING,
    S2: counts.S2 * W_LENGTH,
    S3: counts.S3 * W_BEND,
    S4: counts.S4 * W_BACK_EDGE,
    S5: counts.S5 * W_MISALIGN,
    S6: counts.S6 * W_GRAZE,
    S7: counts.S7 * W_LABEL,
    S8: counts.S8 * W_SERVICE_CHANGE,
    S9: counts.S9 * W_NEIGHBOUR,
    S10: counts.S10 * W_COMPACT,
    S11: counts.S11 * W_ASPECT,
    S12: counts.S12 * W_INVENTORY_ORDER,
    S13: counts.S13 * W_REGION_ORDER,
  }
  let soft = 0
  for (const id of SOFT_RULES) soft += terms[id]
  return { total: violations.length === 0 ? soft : Infinity, soft, terms, counts, violations }
}
