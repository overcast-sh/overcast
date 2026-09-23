import * as React from "react"
import { useOverflowEdges } from "@/hooks/use-overflow-edges"
import { cn } from "@/lib/utils"

/**
 * The console's narrow-width contract for a wide list: when the columns outrun
 * the pane the content scrolls sideways inside its card, and the edge it can
 * still scroll towards says so.
 *
 * Reflowing every table to stacked cards below a breakpoint was the
 * alternative and was not taken. These are dense machine tables — ARNs, sizes,
 * timestamps, status codes — read by scanning one column down, and a card per
 * row turns eight columns into eight labelled lines and a page into a mile of
 * them. Scrolling keeps the shape of the data at every width, and WCAG 1.4.10
 * asks that content not be *lost* at 320px rather than that a table stop being
 * one (it names tables in its own exception). What was lost was the columns
 * past the right edge of a container that did not scroll at all, and the
 * scrollbar that would have hinted at it: overlay scrollbars on macOS and
 * Windows 11 fade out when the pointer stops, so they announce nothing.
 *
 * `tabIndex={0}` on the scroller because a container that scrolls but cannot
 * be focused is reachable by trackpad and by nothing else (WCAG 2.1.1).
 *
 * @param className         wrapper — layout, borders, anything positioned against the edges
 * @param scrollerClassName the scrolling element itself
 * @param edgeClassName     both edge shadows — a wider one for a dense grid
 * @param startEdgeClassName the start shadow only — `left-12` moves it clear of
 *                          a sticky first column, which would otherwise sit
 *                          under the shadow it is meant to cast
 */
function ScrollX({
  className,
  scrollerClassName,
  edgeClassName,
  startEdgeClassName,
  children,
}: {
  className?: string
  scrollerClassName?: string
  edgeClassName?: string
  startEdgeClassName?: string
  children: React.ReactNode
}) {
  const { ref, start, end } = useOverflowEdges<HTMLDivElement>()
  return (
    <div className={cn("relative w-full", className)}>
      <div
        ref={ref}
        tabIndex={0}
        className={cn("w-full overflow-auto focus-visible:outline-2", scrollerClassName)}
      >
        {children}
      </div>
      <ScrollEdge side="start" show={start} className={cn(edgeClassName, startEdgeClassName)} />
      <ScrollEdge side="end" show={end} className={edgeClassName} />
    </div>
  )
}

/**
 * The "there is more this way" shadow.
 *
 * A sibling of the scroller rather than a child, so it stays put while the
 * content moves under it, and a shadow rather than a fade to the surface
 * colour because a table has no single surface: the header strip is `bg-bg`,
 * the rows are the card, and a hovered or focused row is `--accent-muted`. One
 * fade colour would be wrong on two of the three; ink sits over all of them.
 *
 * `aria-hidden` and `pointer-events-none` — it is a picture of a fact the
 * scroller already exposes to assistive technology and to the pointer.
 */
function ScrollEdge({
  side,
  show,
  className,
  style,
}: {
  side: "start" | "end"
  show: boolean
  className?: string
  /** Where the edge sits when it is not flush with the container — past a pinned column. */
  style?: React.CSSProperties
}) {
  return (
    <div
      aria-hidden
      className={cn(
        "pointer-events-none absolute inset-y-0 w-6 from-scrim-edge to-transparent transition-opacity",
        side === "start" ? "left-0 bg-linear-to-r" : "right-0 bg-linear-to-l",
        show ? "opacity-100" : "opacity-0",
        className,
      )}
      style={style}
    />
  )
}

export { ScrollEdge, ScrollX }
