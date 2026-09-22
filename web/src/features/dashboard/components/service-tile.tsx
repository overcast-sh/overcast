import type { ReactNode } from "react"
import { Link } from "@tanstack/react-router"
import { Tooltip } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import type { ServiceTierEntry } from "../service-defs"

/**
 * Shared wrapper for every dashboard service tile — card, compact card, chip, list row.
 *
 * The tile is a plain container and the navigation target is a `ServiceTileLink`
 * inside it, stretched over the whole tile with an `::after` overlay. That keeps
 * the whole tile clickable while leaving room for controls of its own (docs, pin)
 * as *siblings* of the link: a button nested in an `<a>` is invalid, and its click
 * would have to cancel the link's navigation to work at all. Controls raise
 * themselves above the overlay with `relative z-10`.
 *
 * Hover and focus styling live on the tile: `interactiveClassName` should key
 * focus rings off `has-[a:focus-visible]` rather than `focus-visible`, since the
 * tile itself never takes focus.
 */
export function ServiceTile({
  className,
  interactiveClassName,
  role,
  children,
}: {
  /** Applied in both states. */
  className: string
  /** Hover/focus affordances, merged with className. */
  interactiveClassName?: string
  /** ARIA role for the tile itself, e.g. "row" inside the list view's table. */
  role?: string
  children: ReactNode
}) {
  return (
    <div role={role} className={cn("relative", className, interactiveClassName)}>
      {children}
    </div>
  )
}

/** The tile's navigation target. Its hit area covers the enclosing `ServiceTile`. */
export function ServiceTileLink({
  entry,
  onNavigate,
  className,
  tooltip,
  children,
}: {
  entry: ServiceTierEntry
  onNavigate?: (key: string) => void
  className?: string
  tooltip?: ReactNode
  children: ReactNode
}) {
  const { service } = entry

  const link = (
    <Link
      to={service.to}
      onClick={() => onNavigate?.(service.to)}
      className={cn("outline-none after:absolute after:inset-0 after:content-['']", className)}
    >
      {children}
    </Link>
  )

  return tooltip ? <Tooltip content={tooltip}>{link}</Tooltip> : link
}

/** Focus ring for a tile, drawn when its link has keyboard focus. */
export const TILE_FOCUS_RING =
  "has-[a:focus-visible]:outline-2 has-[a:focus-visible]:outline-offset-2 has-[a:focus-visible]:outline-accent"
