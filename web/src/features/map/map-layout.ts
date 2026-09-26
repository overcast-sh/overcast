/**
 * map-layout — automatic graph layout for the system map.
 *
 * Hierarchy: region → (CloudFormation stack, recursive | VPC) → resource.
 * Each level lays out its own members with the engine in
 * map-layout-engine.ts and hands the parent a single fat box, so a stack or
 * a VPC is one node to the level above it. Within a level the members are
 * split into connected components, each is laid out on its own, and the
 * blocks are packed with the flows above the loose inventory, which is
 * grouped by service. What "good" means for all of that is the score in
 * map-layout-score.ts; docs/plans/map-layout-objective.md is the
 * specification.
 *
 * Every edge's waypoints are carried up through the hierarchy into absolute
 * coordinates, then map-edge-routing checks each one against the nodes it
 * is not attached to and re-routes the ones that would cut through a box.
 * All of it runs in a worker before anything is drawn, so nodes appear in
 * their final place.
 */

import type { Node } from "@xyflow/react"
import type { TopologyNode, TopologyEdge } from "@/types"
import { createWorkerClient } from "@/lib/worker-client"
import { ObstacleIndex, routeEdge, type Pt, type Rect } from "./map-edge-routing"
import {
  COLLAPSED_STACK_HEIGHT,
  COLLAPSED_STACK_WIDTH,
  EMPTY_REGION_HEIGHT,
  EMPTY_REGION_WIDTH,
  isContainerType,
  REGION_GAP,
  REGION_PADDING,
  STACK_PADDING,
  VPC_PADDING,
} from "./map-layout-constants"
import { absoluteRects, type SizeOverrides } from "./map-layout-score"
import {
  annealingIterationsPerNode,
  FULL_LEVEL_PARAMS,
  layoutLevel,
  translateRoutes,
  type LevelParams,
  type Positioned,
  type Routes,
  type SubgraphLayout,
} from "./map-layout-engine"

export {
  COLLAPSED_STACK_HEIGHT,
  COLLAPSED_STACK_WIDTH,
  ESM_FILTER_NODE_SIZE,
  IGW_NODE_HEIGHT,
  IGW_NODE_WIDTH,
  NODE_HEIGHT,
  NODE_WIDTH,
  VPC_NODE_HEIGHT,
} from "./map-layout-constants"
export { segmentsCross } from "./map-edge-routing"


// ─── Lambda group sizing ──────────────────────────────────────────────────
//
// Lives here (not topology-nodes.tsx) so this plain-data module can be
// imported from the layout worker without pulling React component code
// into the worker bundle — see map-layout.worker.ts.

/** Header height (px) of a LambdaGroupNode — keep in sync with topology-nodes.tsx. */
export const LAMBDA_GROUP_HEADER_H = 56
/** Row height (px) of a single instance card — keep in sync with lambda-instance-node.tsx's LAMBDA_INSTANCE_H. */
const LAMBDA_INSTANCE_H = 100
/** Padding above/below the scrollable instance list inside a LambdaGroupNode. */
const LAMBDA_LIST_PAD_TOP = 8
const LAMBDA_LIST_PAD_BOTTOM = 8

/**
 * Maximum number of Lambda instance rows shown before the list becomes
 * internally scrollable. Bounds LambdaGroupNode's height so a function with
 * many concurrent instances can't grow into overlapping its neighbours on
 * the map — mirrors the fixed max-height + scroll pattern already used by
 * the SQS message list and CloudWatch Logs stream list.
 */
export const LAMBDA_GROUP_MAX_VISIBLE = 4

/**
 * Total LambdaGroupNode box height for a given instance count, capped at
 * LAMBDA_GROUP_MAX_VISIBLE rows. Both map-page.tsx (layout sizing) and
 * topology-nodes.tsx (actual rendered height) call this so the two always
 * agree — deterministic from data, no post-render measurement required.
 */
export function lambdaGroupHeight(instanceCount: number): number {
  const visibleRows = Math.min(Math.max(instanceCount, 0), LAMBDA_GROUP_MAX_VISIBLE)
  return (
    LAMBDA_GROUP_HEADER_H +
    LAMBDA_LIST_PAD_TOP +
    visibleRows * LAMBDA_INSTANCE_H +
    LAMBDA_LIST_PAD_BOTTOM
  )
}

// ─── Level layout ─────────────────────────────────────────────────────────

/**
 * Lays out one level of the hierarchy with the engine: components, each
 * through the Sugiyama pipeline, then packed with the flows above the loose
 * inventory. See map-layout-engine.ts.
 */
function layoutSubgraph(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  sizeOverrides: SizeOverrides,
  iterationsPerNode: number,
  /** A region's direct contents (as opposed to a stack's or a VPC's), where the aspect term applies. */
  region: boolean,
  params: LevelParams = FULL_LEVEL_PARAMS,
): SubgraphLayout {
  return layoutLevel(nodes, edges, sizeOverrides, params, iterationsPerNode, isPhantomId, region)
}

// ─── React Flow node construction ─────────────────────────────────────────

/** Ids the layout makes up for a stack or VPC box standing in for its contents. */
function isPhantomId(id: string): boolean {
  return id.startsWith("stack::") || id.startsWith("vpc-group-")
}

/** Map service key to custom React Flow node type. */
function nodeType(service: string): string {
  switch (service) {
    case "vpc":
      return "vpcNetworkNode"
    case "igw":
      return "igwNode"
    case "athena":
      return "athenaWorkgroup"
    case "glue":
    case "s3tables":
      return "dataCatalog"
    default:
      return "serviceNode"
  }
}

