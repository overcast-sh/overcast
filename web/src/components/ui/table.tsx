import * as React from "react"
import { ScrollX } from "@/components/ui/scroll-x"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"

/**
 * A table on its scroller. `ScrollX` owns the narrow-width contract — the
 * sideways scroll and the edge shadow that says there is more of it — so every
 * `ResourceTable` consumer inherits the same behaviour from one place.
 */
function Table({
  className,
  scrollerClassName,
  edgeClassName,
  startEdgeClassName,
  ...props
}: React.HTMLAttributes<HTMLTableElement> & {
  /**
   * Classes for the scrolling element — a `max-h-*` here makes the table
   * scroll vertically too, which is what a sticky header needs to stick to.
   */
  scrollerClassName?: string
  /** Passed to `ScrollX`'s overflow shadows; see there. */
  edgeClassName?: string
  startEdgeClassName?: string
}) {
  return (
    <ScrollX
      scrollerClassName={scrollerClassName}
      edgeClassName={edgeClassName}
      startEdgeClassName={startEdgeClassName}
    >
      <table className={cn("w-full caption-bottom text-sm", className)} {...props} />
    </ScrollX>
  )
}

function TableHeader({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn("border-b border-border bg-bg", className)} {...props} />
}

function TableBody({ className, ...props }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("[&_tr:last-child]:border-0", className)} {...props} />
}

/**
 * A row that navigates is a control, so it takes focus and answers Enter and
 * Space like one. The handler only fires for keys pressed on the row itself —
 * a button or link inside the row keeps its own Enter/Space behaviour.
 */
function TableRow({
  className,
  onClick,
  tabIndex,
  onKeyDown,
  ...props
}: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr
      className={cn(
        "border-b border-border-muted transition-colors",
        // `oc-row-focus` and not an outline utility: an outline on a <tr> in a
        // collapsed-border table paints as a line along the row's top and
        // bottom and nothing down the sides, which is a far weaker mark than
        // the ring every other control gets (#1610). The class draws the ring
        // across the row's own cells instead — see global.css.
        onClick && "oc-row-focus cursor-pointer hover:bg-accent-muted",
        "data-[selected=true]:bg-accent-muted",
        className,
      )}
      onClick={onClick}
      tabIndex={onClick ? (tabIndex ?? 0) : tabIndex}
      onKeyDown={(event) => {
        onKeyDown?.(event)
        if (!onClick || event.defaultPrevented) return
        if (event.target !== event.currentTarget) return
        if (event.key !== "Enter" && event.key !== " ") return
        event.preventDefault()
        event.currentTarget.click()
      }}
      {...props}
    />
  )
}

/**
 * Column header: the field-label spec on the recessed `bg-bg` strip.
 *
 * `scope="col"` is stated rather than left to be inferred, and an empty header —
 * the blank cell above a checkbox or an actions column — renders as a `<td>`: a
 * header that names nothing is not a header, and announcing "blank, column one"
 * ahead of every row is worse than announcing nothing.
 */
function TableHead({
  className,
  children,
  scope,
  ...props
}: React.ThHTMLAttributes<HTMLTableCellElement>) {
  const styles = cn(
    fieldLabel,
    "px-4 py-2 text-left align-middle whitespace-nowrap text-fg-subtle",
    className,
  )
  const names = React.Children.toArray(children).some(
    (child) => typeof child !== "string" || child.trim() !== "",
  )
  if (!names) return <td className={styles} {...props} />
  return (
    <th scope={scope ?? "col"} className={styles} {...props}>
      {children}
    </th>
  )
}

/**
 * A table body is machine output — identifiers, ARNs, sizes, timestamps, counts
 * — so **mono is the default**, matching every table the canvas draws (3a, 3b,
 * 4a and 6a all set `font-family: var(--oc-font-mono); font-size: 12px` on the
 * row's own cells). Making it the default here is what keeps the ~460 cells
 * across the app in one voice without one class per site.
 *
 * The handful of cells holding prose — a description, a comment, a failure
 * reason — are the exception and use `TableCellProse`.
 */
function TableCell({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <td
      className={cn("px-4 py-2.5 align-middle font-mono text-xs text-fg", className)}
      {...props}
    />
  )
}

/**
 * The prose exception to the mono default: a cell holding a sentence a human
 * wrote — `description`, `comment`, `ResourceStatusReason`. Sans 13px on the
 * body colour, the canvas's prose setting (4b's confirm copy, 2b's explainer).
 * Setting these back explicitly is what makes the mono default safe to inherit.
 */
function TableCellProse({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <TableCell className={cn("font-sans text-[13px] text-fg-muted", className)} {...props} />
}

function TableEmpty({ children, colSpan = 99 }: { children: React.ReactNode; colSpan?: number }) {
  return (
    <tr>
      <td colSpan={colSpan} className="py-12 text-center text-[13px] text-fg-subtle">
        {children}
      </td>
    </tr>
  )
}

export { Table, TableHeader, TableBody, TableRow, TableHead, TableCell, TableCellProse, TableEmpty }
