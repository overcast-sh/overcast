/**
 * Seeded topology generator for the layout fixtures.
 *
 * Every fixture in docs/plans/map-layout-objective.md §4 other than the
 * captured `orders-app` comes from here, generated on demand when a fixture
 * is loaded. The generator is deterministic — a fixed seed per fixture, a
 * tiny PRNG, no Math.random — so the same topology comes back on every run
 * and machine, and the set can grow without anyone hand-writing (or
 * committing) a thousand-node topology.
 *
 * Ids follow the API's conventions (`<region>::<service>::<name>`,
 * `<type>::<source>→<target>` for edges, `stack::<region>::<name>` for
 * nested-stack edges) so the layout treats generated and captured data alike.
 */

import type { TopologyEdge, TopologyNode } from "@/types"
import { lambdaGroupHeight, NODE_WIDTH } from "../map-layout"
import { seededRandom } from "../map-layout-random"

/** A fixture file: the topology plus what the map page would pass alongside it. */
export interface FixtureFile {
  regions: string[]
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  /** Per-node size overrides the map page could not derive from the node alone (Lambda groups). */
  sizes?: Record<string, { width: number; height: number }>
  /** Stack phantom ids (`stack::<region>::<name>`) rendered as collapsed chips. */
  collapsed?: string[]
}

class Builder {
  readonly nodes: TopologyNode[] = []
  readonly edges: TopologyEdge[] = []
  readonly sizes: Record<string, { width: number; height: number }> = {}
  readonly collapsed: string[] = []
  readonly regions: string[] = []
  private readonly ids = new Set<string>()
  private readonly edgeKeys = new Set<string>()
  readonly random: () => number

  constructor(seed: number) {
    this.random = seededRandom(seed)
  }

  /** Random integer in [0, n). */
  int(n: number): number {
    return Math.floor(this.random() * n)
  }

  pick<T>(list: readonly T[]): T {
    return list[this.int(list.length)]
  }

  node(
    region: string,
    service: string,
    name: string,
    extra: Partial<TopologyNode> = {},
  ): TopologyNode {
    const id = `${region}::${service}::${name}`
    if (this.ids.has(id)) throw new Error(`duplicate node ${id}`)
    this.ids.add(id)
    if (!this.regions.includes(region)) this.regions.push(region)
    const n: TopologyNode = { id, service, label: name, region, ...extra }
    this.nodes.push(n)
    return n
  }

  /** Adds an edge unless one of that type already joins the pair (or it would be a self loop). */
  edge(source: TopologyNode, target: TopologyNode, type: string, label?: string): boolean {
    if (source.id === target.id) return false
    const key = `${type}::${source.id}→${target.id}`
    if (this.edgeKeys.has(key)) return false
    this.edgeKeys.add(key)
    const e: TopologyEdge = {
      id: key,
      source: source.id,
      target: target.id,
      type,
      sourceRegion: source.region,
      targetRegion: target.region,
    }
    if (label) e.label = label
    this.edges.push(e)
    return true
  }

  nestedStack(region: string, parent: string, child: string): void {
    const src = `stack::${region}::${parent}`
    const tgt = `stack::${region}::${child}`
    this.edges.push({
      id: `nested-stack::${src}→${tgt}`,
      source: src,
      target: tgt,
      type: "nested-stack",
      sourceRegion: region,
      targetRegion: region,
    })
  }

  lambdaInstances(fn: TopologyNode, count: number): void {
    this.sizes[fn.id] = { width: NODE_WIDTH, height: lambdaGroupHeight(count) }
  }

  build(): FixtureFile {
    const out: FixtureFile = { regions: this.regions, nodes: this.nodes, edges: this.edges }
    if (Object.keys(this.sizes).length > 0) out.sizes = this.sizes
    if (this.collapsed.length > 0) out.collapsed = this.collapsed
    return out
  }
}

const REGION = "us-east-1"

/** The edge type a connection between two services would carry on the real map. */
function edgeType(from: string, to: string): string {
  if (from === "s3") return "notification"
  if (from === "sns") return "subscription"
  if (from === "sqs" && to === "sqs") return "dlq"
  if (from === "sqs" || from === "dynamodb" || from === "kinesis") return "esm"
  if (from === "apigateway") return "apigw-integration"
  if (from === "lambda" && to === "logs") return "logs"
  if (from === "lambda") return "invoke"
  if (from === "eventbridge") return "rule-target"
  return "reference"
}