/** Create a React Flow node from a positioned topology node. */
function toFlowNode(p: Positioned, parentId?: string): Node {
  return {
    id: p.node.id,
    type: nodeType(p.node.service),
    ...(parentId ? { parentId, extent: "parent" as const } : {}),
    position: { x: p.x, y: p.y },
    width: p.w,
    height: p.h,
    data: {
      service: p.node.service,
      label: p.node.label,
      region: p.node.region,
      streamEnabled: p.node.streamEnabled,
      approximateNumberOfMessages: p.node.approximateNumberOfMessages,
      approximateNumberOfMessagesNotVisible: p.node.approximateNumberOfMessagesNotVisible,
      status: p.node.status,
      cidrBlock: p.node.cidrBlock,
      subnetCount: p.node.subnetCount,
      hasInternetGateway: p.node.hasInternetGateway,
      attachedVpcId: p.node.attachedVpcId,
      protocolType: p.node.protocolType,
      routeCount: p.node.routeCount,
      stageCount: p.node.stageCount,
      authenticationType: p.node.authenticationType,
      dataSourceCount: p.node.dataSourceCount,
      resolverCount: p.node.resolverCount,
      repositoryUri: p.node.repositoryUri,
      scope: p.node.scope,
      ruleCount: p.node.ruleCount,
      esmId: p.node.esmId,
      functionName: p.node.functionName,
      eventSource: p.node.eventSource,
      sourceType: p.node.sourceType,
      filterPatterns: p.node.filterPatterns,
      ecsResourceType: p.node.ecsResourceType,
      clusterName: p.node.clusterName,
      taskId: p.node.taskId,
      desiredCount: p.node.desiredCount,
      runningCount: p.node.runningCount,
    },
    style: { width: p.w },
  }
}

function groupNode(
  id: string,
  type: string,
  x: number,
  y: number,
  w: number,
  h: number,
  data: Record<string, unknown>,
  parentId?: string,
): Node {
  return {
    id,
    type,
    ...(parentId ? { parentId, extent: "parent" as const } : {}),
    position: { x, y },
    width: w,
    height: h,
    data,
    style: { width: w, height: h },
    draggable: false,
    selectable: false,
  }
}

/**
 * Services whose node exists because of another resource rather than on its
 * own: a Lambda event-source-mapping filter sits between a table and a
 * function, and a function's log group is created by the function. The
 * topology gives neither a stack (CloudFormation did not declare them), so
 * left alone they land outside the box while the resource they hang off sits
 * in the middle of it, and every one of those edges has to cross half the
 * stack and be detoured around whatever is in the way. Placing them in the
 * stack all their neighbours share lets the layout put each one right next to its
 * resource, which is where a reader looks for it.
 */
const DERIVED_SERVICES = new Set(["esm-filter", "logs"])

function adoptNeighbourStacks(nodes: TopologyNode[], edges: TopologyEdge[]): TopologyNode[] {
  const orphans = nodes.filter((n) => DERIVED_SERVICES.has(n.service) && !n.stackName)
  if (orphans.length === 0) return nodes
  const stackOf = new Map<string, string | undefined>()
  for (const n of nodes) stackOf.set(n.id, n.stackName ?? undefined)
  const adopted = new Map<string, string>()
  for (const o of orphans) {
    let stack: string | undefined
    let agree = true
    for (const e of edges) {
      const other = e.source === o.id ? e.target : e.target === o.id ? e.source : null
      if (!other) continue
      const s = stackOf.get(other)
      if (!s) {
        agree = false
        break
      }
      if (stack === undefined) stack = s
      else if (stack !== s) {
        agree = false
        break
      }
    }
    // A node with no connections at all has nothing to adopt from.
    if (agree && stack) adopted.set(o.id, stack)
  }
  if (adopted.size === 0) return nodes
  return nodes.map((n) => (adopted.has(n.id) ? { ...n, stackName: adopted.get(n.id) } : n))
}

export interface MapLayout {
  nodes: Node[]
  /** Edge id → interior waypoints in absolute canvas coordinates. Absent means a plain curve. */
  routes: Record<string, Pt[]>
}

/**
 * Converts topology nodes and edges into positioned React Flow nodes plus a
 * waypoint route for every edge that needs one.
 *
 * @param nodeSizeOverrides - optional per-node {width, height} overrides, keyed by node ID.
 */
