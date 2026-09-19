/**
 * map-layout-constants — the geometry every map layout module agrees on.
 *
 * The layout engine positions cards, the scorer judges the result and the
 * page renders it; all three need the same card sizes, column gap and
 * container padding. They live here, in a module with no imports, so the
 * engine and the scorer can share them without importing each other.
 */

export const NODE_WIDTH = 260
/**
 * Height of a plain resource card: icon row plus padding and border. Every
 * taller variant (queues, logs, status pills, VPCs, Lambda groups) reports
 * its own height through the size overrides, so a node's handles always sit
 * on the midline of the box the layout reserved for it.
 */
export const NODE_HEIGHT = 56

/**
 * Horizontal gap between adjacent columns. Every connection has to span it,
 * so it is as short as an edge label ("DLQ (max 3)") still fits in. It is
 * also the unavoidable minimum length the scorer forgives every edge.
 */
export const RANK_GAP = 96
/**
 * Vertical gap between cards stacked in one column, and between packed
 * blocks. Wide enough for a detour to pass between two cards (see
 * ROUTABLE_GAP in map-edge-routing.ts).
 */
export const NODE_GAP = 48

/** Collapsed nested stack chip dimensions (matches CollapsedStackNode). */
export const COLLAPSED_STACK_WIDTH = 200
export const COLLAPSED_STACK_HEIGHT = 56

/** IGW nodes are smaller (pill-shaped). */
export const IGW_NODE_WIDTH = 200
export const IGW_NODE_HEIGHT = 52
/** VPC cards carry a second row (subnets, status) under the header. */
export const VPC_NODE_HEIGHT = 86
/** A Lambda event-source-mapping filter is a small square between a source and its function. */
export const ESM_FILTER_NODE_SIZE = 56

/** Padding inside a container box, per React Flow node type. */
export interface ContainerPadding {
  x: number
  /** Room for the badge that hangs off the top edge. */
  top: number
  bottom: number
}

export const REGION_PADDING: ContainerPadding = { x: 40, top: 48, bottom: 32 }
export const STACK_PADDING: ContainerPadding = { x: 20, top: 28, bottom: 24 }
export const VPC_PADDING: ContainerPadding = { x: 20, top: 28, bottom: 24 }

/** Node types that are boxes around other nodes, as opposed to cards, and their padding. */
export const CONTAINER_PADDING: Readonly<Record<string, ContainerPadding | undefined>> = {
  regionGroup: REGION_PADDING,
  stackGroup: STACK_PADDING,
  vpcGroup: VPC_PADDING,
}

export function isContainerType(type: string | undefined): boolean {
  return type !== undefined && type in CONTAINER_PADDING
}

/** Vertical gap between stacked region groups. */
export const REGION_GAP = 60

/** Placeholder box for a region with nothing in it. */
export const EMPTY_REGION_WIDTH = 340
export const EMPTY_REGION_HEIGHT = 140
