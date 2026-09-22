import { Star } from "lucide-react"
import { cva } from "class-variance-authority"
import { cn } from "@/lib/utils"
import { useFavourites } from "@/hooks/use-favourites"
import { isFavouritable } from "@/lib/nav-services"

/**
 * How much of the star shows before its surface is hovered or focused. Hover
 * reveals key off the nearest Tailwind `group`, so the surface must carry one.
 *
 * - `always`: always drawn (the global search, where scanning for what is pinned
 *   is part of the task).
 * - `pinned`: a filled star stays as a pinned marker; the outline star to pin
 *   appears on hover (dashboard tiles).
 * - `hover`: only on hover (sidebar rows, where every pinned row being starred
 *   would say nothing the section heading does not).
 *
 * The star stays visible while focused whatever the mode, so a keyboard user
 * never tabs onto an invisible control.
 */
export type PinReveal = "always" | "pinned" | "hover"

const pinVariants = cva(
  "flex shrink-0 items-center justify-center rounded p-0.5 transition-[color,opacity] focus-visible:opacity-100",
  {
    variants: {
      pinned: {
        true: "text-accent hover:text-accent/70",
        false: "text-fg-subtle hover:text-fg-muted",
      },
      hidden: {
        true: "opacity-0 group-hover:opacity-100",
        false: "",
      },
    },
  },
)

function pinLabel(label: string, pinned: boolean) {
  return pinned ? `Unpin ${label} from sidebar` : `Pin ${label} to sidebar`
}

/**
 * The one control that pins a service to the sidebar or unpins it, wherever a
 * service is listed. Renders nothing for a service the sidebar cannot show.
 *
 * Always a sibling of the surface's navigation target, never nested inside it —
 * a button inside a link is invalid, and a click would have to fight the
 * link's own default action.
 */
export function PinButton({
  serviceKey,
  label,
  reveal = "always",
  pinAt = "end",
  className,
}: {
  /** The service's favourites key — its primary route path. */
  serviceKey: string
  label: string
  reveal?: PinReveal
  /**
   * Where a new pin lands in the sidebar order. The sidebar's row for the page
   * you are on sits above the pins, so it pins to the start: the row then
   * stays exactly where it was instead of jumping out from under the pointer.
   */
  pinAt?: "start" | "end"
  className?: string
}) {
  const { isFavourite, toggleFavourite } = useFavourites()
  if (!isFavouritable(serviceKey)) return null

  const pinned = isFavourite(serviceKey)
  const hidden = reveal === "hover" || (reveal === "pinned" && !pinned)
  const text = pinLabel(label, pinned)

  return (
    <button
      type="button"
      aria-label={text}
      title={text}
      onClick={(event) => {
        // Surfaces with a whole-row click target (the global search cards) must
        // not also treat the pin as a click on the row.
        event.stopPropagation()
        toggleFavourite(serviceKey, { at: pinAt })
      }}
      className={cn(pinVariants({ pinned, hidden }), className)}
    >
      <Star
        aria-hidden
        className="h-[13px] w-[13px]"
        fill={pinned ? "currentColor" : "none"}
        strokeWidth={1.6}
      />
    </button>
  )
}