export function buildLayout(
  topologyNodes: TopologyNode[],
  topologyEdges: TopologyEdge[] = [],
  nodeSizeOverrides: SizeOverrides = {},
  /** When set, guarantees this region has a group box even if it has no resources. */
  activeRegion?: string,
  /** Stack phantom IDs to render as collapsed chips instead of full groups. */
  collapsedStacks: Set<string> = new Set(),
): MapLayout {
  topologyNodes = adoptNeighbourStacks(topologyNodes, topologyEdges)
  // The annealing budget is set once from the whole map's size, so every
  // level gets its share and the H6 limit holds for the call as a whole.
  const iterationsPerNode = annealingIterationsPerNode(topologyNodes.length)

  // Group nodes by region.
  const byRegion = new Map<string, TopologyNode[]>()
  for (const n of topologyNodes) {
    const r = n.region
    if (!byRegion.has(r)) byRegion.set(r, [])
    byRegion.get(r)!.push(n)
  }

  // Ensure the active region exists even when empty.
  if (activeRegion && !byRegion.has(activeRegion)) {
    byRegion.set(activeRegion, [])
  }

  // The region being worked in comes first; the rest follow alphabetically.
  const regions = [...byRegion.keys()].sort(
    (a, b) => Number(b === activeRegion) - Number(a === activeRegion) || a.localeCompare(b),
  )
  if (regions.length === 0) return { nodes: [], routes: {} }
  const gapBelow = regionGaps(regions, topologyNodes, topologyEdges)

  // Every region gets a group container with a region badge — even when
  // there is only one region — so the developer always sees which region
  // resources belong to.
  const result: Node[] = []
  /** Absolute-coordinate routes, accumulated across regions. */
  const absRoutes: Routes = new Map()
  let yOffset = 0

  for (const region of regions) {
    const regionNodes = byRegion.get(region)!
    const groupId = `region::${region}`

    if (regionNodes.length === 0) {
      // Empty region — render a placeholder-sized box.
      result.push(
        groupNode(groupId, "regionGroup", 0, yOffset, EMPTY_REGION_WIDTH, EMPTY_REGION_HEIGHT, {
          region,
          empty: true,
          active: region === activeRegion,
        }),
      )
      yOffset += EMPTY_REGION_HEIGHT + gapBelow.get(region)!
      continue
    }

    // Separate nodes into stacked (belongs to a CFN stack), VPC-grouped, and unstacked.
    const byStack = new Map<string, TopologyNode[]>()
    const byVpc = new Map<string, TopologyNode[]>()
    const unstacked: TopologyNode[] = []
    for (const n of regionNodes) {
      if (n.stackName) {
        if (!byStack.has(n.stackName)) byStack.set(n.stackName, [])
        byStack.get(n.stackName)!.push(n)
      } else if (n.vpcId) {
        if (!byVpc.has(n.vpcId)) byVpc.set(n.vpcId, [])
        byVpc.get(n.vpcId)!.push(n)
      } else {
        unstacked.push(n)
      }
    }

    const regionNodeIds = new Set(regionNodes.map((n) => n.id))
    const regionEdges = topologyEdges.filter(
      (e) => regionNodeIds.has(e.source) && regionNodeIds.has(e.target),
    )

    if (byStack.size === 0 && byVpc.size === 0) {
      // No stacks — simple flat layout (fast path).
      const { positioned, width, height, routes } = layoutSubgraph(
        regionNodes,
        regionEdges,
        nodeSizeOverrides,
        iterationsPerNode,
        true,
      )
      const groupW = Math.round(width + REGION_PADDING.x * 2)
      const groupH = Math.round(height + REGION_PADDING.top + REGION_PADDING.bottom)

      result.push(
        groupNode(groupId, "regionGroup", 0, yOffset, groupW, groupH, {
          region,
          active: region === activeRegion,
        }),
      )
      for (const p of positioned) {
        result.push(toFlowNode({ ...p, x: p.x + REGION_PADDING.x, y: p.y + REGION_PADDING.top }, groupId))
      }
      translateRoutes(routes, REGION_PADDING.x, REGION_PADDING.top + yOffset, absRoutes)

      yOffset += groupH + gapBelow.get(region)!
      continue
    }

    // ── Nested stack layout: region → stack (recursive) / vpc → node ─────

    // Parse nested-stack edges to derive parent→child stack relationships.
    const stackParent = new Map<string, string>()
    const stackChildMap = new Map<string, string[]>()
    for (const e of topologyEdges) {
      if (e.type !== "nested-stack") continue
      const pn = e.source.split("::").pop()!
      const cn = e.target.split("::").pop()!
      if (pn && cn) {
        stackParent.set(cn, pn)
        const arr = stackChildMap.get(pn) ?? []
        arr.push(cn)
        stackChildMap.set(pn, arr)
      }
    }

    // Root stacks: present in byStack and not a child of another stack.
    const rootStacks = [...byStack.keys()].filter((n) => !stackParent.has(n))

    // Total recursive resource count (for collapsed chip badges).
    function totalResources(name: string): number {
      const direct = byStack.get(name)?.length ?? 0
      return direct + (stackChildMap.get(name) ?? []).reduce((s, c) => s + totalResources(c), 0)
    }

    // Collect all descendant resource node IDs for edge remapping.
    function descendantNodeIds(name: string): string[] {
      const ids = (byStack.get(name) ?? []).map((n) => n.id)
      for (const c of stackChildMap.get(name) ?? []) {
        ids.push(...descendantNodeIds(c))
      }
      return ids
    }

    // ── Recursive stack layout (bottom-up) ──────────────────────────────
    // Each stack's direct resources + child stacks are laid out by the engine.
    // Collapsed children become compact chip nodes; expanded children
    // become nested sub-groups.

    interface StackLayoutResult {
      resultNodes: Node[]
      width: number
      height: number
      resourceCount: number
      /** Routes relative to the stack group's own top-left corner. */
      routes: Routes
    }

    function layoutStackHierarchy(stackName: string): StackLayoutResult {
      const stackId = `stack::${region}::${stackName}`
      const directNodes = byStack.get(stackName) ?? []
      const children = stackChildMap.get(stackName) ?? []

      // --- Recurse into children first (bottom-up) ---
      const childResults = new Map<string, StackLayoutResult>()
      const childOverrides: Record<string, { width: number; height: number }> = {}

      for (const cn of children) {
        const childId = `stack::${region}::${cn}`
        if (collapsedStacks.has(childId)) {
          childResults.set(cn, {
            resultNodes: [],
            width: COLLAPSED_STACK_WIDTH,
            height: COLLAPSED_STACK_HEIGHT,
            resourceCount: totalResources(cn),
            routes: new Map(),
          })
          childOverrides[childId] = {
            width: COLLAPSED_STACK_WIDTH,
            height: COLLAPSED_STACK_HEIGHT,
          }
        } else {
          const cr = layoutStackHierarchy(cn)
          childResults.set(cn, cr)
          childOverrides[childId] = {
            width: Math.round(cr.width + STACK_PADDING.x * 2),
            height: Math.round(cr.height + STACK_PADDING.top + STACK_PADDING.bottom),
          }
        }
      }

      // --- Build the level: direct resources + child phantoms ---
      const layoutNodes: TopologyNode[] = [
        ...directNodes,
        ...children.map((cn) => ({
          id: `stack::${region}::${cn}`,
          service: "cloudformation",
          label: cn,
          region,
        })),
      ]

      // Remap edges: child descendants → child phantom ID.
      const nodeToChild = new Map<string, string>()
      for (const cn of children) {
        const cid = `stack::${region}::${cn}`
        for (const nid of descendantNodeIds(cn)) {
          nodeToChild.set(nid, cid)
        }
      }
      const layoutIds = new Set(layoutNodes.map((n) => n.id))
      const intraEdges = regionEdges
        .map((e) => ({
          ...e,
          source: nodeToChild.get(e.source) ?? e.source,
          target: nodeToChild.get(e.target) ?? e.target,
        }))
        .filter((e) => layoutIds.has(e.source) && layoutIds.has(e.target))
        .filter((e) => e.source !== e.target)

      const mergedSizes = { ...nodeSizeOverrides, ...childOverrides }
      const { positioned, width, height, routes } = layoutSubgraph(
        layoutNodes,
        intraEdges,
        mergedSizes,
        iterationsPerNode,
        false,
      )

      // --- Emit React Flow nodes ---
      const resultNodes: Node[] = []
      const sw = Math.round(width + STACK_PADDING.x * 2)
      const sh = Math.round(height + STACK_PADDING.top + STACK_PADDING.bottom)
      const stackRoutes: Routes = new Map()
      translateRoutes(routes, STACK_PADDING.x, STACK_PADDING.top, stackRoutes)

      // Stack group container (position set by caller).
      resultNodes.push(groupNode(stackId, "stackGroup", 0, 0, sw, sh, { stackName }))

      for (const p of positioned) {
        const px = p.x + STACK_PADDING.x
        const py = p.y + STACK_PADDING.top

        if (p.node.id.startsWith("stack::")) {
          // Child stack phantom.
          const cn = p.node.label
          const cid = p.node.id
          const cr = childResults.get(cn)!

          if (collapsedStacks.has(cid)) {
            // Collapsed chip: small clickable node.
            resultNodes.push({
              id: cid,
              type: "collapsedStack",
              parentId: stackId,
              extent: "parent" as const,
              position: { x: px, y: py },
              width: COLLAPSED_STACK_WIDTH,
              height: COLLAPSED_STACK_HEIGHT,
              data: { stackName: cn, resourceCount: cr.resourceCount },
              style: { width: COLLAPSED_STACK_WIDTH },
              draggable: false,
              selectable: false,
            })
          } else {
            // Expanded sub-group: reposition and wire parentId.
            const [group, ...groupChildren] = cr.resultNodes
            resultNodes.push({
              ...group,
              parentId: stackId,
              extent: "parent" as const,
              position: { x: px, y: py },
            })
            resultNodes.push(...groupChildren)
            translateRoutes(cr.routes, px, py, stackRoutes)
          }
        } else {
          // Regular resource node.
          resultNodes.push(toFlowNode({ ...p, x: px, y: py }, stackId))
        }
      }

      return {
        resultNodes,
        width,
        height,
        resourceCount: totalResources(stackName),
        routes: stackRoutes,
      }
    }

    // --- Run recursive layout for root stacks ---
    const stackResults = new Map<string, StackLayoutResult>()
    const stackSizeOverrides: Record<string, { width: number; height: number }> = {}

    for (const sn of rootStacks) {
      const sr = layoutStackHierarchy(sn)
      stackResults.set(sn, sr)
      const pid = `stack::${region}::${sn}`
      stackSizeOverrides[pid] = {
        width: Math.round(sr.width + STACK_PADDING.x * 2),
        height: Math.round(sr.height + STACK_PADDING.top + STACK_PADDING.bottom),
      }
    }

    // 1b. Layout each VPC internally.
    const vpcLayouts = new Map<string, SubgraphLayout>()
    const vpcSizeOverrides: Record<string, { width: number; height: number }> = {}

    for (const [vpcId, vpcNodes] of byVpc) {
      const vpcNodeIds = new Set(vpcNodes.map((n) => n.id))
      const intraEdges = regionEdges.filter(
        (e) => vpcNodeIds.has(e.source) && vpcNodeIds.has(e.target),
      )
      const layout = layoutSubgraph(vpcNodes, intraEdges, nodeSizeOverrides, iterationsPerNode, false)
      vpcLayouts.set(vpcId, layout)

      const phantomId = `vpc-group-${vpcId}`
      vpcSizeOverrides[phantomId] = {
        width: Math.round(layout.width + VPC_PADDING.x * 2),
        height: Math.round(layout.height + VPC_PADDING.top + VPC_PADDING.bottom),
      }
    }

    // 2. Build phantom nodes for root stacks + VPCs + keep unstacked nodes.
    const regionLevelNodes: TopologyNode[] = [
      ...unstacked,
      ...rootStacks.map((name) => ({
        id: `stack::${region}::${name}`,
        service: "cloudformation",
        label: name,
        region,
      })),
      ...[...byVpc.keys()].map((vpcId) => ({
        id: `vpc-group-${vpcId}`,
        service: "vpc",
        label: vpcId,
        region,
      })),
    ]

    // 3. Remap edges: all stacked/VPC node IDs → root phantom IDs.
    const nodeToPhantom = new Map<string, string>()
    for (const sn of rootStacks) {
      const pid = `stack::${region}::${sn}`
      function mapAll(name: string) {
        for (const n of byStack.get(name) ?? []) {
          nodeToPhantom.set(n.id, pid)
        }
        for (const cn of stackChildMap.get(name) ?? []) {
          mapAll(cn)
        }
      }
      mapAll(sn)
    }
    for (const [vpcId, vpcNodes] of byVpc) {
      const phantomId = `vpc-group-${vpcId}`
      for (const n of vpcNodes) {
        nodeToPhantom.set(n.id, phantomId)
      }
    }

    const remappedEdges = regionEdges
      .map((e) => ({
        ...e,
        source: nodeToPhantom.get(e.source) ?? e.source,
        target: nodeToPhantom.get(e.target) ?? e.target,
      }))
      .filter((e) => e.source !== e.target)

    // 4. Run region-level layout with root stacks and VPCs as fat nodes.
    const mergedOverrides = { ...nodeSizeOverrides, ...stackSizeOverrides, ...vpcSizeOverrides }
    const { positioned, width, height, routes } = layoutSubgraph(
      regionLevelNodes,
      remappedEdges,
      mergedOverrides,
      iterationsPerNode,
      true,
    )

    const groupW = Math.round(width + REGION_PADDING.x * 2)
    const groupH = Math.round(height + REGION_PADDING.top + REGION_PADDING.bottom)
    const originX = REGION_PADDING.x
    const originY = REGION_PADDING.top + yOffset

    // Region group node
    result.push(
      groupNode(groupId, "regionGroup", 0, yOffset, groupW, groupH, {
        region,
        active: region === activeRegion,
      }),
    )
    translateRoutes(routes, originX, originY, absRoutes)

    // 5. Emit positioned nodes — root stacks use recursive results,
    //    VPCs expand as before, unstacked nodes are direct children.
    for (const p of positioned) {
      const regionX = p.x + REGION_PADDING.x
      const regionY = p.y + REGION_PADDING.top

      if (p.node.id.startsWith("stack::")) {
        // Root stack → emit all nodes from recursive layout.
        const stackName = p.node.label
        const sr = stackResults.get(stackName)!
        const [group, ...groupChildren] = sr.resultNodes
        result.push({
          ...group,
          parentId: groupId,
          extent: "parent" as const,
          position: { x: regionX, y: regionY },
        })
        result.push(...groupChildren)
        translateRoutes(sr.routes, p.x + originX, p.y + originY, absRoutes)
      } else if (p.node.id.startsWith("vpc-group-")) {
        // Phantom → emit VPC group container + its children.
        const vpcId = p.node.label
        const vpcGroupId = p.node.id
        result.push(
          groupNode(vpcGroupId, "vpcGroup", regionX, regionY, p.w, p.h, { vpcId }, groupId),
        )

        const layout = vpcLayouts.get(vpcId)!
        for (const cp of layout.positioned) {
          result.push(toFlowNode({ ...cp, x: cp.x + VPC_PADDING.x, y: cp.y + VPC_PADDING.top }, vpcGroupId))
        }
        translateRoutes(
          layout.routes,
          p.x + originX + VPC_PADDING.x,
          p.y + originY + VPC_PADDING.top,
          absRoutes,
        )
      } else {
        // Unstacked node — direct child of region group.
        result.push(toFlowNode({ ...p, x: regionX, y: regionY }, groupId))
      }
    }

    yOffset += groupH + gapBelow.get(region)!
  }

  // Cross-region edges still reference the child nodes directly — React Flow
  // handles edges between children of different group nodes automatically.

  return { nodes: result, routes: routeEdges(result, topologyEdges, absRoutes) }
}

