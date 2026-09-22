import type { LucideIcon } from "lucide-react"
import type { FileRouteTypes } from "@/routeTree.gen"
import { findServiceKeyForPathname } from "@/lib/nav-services"
import { SERVICES, type ServiceEntry } from "@/lib/service-registry"
import type { EmulationTier } from "@/types/common"

export interface ServiceCardDef {
  name: string
  label: string
  to: FileRouteTypes["to"]
  icon: LucideIcon
  /** Categorical-ramp slot, e.g. "text-cat-2" — see ServiceIconTile. */
  color: string
  bg: string
  border: string
  description: string
  /** Filename stem in docs/services/{docKey}.md. Omit if no docs exist. */
  docKey?: string
  /**
   * The sidebar service this card pins. Usually its own route, but a card can
   * front a sidebar group (CloudWatch's card opens /cloudwatch/logs, and the
   * sidebar pins /cloudwatch). Omitted when no sidebar service owns the route.
   */
  pinKey?: string
}

/** A service paired with the emulator's live view of it. */
export interface ServiceTierEntry {
  service: ServiceCardDef
  tier: EmulationTier
}

/** Every service that warrants a dashboard entry, derived from the registry. */
export const ALL_SERVICES: ServiceCardDef[] = Object.entries(
  SERVICES as Record<string, ServiceEntry>,
)
  .filter(([, e]) => e.dashboardCard !== false && e.to != null)
  .map(([name, e]) => ({
    name,
    label: e.dashboardLabel ?? e.label,
    to: e.to as FileRouteTypes["to"],
    icon: e.icon,
    color: e.color,
    bg: e.bg,
    border: e.border,
    description: e.dashboardDescription ?? e.description ?? "",
    ...(e.docKey ? { docKey: e.docKey } : {}),
    ...pinKeyFor(e.to as string),
  }))

function pinKeyFor(to: string): { pinKey?: string } {
  const pinKey = findServiceKeyForPathname(to)
  return pinKey ? { pinKey } : {}
}
