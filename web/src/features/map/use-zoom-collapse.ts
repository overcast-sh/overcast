/**
 * use-zoom-collapse — derives which nested CloudFormation stacks should
 * render as collapsed chips vs. expanded groups at the current zoom level.
 *
 * Uses the `nested-stack` topology edges to build a stack nesting tree,
 * then applies a depth-based zoom threshold: deeper stacks require more
 * zoom to expand. Returns a referentially-stable Set that only changes
 * when the actual collapsed/expanded state flips — avoiding unnecessary
 * layout re-runs.
 */

import { useMemo, useState } from "react"
import type { TopologyEdge } from "@/types"

/**
 * Zoom threshold at which a depth-d (≥ 1) nested stack expands:
 *
 *    expand when zoom ≥ EXPAND_BASE + (d − 1) × EXPAND_STEP
 *
 *    depth 1 → ≥ 0.55   (expanded at most default views)
 *    depth 2 → ≥ 1.00   (need to zoom in a bit)
 *    depth 3 → ≥ 1.45   (need to zoom in more)
 */
const EXPAND_BASE = 0.55
const EXPAND_STEP = 0.45

/** Hysteresis gap: collapse only when zoom drops this far below the expand threshold. */
const HYSTERESIS = 0.12

/**
 * Returns the set of stack phantom IDs (e.g. `stack::us-east-1::ChildStack`)
 * that should be rendered as collapsed chips at the given zoom level.
 * Root stacks (depth 0) always expand.
 *
 * The returned Set is referentially stable — a new object is only created
 * when the set of collapsed IDs actually changes.
 */
export function useZoomCollapse(edges: TopologyEdge[], zoom: number): Set<string> {
  const [prev, setPrev] = useState<Set<string>>(() => new Set())

  const next = useMemo(() => {
    // Build child → parent lookup from nested-stack edges.
    const childToParent = new Map<string, string>()
    for (const e of edges) {
      if (e.type === "nested-stack") {
        childToParent.set(e.target, e.source)
      }
    }

    if (childToParent.size === 0) {
      return new Set<string>()
    }

    // Compute nesting depth with memoisation (depth 0 = root).
    const cache = new Map<string, number>()
    function depth(id: string): number {
      if (cache.has(id)) return cache.get(id)!
      const parent = childToParent.get(id)
      const d = parent ? 1 + depth(parent) : 0
      cache.set(id, d)
      return d
    }

    const result = new Set<string>()
    for (const id of childToParent.keys()) {
      const d = depth(id)
      const expandAt = EXPAND_BASE + (d - 1) * EXPAND_STEP
      const wasCollapsed = prev.has(id)
      // Hysteresis: once expanded, stay expanded until zoom drops further
      // below the threshold. This prevents jittery toggling at the boundary
      // and avoids fighting the user's zoom gesture.
      if (wasCollapsed) {
        // Currently collapsed → expand only when zoom reaches the full threshold
        if (zoom < expandAt) result.add(id)
      } else {
        // Currently expanded → collapse only when zoom drops below threshold − gap
        if (zoom < expandAt - HYSTERESIS) result.add(id)
      }
    }

    return result
  }, [edges, zoom, prev])

  // Update state only when the set contents actually changed — keeps the
  // returned reference stable and avoids unnecessary layout re-runs.
  if (next.size !== prev.size || ![...next].every((s) => prev.has(s))) {
    setPrev(next)
  }

  return next === prev ? prev : next
}