/** Vertical pitch of the lanes cross-region edges take through a region gap or a side channel. */
const CROSS_REGION_LANE_PITCH = 12
/** Clearance between the region column and the first lane of a side channel. */
const SIDE_CHANNEL_MARGIN = 24

/**
 * Which region gaps a cross-region edge runs along. Regions are stacked in
 * display order and gap g lies between region g and region g+1. An edge
 * leaves its source region into the gap on the target's side of it and
 * enters the target region from the gap on the source's side of it; between
 * two adjacent regions those are the same gap, otherwise a channel along the
 * side of the region column joins the two, so the edge never crosses a
 * region it is not attached to.
 */
function crossRegionGaps(source: number, target: number): { exit: number; entry: number } {
  return source < target ? { exit: source, entry: target - 1 } : { exit: source - 1, entry: target }
}

/**
 * The gap below each region box, grown to hold one lane per cross-region
 * edge that will run along it: eight connections do not share one line.
 */
function regionGaps(regions: string[], nodes: TopologyNode[], edges: TopologyEdge[]): Map<string, number> {
  const regionOf = new Map(nodes.map((n) => [n.id, n.region]))
  const order = new Map(regions.map((r, i) => [r, i]))
  const lanes = new Array<number>(regions.length).fill(0)
  for (const e of edges) {
    const a = regionOf.get(e.source)
    const b = regionOf.get(e.target)
    if (a === undefined || b === undefined || a === b) continue
    const { exit, entry } = crossRegionGaps(order.get(a)!, order.get(b)!)
    lanes[exit]++
    if (entry !== exit) lanes[entry]++
  }
  const gaps = new Map<string, number>()
  regions.forEach((r, i) => gaps.set(r, Math.max(REGION_GAP, CROSS_REGION_LANE_PITCH * (lanes[i] + 1))))
  return gaps
}

