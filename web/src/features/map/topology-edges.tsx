/**
 * topology-edges — custom animated React Flow edges.
 *
 * Two visual styles:
 *   - "solid"  — direct wiring (S3 notification, SNS subscription, Lambda ESM)
 *   - "dashed" — EventBridge Pipe (labeled with pipe name)
 *
 * An edge that the layout routed around other nodes carries its waypoints in
 * `data.route`; the path is one smooth curve through them, leaving the source
 * and entering the target horizontally like a plain Bézier would. Without a
 * route it is React Flow's ordinary Bézier.
 *
 * When an animation is active (triggered via the `animated` + `data.glowing`
 * props set by use-event-animations), the edge glows and a particle travels
 * along the path.
 */

import { memo } from "react"
import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from "@xyflow/react"
import { cn } from "@/lib/utils"
import { EDGE_THEME, FALLBACK_COLOR } from "./map-theme"
import { routedPath } from "./map-edge-routing"
import type { Pt } from "./map-edge-routing"

export interface TopologyEdgeData extends Record<string, unknown> {
  /** true while an event is animating along this edge */
  glowing?: boolean
  /** edge kind — determines dash style and colour */
  edgeType?:
    | "notification"
    | "subscription"
    | "esm"
    | "pipe"
    | "logs"
    | "dlq"
    | "esm-filter"
    | "apigw-integration"
    | "table-location"
    | "federation"
    | "query-results"
    | "queries"
  /** pipe/dlq label shown at mid-point */
  label?: string
  /** pipe state — only relevant when edgeType === "pipe" */
  state?: string
  /**
   * Accumulated event count since the last drain tick (drains 1 every ~2 s).
   * Shown as a small badge so bursts of fast events remain visible after the glow fades.
   */
  burstCount?: number
  /** Waypoints strictly between the two handles, absolute canvas coordinates. */
  route?: Pt[]
  /** true when something else on the map has focus and this edge is not part of it */
  dimmed?: boolean
}

function areEdgePropsEqual(prev: EdgeProps, next: EdgeProps): boolean {
  if (prev.selected !== next.selected) return false
  const pd = (prev.data ?? {}) as TopologyEdgeData
  const nd = (next.data ?? {}) as TopologyEdgeData
  return (
    pd.glowing === nd.glowing &&
    pd.edgeType === nd.edgeType &&
    pd.label === nd.label &&
    pd.state === nd.state &&
    pd.burstCount === nd.burstCount &&
    pd.route === nd.route &&
    pd.dimmed === nd.dimmed &&
    prev.sourceX === next.sourceX &&
    prev.sourceY === next.sourceY &&
    prev.targetX === next.targetX &&
    prev.targetY === next.targetY
  )
}

export const TopologyEdge = memo(function TopologyEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  markerEnd,
}: EdgeProps) {
  const { glowing, edgeType, label, state, burstCount, route, dimmed } = (data ??
    {}) as TopologyEdgeData
  const isPipe = edgeType === "pipe"
  const isDlq = edgeType === "dlq"
  const isESMFilter = edgeType === "esm-filter"
  const isDashed = EDGE_THEME[edgeType ?? ""]?.dash ?? false
  const isStopped = isPipe && state === "STOPPED"
  const color = isStopped ? FALLBACK_COLOR : (EDGE_THEME[edgeType ?? ""]?.color ?? FALLBACK_COLOR)

  const [edgePath, labelX, labelY] =
    route && route.length > 0
      ? routedPath([{ x: sourceX, y: sourceY }, ...route, { x: targetX, y: targetY }])
      : getBezierPath({
          sourceX,
          sourceY,
          sourcePosition,
          targetX,
          targetY,
          targetPosition,
        })

  const active = glowing && !isStopped && !dimmed

  // An idle wire sits back behind the cards; a wire carrying an event, or one
  // picked out by hover focus, comes forward at full strength.
  return (
    <g
      className={cn(
        "transition-opacity duration-150",
        dimmed ? "opacity-15" : active ? "opacity-100" : "opacity-70",
      )}
    >
      {/* Glow layer — rendered behind the main stroke when active */}
      {active && (
        <path
          d={edgePath}
          fill="none"
          stroke={color}
          strokeWidth={6}
          strokeOpacity={0.35}
          className="pointer-events-none"
          style={{ filter: `drop-shadow(0 0 4px ${color})` }}
        />
      )}

      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{
          stroke: color,
          strokeWidth: active ? 2 : 1.5,
          strokeLinecap: "round",
          strokeLinejoin: "round",
          strokeDasharray: isDashed ? "6 3" : undefined,
          opacity: isStopped ? 0.4 : 1,
          transition: "stroke-width 0.15s, opacity 0.2s",
        }}
      />

      {/* Travelling particle */}
      {active && (
        <circle r={4} fill={color} style={{ filter: `drop-shadow(0 0 3px ${color})` }}>
          <animateMotion dur="0.8s" repeatCount="1" path={edgePath} />
        </circle>
      )}

      {/* Edge label (pipes, DLQ, and other labeled edges) */}
      {isDashed && label && !isESMFilter && (
        <EdgeLabelRenderer>
          <div
            className={cn(
              "map-edge-label nodrag nopan pointer-events-none absolute rounded border px-1.5 py-0.5",
              "text-2xs leading-tight font-medium transition-opacity duration-150",
              dimmed && "opacity-15",
              isStopped
                ? "border-transparent bg-bg-muted text-fg-subtle"
                : isDlq
                  ? "border-danger/25 bg-bg-elevated text-danger"
                  : "border-accent/25 bg-bg-elevated text-accent",
            )}
            style={{
              transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
              zIndex: 10,
            }}
          >
            {label}
            {isStopped && " (stopped)"}
          </div>
        </EdgeLabelRenderer>
      )}

      {/* Burst counter badge — shown when > 1 to avoid interfering with type labels */}
      {(burstCount ?? 0) > 1 && !isStopped && !isESMFilter && !dimmed && (
        <EdgeLabelRenderer>
          <div
            className="nodrag nopan pointer-events-none absolute flex items-center gap-0.5 rounded-full px-1.5 py-0.5 font-mono text-2xs font-bold tabular-nums"
            style={{
              transform: `translate(-50%, calc(-50% - ${(isPipe || isDlq) && label ? 16 : 0}px)) translate(${labelX}px,${labelY}px)`,
              zIndex: 11,
              background: `color-mix(in srgb, ${color} 20%, transparent)`,
              border: `1px solid color-mix(in srgb, ${color} 40%, transparent)`,
              color,
            }}
          >
            ×{burstCount}
          </div>
        </EdgeLabelRenderer>
      )}
    </g>
  )
}, areEdgePropsEqual)
