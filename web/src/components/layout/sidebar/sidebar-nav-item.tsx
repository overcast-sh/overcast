import { Link } from "@tanstack/react-router"
import { ChevronDown, GripVertical, type LucideIcon } from "lucide-react"
import type { DraggableAttributes, DraggableSyntheticListeners } from "@dnd-kit/core"
import { cva } from "class-variance-authority"
import { cn } from "@/lib/utils"
import { PinButton } from "@/components/service/pin-button"
import type { SubNavChild } from "@/lib/nav-services"
import { useServiceIconColor } from "@/hooks/use-service-icon-color"
import { flatChildren } from "./nav-children"
import { SidebarBadge } from "./sidebar-badge"
import { SidebarSubNav } from "./sidebar-sub-nav"
import { SidebarTooltip } from "./sidebar-tooltip"

export interface SidebarNavEntry {
  to: string
  label: string
  icon: LucideIcon
  /**
   * Categorical-ramp text colour class (e.g. "text-cat-2"), applied to the
   * icon glyph when the row is not the active route and the service-icon-
   * colour preference is on. Rows without an identity (e.g. the dashboard
   * link) pass "text-fg-muted" and get no visible change either way.
   */
  color?: string
  children?: SubNavChild[]
  exact?: boolean
}

/**
 * Wiring handed down by the sortable wrapper, split across two elements.
 *
 * `listeners` go on the row as well as the grip, so a pointer can still pick a row up
 * from anywhere on it. `attributes` go on the grip *only*: they carry `role="button"`
 * and `tabIndex`, and a row that claims to be a button while containing a link is a
 * widget with a focusable descendant — invalid, and unusable either way, since focus
 * lands on the row while the link inside is a second stop the row's own role denies
 * exists. The grip is also the activator node, which is what gives the keyboard sensor
 * something to start from.
 */
export interface SidebarSortableProps {
  setRowRef: (element: HTMLElement | null) => void
  style: React.CSSProperties
  attributes: DraggableAttributes
  listeners: DraggableSyntheticListeners
  /** dnd-kit's activator ref setter — the element that starts a drag. */
  setActivatorRef: (element: HTMLElement | null) => void
  /** Names the row being reordered, for the grip's accessible name. */
  label: string
}

const rowVariants = cva(
  "group relative flex items-center rounded-control font-mono transition-colors",
  {
    variants: {
      collapsed: {
        true: "h-8 w-9 justify-center",
        // Padding lives on the interactive child instead — see `rowHitArea`.
        false: "text-[13px]",
      },
      active: { true: "bg-accent-muted text-accent", false: "" },
      tone: { default: "text-fg-muted", muted: "text-fg-subtle" },
    },
    compoundVariants: [
      { active: false, class: "hover:bg-sidebar-item-hover hover:text-accent" },
      { active: true, collapsed: false, class: "font-bold" },
      { active: true, tone: "default", class: "text-accent" },
      { active: true, tone: "muted", class: "text-accent" },
    ],
    defaultVariants: { collapsed: false, active: false, tone: "default" },
  },
)

/**
 * The link or button inside an expanded row. It carries the row padding and
 * stretches to fill the row so the clickable area matches the hovered area.
 */
const rowHitArea = "flex min-w-0 flex-1 items-center gap-2.5 px-2.5 py-[7px]"

interface SidebarNavItemProps {
  item: SidebarNavEntry
  collapsed: boolean
  pathname: string
  tone?: "default" | "muted"
  expanded?: boolean
  onToggleExpand?: (key: string) => void
  badge?: { count: number; label: string }
  sortable?: SidebarSortableProps
  /**
   * Offers a pin toggle at the row's trailing edge, revealed on hover or focus,
   * and where a new pin lands in the order (see `PinButton`). The collapsed
   * rail has no room for it, so it is dropped there.
   */
  pinnable?: boolean
  pinAt?: "start" | "end"
}