/**
 * Backwards-compatible entry point: just the positioned nodes.
 */
export function buildLayoutNodes(
  topologyNodes: TopologyNode[],
  topologyEdges: TopologyEdge[] = [],
  nodeSizeOverrides: SizeOverrides = {},
  activeRegion?: string,
  collapsedStacks: Set<string> = new Set(),
): Node[] {
  return buildLayout(topologyNodes, topologyEdges, nodeSizeOverrides, activeRegion, collapsedStacks)
    .nodes
}

/** Absolute rectangle of every leaf node, resolved through the parent chain. */
export function absoluteLeafRects(nodes: Node[]): Map<string, Rect> {
  const rects = absoluteRects(nodes)
  for (const n of nodes) if (isContainerType(n.type)) rects.delete(n.id)
  return rects
}

/**
 * Final routing pass: every edge whose two ends are boxes on the canvas gets
 * its polyline (right handle → the level's waypoints → left handle) checked
 * against every other box, and re-routed around them when it would cut
 * through one. Edges the engine never saw as a whole get a route of their
 * own first: a corridor between the two region boxes for a cross-region
 * edge, and a loop for a backward edge — so only the leg out of the source
 * and the leg into the target ever need a detour. (A lane past the two boxes
 * for an edge between sibling containers was tried and dropped: see
 * docs/plans/map-layout-objective.md §8.)
 */
