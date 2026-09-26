/**
 * MapPage — full-page topology canvas at /map.
 *
 * Shows all emulator resources as nodes arranged in service columns.
 * Edges represent actual configured connections (S3 notifications, SNS
 * subscriptions, Lambda ESMs, Pipes).  Events from the SSE stream animate
 * as travelling particles with a brief edge glow.
 */

import { useMemo, useRef, useEffect, useState, useCallback, useSyncExternalStore } from "react"
import { cn } from "@/lib/utils"
import { sectionLabel } from "@/lib/typography"
import {
  ReactFlow,
  Background,
  Controls,
  Panel,
  MarkerType,
  useReactFlow,
  useNodes,
  useEdges,
  useViewport,
  useStore,
  type Edge,
  type Node,
  BackgroundVariant,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { useTopology } from "./use-topology"
import { useEventAnimations } from "./use-event-animations"
import {
  requestLayoutAsync,
  subscribeToLayout,
  getLayoutSnapshot,
  isLayoutLoading,
  NODE_WIDTH,
  IGW_NODE_WIDTH,
  IGW_NODE_HEIGHT,
  VPC_NODE_HEIGHT,
  ESM_FILTER_NODE_SIZE,
  lambdaGroupHeight,
} from "./map-layout"
import {
  ServiceNode,
  LambdaGroupNode,
  SQS_NODE_EXPANDED_H,
  SQS_NODE_IDLE_H,
  LOGS_NODE_EXPANDED_H,
  STATUS_NODE_H,
} from "./topology-nodes"
import "./map-animations.css"
import { TopologyEdge } from "./topology-edges"
import { LAMBDA_GHOST_TTL } from "./lambda-instance-node"
import { useLambdaInstances, type InstancesByFunction } from "@/hooks/use-lambda-instances"
import { useGhostTracker, type Ghost } from "@/hooks/use-ghost-tracker"
import { LogStreamPeek } from "./log-stream-peek"
import type { LogStreamTarget } from "./log-stream-peek"
import { RegionGroupNode } from "./region-group-node"
import { StackGroupNode } from "./stack-group-node"
import { CollapsedStackNode } from "./collapsed-stack-node"
import { useZoomCollapse } from "./use-zoom-collapse"
import { VpcGroupNode } from "./vpc-group-node"
import { VpcNetworkNode } from "./vpc-network-node"
import { IgwNode } from "./igw-node"
import { SERVICE_THEME, EDGE_THEME, FALLBACK_COLOR } from "./map-theme"
import { useEndpoint } from "@/hooks/use-endpoint"
import type { LambdaInstance, TopologyNode } from "@/types"
import { AthenaWorkgroupNode } from "./athena-workgroup-node"
import { DataCatalogNode } from "./data-catalog-node"
import { DataLakeOverlayContext, DataLakePeekContext, type DataLakePeek } from "./data-lake-context"
import { DataLakePeeks } from "./data-lake-peeks"
import { tableList, tableListHeight, workgroupHeight } from "./data-lake-layout"
import { nodeSuffix, type DataLakeOverlay } from "./data-lake-overlay"

const NODE_TYPES = {
  serviceNode: ServiceNode,
  lambdaGroup: LambdaGroupNode,
  regionGroup: RegionGroupNode,
  stackGroup: StackGroupNode,
  collapsedStack: CollapsedStackNode,
  vpcGroup: VpcGroupNode,
  vpcNetworkNode: VpcNetworkNode,
  igwNode: IgwNode,
  athenaWorkgroup: AthenaWorkgroupNode,
  dataCatalog: DataCatalogNode,
}

/** Node types that draw their own handles from `hasTarget` / `hasSource`. */
const HANDLED_TYPES = new Set([
  "serviceNode",
  "vpcNetworkNode",
  "igwNode",
  "athenaWorkgroup",
  "dataCatalog",
])

/** Node types that render the live topology node rather than the layout's copy of it. */
const DATA_LAKE_TYPES = new Set(["athenaWorkgroup", "dataCatalog"])
const EDGE_TYPES = { topologyEdge: TopologyEdge }

/** Group containers: never dimmed, never a hover target. */
const CONTAINER_TYPES = new Set(["regionGroup", "stackGroup", "vpcGroup"])

/**
 * Below this zoom the text inside a node's message, stream and instance lists
 * is too small to read, so CSS hides those lists (and edge labels) and the
 * map reads as boxes and wires. See map-animations.css.
 */
const FAR_ZOOM = 0.6

/** Hover has to rest on a node this long before the rest of the map dims. */
const FOCUS_DELAY_MS = 140

/** What the map is focused on: a node and its neighbours, or one kind of connection. */
type Focus = { node: string } | { edgeType: string } | null

/** Calls fitView whenever the ReactFlow container element resizes. */
function FitViewOnResize() {
  const { fitView } = useReactFlow()
  const containerRef = useRef<Element | null>(null)

  useEffect(() => {
    // Walk up from the internal react-flow__renderer to the outer wrapper
    const el = document.querySelector(".react-flow")
    if (!el) return
    containerRef.current = el
    const ro = new ResizeObserver(() => {
      void fitView({ padding: 0.2, maxZoom: 1.2, duration: 200 })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [fitView])

  return null
}

/**
 * Re-fits the canvas (with animation) when the *topology data* count changes
 * (new resources appear or disappear). Does NOT trigger on collapse/expand
 * which only changes the React Flow node count — that would fight the user's
 * manual zoom and cause a snap-back loop.
 */
function FitViewOnDataChange({ dataCount }: { dataCount: number }) {
  const { fitView } = useReactFlow()
  const prevCount = useRef(dataCount)

  useEffect(() => {
    if (dataCount !== prevCount.current) {
      prevCount.current = dataCount
      void fitView({ padding: 0.2, maxZoom: 1.2, duration: 450 })
    }
  }, [dataCount, fitView])

  return null
}

/**
 * When stacks collapse (zoom-out), smoothly pan to keep the collapsed chip
 * centred in the viewport. Without this the user ends up staring at empty
 * space where the expanded group used to be.
 *
 * Strategy: find the newly-collapsed stack node(s), compute their centre,
 * and `setCenter` at the *current* zoom — a pure pan, no zoom change.
 * Only triggers on collapse (set grows), not expand.
 */
function FitViewOnCollapse({ collapsedStacks }: { collapsedStacks: Set<string> }) {
  const { setCenter, getZoom, getNodes } = useReactFlow()
  const prevRef = useRef(collapsedStacks)

  useEffect(() => {
    const prev = prevRef.current
    prevRef.current = collapsedStacks

    if (collapsedStacks === prev) return
    // Only act when stacks were newly collapsed (set grew).
    if (collapsedStacks.size <= prev.size) return

    // IDs that just switched from expanded → collapsed.
    const newlyCollapsed = new Set<string>()
    for (const id of collapsedStacks) {
      if (!prev.has(id)) newlyCollapsed.add(id)
    }
    if (newlyCollapsed.size === 0) return

    const currentZoom = getZoom()

    // Small delay so React Flow commits the new layout before we read positions.
    const t = setTimeout(() => {
      const nodes = getNodes()
      // Build a map to resolve absolute positions (children have relative coords).
      const nodeMap = new Map(nodes.map((n) => [n.id, n]))
      function absPos(n: { position: { x: number; y: number }; parentId?: string }) {
        let x = n.position.x
        let y = n.position.y
        let cur = n
        while (cur.parentId) {
          const p = nodeMap.get(cur.parentId) as typeof cur | undefined
          if (!p) break
          x += p.position.x
          y += p.position.y
          cur = p
        }
        return { x, y }
      }

      // Find the collapsed-chip nodes for the newly collapsed IDs.
      const targets = nodes.filter((n) => n.type === "collapsedStack" && newlyCollapsed.has(n.id))
      if (targets.length === 0) return

      // Centre on the midpoint of all newly-collapsed chips.
      let cx = 0,
        cy = 0
      for (const n of targets) {
        const pos = absPos(n)
        cx += pos.x + (n.width ?? 200) / 2
        cy += pos.y + (n.height ?? 56) / 2
      }
      cx /= targets.length
      cy /= targets.length

      void setCenter(cx, cy, { zoom: currentZoom, duration: 350 })
    }, 60)
    return () => clearTimeout(t)
  }, [collapsedStacks, setCenter, getZoom, getNodes])

  return null
}

/**
 * Zooms/pans the canvas to focus on the region group matching the global
 * endpoint region — both on initial load and when the dropdown changes.
 */
function FitViewOnRegionChange({
  region,
  hasRegionGroups,
}: {
  region: string
  hasRegionGroups: boolean
}) {
  const { fitView } = useReactFlow()
  const prevRegion = useRef<string | null>(null)

  useEffect(() => {
    if (!hasRegionGroups) return
    if (region === prevRegion.current) return
    prevRegion.current = region

    const groupId = `region::${region}`
    // Small delay so React Flow has committed the nodes before we fitView.
    const t = setTimeout(() => {
      void fitView({ nodes: [{ id: groupId }], padding: 0.15, maxZoom: 1.2, duration: 500 })
    }, 50)
    return () => clearTimeout(t)
  }, [region, hasRegionGroups, fitView])

  return null
}

const MM_W = 168
const MM_H = 116
const MM_PAD = 10
const NODE_R = 7

// ── Pure transform helpers for the rfNodes pipeline ──────────────────────

/** Compute per-node size overrides for lambda groups, expanded SQS nodes and data-lake lists. */
function computeSizeOverrides(
  topologyNodes: TopologyNode[],
  instancesByFunction: InstancesByFunction,
  ghostInstances: Map<string, Ghost<LambdaInstance>>,
  lakeGhosts: DataLakeOverlay["ghosts"],
): Record<string, { width: number; height: number }> {
  const overrides: Record<string, { width: number; height: number }> = {}
  for (const n of topologyNodes) {
    if (n.service === "athena") {
      overrides[n.id] = { width: NODE_WIDTH, height: workgroupHeight(n.recentQueries?.length ?? 0) }
      continue
    }
    if (n.service === "glue" || n.service === "s3tables") {
      const list = tableList(n.tables ?? [], lakeGhosts[nodeSuffix(n.id)])
      overrides[n.id] = { width: NODE_WIDTH, height: tableListHeight(list) }
      continue
    }
    if (n.service === "lambda") {
      const liveInsts = instancesByFunction[n.label] ?? []
      const ghostCount = [...ghostInstances.values()].filter(
        (g) =>
          g.item.functionName === n.label &&
          !liveInsts.some((li) => li.instanceId === g.item.instanceId),
      ).length
      const totalCount = liveInsts.length + ghostCount
      if (totalCount > 0) {
        overrides[n.id] = { width: NODE_WIDTH, height: lambdaGroupHeight(totalCount) }
      }
    } else if (n.service === "sqs") {
      const hasMessages =
        (n.approximateNumberOfMessages ?? 0) + (n.approximateNumberOfMessagesNotVisible ?? 0) > 0
      overrides[n.id] = {
        width: NODE_WIDTH,
        height: hasMessages ? SQS_NODE_EXPANDED_H : SQS_NODE_IDLE_H,
      }
    } else if (n.service === "rds" && n.status) {
      overrides[n.id] = { width: NODE_WIDTH, height: STATUS_NODE_H }
    } else if (n.service === "logs") {
      overrides[n.id] = { width: NODE_WIDTH, height: LOGS_NODE_EXPANDED_H }
    } else if (n.service === "esm-filter") {
      overrides[n.id] = { width: ESM_FILTER_NODE_SIZE, height: ESM_FILTER_NODE_SIZE }
    } else if (n.service === "igw") {
      overrides[n.id] = { width: IGW_NODE_WIDTH, height: IGW_NODE_HEIGHT }
    } else if (n.service === "vpc") {
      overrides[n.id] = { width: NODE_WIDTH, height: VPC_NODE_HEIGHT }
    }
  }
  return overrides
}

/**
 * Node wrapper transition: a re-layout glides boxes to their new place, and a
 * focus change fades the unrelated ones. Set inline because React Flow puts
 * `style` on the wrapper, where an inline `transition` would otherwise
 * replace any stylesheet one.
 */
const NODE_TRANSITION = "transform 0.45s cubic-bezier(0.34, 1.56, 0.64, 1), opacity 0.15s"

/** Expand positioned layout nodes into final React Flow nodes with event counts and lambda children. */
function expandFlowNodes(
  positioned: Node[],
  ctx: {
    instancesByFunction: InstancesByFunction
    ghostInstances: Map<string, Ghost<LambdaInstance>>
    nodeCounts: Record<string, number>
    nodeWriteCounts: Record<string, number>
    nodeWriteBurstCounts: Record<string, number>
    isInitialLoad: boolean
    seenNodeIds: Set<string>
    onPeek: (target: LogStreamTarget) => void
    onPeekStream: (target: LogStreamTarget) => void
    /** The topology's nodes by ID, fresher than the copies the layout holds. */
    liveNodes: Map<string, TopologyNode>
  },
): Node[] {
  const result: Node[] = []
  for (const n of positioned) {
    // Group container nodes pass through without modification.
    if (n.type === "regionGroup" || n.type === "stackGroup" || n.type === "vpcGroup") {
      result.push(n)
      continue
    }
    // The layout re-runs only when sizes or connections change, so a data-lake
    // node reads its rows from the live topology node instead of its copy.
    if (DATA_LAKE_TYPES.has(n.type ?? "")) {
      const live = ctx.liveNodes.get(n.id)
      // Gone from the topology, and the layout has not caught up yet.
      if (!live) continue
      result.push({
        ...n,
        style: { ...n.style, transition: NODE_TRANSITION },
        data: {
          ...n.data,
          topologyNode: live,
          isNew: !ctx.isInitialLoad && !ctx.seenNodeIds.has(n.id),
        },
      })
      continue
    }
    const isLambda = (n.data as { service?: string }).service === "lambda"
    const funcName = (n.data as { label?: string }).label ?? ""
    const liveInstances = isLambda ? (ctx.instancesByFunction[funcName] ?? []) : []
    const ghostsForFunc = isLambda
      ? [...ctx.ghostInstances.values()]
          .filter(
            (g) =>
              g.item.functionName === funcName &&
              !liveInstances.some((li) => li.instanceId === g.item.instanceId),
          )
          .sort((a, b) => a.deletedAt - b.deletedAt)
      : []
    const allInstances = [
      ...liveInstances.map((i) => ({ instance: i, isGhost: false, deletedAt: 0 })),
      ...ghostsForFunc.map((g) => ({
        instance: g.item,
        isGhost: true,
        deletedAt: g.deletedAt,
      })),
    ]
    const eventCount = ctx.nodeCounts[n.id] ?? 0
    const writeCount = ctx.nodeWriteCounts[n.id] ?? 0
    const writeBurstCount = ctx.nodeWriteBurstCounts[n.id] ?? 0
    const isNew = !ctx.isInitialLoad && !ctx.seenNodeIds.has(n.id)

    if (isLambda && allInstances.length > 0) {
      // Instance rendering itself lives inside LambdaGroupNode (it fetches its
      // own instances/ghosts) — this only needs the identical capped height
      // formula so the layout reserves the exact space the node will render at.
      const groupH = lambdaGroupHeight(allInstances.length)
      result.push({
        ...n,
        type: "lambdaGroup",
        width: NODE_WIDTH,
        height: groupH,
        style: {
          width: NODE_WIDTH,
          height: groupH,
          transition: NODE_TRANSITION,
        },
        data: {
          ...n.data,
          eventCount,
          onPeek: ctx.onPeek,
        },
      })
    } else {
      result.push({
        ...n,
        style: {
          ...n.style,
          transition: NODE_TRANSITION,
        },
        data: {
          ...n.data,
          eventCount,
          writeCount,
          writeBurstCount,
          isNew,
          onPeekStream: ctx.onPeekStream,
        },
      })
    }
  }
  return result
}

/**
 * CustomMiniMap — SVG overview of the topology canvas.
 *
 * Renders service-colored circles with initials for nodes, edge-type-colored
 * lines for connections, and a viewport rectangle that tracks current pan/zoom.
 * Must be rendered inside a ReactFlow context.
 */
function CustomMiniMap() {
  const nodes = useNodes()
  const edges = useEdges()
  const viewport = useViewport()
  const { setCenter } = useReactFlow()
  const containerW = useStore((s) => s.width)
  const containerH = useStore((s) => s.height)
  const dragging = useRef(false)

  // Show service/resource nodes only — exclude region/stack/VPC group containers.
  // (Lambda instances are no longer separate React Flow nodes — see
  // LambdaGroupNode, which renders its own instance list internally.)
  const topNodes = useMemo(() => nodes.filter((n) => !CONTAINER_TYPES.has(n.type ?? "")), [nodes])
  const topNodeById = useMemo(() => new Map(topNodes.map((n) => [n.id, n])), [topNodes])

  // Build a map from node ID → absolute position for the minimap.
  // Children of group nodes have relative positions — we walk up the
  // parent chain (node → stackGroup → regionGroup) to accumulate offsets.
  const nodeAbsPos = useMemo(() => {
    const posMap = new Map<string, { x: number; y: number }>()
    const nodeMap = new Map(nodes.map((n) => [n.id, n]))
    for (const n of nodes) {
      let x = n.position.x
      let y = n.position.y
      let cur = n
      while (cur.parentId) {
        const parent = nodeMap.get(cur.parentId)
        if (!parent) break
        x += parent.position.x
        y += parent.position.y
        cur = parent
      }
      posMap.set(n.id, { x, y })
    }
    return posMap
  }, [nodes])

  // Bounding box across all resource nodes (using absolute positions)
  const bounds = useMemo(() => {
    if (topNodes.length === 0) return { minX: 0, minY: 0, rangeX: 1, rangeY: 1 }
    let x0 = Infinity,
      y0 = Infinity,
      x1 = -Infinity,
      y1 = -Infinity
    for (const n of topNodes) {
      const abs = nodeAbsPos.get(n.id) ?? n.position
      const r = abs.x + (n.width ?? 180)
      const b = abs.y + (n.height ?? 56)
      if (abs.x < x0) x0 = abs.x
      if (abs.y < y0) y0 = abs.y
      if (r > x1) x1 = r
      if (b > y1) y1 = b
    }
    return { minX: x0, minY: y0, rangeX: x1 - x0 || 1, rangeY: y1 - y0 || 1 }
  }, [topNodes, nodeAbsPos])

  const svgW = MM_W - MM_PAD * 2
  const svgH = MM_H - MM_PAD * 2
  const scale = Math.min(svgW / bounds.rangeX, svgH / bounds.rangeY)
  const offsetX = (svgW - bounds.rangeX * scale) / 2
  const offsetY = (svgH - bounds.rangeY * scale) / 2

  function toSvg(fx: number, fy: number): [number, number] {
    return [
      MM_PAD + offsetX + (fx - bounds.minX) * scale,
      MM_PAD + offsetY + (fy - bounds.minY) * scale,
    ]
  }

  // Viewport rectangle in SVG coordinates
  const vpFlowX = -viewport.x / viewport.zoom
  const vpFlowY = -viewport.y / viewport.zoom
  const [vpSvgX, vpSvgY] = toSvg(vpFlowX, vpFlowY)
  const vpSvgW = (containerW / viewport.zoom) * scale
  const vpSvgH = (containerH / viewport.zoom) * scale

  function svgToFlow(e: React.MouseEvent<SVGSVGElement>): [number, number] {
    const rect = e.currentTarget.getBoundingClientRect()
    const svgX = e.clientX - rect.left
    const svgY = e.clientY - rect.top
    return [
      (svgX - MM_PAD - offsetX) / scale + bounds.minX,
      (svgY - MM_PAD - offsetY) / scale + bounds.minY,
    ]
  }

  function handlePointerDown(e: React.MouseEvent<SVGSVGElement>) {
    dragging.current = true
    const [flowX, flowY] = svgToFlow(e)
    void setCenter(flowX, flowY, { zoom: viewport.zoom, duration: 150 })
  }

  function handlePointerMove(e: React.MouseEvent<SVGSVGElement>) {
    if (!dragging.current) return
    const [flowX, flowY] = svgToFlow(e)
    void setCenter(flowX, flowY, { zoom: viewport.zoom, duration: 0 })
  }

  function handlePointerUp() {
    dragging.current = false
  }

  return (
    <Panel position="bottom-right">
      <div
        className="overflow-hidden rounded-lg border border-border bg-bg-elevated/90 shadow-sm backdrop-blur-sm"
        style={{ width: MM_W, height: MM_H }}
      >
        <svg
          width={MM_W}
          height={MM_H}
          onMouseDown={handlePointerDown}
          onMouseMove={handlePointerMove}
          onMouseUp={handlePointerUp}
          onMouseLeave={handlePointerUp}
          className="block cursor-crosshair select-none"
        >
          {/* Edges — drawn to circle edge (not center) so they don't bleed through nodes */}
          {edges.map((e) => {
            const src = topNodeById.get(e.source)
            const tgt = topNodeById.get(e.target)
            if (!src || !tgt) return null
            const edgeType = (e.data as { edgeType?: string }).edgeType ?? ""
            const et = EDGE_THEME[edgeType]
            const style = et
              ? { color: et.color, dash: et.dash }
              : { color: FALLBACK_COLOR, dash: false }
            const srcAbs = nodeAbsPos.get(src.id) ?? src.position
            const tgtAbs = nodeAbsPos.get(tgt.id) ?? tgt.position
            const [cx1, cy1] = toSvg(
              srcAbs.x + (src.width ?? 180) / 2,
              srcAbs.y + (src.height ?? 56) / 2,
            )
            const [cx2, cy2] = toSvg(
              tgtAbs.x + (tgt.width ?? 180) / 2,
              tgtAbs.y + (tgt.height ?? 56) / 2,
            )
            // Shorten line to start/end at circle circumference
            const dx = cx2 - cx1
            const dy = cy2 - cy1
            const dist = Math.sqrt(dx * dx + dy * dy) || 1
            const ux = dx / dist
            const uy = dy / dist
            const gap = NODE_R + 1
            return (
              <line
                key={e.id}
                x1={cx1 + ux * gap}
                y1={cy1 + uy * gap}
                x2={cx2 - ux * gap}
                y2={cy2 - uy * gap}
                stroke={style.color}
                strokeWidth={1.5}
                strokeDasharray={style.dash ? "3 2" : undefined}
                strokeOpacity={0.65}
              />
            )
          })}

          {/* Nodes */}
          {topNodes.map((n) => {
            const svc = (n.data as { service?: string }).service ?? ""
            const theme = SERVICE_THEME[svc]
            const color = theme?.css ?? FALLBACK_COLOR
            const letter = theme?.letter ?? "?"
            const abs = nodeAbsPos.get(n.id) ?? n.position
            const [cx, cy] = toSvg(abs.x + (n.width ?? 180) / 2, abs.y + (n.height ?? 56) / 2)
            return (
              <g key={n.id}>
                <circle
                  cx={cx}
                  cy={cy}
                  r={NODE_R}
                  fill={color}
                  fillOpacity={0.2}
                  stroke={color}
                  strokeWidth={1.5}
                />
                <text
                  x={cx}
                  y={cy}
                  textAnchor="middle"
                  dominantBaseline="central"
                  fontSize={7}
                  fontWeight="700"
                  fill={color}
                >
                  {letter}
                </text>
              </g>
            )
          })}

          {/* Viewport rectangle — shows where the canvas is currently focused */}
          <rect
            x={vpSvgX}
            y={vpSvgY}
            width={Math.max(6, vpSvgW)}
            height={Math.max(6, vpSvgH)}
            fill="currentColor"
            fillOpacity={0.04}
            stroke="currentColor"
            strokeWidth={1}
            strokeOpacity={0.35}
            className="text-fg"
            rx={2}
            ry={2}
          />
        </svg>
      </div>
    </Panel>
  )
}

export function MapPage({ focusRegion }: { focusRegion?: string }) {
  const { data, isLoading, isError } = useTopology()
  const endpoint = useEndpoint()

  // focusRegion comes from a search-param view-transition (minimap click).
  // Fall back to the live endpoint region so region-switcher changes are reflected.
  const effectiveRegion = focusRegion ?? endpoint.region
  const instancesByFunction = useLambdaInstances()
  const [peekTarget, setPeekTarget] = useState<LogStreamTarget | null>(null)
  const onPeek = useCallback((target: LogStreamTarget) => setPeekTarget(target), [])
  const onPeekClose = useCallback(() => setPeekTarget(null), [])
  const [lakePeek, setLakePeek] = useState<DataLakePeek | null>(null)
  const onLakePeekClose = useCallback(() => setLakePeek(null), [])

  // ── Ghost lambda instances ───────────────────────────────────────────────
  // When a running instance disappears from the live list it lingers as a
  // ghost for LAMBDA_GHOST_TTL ms so the developer can still see it on the
  // map before it fades out. This must use the same TTL LambdaGroupNode uses
  // for its own (independent) ghost tracking, so the box height computed
  // here for the layout matches what the node actually renders.
  const allInstances = useMemo(
    () => Object.values(instancesByFunction).flatMap((arr) => arr ?? []),
    [instancesByFunction],
  )
  const ghostInstances = useGhostTracker({
    items: allInstances,
    getKey: (i) => i.instanceId,
    ttl: LAMBDA_GHOST_TTL,
  })

  const topologyNodes = useMemo(() => data?.nodes ?? [], [data])
  const topologyEdges = useMemo(() => data?.edges ?? [], [data])

  // ── Zoom-aware nested stack collapse ───────────────────────────────────
  // Quantise zoom to 0.05 increments via onViewportChange to avoid
  // excessive re-renders. The collapse hook returns a stable Set that only
  // changes when actual collapse/expand state flips.
  const [zoom, setZoom] = useState(1)
  const handleViewportChange = useCallback(({ zoom: z }: { zoom: number }) => {
    const q = Math.round(z * 20) / 20
    setZoom((prev) => (prev === q ? prev : q))
  }, [])
  const collapsedStacks = useZoomCollapse(topologyEdges, zoom)

  // Track which node IDs have been shown so we can flag genuinely new ones.
  // A ref is used because this accumulates over time without needing to trigger
  // re-renders — the value is read during render intentionally (it determines
  // the `isNew` flag on nodes for the pop-in animation).
  const seenNodeIds = useRef<Set<string>>(new Set())

  // ── Async layout via Web Worker ──────────────────────────────────────
  // useSyncExternalStore gives us a reactive subscription to the module-level
  // worker result — no tearing, no effects needed on the read side.
  const layout = useSyncExternalStore(subscribeToLayout, getLayoutSnapshot)
  const layoutPending = useSyncExternalStore(subscribeToLayout, isLayoutLoading)
  const positionedNodes = layout.nodes

  // Trigger layout when the inputs change.  requestLayoutAsync deduplicates
  // internally — skips when the content hash matches the previous request.
  const {
    glowingEdges,
    nodeCounts,
    nodeWriteCounts,
    edgeBurstCounts,
    nodeWriteBurstCounts,
    dataLake,
  } = useEventAnimations(topologyNodes, topologyEdges)
  // Only a dropped table's ghost changes a node's size; flashes and ticks do not.
  const lakeGhosts = dataLake.ghosts

  useEffect(() => {
    if (topologyNodes.length === 0) return
    const sizes = computeSizeOverrides(
      topologyNodes,
      instancesByFunction,
      ghostInstances,
      lakeGhosts,
    )
    requestLayoutAsync(topologyNodes, topologyEdges, sizes, effectiveRegion, collapsedStacks)
  }, [
    topologyNodes,
    topologyEdges,
    instancesByFunction,
    ghostInstances,
    lakeGhosts,
    effectiveRegion,
    collapsedStacks,
  ])

  const liveNodes = useMemo(() => new Map(topologyNodes.map((n) => [n.id, n])), [topologyNodes])

  // Expand positioned layout into final React Flow nodes with event counts
  // and injected hasTarget/hasSource booleans from the edge lookup sets.
  // The dep list intentionally excludes `seenNodeIds.current` — reading it
  // during render tracks accumulating state that shouldn't trigger re-runs.
  /* eslint-disable react-hooks/refs */
  const rfNodes = useMemo(() => {
    if (positionedNodes.length === 0) return []
    const expanded = expandFlowNodes(positionedNodes, {
      instancesByFunction,
      ghostInstances,
      nodeCounts,
      nodeWriteCounts,
      nodeWriteBurstCounts,
      isInitialLoad: seenNodeIds.current.size === 0,
      seenNodeIds: seenNodeIds.current,
      onPeek,
      onPeekStream: onPeek,
      liveNodes,
    })
    const targetNodes = new Set(topologyEdges.map((e) => e.target))
    const sourceNodes = new Set(topologyEdges.map((e) => e.source))
    for (const n of expanded) {
      if (HANDLED_TYPES.has(n.type ?? "")) {
        n.data = { ...n.data, hasTarget: targetNodes.has(n.id), hasSource: sourceNodes.has(n.id) }
      }
    }
    return expanded
  }, [
    positionedNodes,
    liveNodes,
    topologyEdges,
    instancesByFunction,
    ghostInstances,
    nodeCounts,
    nodeWriteCounts,
    nodeWriteBurstCounts,
    onPeek,
  ])
  /* eslint-enable react-hooks/refs */

  // After the first paint, record all node IDs so they won't be treated as
  // "new" on subsequent renders.  Ref mutation in an effect is intentional
  // here — the accumulator doesn't need to trigger a re-render.
  useEffect(() => {
    for (const n of topologyNodes) {
      seenNodeIds.current.add(n.id)
    }
  }, [topologyNodes])

  // ── Focus: hover a node to see only its neighbourhood; hover a legend
  // entry to see only that kind of connection. Everything else dims.
  const [focus, setFocus] = useState<Focus>(null)
  const focusTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const clearFocusTimer = () => {
    if (focusTimer.current) {
      clearTimeout(focusTimer.current)
      focusTimer.current = null
    }
  }
  const onNodeMouseEnter = useCallback((_: React.MouseEvent, node: Node) => {
    if (CONTAINER_TYPES.has(node.type ?? "")) return
    clearFocusTimer()
    // A short rest before dimming: sweeping the pointer across the map must
    // not strobe every box on the way.
    focusTimer.current = setTimeout(() => setFocus({ node: node.id }), FOCUS_DELAY_MS)
  }, [])
  const onNodeMouseLeave = useCallback(() => {
    clearFocusTimer()
    setFocus(null)
  }, [])
  useEffect(() => clearFocusTimer, [])

  // Neighbourhood of the focused node, or the edges of the focused type.
  const { focusNodes, focusEdges } = useMemo(() => {
    const nodes = new Set<string>()
    const edges = new Set<string>()
    if (focus && "node" in focus) {
      nodes.add(focus.node)
      for (const e of topologyEdges) {
        if (e.source === focus.node || e.target === focus.node) {
          edges.add(e.id)
          nodes.add(e.source)
          nodes.add(e.target)
        }
      }
    } else if (focus && "edgeType" in focus) {
      for (const e of topologyEdges) {
        if (e.type === focus.edgeType) {
          edges.add(e.id)
          nodes.add(e.source)
          nodes.add(e.target)
        }
      }
    }
    return { focusNodes: nodes, focusEdges: edges }
  }, [focus, topologyEdges])

  // Dimming is a class on the React Flow wrapper, so a hover never re-renders
  // the (heavy) node components themselves — their `data` is unchanged.
  const displayNodes = useMemo(() => {
    if (!focus) return rfNodes
    return rfNodes.map((n) =>
      CONTAINER_TYPES.has(n.type ?? "") || focusNodes.has(n.id)
        ? n
        : { ...n, className: cn(n.className, "map-dim") },
    )
  }, [rfNodes, focus, focusNodes])

  // Build React Flow edges with glow state, the layout's route and the
  // focus state injected.
  // Filter out any edge whose source or target node isn't present — a missing
  // node causes React Flow to render a dangling line going nowhere.
  const rfEdges: Edge[] = useMemo(() => {
    const nodeIdSet = new Set(rfNodes.map((n) => n.id))
    return topologyEdges
      .filter((e) => e.type !== "nested-stack") // nesting shown visually
      .filter((e) => nodeIdSet.has(e.source) && nodeIdSet.has(e.target))
      .map((e) => {
        const edgeColor = EDGE_THEME[e.type]?.color ?? FALLBACK_COLOR
        return {
          id: e.id,
          source: e.source,
          target: e.target,
          type: "topologyEdge",
          data: {
            glowing: glowingEdges.has(e.id),
            edgeType: e.type,
            label: e.label,
            state: e.state,
            burstCount: edgeBurstCounts[e.id] ?? 0,
            route: layout.routes[e.id],
            dimmed: focus !== null && !focusEdges.has(e.id),
          },
          markerEnd: {
            type: MarkerType.ArrowClosed,
            width: 9,
            height: 9,
            color: edgeColor,
          },
        }
      })
  }, [rfNodes, topologyEdges, glowingEdges, edgeBurstCounts, layout.routes, focus, focusEdges])

  // Only the connection types actually on the map belong in the legend.
  const legendTypes = useMemo(() => {
    const present = new Set(topologyEdges.map((e) => e.type))
    return Object.entries(EDGE_THEME).filter(
      (entry): entry is [string, NonNullable<(typeof EDGE_THEME)[string]>] =>
        entry[1] != null && present.has(entry[0]),
    )
  }, [topologyEdges])

  const hasRealResources = topologyNodes.length > 0
  const isEmpty = !isLoading && !isError && topologyNodes.length === 0 && rfNodes.length === 0
  const hasRegionGroups = rfNodes.some((n) => n.type === "regionGroup")
  // The first layout runs in the worker after the data arrives: that gap is
  // still "loading" to the person waiting for the map.
  const showSpinner = isLoading || (hasRealResources && rfNodes.length === 0 && layoutPending)

  return (
    <div className="flex h-full flex-col" style={{ viewTransitionName: "system-map" }}>
      <div className="shrink-0 px-6 pt-6 pb-4">
        <PageHeader
          title="System Map"
          description="Live topology of all services and connections. Particles animate when data moves."
        />
      </div>

      <div
        className="map-canvas relative min-h-0 flex-1 overflow-hidden"
        data-zoom={zoom < FAR_ZOOM ? "far" : "near"}
      >
        {showSpinner && (
          <div className="absolute inset-0 flex items-center justify-center">
            <Spinner className="h-6 w-6" />
          </div>
        )}

        {isError && (
          <div className="absolute inset-0 flex items-center justify-center text-sm text-fg-muted">
            Could not load topology — is the emulator running?
          </div>
        )}

        {isEmpty && (
          <div className="absolute inset-0 flex items-center justify-center text-sm text-fg-muted">
            No resources found. Create some buckets, queues, or tables to see them here.
          </div>
        )}

        {!isLoading && !isError && rfNodes.length > 0 && (
          <DataLakePeekContext value={setLakePeek}>
            <DataLakeOverlayContext value={dataLake}>
              <ReactFlow
                onlyRenderVisibleElements
                nodes={displayNodes}
                edges={rfEdges}
                nodeTypes={NODE_TYPES}
                edgeTypes={EDGE_TYPES}
                fitView
                fitViewOptions={{ padding: 0.2, maxZoom: 1.2 }}
                // React Flow's default floor of 0.5 stops a map of any real size
                // from ever fitting a laptop viewport.
                minZoom={0.1}
                nodesDraggable
                nodesConnectable={false}
                edgesFocusable={false}
                onViewportChange={handleViewportChange}
                onNodeMouseEnter={onNodeMouseEnter}
                onNodeMouseLeave={onNodeMouseLeave}
                className="bg-bg"
              >
                <FitViewOnResize />
                <FitViewOnDataChange dataCount={topologyNodes.length} />
                <FitViewOnCollapse collapsedStacks={collapsedStacks} />
                <FitViewOnRegionChange region={effectiveRegion} hasRegionGroups={hasRegionGroups} />
                <Background
                  variant={BackgroundVariant.Dots}
                  gap={20}
                  size={1}
                  className="opacity-20"
                />
                <Controls showInteractive={false} />
                <CustomMiniMap />
              </ReactFlow>
            </DataLakeOverlayContext>
          </DataLakePeekContext>
        )}

        {/* Legend — only the connection types on this map; hover one to pick it out */}
        {!showSpinner && hasRealResources && legendTypes.length > 0 && (
          <div
            className="absolute top-3 right-3 rounded-lg border border-border bg-bg-elevated/90 p-3 text-2xs backdrop-blur-sm"
            onMouseLeave={() => setFocus(null)}
          >
            <p className={cn(sectionLabel, "mb-2 text-fg-muted")}>Connections</p>
            <ul className="flex flex-col gap-1">
              {legendTypes.map(([type, item]) => {
                const selected = focus !== null && "edgeType" in focus && focus.edgeType === type
                return (
                  <li key={type}>
                    <button
                      type="button"
                      onMouseEnter={() => setFocus({ edgeType: type })}
                      onFocus={() => setFocus({ edgeType: type })}
                      onBlur={() => setFocus(null)}
                      className={cn(
                        "-mx-1.5 flex w-[calc(100%+0.75rem)] items-center gap-2 rounded px-1.5 py-0.5 text-left transition-colors",
                        selected ? "bg-bg-muted text-fg" : "text-fg-muted hover:text-fg",
                      )}
                    >
                      <svg width="24" height="8" className="shrink-0" aria-hidden="true">
                        <line
                          x1="0"
                          y1="4"
                          x2="24"
                          y2="4"
                          stroke={item.color}
                          strokeWidth="2"
                          strokeDasharray={item.dash ? "4 2" : undefined}
                        />
                      </svg>
                      <span>{item.label}</span>
                    </button>
                  </li>
                )
              })}
            </ul>
          </div>
        )}

        {/* Log-stream peek panel */}
        <LogStreamPeek target={peekTarget} onClose={onPeekClose} />
        <DataLakePeeks peek={lakePeek} nodes={liveNodes} onClose={onLakePeekClose} />
      </div>
    </div>
  )
}
