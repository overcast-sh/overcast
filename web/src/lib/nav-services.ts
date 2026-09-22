/**
 * nav-services — sidebar and global search service list.
 *
 * All service metadata comes from service-registry. This module derives
 * the sidebar/search list by filtering to services where nav !== false
 * and both `to` and `category` are set.
 *
 * To add a new service: edit service-registry.ts only.
 */

import {
  LayoutDashboard,
  Network,
  BarChart2,
  Inbox,
  Activity,
  Bug,
  BookOpen,
  Workflow,
  type LucideIcon,
} from "lucide-react"
import {
  SERVICES,
  type ServiceEntry,
  type ServiceCategory,
  type SubNavItem,
  type SubNavGroup,
  type SubNavChild,
  CATEGORY_LABELS,
  CATEGORY_ORDER,
  matchesRoute,
} from "./service-registry"

export type { ServiceCategory, SubNavItem, SubNavGroup, SubNavChild }
export { CATEGORY_LABELS, CATEGORY_ORDER }

export interface ServiceDefinition {
  /** Unique key — matches the primary route path. */
  key: string
  to: string
  label: string
  icon: LucideIcon
  color: string
  bg: string
  border: string
  category: ServiceCategory
  description: string
  children?: SubNavChild[]
  /** Whether users can favourite/pin this service. Defaults to true. */
  favouritable?: boolean
}

export interface BottomNavItem {
  key: string
  to: string
  label: string
  icon: LucideIcon
  color: string
  debugOnly?: boolean
  /** When true, the item is only active at exactly `to`, not at child paths. */
  exact?: boolean
}

export const ALL_SERVICES: ServiceDefinition[] = Object.values(
  SERVICES as Record<string, ServiceEntry>,
)
  .filter(
    (e): e is ServiceEntry & { to: string; category: ServiceCategory } =>
      e.nav !== false && e.to != null && e.category != null,
  )
  .map((e) => {
    const def: ServiceDefinition = {
      key: e.to,
      to: e.to,
      label: e.label,
      icon: e.icon,
      color: e.color,
      bg: e.bg,
      border: e.border,
      category: e.category,
      description: e.description ?? "",
    }
    if (e.children) def.children = e.children
    if (e.favouritable !== undefined) def.favouritable = e.favouritable
    return def
  })

/**
 * Whether `key` can be pinned to the sidebar. Only sidebar services qualify: a
 * key the sidebar cannot render (a dashboard-only service, or one that opts out
 * via `favouritable: false`) would be stored as a pin that never appears.
 */
export function isFavouritable(key: string): boolean {
  const service = ALL_SERVICES.find((s) => s.key === key)
  return service !== undefined && service.favouritable !== false
}

/**
 * Resolve the service that owns `pathname`, using longest-prefix matching
 * against each service's `to` so nested routes resolve to the most specific
 * service (same idea as the sidebar's currentService logic). Returns the
 * matching serviceKey, or `undefined` when no service route is active.
 */
export function findServiceKeyForPathname(pathname: string): string | undefined {
  if (pathname === "/") return undefined
  return ALL_SERVICES.filter((s) => matchesRoute(pathname, s.to)).sort(
    (a, b) => b.to.length - a.to.length,
  )[0]?.key
}

/** Dashboard item — always shown at the top of the sidebar. */
export const DASHBOARD_ITEM = {
  key: "/",
  to: "/",
  label: "Dashboard",
  icon: LayoutDashboard,
  color: "text-fg-muted",
}

/** Tool items — always pinned at the bottom of the sidebar. */
export const BOTTOM_ITEMS: BottomNavItem[] = [
  { key: "/map", to: "/map", label: "Map", icon: Network, color: "text-cat-4" },
  { key: "/events", to: "/events", label: "Events", icon: Activity, color: "text-cat-5" },
  {
    key: "/metrics",
    to: "/metrics",
    label: "Metrics & Health",
    icon: BarChart2,
    color: "text-cat-6",
  },
  {
    key: "/debug",
    to: "/debug",
    label: "Debug",
    icon: Bug,
    color: "text-cat-1",
    debugOnly: true,
    exact: true,
  },
  {
    key: "/debug/traces",
    to: "/debug/traces",
    label: "Traces",
    icon: Workflow,
    color: "text-cat-9",
    debugOnly: true,
  },
  { key: "/inbox", to: "/inbox", label: "Inbox", icon: Inbox, color: "text-cat-3" },
  { key: "/docs", to: "/docs", label: "Docs", icon: BookOpen, color: "text-cat-8" },
]