function routeEdges(nodes: Node[], edges: TopologyEdge[], hints: Routes): Record<string, Pt[]> {
  const all = absoluteRects(nodes)
  const rects = new Map<string, Rect>()
  const regionOf = new Map<string, string>()
  const regionBoxes: Rect[] = []
  const regionIndex = new Map<string, number>()
  for (const n of nodes) {
    if (n.type === "regionGroup") regionBoxes.push(all.get(n.id)!)
    else if (!isContainerType(n.type)) {
      rects.set(n.id, all.get(n.id)!)
      regionOf.set(n.id, String(n.data.region ?? ""))
    }
  }
  regionBoxes.sort((a, b) => a.y - b.y)
  for (const n of nodes) {
    if (n.type === "regionGroup") regionIndex.set(String(n.data.region), regionBoxes.indexOf(all.get(n.id)!))
  }
  const index = new ObstacleIndex([...rects].map(([id, r]) => ({ id, ...r })))
  const out: Record<string, Pt[]> = {}
  const crossing: CrossRegionEdge[] = []
  // In id order, not input order: the lanes are assigned in sequence and
  // the output must not depend on how the API happened to list the edges.
  const ordered = [...edges].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  for (const e of ordered) {
    if (e.type === "nested-stack") continue
    const s = rects.get(e.source)
    const t = rects.get(e.target)
    if (!s || !t) continue
    const from = { x: s.x + s.w, y: s.y + s.h / 2 }
    const to = { x: t.x, y: t.y + t.h / 2 }
    const rs = regionIndex.get(regionOf.get(e.source) ?? "")
    const rt = regionIndex.get(regionOf.get(e.target) ?? "")
    if (rs !== undefined && rt !== undefined && rs !== rt) {
      crossing.push({ edge: e, from, to, source: rs, target: rt, ...crossRegionGaps(rs, rt), channelX: 0, exitY: 0, entryY: 0 })
      continue
    }
    const levelHint = hints.get(e.id)
    const loop = backEdgeLoop(from, to, s, t)
    const hint = levelHint && (loop.length === 0 || levelHint.length >= 2) ? levelHint : loop
    const route = routeEdge(from, to, hint, index, e.source, e.target)
    if (route.length > 0) out[e.id] = route
  }
  routeCrossRegion(crossing, regionBoxes, index, out)
  return out
}

interface CrossRegionEdge {
  edge: TopologyEdge
  from: Pt
  to: Pt
  /** Display index of the source and target regions. */
  source: number
  target: number
  /** The gaps the edge runs along (see crossRegionGaps); equal for adjacent regions. */
  exit: number
  entry: number
  /** The side channel's x when the two gaps differ. */
  channelX: number
  exitY: number
  entryY: number
}

/** One edge's use of one region gap: its horizontal run and the vertical legs that meet it. */
interface GapUser {
  owner: CrossRegionEdge
  /** Whether this is the owner's exit gap (else its entry gap). */
  exit: boolean
  x1: number
  x2: number
  legs: Array<{ x: number; up: boolean }>
}

/**
 * Routes the cross-region edges: side channels first (the nearer side per
 * edge, lanes nested so a shorter run sits inside a longer one and neither
 * crosses the other), then a lane in every gap, then the obstacle pass over
 * the leg out of the source and the leg into the target.
 */
function routeCrossRegion(
  list: CrossRegionEdge[],
  regionBoxes: Rect[],
  index: ObstacleIndex,
  out: Record<string, Pt[]>,
): void {
  let columnRight = 0
  for (const r of regionBoxes) columnRight = Math.max(columnRight, r.x + r.w)
  const sides: Record<"left" | "right", CrossRegionEdge[]> = { left: [], right: [] }
  for (const c of list) {
    if (c.exit === c.entry) continue
    const rightCost = columnRight - c.from.x + (columnRight - c.to.x)
    const leftCost = c.from.x + c.to.x
    sides[leftCost < rightCost ? "left" : "right"].push(c)
  }
  for (const side of ["left", "right"] as const) {
    const users = sides[side].sort(
      (a, b) => Math.abs(a.exit - a.entry) - Math.abs(b.exit - b.entry) || (a.edge.id < b.edge.id ? -1 : 1),
    )
    users.forEach((c, k) => {
      const offset = SIDE_CHANNEL_MARGIN + CROSS_REGION_LANE_PITCH * k
      c.channelX = side === "right" ? columnRight + offset : -offset
    })
  }
  for (let g = 0; g + 1 < regionBoxes.length; g++) {
    const users: GapUser[] = []
    for (const c of list) {
      const exitX = c.from.x + BACK_EDGE_STUB
      const entryX = c.to.x - BACK_EDGE_STUB
      if (c.exit === c.entry) {
        if (c.exit !== g) continue
        users.push({
          owner: c,
          exit: true,
          x1: Math.min(exitX, entryX),
          x2: Math.max(exitX, entryX),
          legs: [
            { x: exitX, up: c.source === g },
            { x: entryX, up: c.target === g },
          ],
        })
      } else if (c.exit === g) {
        users.push({
          owner: c,
          exit: true,
          x1: Math.min(exitX, c.channelX),
          x2: Math.max(exitX, c.channelX),
          legs: [
            { x: exitX, up: c.source === g },
            { x: c.channelX, up: c.entry < c.exit },
          ],
        })
      } else if (c.entry === g) {
        users.push({
          owner: c,
          exit: false,
          x1: Math.min(entryX, c.channelX),
          x2: Math.max(entryX, c.channelX),
          legs: [
            { x: c.channelX, up: c.exit < c.entry },
            { x: entryX, up: c.target === g },
          ],
        })
      }
    }
    if (users.length === 0) continue
    const top = regionBoxes[g].y + regionBoxes[g].h
    const lanes = assignLanes(users)
    users.forEach((u, i) => {
      const y = top + CROSS_REGION_LANE_PITCH * (lanes[i] + 1)
      if (u.exit) u.owner.exitY = y
      else u.owner.entryY = y
    })
  }
  for (const c of list) {
    const exitX = c.from.x + BACK_EDGE_STUB
    const entryX = c.to.x - BACK_EDGE_STUB
    const hint: Pt[] =
      c.exit === c.entry
        ? [
            { x: exitX, y: c.exitY },
            { x: entryX, y: c.exitY },
          ]
        : [
            { x: exitX, y: c.exitY },
            { x: c.channelX, y: c.exitY },
            { x: c.channelX, y: c.entryY },
            { x: entryX, y: c.entryY },
          ]
    const route = routeEdge(c.from, c.to, hint, index, c.edge.source, c.edge.target)
    if (route.length > 0) out[c.edge.id] = route
  }
}

