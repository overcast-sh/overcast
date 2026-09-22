import { useCallback, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { useRouterState } from "@tanstack/react-router"
import { cn } from "@/lib/utils"
import { useFavourites } from "@/hooks/use-favourites"
import { inboxMessagesQueryOptions } from "@/features/mail/data"
import { useInboxReadState } from "@/features/mail/read-state"
import { ALL_SERVICES, DASHBOARD_ITEM } from "@/lib/nav-services"
import { useSidebarCollapse } from "../use-sidebar-collapse"
import { flatChildren } from "./nav-children"
import { SidebarBrand } from "./sidebar-brand"
import { SidebarFavourites } from "./sidebar-favourites"
import { SidebarFooter } from "./sidebar-footer"
import { SidebarNavItem } from "./sidebar-nav-item"
import { SidebarSection } from "./sidebar-section"

export function Sidebar() {
  const { collapsed, toggleCollapsed } = useSidebarCollapse()
  const [expanded, setExpanded] = useState<Record<string, boolean | undefined>>({})
  const { favourites } = useFavourites()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const { data: inboxMessages = [] } = useQuery(inboxMessagesQueryOptions())
  const { unreadCount } = useInboxReadState(inboxMessages)

  const toggleExpand = useCallback((key: string) => {
    setExpanded((prev) => ({ ...prev, [key]: !prev[key] }))
  }, [])

  // Parents of the active route expand automatically until the user overrides it.
  function isExpanded(key: string) {
    const override = expanded[key]
    if (override !== undefined) return override
    const children = ALL_SERVICES.find((service) => service.to === key)?.children
    return children ? flatChildren(children).some((c) => pathname.startsWith(c.to)) : false
  }

  // Active service shown contextually while it is not pinned as a favourite.
  // Longest matching `to` wins so nested routes resolve to the right service.
  const currentService =
    pathname === "/"
      ? undefined
      : ALL_SERVICES.filter(
          (service) => pathname.startsWith(service.to) && !favourites.includes(service.key),
        ).sort((a, b) => b.to.length - a.to.length)[0]

  return (
    <aside
      id="sidebar"
      aria-label="Services"
      className={cn(
        "flex shrink-0 flex-col border-r border-border bg-sidebar-bg transition-all duration-200",
        collapsed ? "w-14 py-3" : "w-57 pt-3",
      )}
    >
      {!collapsed && <SidebarBrand />}

      <nav
        aria-label="Services"
        className={cn(
          "flex flex-1 flex-col overflow-x-hidden overflow-y-auto",
          collapsed ? "items-center gap-0.5" : "gap-px px-2",
        )}
      >
        <SidebarSection label="overview" collapsed={collapsed} first>
          <SidebarNavItem item={DASHBOARD_ITEM} collapsed={collapsed} pathname={pathname} />
        </SidebarSection>

        <SidebarSection label="pinned" collapsed={collapsed}>
          {currentService && (
            <SidebarNavItem
              item={currentService}
              collapsed={collapsed}
              pathname={pathname}
              expanded={isExpanded(currentService.to)}
              onToggleExpand={toggleExpand}
              pinnable
              pinAt="start"
            />
          )}
          <SidebarFavourites
            collapsed={collapsed}
            pathname={pathname}
            isExpanded={isExpanded}
            onToggleExpand={toggleExpand}
          />
        </SidebarSection>
      </nav>

      <SidebarFooter
        collapsed={collapsed}
        pathname={pathname}
        unreadCount={unreadCount}
        onToggleCollapsed={toggleCollapsed}
      />
    </aside>
  )
}
