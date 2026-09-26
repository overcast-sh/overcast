import { ChevronLeft, ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import { BOTTOM_ITEMS } from "@/lib/nav-services"
import { useDebugEnabled } from "@/hooks/use-server-info"
import { SidebarNavItem } from "./sidebar-nav-item"
import { SidebarTooltip } from "./sidebar-tooltip"
import { formatQuantity } from "@/lib/format"

interface SidebarFooterProps {
  collapsed: boolean
  pathname: string
  unreadCount: number
  onToggleCollapsed: () => void
}

/** Tool links plus the collapse toggle, pinned to the bottom of the sidebar. */
export function SidebarFooter({
  collapsed,
  pathname,
  unreadCount,
  onToggleCollapsed,
}: SidebarFooterProps) {
  const debugEnabled = useDebugEnabled()
  const items = BOTTOM_ITEMS.filter((item) => !item.debugOnly || debugEnabled)
  const toggleLabel = collapsed ? "Expand sidebar" : "Collapse sidebar"

  // The label is stated in both states rather than left to the visible "Collapse":
  // expanded, the button's own text says what it does but not what it acts on, and
  // collapsed there is no text at all. `aria-expanded` + `aria-controls` are what tie
  // it to the sidebar, so "collapsed" is announced about something named.
  const toggle = (
    <button
      onClick={onToggleCollapsed}
      aria-label={toggleLabel}
      aria-expanded={!collapsed}
      aria-controls="sidebar"
      className={cn(
        "mt-1 flex items-center rounded-control font-mono text-xs text-fg-subtle transition-colors",
        "hover:bg-sidebar-item-hover hover:text-accent",
        collapsed ? "h-8 w-9 justify-center" : "gap-2.5 px-2.5 py-[7px]",
      )}
    >
      {collapsed ? (
        <ChevronRight className="h-[17px] w-[17px]" />
      ) : (
        <>
          <ChevronLeft className="h-4 w-4" />
          <span>Collapse</span>
        </>
      )}
    </button>
  )

  return (
    <div
      className={cn(
        "mt-auto flex shrink-0 flex-col border-t border-border p-2",
        collapsed ? "w-full items-center gap-0.5" : "gap-px",
      )}
    >
      {/* The tool links are a list like every other group in the sidebar; the collapse
          toggle below is a control, not one of them, so it stays outside. */}
      <ul
        className={cn(
          "m-0 flex list-none flex-col p-0",
          collapsed ? "w-full items-center gap-0.5" : "gap-px",
        )}
        aria-label="Tools"
      >
      {items.map((item) => (
        <SidebarNavItem
          key={item.key}
          item={item}
          collapsed={collapsed}
          pathname={pathname}
          tone="muted"
          badge={
            item.to === "/inbox"
              ? {
                  count: unreadCount,
                  label: formatQuantity(unreadCount, "unread inbox message"),
                }
              : undefined
          }
        />
      ))}
      </ul>
      {collapsed ? <SidebarTooltip label={toggleLabel}>{toggle}</SidebarTooltip> : toggle}
    </div>
  )
}