/**
 * Orders the users of one region gap into lanes, top first, so their
 * vertical legs cross as few of the others' lanes as possible: each is
 * inserted where it adds the fewest crossings with those already placed.
 * Returns each user's lane index.
 */
function assignLanes(users: GapUser[]): number[] {
  const order: number[] = []
  const within = (x: number, u: GapUser) => x > u.x1 && x < u.x2
  // With lane i above lane j: a leg of j heading up passes i's lane, and a
  // leg of i heading down passes j's lane; each is a crossing when its x
  // lies within the other lane's horizontal run.
  const crossings = (i: number, j: number): number => {
    let c = 0
    for (const leg of users[j].legs) if (leg.up && within(leg.x, users[i])) c++
    for (const leg of users[i].legs) if (!leg.up && within(leg.x, users[j])) c++
    return c
  }
  for (let k = 0; k < users.length; k++) {
    let bestAt = 0
    let bestCost = Infinity
    for (let at = 0; at <= order.length; at++) {
      let cost = 0
      for (let p = 0; p < order.length; p++) cost += p < at ? crossings(order[p], k) : crossings(k, order[p])
      if (cost < bestCost) {
        bestCost = cost
        bestAt = at
      }
    }
    order.splice(bestAt, 0, k)
  }
  const lane = new Array<number>(users.length)
  order.forEach((k, i) => (lane[k] = i))
  return lane
}

/**
 * Out of the source and up (or down) to the lane, across, and into the
 * target: two waypoints, so the legs to and from the lane slope rather than
 * turn twice. The obstacle pass straightens any leg that clips a card.
 */
function laneRoute(from: Pt, to: Pt, lane: number): Pt[] {
  return [
    { x: from.x + BACK_EDGE_STUB, y: lane },
    { x: to.x - BACK_EDGE_STUB, y: lane },
  ]
}

/** How far a backward edge runs out of its handle before turning. */
const BACK_EDGE_STUB = 28
/** Clearance between a backward edge's lane and the boxes it loops around. */
const BACK_EDGE_LANE = 32

/**
 * A backward edge — target not clearly to the right of its source — drawn
 * as a plain Bézier between a right-hand and a left-hand handle leaves the
 * source, loops behind both cards and enters the target from the far side:
 * a line that seems to come from nowhere. So it gets an explicit loop
 * instead: out of the source, into a lane above or below both cards
 * (whichever is nearer), across, and into the target. Forward edges return
 * nothing and keep their ordinary curve.
 */
function backEdgeLoop(from: Pt, to: Pt, s: Rect, t: Rect): Pt[] {
  if (to.x >= from.x + BACK_EDGE_STUB * 2) return []
  const top = Math.min(s.y, t.y) - BACK_EDGE_LANE
  const bottom = Math.max(s.y + s.h, t.y + t.h) + BACK_EDGE_LANE
  const mid = (from.y + to.y) / 2
  return laneRoute(from, to, mid - top <= bottom - mid ? top : bottom)
}

// ─── Compact layout for dashboard minimap ────────────────────────────────────

/** Compact node dimensions and gaps used by the dashboard minimap. */
const MINI_PARAMS: LevelParams = {
  rankGap: 60,
  nodeGap: 16,
  edgeGap: 12,
  laneGap: 12,
  blockGap: 16,
  inventoryGap: 16,
  inventoryRowGap: 16,
  defaultW: 140,
  defaultH: 32,
  loopStub: 12,
  loopLane: 12,
}
const MINI_REGION_GAP = 24

/**
 * Builds a flat, compact layout for the dashboard minimap.
 *
 * Unlike `buildLayoutNodes`, this intentionally **omits** region/stack group
 * container nodes — the minimap header shows a stats summary instead.
 * All nodes are emitted as `type: "miniNode"` with smaller dimensions so the
 * topology fits legibly inside a 320px-tall card.
 */