const FLOW_SERVICES = ["apigateway", "lambda", "sqs", "sns", "dynamodb", "s3", "eventbridge", "kinesis"]
const INVENTORY_SERVICES = ["s3", "sqs", "dynamodb", "sns", "logs", "secretsmanager", "ssm", "kms"]

/** A queue with messages waiting is drawn taller; ~one in four in a busy account. */
function queue(b: Builder, region: string, name: string, extra: Partial<TopologyNode> = {}) {
  const busy = b.random() < 0.25
  return b.node(region, "sqs", name, {
    approximateNumberOfMessages: busy ? 1 + b.int(40) : 0,
    approximateNumberOfMessagesNotVisible: busy ? b.int(5) : 0,
    ...extra,
  })
}

// ─── Small, hand-shaped fixtures ─────────────────────────────────────────────

function inventoryOnly(): FixtureFile {
  const b = new Builder(101)
  for (let i = 0; i < 5; i++) {
    b.node(REGION, "s3", `bucket-${i}`)
    queue(b, REGION, `queue-${i}`)
    b.node(REGION, "dynamodb", `table-${i}`, { streamEnabled: i % 2 === 0 })
    b.node(REGION, "logs", `/aws/lambda/fn-${i}`)
    b.node(REGION, "sns", `topic-${i}`)
    if (i < 4) b.node(REGION, "rds", `db-${i}`, { status: "available" })
  }
  b.node(REGION, "vpc", "vpc-0a1b2c3d", { cidrBlock: "10.0.0.0/16", subnetCount: 3 })
  return b.build()
}

function chainFanout(): FixtureFile {
  const b = new Builder(102)
  const chain: TopologyNode[] = []
  const services = ["apigateway", "lambda", "sqs", "lambda", "sns", "sqs", "lambda", "dynamodb", "lambda", "sqs", "lambda", "logs"]
  for (let i = 0; i < 12; i++) {
    const s = services[i]
    chain.push(s === "sqs" ? queue(b, REGION, `chain-${i}`) : b.node(REGION, s, `chain-${i}`))
    if (i > 0) b.edge(chain[i - 1], chain[i], edgeType(services[i - 1], s))
  }
  // Five-way fan-out from the topic in the middle of the chain.
  for (let i = 0; i < 5; i++) {
    const q = queue(b, REGION, `fan-out-${i}`)
    b.edge(chain[4], q, "subscription")
  }
  // Three-way fan-in to the function near the end.
  for (let i = 0; i < 3; i++) {
    const src = b.node(REGION, "s3", `fan-in-${i}`)
    b.edge(src, chain[8], "notification")
  }
  return b.build()
}

function denseDag(): FixtureFile {
  const b = new Builder(103)
  const nodes: TopologyNode[] = []
  for (let i = 0; i < 24; i++) {
    const s = FLOW_SERVICES[i % FLOW_SERVICES.length]
    nodes.push(s === "sqs" ? queue(b, REGION, `dag-${i}`) : b.node(REGION, s, `dag-${i}`))
  }
  // Every node has a path from the first, then random forward edges to 44.
  for (let i = 1; i < 24; i++) b.edge(nodes[b.int(i)], nodes[i], "reference")
  let guard = 0
  while (b.edges.length < 44 && guard++ < 10_000) {
    const i = b.int(23)
    const j = i + 1 + b.int(Math.min(6, 23 - i))
    b.edge(nodes[i], nodes[j], edgeType(nodes[i].service, nodes[j].service))
  }
  return b.build()
}

function cycles(): FixtureFile {
  const b = new Builder(104)
  const hub = b.node(REGION, "lambda", "hub")
  const a = queue(b, REGION, "loop2")
  b.edge(hub, a, "invoke")
  b.edge(a, hub, "esm")
  const b1 = b.node(REGION, "sns", "loop3-a")
  const b2 = queue(b, REGION, "loop3-b")
  b.edge(hub, b1, "invoke")
  b.edge(b1, b2, "subscription")
  b.edge(b2, hub, "esm")
  const c = [
    b.node(REGION, "dynamodb", "loop5-a", { streamEnabled: true }),
    b.node(REGION, "lambda", "loop5-b"),
    queue(b, REGION, "loop5-c"),
    b.node(REGION, "lambda", "loop5-d"),
  ]
  b.edge(hub, c[0], "invoke")
  b.edge(c[0], c[1], "esm")
  b.edge(c[1], c[2], "invoke")
  b.edge(c[2], c[3], "esm")
  b.edge(c[3], hub, "invoke")
  return b.build()
}

