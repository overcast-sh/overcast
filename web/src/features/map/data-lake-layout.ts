/**
 * data-lake-layout — what the Athena, Glue and S3 Tables nodes list, and the
 * height that takes. The layout reserves exactly the height the node then
 * renders, so both map-page.tsx (sizing) and the nodes (rendering) read it
 * from here.
 */

import type { TopologyDataTable } from "@/types"
import { tableRowName, type GhostRow } from "./data-lake-overlay"

/** The header: icon, name and the line under it. */
export const DATA_NODE_HEADER_H = 58
/** One table or query row. */
export const DATA_ROW_H = 24
/** A namespace heading in an S3 Tables bucket. */
export const DATA_SECTION_H = 20
/** The "+N more" line under a list that was cut short. */
export const DATA_MORE_H = 22
/** Space under the last row. */
const DATA_NODE_PAD_B = 6
/** Table rows a node shows before the rest go to "+N more". */
export const DATA_MAX_ROWS = 6
/** Query rows a workgroup node shows: its latest executions. */
export const RUN_ROWS = 3

/** A table as its node lists it: live, or a ghost of one just dropped. */
export interface DisplayTable {
  /** Its name on the node: `namespace.table` in S3 Tables, `table` in Glue. */
  key: string
  name: string
  namespace?: string
  table?: TopologyDataTable
  ghost?: GhostRow
}

export type TableListItem =
  { kind: "namespace"; namespace: string } | { kind: "table"; row: DisplayTable }

export interface TableList {
  items: TableListItem[]
  /** Tables left off the node. */
  hidden: number
}

/**
 * The node's list: its live tables and the ghosts of any just dropped (a
 * ghost the topology still lists live is not a ghost), sorted by namespace
 * and name, a heading before each namespace, and cut at `DATA_MAX_ROWS`.
 */
export function tableList(
  live: readonly TopologyDataTable[],
  ghosts: readonly GhostRow[] = [],
): TableList {
  const rows: DisplayTable[] = live.map((t) => ({
    key: tableRowName(t),
    name: t.name,
    namespace: t.namespace,
    table: t,
  }))
  const liveKeys = new Set(rows.map((r) => r.key))
  for (const g of ghosts) {
    const key = tableRowName(g)
    if (!liveKeys.has(key)) rows.push({ key, name: g.name, namespace: g.namespace, ghost: g })
  }
  rows.sort(
    (a, b) => (a.namespace ?? "").localeCompare(b.namespace ?? "") || a.name.localeCompare(b.name),
  )

  const items: TableListItem[] = []
  let namespace: string | undefined
  for (const row of rows.slice(0, DATA_MAX_ROWS)) {
    if (row.namespace && row.namespace !== namespace) {
      namespace = row.namespace
      items.push({ kind: "namespace", namespace })
    }
    items.push({ kind: "table", row })
  }
  return { items, hidden: Math.max(0, rows.length - DATA_MAX_ROWS) }
}

/** The height of a Glue database or S3 Tables bucket node listing `list`. */
export function tableListHeight(list: TableList): number {
  if (list.items.length === 0) return DATA_NODE_HEADER_H
  const body = list.items.reduce(
    (h, item) => h + (item.kind === "namespace" ? DATA_SECTION_H : DATA_ROW_H),
    0,
  )
  return DATA_NODE_HEADER_H + body + (list.hidden > 0 ? DATA_MORE_H : 0) + DATA_NODE_PAD_B
}

/** The height of an Athena workgroup node listing `runs` executions. */
export function workgroupHeight(runs: number): number {
  const rows = Math.min(runs, RUN_ROWS)
  return rows === 0 ? DATA_NODE_HEADER_H : DATA_NODE_HEADER_H + rows * DATA_ROW_H + DATA_NODE_PAD_B
}
