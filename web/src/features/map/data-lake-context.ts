/**
 * What the Athena, Glue and S3 Tables nodes read besides their own node.
 *
 * The overlay their lists show comes through context rather than node data:
 * it changes on every data-lake event, and only these few nodes read it.
 */

import { createContext, useContext } from "react"
import type { NodeProps } from "@xyflow/react"
import type { TopologyNode } from "@/types"
import { EMPTY_OVERLAY, type DataLakeOverlay } from "./data-lake-overlay"

export const DataLakeOverlayContext = createContext<DataLakeOverlay>(EMPTY_OVERLAY)

export function useDataLakeOverlay(): DataLakeOverlay {
  return useContext(DataLakeOverlayContext)
}

/**
 * A table's peek: a Glue table's first rows or an S3 Tables table's latest
 * commit. It names the table rather than holding it, so it shows the table as
 * the topology has it now — a commit that lands while it is open included.
 */
export interface DataLakePeek {
  kind: "preview" | "commit"
  /** The database or table bucket node the table is on. */
  nodeId: string
  /** The table's name on that node (see `tableRowName`). */
  row: string
}

/**
 * Opens a table's peek. The map holds the one open peek, as it does the log
 * stream peek: a peek inside a node would take the node's clicks with it and
 * close whenever the node scrolls out of view.
 */
export const DataLakePeekContext = createContext<(peek: DataLakePeek) => void>(() => {})

export function useDataLakePeek(): (peek: DataLakePeek) => void {
  return useContext(DataLakePeekContext)
}

/** What map-page gives a data-lake node. */
export interface DataLakeNodeData extends Record<string, unknown> {
  /** The node as the topology last reported it — fresh, unlike the layout's copy. */
  topologyNode: TopologyNode
  hasTarget?: boolean
  hasSource?: boolean
  isNew?: boolean
}

export function areDataLakeNodePropsEqual(prev: NodeProps, next: NodeProps): boolean {
  const pd = prev.data as DataLakeNodeData
  const nd = next.data as DataLakeNodeData
  return (
    prev.selected === next.selected &&
    pd.topologyNode === nd.topologyNode &&
    pd.hasTarget === nd.hasTarget &&
    pd.hasSource === nd.hasSource &&
    pd.isNew === nd.isNew
  )
}