export function buildCompactLayoutNodes(
  topologyNodes: TopologyNode[],
  topologyEdges: TopologyEdge[] = [],
): Node[] {
  if (topologyNodes.length === 0) return []

  // Group nodes by region and lay out each region separately, then stack
  // them vertically with a small gap.
  const byRegion = new Map<string, TopologyNode[]>()
  for (const n of topologyNodes) {
    if (!byRegion.has(n.region)) byRegion.set(n.region, [])
    byRegion.get(n.region)!.push(n)
  }

  const regions = [...byRegion.keys()].sort()
  const result: Node[] = []
  let yOffset = 0

  for (const region of regions) {
    const regionNodes = byRegion.get(region)!
    const regionNodeIds = new Set(regionNodes.map((n) => n.id))
    const regionEdges = topologyEdges.filter(
      (e) => regionNodeIds.has(e.source) && regionNodeIds.has(e.target),
    )

    const { positioned, height } = layoutSubgraph(
      regionNodes,
      regionEdges,
      {},
      annealingIterationsPerNode(topologyNodes.length),
      true,
      MINI_PARAMS,
    )

    for (const p of positioned) {
      result.push({
        id: p.node.id,
        type: "miniNode",
        position: { x: p.x, y: p.y + yOffset },
        width: p.w,
        height: p.h,
        data: {
          service: p.node.service,
          label: p.node.label,
        },
        style: { width: p.w },
      })
    }

    yOffset += height + MINI_REGION_GAP
  }

  return result
}

// ─── Module-level async layout worker ────────────────────────────────────

interface LayoutWorkerReply {
  id: number
  layout: MapLayout | null
  error: string | null
}

// Only the newest layout request matters: replies for superseded inputs are
// dropped by comparing this generation counter when a result lands. (The
// shared kernel resolves every reply it can correlate; staleness is this
// store's semantics, so the guard lives here.)
let _layoutId = 0
const _layoutListeners = new Set<() => void>()
const EMPTY_LAYOUT: MapLayout = { nodes: [], routes: {} }
let _layoutResult: MapLayout = EMPTY_LAYOUT
let _layoutLoading = false
let _lastLayoutHash = ""

// The shared kernel (lib/worker-client.ts) owns the lifecycle: this client
// used to leave the map loading forever if the worker died. Now a dead or
// unbuildable worker computes the layout synchronously on the main thread
// (buildLayout lives in this module), and one failure gets a respawn
// before the session gives up.
const layoutWorker = createWorkerClient<LayoutWorkerReply>({
  create: () => new Worker(new URL("./map-layout.worker.ts", import.meta.url), { type: "module" }),
  failureLimit: 2,
})

function layoutInputHash(
  topologyNodes: TopologyNode[],
  topologyEdges: TopologyEdge[],
  nodeSizeOverrides: SizeOverrides,
  activeRegion?: string,
  collapsedStacks?: Set<string>,
): string {
  const overridesStr = Object.entries(nodeSizeOverrides)
    .filter(([, v]) => v)
    .map(([k, v]) => `${k}:${v!.width}x${v!.height}`)
    .join(";")
  // Edge ids and the collapsed set are spelled out, not counted: swapping one
  // edge for another (or one collapsed stack for another) changes the layout.
  const edgesStr = topologyEdges.map((e) => e.id).join(",")
  const collapsedStr = collapsedStacks ? [...collapsedStacks].sort().join(",") : ""
  return `${topologyNodes.map((n) => n.id).join(",")}|${edgesStr}|${overridesStr}|${activeRegion ?? ""}|${collapsedStr}`
}

/**
 * Request async layout computation.
 * Deduplicates — skips if inputs are identical to the previous request.
 */
export function requestLayoutAsync(
  topologyNodes: TopologyNode[],
  topologyEdges: TopologyEdge[],
  nodeSizeOverrides: SizeOverrides,
  activeRegion?: string,
  collapsedStacks?: Set<string>,
): void {
  if (topologyNodes.length === 0) {
    _layoutResult = EMPTY_LAYOUT
    _layoutLoading = false
    _lastLayoutHash = ""
    _layoutListeners.forEach((cb) => cb())
    return
  }

  const hash = layoutInputHash(
    topologyNodes,
    topologyEdges,
    nodeSizeOverrides,
    activeRegion,
    collapsedStacks,
  )
  if (hash === _lastLayoutHash && !_layoutLoading) return
  _lastLayoutHash = hash

  _layoutLoading = true
  _layoutListeners.forEach((cb) => cb())
  const generation = ++_layoutId
  const commit = (reply: LayoutWorkerReply) => {
    if (generation !== _layoutId) return // superseded — a newer request owns the store
    if (reply.error) {
      console.error("Layout worker error:", reply.error)
      _layoutLoading = false
      _layoutListeners.forEach((cb) => cb())
      return
    }
    _layoutResult = reply.layout ?? EMPTY_LAYOUT
    _layoutLoading = false
    _layoutListeners.forEach((cb) => cb())
  }
  const outcome = layoutWorker.request<LayoutWorkerReply>({
    message: (id) => ({
      id,
      topologyNodes,
      topologyEdges,
      nodeSizeOverrides,
      activeRegion,
      collapsedStacks: collapsedStacks ? [...collapsedStacks] : [],
    }),
    decode: (reply) => reply,
    // No worker (or a dying one): compute on the main thread — same
    // inputs, same try/catch shape as map-layout.worker.ts.
    fallback: () => {
      try {
        const layout = buildLayout(
          topologyNodes,
          topologyEdges,
          nodeSizeOverrides,
          activeRegion,
          collapsedStacks ?? new Set(),
        )
        return { id: -1, layout, error: null }
      } catch (err) {
        return { id: -1, layout: null, error: String(err) }
      }
    },
  })
  if (outcome.async) void outcome.promise.then(commit)
  else commit(outcome.value)
}

/** useSyncExternalStore subscribe function — returns unsubscribe. */
export function subscribeToLayout(cb: () => void): () => void {
  _layoutListeners.add(cb)
  return () => {
    _layoutListeners.delete(cb)
  }
}

/** useSyncExternalStore snapshot function. */
export function getLayoutSnapshot(): MapLayout {
  return _layoutResult
}

/** Whether a layout computation is in-flight. */
export function isLayoutLoading(): boolean {
  return _layoutLoading
}
