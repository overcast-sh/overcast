/**
 * map-layout.test — verifies buildLayoutNodes never produces overlapping
 * boxes when node types have very different (and in Lambda's case,
 * data-dependent) dimensions.
 *
 * This is a pure-function test over the layout: no rendering, no React Flow
 * runtime — just geometry over the returned Node[] positions. The engine
 * itself is held to the objective on every fixture in
 * map-layout-engine.test.ts.
 */
import { describe, expect, it } from "vitest"
import type { Node } from "@xyflow/react"
import { buildLayoutNodes, NODE_WIDTH, NODE_HEIGHT, lambdaGroupHeight } from "./map-layout"
import { SQS_NODE_EXPANDED_H, LOGS_NODE_EXPANDED_H } from "./topology-nodes"
import type { TopologyEdge, TopologyNode } from "@/types"

interface Rect {
  id: string
  x: number
  y: number
  w: number
  h: number
}

/** Resolves every node's ABSOLUTE position by walking its parentId chain. */
function absoluteRects(nodes: Node[]): Rect[] {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  function resolve(n: Node): { x: number; y: number } {
    if (!n.parentId) return { x: n.position.x, y: n.position.y }
    const parent = byId.get(n.parentId)
    if (!parent) return { x: n.position.x, y: n.position.y }
    const p = resolve(parent)
    return { x: p.x + n.position.x, y: p.y + n.position.y }
  }
  return nodes.map((n) => {
    const { x, y } = resolve(n)
    return { id: n.id, x, y, w: n.width ?? 0, h: n.height ?? 0 }
  })
}

