/**
 * map-theme — map color tokens derived from the central service registry.
 *
 * Service colors, edge colors, and sweep helpers all live here so that
 * map-page.tsx, topology-nodes.tsx, and topology-edges.tsx stay in sync.
 * Add or change service colors in @/lib/service-registry instead.
 */

import { SERVICES } from "@/lib/service-registry"

/** Per-service color tokens used by topology nodes and the minimap.
 *  Derived automatically from the central service registry. */
export const SERVICE_THEME: Record<
  string,
  { css: string; color: string; bg: string; border: string; letter: string } | undefined
> = Object.fromEntries(
  Object.entries(SERVICES).map(([key, s]) => [
    key,
    { css: s.css, color: s.color, bg: s.bg, border: s.border, letter: s.letter },
  ]),
)

/**
 * Edge-type theme tokens. Keys match the topology API edge type strings.
 *
 * An edge is drawn in the colour of the service that owns it, so each entry
 * points at the same ramp slot that service does in the registry — kept as a
 * literal `var(--cat-*)` rather than read back out of SERVICES because two of
 * these (`esm-filter`, `cfn-ref`) name a *relationship* rather than a service.
 */
export const EDGE_THEME: Record<
  string,
  { color: string; dash: boolean; label: string } | undefined
> = {
  notification: { color: "var(--cat-2)", dash: false, label: "S3 notification" },
  subscription: { color: "var(--cat-10)", dash: false, label: "SNS subscription" },
  esm: { color: "var(--cat-9)", dash: false, label: "Lambda ESM" },
  "esm-filter": { color: "var(--cat-3)", dash: true, label: "ESM filter" },
  pipe: { color: "var(--cat-6)", dash: true, label: "EventBridge Pipe" },
  logs: { color: "var(--cat-5)", dash: false, label: "CloudWatch Logs" },
  dlq: { color: "var(--danger)", dash: true, label: "Dead Letter Queue" },
  "vpc-attachment": { color: "var(--cat-5)", dash: false, label: "IGW Attachment" },
  "vpc-member": { color: "var(--cat-5)", dash: true, label: "VPC Member" },
  "cfn-export": { color: "var(--cat-6)", dash: true, label: "CFN Export" },
  "cfn-ref": { color: "var(--fg-subtle)", dash: true, label: "CFN Reference" },
  "apigw-integration": { color: "var(--cat-4)", dash: false, label: "API Gateway → Lambda" },
  "table-location": { color: "var(--cat-9)", dash: false, label: "Glue table data" },
  federation: { color: "var(--cat-9)", dash: true, label: "Glue federation" },
  "query-results": { color: "var(--cat-8)", dash: false, label: "Athena results" },
  // Drawn from recent executions, not configuration: dashed, and "recent" in the legend.
  queries: { color: "var(--cat-8)", dash: true, label: "Athena queries (recent)" },
}

/** Colour for a node or edge whose type the registry has no entry for. */
export const FALLBACK_COLOR = "var(--fg-subtle)"

/**
 * A service colour dimmed to a 35%-opacity wash, for the sweep animation that
 * runs across an active node. `color-mix` rather than an rgba() literal because
 * the input is now a `var(--cat-*)` whose value differs per theme.
 */
export function toSweep(color: string): string {
  return `color-mix(in oklab, ${color} 35%, transparent)`
}
