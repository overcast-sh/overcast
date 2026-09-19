/**
 * Layout fixtures: the topologies the layout engine is measured on.
 *
 * A fixture is a `TopologyResponse` plus an optional `sizes` map and
 * `collapsed` list (see generate.ts). `orders-app` was captured from
 * `GET /_overcast/topology` and lives beside this file as JSON; the rest are
 * generated on demand by seeded generators in generate.ts — deterministic, so
 * nothing is gained by committing their JSON (the 1,000-node one alone is
 * 18,000 lines). Loading a fixture also derives the per-service card sizes
 * the map page would pass (`fixtureSizes`), so a fixture only spells out the
 * sizes that depend on runtime state, such as a Lambda group's instance count.
 */

import { readFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"
import type { TopologyEdge, TopologyNode } from "@/types"
import {
  ESM_FILTER_NODE_SIZE,
  IGW_NODE_HEIGHT,
  IGW_NODE_WIDTH,
  NODE_WIDTH,
  VPC_NODE_HEIGHT,
} from "../map-layout-constants"
import { LOGS_NODE_EXPANDED_H, SQS_NODE_EXPANDED_H, SQS_NODE_IDLE_H, STATUS_NODE_H } from "../topology-nodes"
import { GENERATED_FIXTURES, type FixtureFile } from "./generate"

export const FIXTURE_NAMES = [
  "orders-app",
  "inventory-only",
  "chain-fanout",
  "dense-dag",
  "cycles",
  "two-stacks-cross",
  "nested-collapsed",
  "vpc-members",
  "multi-region",
  "mixed-150",
  "mixed-300",
  "mixed-600",
  "mixed-1000",
] as const
export type FixtureName = (typeof FIXTURE_NAMES)[number]

export interface LayoutFixture {
  name: FixtureName
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  sizes: Record<string, { width: number; height: number }>
  collapsed: Set<string>
  /** The first listed region plays the active one, as the map page would pass it. */
  activeRegion: string
}

/**
 * The size overrides map-page.tsx derives from a node's own fields — the
 * same rules, minus the Lambda instance list it gets from elsewhere.
 */
export function fixtureSizes(nodes: TopologyNode[]): Record<string, { width: number; height: number }> {
  const sizes: Record<string, { width: number; height: number }> = {}
  for (const n of nodes) {
    if (n.service === "sqs") {
      const busy = (n.approximateNumberOfMessages ?? 0) + (n.approximateNumberOfMessagesNotVisible ?? 0) > 0
      sizes[n.id] = { width: NODE_WIDTH, height: busy ? SQS_NODE_EXPANDED_H : SQS_NODE_IDLE_H }
    } else if (n.service === "rds" && n.status) {
      sizes[n.id] = { width: NODE_WIDTH, height: STATUS_NODE_H }
    } else if (n.service === "logs") {
      sizes[n.id] = { width: NODE_WIDTH, height: LOGS_NODE_EXPANDED_H }
    } else if (n.service === "esm-filter") {
      sizes[n.id] = { width: ESM_FILTER_NODE_SIZE, height: ESM_FILTER_NODE_SIZE }
    } else if (n.service === "igw") {
      sizes[n.id] = { width: IGW_NODE_WIDTH, height: IGW_NODE_HEIGHT }
    } else if (n.service === "vpc") {
      sizes[n.id] = { width: NODE_WIDTH, height: VPC_NODE_HEIGHT }
    }
  }
  return sizes
}

// Resolved through fileURLToPath rather than `new URL(…, import.meta.url)`,
// which Vite would rewrite into an asset import.
const FIXTURE_DIR = path.dirname(fileURLToPath(import.meta.url))

function fixturePath(name: FixtureName): string {
  return path.join(FIXTURE_DIR, `${name}.topology.json`)
}

function readFixtureFile(name: FixtureName): FixtureFile {
  const generate = GENERATED_FIXTURES[name]
  if (generate) return generate()
  return JSON.parse(readFileSync(fixturePath(name), "utf8")) as FixtureFile
}

export function loadFixture(name: FixtureName): LayoutFixture {
  const file = readFixtureFile(name)
  return {
    name,
    nodes: file.nodes,
    edges: file.edges,
    sizes: { ...fixtureSizes(file.nodes), ...file.sizes },
    collapsed: new Set(file.collapsed ?? []),
    activeRegion: file.regions[0],
  }
}