/** True if two axis-aligned rects overlap (touching edges is not an overlap). */
function overlaps(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

/**
 * Asserts no two rects in the same list overlap, EXCLUDING ancestor/descendant
 * pairs (a group container is expected to visually contain its children).
 */
function assertNoOverlaps(nodes: Node[]) {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  function isAncestor(maybeAncestorId: string, n: Node): boolean {
    let cur: Node | undefined = n
    while (cur?.parentId) {
      if (cur.parentId === maybeAncestorId) return true
      cur = byId.get(cur.parentId)
    }
    return false
  }

  const rects = absoluteRects(nodes)
  for (let i = 0; i < rects.length; i++) {
    for (let j = i + 1; j < rects.length; j++) {
      const a = rects[i]
      const b = rects[j]
      if (isAncestor(a.id, byId.get(b.id)!) || isAncestor(b.id, byId.get(a.id)!)) continue
      if (overlaps(a, b)) {
        throw new Error(
          `Nodes overlap: ${a.id} (${a.x},${a.y} ${a.w}x${a.h}) vs ${b.id} (${b.x},${b.y} ${b.w}x${b.h})`,
        )
      }
    }
  }
}

/** Builds the same per-type size overrides map-page.tsx computes for real data. */
function overridesFor(nodes: TopologyNode[], lambdaInstanceCounts: Record<string, number>) {
  const overrides: Record<string, { width: number; height: number }> = {}
  for (const n of nodes) {
    if (n.service === "lambda") {
      const count = lambdaInstanceCounts[n.label] ?? 0
      if (count > 0) overrides[n.id] = { width: NODE_WIDTH, height: lambdaGroupHeight(count) }
    } else if (
      n.service === "sqs" &&
      (n.approximateNumberOfMessages ?? 0) + (n.approximateNumberOfMessagesNotVisible ?? 0) > 0
    ) {
      overrides[n.id] = { width: NODE_WIDTH, height: SQS_NODE_EXPANDED_H }
    } else if (n.service === "logs") {
      overrides[n.id] = { width: NODE_WIDTH, height: LOGS_NODE_EXPANDED_H }
    }
  }
  return overrides
}

describe("buildLayoutNodes", () => {
  it("preserves ECS identity and health metadata for rendering and navigation", () => {
    const ecsService: TopologyNode = {
      id: "ecs-service",
      service: "ecs",
      label: "website",
      region: "ap-southeast-2",
      ecsResourceType: "service",
      clusterName: "demo",
      desiredCount: 2,
      runningCount: 1,
    }

    const result = buildLayoutNodes([ecsService])

    expect(result.find((node) => node.id === ecsService.id)?.data).toEqual(
      expect.objectContaining({
        ecsResourceType: "service",
        clusterName: "demo",
        desiredCount: 2,
        runningCount: 1,
      }),
    )
  })

  it("does not overlap nodes of mixed types with very different heights", () => {
    const region = "us-east-1"
    const nodes: TopologyNode[] = [
      // Several SQS queues, some with messages (tall, fixed max-height box).
      { id: "sqs-1", service: "sqs", label: "queue-one", region, approximateNumberOfMessages: 40 },
      {
        id: "sqs-2",
        service: "sqs",
        label: "queue-two",
        region,
        approximateNumberOfMessagesNotVisible: 5,
      },
      { id: "sqs-3", service: "sqs", label: "queue-three", region },
      // S3 buckets — plain default-sized nodes.
      { id: "s3-1", service: "s3", label: "bucket-one", region },
      { id: "s3-2", service: "s3", label: "bucket-two", region },
      // A VPC with an EC2 instance inside it (byVpc grouping).
      {
        id: "vpc-1",
        service: "vpc",
        label: "vpc-abc",
        region,
        cidrBlock: "10.0.0.0/16",
        subnetCount: 2,
      },
      { id: "ec2-1", service: "ec2", label: "i-abc123", region, vpcId: "vpc-1" },
      // Lambda functions with a wide spread of concurrent-instance counts —
      // one of these would previously grow unbounded tall.
      { id: "lambda-1", service: "lambda", label: "fn-quiet", region },
      { id: "lambda-2", service: "lambda", label: "fn-busy", region },
      { id: "lambda-3", service: "lambda", label: "fn-very-busy", region },
      // CloudWatch Logs group (also a tall, fixed max-height box).
      { id: "logs-1", service: "logs", label: "/aws/lambda/fn-busy", region },
    ]
    const edges: TopologyEdge[] = [
      { id: "e1", source: "lambda-2", target: "sqs-1", type: "invoke" },
      { id: "e2", source: "lambda-3", target: "s3-1", type: "invoke" },
    ]

    const lambdaInstanceCounts = {
      "fn-quiet": 1,
      "fn-busy": 4,
      // Far beyond LAMBDA_GROUP_MAX_VISIBLE — used to grow unbounded before capping.
      "fn-very-busy": 25,
    }
    const overrides = overridesFor(nodes, lambdaInstanceCounts)

    const result = buildLayoutNodes(nodes, edges, overrides, region)

    // Sanity: every leaf resource node made it into the result.
    for (const n of nodes) {
      expect(result.some((r) => r.id === n.id)).toBe(true)
    }

    assertNoOverlaps(result)
  })

  it("caps Lambda group height regardless of instance count", () => {
    // Height must stop growing once past LAMBDA_GROUP_MAX_VISIBLE rows —
    // otherwise a single busy function could grow tall enough to overlap
    // whatever dagre placed next to it.
    const capped = lambdaGroupHeight(4)
    expect(lambdaGroupHeight(25)).toBe(capped)
    expect(lambdaGroupHeight(1000)).toBe(capped)
    // ...but still shrinks for the (much more common) low-concurrency case.
    expect(lambdaGroupHeight(1)).toBeLessThan(capped)
  })

  it("still fits plain default-sized nodes without an override", () => {
    // Guards the NODE_WIDTH/NODE_HEIGHT re-export used by this test file.
    expect(NODE_WIDTH).toBeGreaterThan(0)
    expect(NODE_HEIGHT).toBeGreaterThan(0)
  })
})

// ─── Component packing and edge routing ─────────────────────────────────────

import { buildLayout, absoluteLeafRects, segmentsCross } from "./map-layout"
import { ObstacleIndex, polylineClear } from "./map-edge-routing"

/** Every edge's polyline (handle → route → handle) must clear every other node. */
function assertRoutesClear(layout: ReturnType<typeof buildLayout>, edges: TopologyEdge[]) {
  const rects = absoluteLeafRects(layout.nodes)
  const index = new ObstacleIndex([...rects].map(([id, r]) => ({ id, ...r })))
  for (const e of edges) {
    const s = rects.get(e.source)
    const t = rects.get(e.target)
    if (!s || !t) continue
    const poly = [
      { x: s.x + s.w, y: s.y + s.h / 2 },
      ...(layout.routes[e.id] ?? []),
      { x: t.x, y: t.y + t.h / 2 },
    ]
    if (!polylineClear(poly, index, e.source, e.target)) {
      throw new Error(`Edge ${e.id} cuts through a node it is not attached to`)
    }
  }
}

describe("buildLayout packing", () => {
  const region = "us-east-1"
  const n = (id: string, service: string, extra: Partial<TopologyNode> = {}): TopologyNode => ({
    id,
    service,
    label: id,
    region,
    ...extra,
  })

  it("tiles unconnected resources in rows grouped by service instead of one column", () => {
    const nodes: TopologyNode[] = []
    for (let i = 0; i < 6; i++) nodes.push(n(`bucket-${i}`, "s3"))
    for (let i = 0; i < 6; i++) nodes.push(n(`queue-${i}`, "sqs"))
    // Interleave so input order cannot be what groups them.
    nodes.sort((a, b) => a.id.localeCompare(b.id))

    const rects = absoluteLeafRects(buildLayout(nodes, [], {}, region).nodes)
    const xs = new Set([...rects.values()].map((r) => r.x))
    const ys = new Set([...rects.values()].map((r) => r.y))
    expect(xs.size).toBeGreaterThan(1) // more than one column
    expect(ys.size).toBeLessThan(nodes.length) // fewer rows than nodes

    // Reading order (row-major) keeps every bucket before every queue.
    const order = [...rects.entries()].sort((a, b) => a[1].y - b[1].y || a[1].x - b[1].x)
    const services = order.map(([id]) => (id.startsWith("bucket") ? "s3" : "sqs"))
    const firstQueue = services.indexOf("sqs")
    expect(services.slice(firstQueue).every((s) => s === "sqs")).toBe(true)
  })

  it("keeps a connected flow together and above the loose resources", () => {
    const nodes: TopologyNode[] = [
      n("api", "apigateway"),
      n("fn", "lambda"),
      n("table", "dynamodb"),
      n("logs", "logs"),
      ...Array.from({ length: 8 }, (_, i) => n(`bucket-${i}`, "s3")),
    ]
    const edges: TopologyEdge[] = [
      { id: "e1", source: "api", target: "fn", type: "apigw-integration" },
      { id: "e2", source: "fn", target: "table", type: "esm" },
      { id: "e3", source: "fn", target: "logs", type: "logs" },
    ]
    const layout = buildLayout(nodes, edges, overridesFor(nodes, {}), region)
    const rects = absoluteLeafRects(layout.nodes)
    const flowBottom = Math.max(
      ...["api", "fn", "table", "logs"].map((id) => rects.get(id)!.y + rects.get(id)!.h),
    )
    for (let i = 0; i < 8; i++) {
      expect(rects.get(`bucket-${i}`)!.y).toBeGreaterThanOrEqual(flowBottom)
    }
    // The flow reads left to right.
    expect(rects.get("api")!.x).toBeLessThan(rects.get("fn")!.x)
    expect(rects.get("fn")!.x).toBeLessThan(rects.get("table")!.x)
    assertNoOverlaps(layout.nodes)
    assertRoutesClear(layout, edges)
  })

  it("is deterministic for the same input", () => {
    const nodes = [n("a", "s3"), n("b", "sqs"), n("c", "lambda"), n("d", "logs"), n("e", "sns")]
    const edges: TopologyEdge[] = [
      { id: "e1", source: "a", target: "b", type: "notification" },
      { id: "e2", source: "b", target: "c", type: "esm" },
      { id: "e3", source: "c", target: "d", type: "logs" },
    ]
    const one = buildLayout(nodes, edges, {}, region)
    const two = buildLayout([...nodes].reverse(), [...edges].reverse(), {}, region)
    expect(absoluteLeafRects(two.nodes)).toEqual(absoluteLeafRects(one.nodes))
    expect(two.routes).toEqual(one.routes)
  })

  it("routes an edge that skips a rank around the node in between", () => {
    // a → b → c and a → c: the long edge must not pass through b.
    const nodes = [n("a", "s3"), n("b", "sqs"), n("c", "lambda")]
    const edges: TopologyEdge[] = [
      { id: "ab", source: "a", target: "b", type: "notification" },
      { id: "bc", source: "b", target: "c", type: "esm" },
      { id: "ac", source: "a", target: "c", type: "notification" },
    ]
    const layout = buildLayout(nodes, edges, {}, region)
    assertRoutesClear(layout, edges)
  })

  it("routes edges between two stacks around the nodes inside them", () => {
    // Stack A: a1 → a2 → a3 (a3 is the last rank). Stack B: b1 → b2.
    // Cross-stack edge a1 → b2 leaves from the FIRST rank of A, so the
    // straight line to B's box would cut through a2 and a3.
    const nodes: TopologyNode[] = [
      n("a1", "s3", { stackName: "A" }),
      n("a2", "sqs", { stackName: "A" }),
      n("a3", "lambda", { stackName: "A" }),
      n("b1", "sns", { stackName: "B" }),
      n("b2", "sqs", { stackName: "B" }),
    ]
    const edges: TopologyEdge[] = [
      { id: "a12", source: "a1", target: "a2", type: "notification" },
      { id: "a23", source: "a2", target: "a3", type: "esm" },
      { id: "b12", source: "b1", target: "b2", type: "subscription" },
      { id: "x", source: "a1", target: "b2", type: "notification" },
    ]
    const layout = buildLayout(nodes, edges, {}, region)
    assertNoOverlaps(layout.nodes)
    assertRoutesClear(layout, edges)
  })

  it("lays out a few hundred resources in well under a second", () => {
    const nodes: TopologyNode[] = []
    const edges: TopologyEdge[] = []
    const services = ["s3", "sqs", "lambda", "dynamodb", "sns", "logs"]
    for (let i = 0; i < 300; i++) {
      nodes.push(n(`r${i}`, services[i % services.length], i % 3 === 0 ? { stackName: `stack-${i % 7}` } : {}))
    }
    // A dozen chains of varying length plus some cross links.
    for (let i = 0; i < 240; i++) {
      if (i % 20 !== 19) edges.push({ id: `c${i}`, source: `r${i}`, target: `r${i + 1}`, type: "esm" })
      if (i % 9 === 0) edges.push({ id: `x${i}`, source: `r${i}`, target: `r${i + 5}`, type: "logs" })
    }
    const start = performance.now()
    const layout = buildLayout(nodes, edges, overridesFor(nodes, {}), region)
    const ms = performance.now() - start
    expect(layout.nodes.length).toBeGreaterThan(300)
    assertNoOverlaps(layout.nodes)
    assertRoutesClear(layout, edges)
    expect(ms).toBeLessThan(1000)
  })
})

describe("adopting derived resources into a stack", () => {
  const region = "us-east-1"
  it("puts a function's log group and stream filter inside the function's stack box", () => {
    const nodes: TopologyNode[] = [
      { id: "table", service: "dynamodb", label: "orders", region, stackName: "app" },
      { id: "filter", service: "esm-filter", label: "filter", region },
      { id: "fn", service: "lambda", label: "audit", region, stackName: "app" },
      { id: "logs", service: "logs", label: "/aws/lambda/audit", region },
      // A log group shared by functions in two stacks stays outside both.
      { id: "fn2", service: "lambda", label: "other", region, stackName: "other" },
      { id: "shared", service: "logs", label: "/shared", region },
      // An unconnected log group has nothing to adopt from.
      { id: "loose", service: "logs", label: "/loose", region },
    ]
    const edges: TopologyEdge[] = [
      { id: "in", source: "table", target: "filter", type: "esm-filter" },
      { id: "out", source: "filter", target: "fn", type: "esm" },
      { id: "l1", source: "fn", target: "logs", type: "logs" },
      { id: "l2", source: "fn", target: "shared", type: "logs" },
      { id: "l3", source: "fn2", target: "shared", type: "logs" },
    ]
    const { nodes: laid } = buildLayout(nodes, edges, {}, region)
    const parentOf = (id: string) => laid.find((n) => n.id === id)?.parentId
    expect(parentOf("filter")).toBe(`stack::${region}::app`)
    expect(parentOf("logs")).toBe(`stack::${region}::app`)
    expect(parentOf("shared")).toBe(`region::${region}`)
    expect(parentOf("loose")).toBe(`region::${region}`)
    // The log group sits in the rank after its function, on the same shelf.
    const rects = absoluteLeafRects(laid)
    expect(rects.get("logs")!.x).toBeGreaterThan(rects.get("fn")!.x)
    expect(Math.abs(rects.get("logs")!.y - rects.get("fn")!.y)).toBeLessThan(200)
  })
})

describe("ordering", () => {
  const region = "us-east-1"
  it("leads with a self-contained stack box, then the loose resources", () => {
    const nodes: TopologyNode[] = [
      { id: "a", service: "s3", label: "a", region, stackName: "app" },
      { id: "b", service: "sqs", label: "b", region, stackName: "app" },
      { id: "bucket", service: "s3", label: "bucket", region },
      { id: "zebra", service: "sqs", label: "zebra", region },
    ]
    const edges: TopologyEdge[] = [{ id: "e", source: "a", target: "b", type: "notification" }]
    const rects = absoluteLeafRects(buildLayout(nodes, edges, {}, region).nodes)
    const stackBottom = Math.max(rects.get("a")!.y + rects.get("a")!.h, rects.get("b")!.y + rects.get("b")!.h)
    expect(rects.get("bucket")!.y).toBeGreaterThanOrEqual(stackBottom)
    expect(rects.get("zebra")!.y).toBeGreaterThanOrEqual(stackBottom)
  })

  it("puts the active region first", () => {
    const nodes: TopologyNode[] = [
      { id: "eu", service: "s3", label: "eu", region: "eu-west-1" },
      { id: "us", service: "s3", label: "us", region: "us-east-1" },
    ]
    const rects = absoluteLeafRects(buildLayout(nodes, [], {}, "us-east-1").nodes)
    expect(rects.get("us")!.y).toBeLessThan(rects.get("eu")!.y)
  })
})

describe("backward edges", () => {
  const region = "us-east-1"
  it("loops a back edge around the cards instead of behind them", () => {
    // a → b and b → a: dagre reverses one, leaving its target left of its source.
    const nodes: TopologyNode[] = [
      { id: "a", service: "sqs", label: "a", region },
      { id: "b", service: "lambda", label: "b", region },
    ]
    const edges: TopologyEdge[] = [
      { id: "fwd", source: "a", target: "b", type: "esm" },
      { id: "back", source: "b", target: "a", type: "notification" },
    ]
    const layout = buildLayout(nodes, edges, {}, region)
    const rects = absoluteLeafRects(layout.nodes)
    const left = rects.get("a")!.x < rects.get("b")!.x ? "a" : "b"
    const backId = left === "a" ? "back" : "fwd"
    const route = layout.routes[backId]
    expect(route.length).toBeGreaterThanOrEqual(2)
    const top = Math.min(rects.get("a")!.y, rects.get("b")!.y)
    const bottom = Math.max(rects.get("a")!.y + rects.get("a")!.h, rects.get("b")!.y + rects.get("b")!.h)
    // The lane runs entirely above or entirely below both cards.
    const laneYs = route.map((p) => p.y)
    expect(laneYs.every((y) => y < top) || laneYs.every((y) => y > bottom)).toBe(true)
    assertRoutesClear(layout, edges)
  })
})

describe("untangling", () => {
  const region = "us-east-1"
  /** Proper crossings between the straight legs of every pair of unrelated edges. */
  function countCrossings(layout: ReturnType<typeof buildLayout>, edges: TopologyEdge[]) {
    const rects = absoluteLeafRects(layout.nodes)
    const poly = edges.map((e) => {
      const s = rects.get(e.source)!
      const t = rects.get(e.target)!
      return [
        { x: s.x + s.w, y: s.y + s.h / 2 },
        ...(layout.routes[e.id] ?? []),
        { x: t.x, y: t.y + t.h / 2 },
      ]
    })
    let n = 0
    for (let a = 0; a < poly.length; a++) {
      for (let b = a + 1; b < poly.length; b++) {
        const ea = edges[a]
        const eb = edges[b]
        if ([ea.source, ea.target].some((x) => x === eb.source || x === eb.target)) continue
        for (let i = 0; i + 1 < poly[a].length; i++) {
          for (let j = 0; j + 1 < poly[b].length; j++) {
            if (segmentsCross(poly[a][i], poly[a][i + 1], poly[b][j], poly[b][j + 1])) n++
          }
        }
      }
    }
    return n
  }

  it("finds the crossing-free order for the orders flow regardless of input order", () => {
    // The node and edge order the topology API returned for this flow: the
    // order from which a single dagre run crosses the API edge over the DLQ edge.
    const nodes: TopologyNode[] = [
      { id: "s3", service: "s3", label: "orders-uploads", region, stackName: "app" },
      { id: "q", service: "sqs", label: "orders-ingest", region, stackName: "app" },
      { id: "dlq", service: "sqs", label: "orders-ingest-dlq", region, stackName: "app" },
      { id: "fn", service: "lambda", label: "process-orders", region, stackName: "app" },
      { id: "logs", service: "logs", label: "/aws/lambda/process-orders", region },
      { id: "api", service: "apigateway", label: "orders-api", region, stackName: "app" },
    ]
    const edges: TopologyEdge[] = [
      { id: "notif", source: "s3", target: "q", type: "notification" },
      { id: "dlq", source: "q", target: "dlq", type: "dlq" },
      { id: "logs", source: "fn", target: "logs", type: "logs" },
      { id: "esm", source: "q", target: "fn", type: "esm" },
      { id: "apigw", source: "api", target: "fn", type: "apigw-integration" },
    ]
    const sizes = { fn: { width: NODE_WIDTH, height: lambdaGroupHeight(3) } }
    for (const order of [nodes, [...nodes].reverse()]) {
      const layout = buildLayout(order, edges, sizes, region)
      expect(countCrossings(layout, edges)).toBe(0)
    }
  })
})