/** api → fn → queue → fn → table inside one stack. */
function stackFlow(b: Builder, region: string, stackName: string): TopologyNode[] {
  const api = b.node(region, "apigateway", `${stackName}-api`, { stackName, protocolType: "REST" })
  const fn1 = b.node(region, "lambda", `${stackName}-ingest`, { stackName })
  const q = queue(b, region, `${stackName}-jobs`, { stackName })
  const fn2 = b.node(region, "lambda", `${stackName}-worker`, { stackName })
  const table = b.node(region, "dynamodb", `${stackName}-state`, { stackName })
  b.edge(api, fn1, "apigw-integration")
  b.edge(fn1, q, "invoke")
  b.edge(q, fn2, "esm")
  b.edge(fn2, table, "invoke")
  return [api, fn1, q, fn2, table]
}

function twoStacksCross(): FixtureFile {
  const b = new Builder(105)
  const a = stackFlow(b, REGION, "orders")
  const c = stackFlow(b, REGION, "billing")
  b.edge(a[1], c[2], "invoke")
  b.edge(a[3], c[4], "invoke")
  b.edge(c[3], a[2], "invoke")
  b.edge(a[4], c[1], "esm")
  const shared = b.node(REGION, "logs", "/aws/lambda/shared")
  b.edge(a[3], shared, "logs")
  b.edge(c[3], shared, "logs")
  return b.build()
}

function nestedCollapsed(): FixtureFile {
  const b = new Builder(106)
  const root = stackFlow(b, REGION, "platform")
  const child = stackFlow(b, REGION, "platform-network")
  const grand = stackFlow(b, REGION, "platform-network-edge")
  b.nestedStack(REGION, "platform", "platform-network")
  b.nestedStack(REGION, "platform-network", "platform-network-edge")
  b.edge(root[3], child[0], "invoke")
  b.edge(child[3], grand[0], "invoke")
  b.edge(root[1], grand[2], "invoke")
  b.edge(grand[4], root[4], "reference")
  b.collapsed.push(`stack::${REGION}::platform-network-edge`)
  return b.build()
}

function vpcMembers(): FixtureFile {
  const b = new Builder(107)
  for (const [i, vpcId] of ["vpc-0aaa1111", "vpc-0bbb2222"].entries()) {
    const vpc = b.node(REGION, "vpc", vpcId, { cidrBlock: `10.${i}.0.0/16`, subnetCount: 4, hasInternetGateway: true })
    const igw = b.node(REGION, "igw", `igw-${i}f00d`, { attachedVpcId: vpcId })
    b.edge(igw, vpc, "vpc-attachment")
    for (let k = 0; k < 3; k++) {
      const ec2 = b.node(REGION, "ec2", `i-${i}${k}abc`, { vpcId })
      b.edge(vpc, ec2, "vpc-member")
    }
    const rds = b.node(REGION, "rds", `db-${i}`, { vpcId, status: "available" })
    b.edge(vpc, rds, "vpc-member")
  }
  b.node(REGION, "s3", "assets")
  return b.build()
}

function multiRegion(): FixtureFile {
  const b = new Builder(108)
  const regions = ["eu-west-1", "us-east-1", "ap-southeast-2"]
  const flows = regions.map((r) => stackFlow(b, r, `${r}-app`))
  for (const r of regions) {
    b.node(r, "s3", `${r}-assets`)
    queue(b, r, `${r}-overflow`)
    b.node(r, "sns", `${r}-alerts`)
  }
  // Eight cross-region edges: fan-out topics and replicated tables.
  const pairs: Array<[number, number, number, number, string]> = [
    [0, 3, 1, 2, "invoke"],
    [1, 3, 2, 2, "invoke"],
    [2, 3, 0, 2, "invoke"],
    [0, 4, 1, 4, "reference"],
    [1, 4, 2, 4, "reference"],
    [2, 1, 0, 1, "invoke"],
    [0, 1, 2, 4, "invoke"],
    [1, 1, 0, 4, "invoke"],
  ]
  for (const [sr, si, tr, ti, type] of pairs) b.edge(flows[sr][si], flows[tr][ti], type)
  return b.build()
}

