/**
 * map-edge-routing.test — the obstacle index, the detour search and the
 * curve builder, each on hand-drawn geometry small enough to reason about.
 */
import { describe, expect, it } from "vitest"
import {
  ObstacleIndex,
  ROUTE_MARGIN,
  polylineClear,
  routeEdge,
  routedPath,
  segmentHitsRect,
  type Pt,
} from "./map-edge-routing"

describe("segmentHitsRect", () => {
  const box = { x: 100, y: 100, w: 50, h: 50 }
  it("detects a crossing, a touch and a miss", () => {
    expect(segmentHitsRect({ x: 0, y: 125 }, { x: 300, y: 125 }, box)).toBe(true)
    expect(segmentHitsRect({ x: 0, y: 0 }, { x: 125, y: 125 }, box)).toBe(true)
    expect(segmentHitsRect({ x: 0, y: 0 }, { x: 300, y: 50 }, box)).toBe(false)
    expect(segmentHitsRect({ x: 0, y: 200 }, { x: 300, y: 200 }, box)).toBe(false)
  })
  it("treats a vertical or horizontal segment like any other", () => {
    expect(segmentHitsRect({ x: 125, y: 0 }, { x: 125, y: 300 }, box)).toBe(true)
    expect(segmentHitsRect({ x: 90, y: 0 }, { x: 90, y: 300 }, box)).toBe(false)
  })
})

describe("routeEdge", () => {
  // Three boxes in a row: a → c with b squarely in between.
  const a = { id: "a", x: 0, y: 100, w: 100, h: 60 }
  const b = { id: "b", x: 300, y: 100, w: 100, h: 60 }
  const c = { id: "c", x: 600, y: 100, w: 100, h: 60 }
  const index = new ObstacleIndex([a, b, c])
  const from: Pt = { x: a.x + a.w, y: a.y + a.h / 2 }
  const to: Pt = { x: c.x, y: c.y + c.h / 2 }

  it("keeps a clear hint untouched", () => {
    const hint = [{ x: 350, y: 30 }]
    expect(routeEdge(from, to, hint, index, "a", "c")).toBe(hint)
  })

  it("detours around a box the straight line would cut through", () => {
    const route = routeEdge(from, to, [], index, "a", "c")
    expect(route.length).toBeGreaterThan(0)
    const poly = [from, ...route, to]
    expect(polylineClear(poly, index, "a", "c")).toBe(true)
    // Every waypoint stays at least the margin away from b.
    for (const p of route) {
      const inside =
        p.x > b.x - ROUTE_MARGIN &&
        p.x < b.x + b.w + ROUTE_MARGIN &&
        p.y > b.y - ROUTE_MARGIN &&
        p.y < b.y + b.h + ROUTE_MARGIN
      expect(inside).toBe(false)
    }
  })

  it("ignores the two endpoint boxes when checking", () => {
    // a → b: the straight line starts on a's boundary and ends on b's.
    const toB: Pt = { x: b.x, y: b.y + b.h / 2 }
    expect(routeEdge(from, toB, [], index, "a", "b")).toEqual([])
  })
})

describe("routedPath", () => {
  it("starts and ends at the handles and passes through every waypoint", () => {
    const pts: Pt[] = [
      { x: 0, y: 0 },
      { x: 100, y: -80 },
      { x: 200, y: 0 },
    ]
    const [d] = routedPath(pts)
    expect(d.startsWith("M 0 0")).toBe(true)
    // Two cubic legs, each ending exactly on the next waypoint.
    const legs = d.split(" C ").slice(1)
    expect(legs).toHaveLength(2)
    expect(legs[0].endsWith("100 -80")).toBe(true)
    expect(legs[1].endsWith("200 0")).toBe(true)
  })

  it("leaves the source horizontally", () => {
    const [d] = routedPath([
      { x: 0, y: 0 },
      { x: 60, y: 200 },
      { x: 300, y: 200 },
    ])
    // The first control point shares the start's y.
    const firstLeg = d.split(" C ")[1]
    const c1 = firstLeg.split(",")[0].trim().split(" ")
    expect(Number(c1[1])).toBe(0)
    expect(Number(c1[0])).toBeGreaterThan(0)
  })

  it("puts the label half-way along the polyline", () => {
    const [, lx, ly] = routedPath([
      { x: 0, y: 0 },
      { x: 100, y: 0 },
      { x: 100, y: 100 },
    ])
    expect(lx).toBe(100)
    expect(ly).toBe(0)
  })
})