export function SidebarNavItem({
  item,
  collapsed,
  pathname,
  tone = "default",
  expanded = false,
  onToggleExpand,
  badge,
  sortable,
  pinnable = false,
  pinAt,
}: SidebarNavItemProps) {
  const { to, label, icon: Icon, color, children, exact } = item
  const active = exact
    ? pathname === to
    : to === "/"
      ? pathname === "/"
      : pathname.startsWith(to)
  const rowCls = rowVariants({ collapsed, active, tone })
  const { enabled: colorEnabled } = useServiceIconColor()
  // The glyph keeps its own ramp colour in every state, active included — the
  // "you are here" signal lives on the row (bg-accent-muted, bold, and the
  // label text going accent via rowVariants), not on the icon. An explicit
  // class on the icon itself always wins over the row's inherited
  // `currentColor`, active/hover states included, so this is also what keeps
  // hover from re-forcing accent onto a coloured glyph.
  const tint = colorEnabled && color ? color : undefined
  const iconCls = cn("shrink-0", collapsed ? "h-[17px] w-[17px]" : "h-4 w-4", tint)
  const badgeNode = badge && badge.count > 0 && (
    <SidebarBadge count={badge.count} collapsed={collapsed} label={badge.label} />
  )
  // A sibling of the link/button rather than inside it, so it is its own tab stop.
  const pinNode = pinnable && (
    <PinButton serviceKey={to} label={label} reveal="hover" pinAt={pinAt} className="mr-1.5" />
  )

  if (collapsed) {
    const target = children?.length ? flatChildren(children)[0].to : to
    return (
      <li>
        <SidebarTooltip label={label}>
          <Link
            to={target}
            className={rowCls}
            aria-label={label}
            aria-current={active ? "page" : undefined}
          >
            <Icon className={iconCls} />
            {badgeNode}
          </Link>
        </SidebarTooltip>
      </li>
    )
  }

  if (children?.length) {
    // The disclosed list is a sibling of the trigger, so the relationship has to be
    // stated: without aria-controls, aria-expanded announces "collapsed" with nothing
    // named as the thing that is collapsed.
    const subNavId = `sidebar-subnav-${to.replace(/[^a-zA-Z0-9]+/g, "-").replace(/^-|-$/g, "")}`
    return (
      <li>
        <div ref={sortable?.setRowRef} style={sortable?.style} className={rowCls} {...sortable?.listeners}>
          {sortable && <SidebarGrip {...sortable} />}
          <button
            onClick={(event) => {
              event.stopPropagation()
              onToggleExpand?.(to)
            }}
            className={cn(rowHitArea, "text-left")}
            aria-expanded={expanded}
            aria-controls={subNavId}
            aria-current={active ? "page" : undefined}
          >
            <Icon className={iconCls} />
            <span className="flex-1 truncate">{label}</span>
            <ChevronDown
              className={cn("h-3 w-3 shrink-0 transition-transform", expanded && "rotate-180")}
            />
          </button>
          {pinNode}
        </div>
        {expanded && <SidebarSubNav id={subNavId} items={children} pathname={pathname} />}
      </li>
    )
  }

  return (
    <li ref={sortable?.setRowRef} style={sortable?.style} className={rowCls} {...sortable?.listeners}>
      {sortable && <SidebarGrip {...sortable} />}
      <Link
        to={to}
        className={rowHitArea}
        draggable={false}
        aria-current={active ? "page" : undefined}
      >
        <Icon className={iconCls} />
        <span className="truncate">{label}</span>
        {badgeNode}
      </Link>
      {pinNode}
    </li>
  )
}

/**
 * Drag affordance and the drag activator both. Absolutely positioned so it never shifts
 * icon alignment, hanging into the nav's left gutter (`px-2` on the `<nav>`) so it
 * clears the service icon rather than sitting on top of it.
 *
 * A real `<button>`, not a decorative glyph: it is what carries dnd-kit's listeners, so
 * it is also the thing a keyboard user tabs to and presses Space on to pick the row up.
 * It stays visible while focused for the same reason it appears on hover.
 */
function SidebarGrip({
  setActivatorRef,
  label,
  attributes,
  listeners,
}: Pick<SidebarSortableProps, "setActivatorRef" | "label" | "attributes" | "listeners">) {
  return (
    <button
      type="button"
      ref={setActivatorRef}
      aria-label={`Reorder ${label}`}
      className="absolute top-1/2 -left-1 flex h-4 w-4 -translate-y-1/2 cursor-grab items-center justify-center opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
      {...attributes}
      {...listeners}
    >
      <GripVertical aria-hidden className="h-3 w-3 text-fg-subtle/40" />
    </button>
  )
}