// ─── Mixed, scaled fixtures ──────────────────────────────────────────────────

/**
 * A believable large account: most resources in stacks with a real flow in
 * each, a slice of loose inventory, a second smaller region, some cross-stack
 * and cross-region wiring, and a few cycles.
 */
function mixed(seed: number, nodeCount: number, edgeCount: number): FixtureFile {
  const b = new Builder(seed)
  const regions = ["us-east-1", "eu-west-1"]
  const looseCount = Math.round(nodeCount * 0.27)
  const stackCount = Math.max(6, Math.round(nodeCount / 25))
  const stacked = nodeCount - looseCount
  const perStack = Math.floor(stacked / stackCount)
  const stacks: TopologyNode[][] = []
  let made = 0
  for (let s = 0; s < stackCount; s++) {
    const region = s % 5 === 4 ? regions[1] : regions[0]
    const stackName = `stack-${s}`
    const members: TopologyNode[] = []
    const count = s === stackCount - 1 ? stacked - made : perStack
    for (let i = 0; i < count; i++) {
      const service = FLOW_SERVICES[b.int(FLOW_SERVICES.length)]
      const name = `${stackName}-${service}-${i}`
      const n = service === "sqs" ? queue(b, region, name, { stackName }) : b.node(region, service, name, { stackName })
      if (service === "lambda" && b.random() < 0.15) b.lambdaInstances(n, 1 + b.int(3))
      members.push(n)
    }
    made += count
    stacks.push(members)
    // A flow through the stack: every member reachable from an earlier one.
    for (let i = 1; i < members.length; i++) {
      const src = members[Math.max(0, i - 1 - b.int(3))]
      b.edge(src, members[i], edgeType(src.service, members[i].service))
    }
  }
  const loose: TopologyNode[] = []
  for (let i = 0; i < looseCount; i++) {
    const region = i % 5 === 4 ? regions[1] : regions[0]
    const service = INVENTORY_SERVICES[b.int(INVENTORY_SERVICES.length)]
    const name = `loose-${service}-${i}`
    loose.push(service === "sqs" ? queue(b, region, name) : b.node(region, service, name))
  }
  // Loose log groups and tables hang off functions in the stacks.
  for (const n of loose) {
    if (n.service === "logs" && b.random() < 0.7) {
      const stack = b.pick(stacks)
      const fn = stack.find((m) => m.service === "lambda" && m.region === n.region)
      if (fn) b.edge(fn, n, "logs")
    }
  }
  let guard = 0
  while (b.edges.length < edgeCount && guard++ < 100_000) {
    const roll = b.random()
    if (roll < 0.8) {
      // Extra wiring inside one stack, forward so the flow stays a DAG.
      const stack = b.pick(stacks)
      if (stack.length < 3) continue
      const i = b.int(stack.length - 1)
      const j = i + 1 + b.int(Math.min(4, stack.length - 1 - i))
      b.edge(stack[i], stack[j], edgeType(stack[i].service, stack[j].service))
    } else if (roll < 0.94) {
      const from = b.pick(b.pick(stacks))
      const to = b.pick(b.pick(stacks))
      if (from.region === to.region) b.edge(from, to, edgeType(from.service, to.service))
    } else if (roll < 0.97) {
      // A cycle: an edge back to an earlier member.
      const stack = b.pick(stacks)
      if (stack.length < 3) continue
      const j = 1 + b.int(stack.length - 1)
      b.edge(stack[j], stack[b.int(j)], "invoke")
    } else {
      const from = b.pick(b.pick(stacks))
      const to = b.pick(b.pick(stacks))
      if (from.region !== to.region) b.edge(from, to, "invoke")
    }
  }
  return b.build()
}

export const GENERATED_FIXTURES: Record<string, () => FixtureFile> = {
  "inventory-only": inventoryOnly,
  "chain-fanout": chainFanout,
  "dense-dag": denseDag,
  cycles,
  "two-stacks-cross": twoStacksCross,
  "nested-collapsed": nestedCollapsed,
  "vpc-members": vpcMembers,
  "multi-region": multiRegion,
  "mixed-150": () => mixed(150, 150, 200),
  "mixed-300": () => mixed(300, 300, 400),
  "mixed-600": () => mixed(600, 600, 800),
  "mixed-1000": () => mixed(1000, 1000, 1400),
}
