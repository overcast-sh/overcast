/**
 * web-function-layout — what a Lambda Web Function node lists, and the height
 * that takes. The layout reserves exactly the height the node then renders, so
 * both map-page.tsx (sizing) and the node (rendering) read it from here — and,
 * like data-lake-layout.ts, it is plain data, importable by the layout worker.
 */

import type { TopologyWebEndpoint } from "@/types"
import { DATA_MORE_H, DATA_NODE_HEADER_H } from "./data-lake-layout"
import type { GhostRow } from "./data-lake-overlay"

/** One endpoint: its name and badges on a line, what it serves on the next. */
export const ENDPOINT_ROW_H = 40
/** Space under the last row. */
const NODE_PAD_B = 6
/** Endpoint rows a node shows before the rest go to "+N more". */
export const MAX_ENDPOINT_ROWS = 4

/** The line under an endpoint's name: the revisions it serves, with their weights. */
export function servesText(serves: TopologyWebEndpoint["serves"]): string {
  return serves.length === 0
    ? "no revision"
    : serves.map((s) => `${s.revisionId} ${s.weight}%`).join(" · ")
}

/** An endpoint as the node lists it: live, or a ghost of one just deleted. */
export interface DisplayEndpoint {
  name: string
  endpoint?: TopologyWebEndpoint
  ghost?: GhostRow
}

export interface EndpointList {
  items: DisplayEndpoint[]
  /** Endpoints left off the node. */
  hidden: number
}

/**
 * The node's list: its live endpoints and the ghosts of any just deleted (a
 * ghost the topology still lists live is not a ghost), by name, cut at
 * `MAX_ENDPOINT_ROWS`.
 */
export function endpointList(
  live: readonly TopologyWebEndpoint[],
  ghosts: readonly GhostRow[] = [],
): EndpointList {
  const rows: DisplayEndpoint[] = live.map((endpoint) => ({ name: endpoint.name, endpoint }))
  const liveNames = new Set(rows.map((r) => r.name))
  for (const g of ghosts) {
    if (!liveNames.has(g.name)) rows.push({ name: g.name, ghost: g })
  }
  rows.sort((a, b) => a.name.localeCompare(b.name))
  return {
    items: rows.slice(0, MAX_ENDPOINT_ROWS),
    hidden: Math.max(0, rows.length - MAX_ENDPOINT_ROWS),
  }
}

/** The height of a Lambda Web Function node listing `list`. */
export function webFunctionHeight(list: EndpointList): number {
  if (list.items.length === 0) return DATA_NODE_HEADER_H
  return (
    DATA_NODE_HEADER_H +
    list.items.length * ENDPOINT_ROW_H +
    (list.hidden > 0 ? DATA_MORE_H : 0) +
    NODE_PAD_B
  )
}
