import { memo } from "react"
import { cva } from "class-variance-authority"
import { Maximize2 } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import type { DataColumn } from "@/lib/data-sources/row-source"
import { MONO_CHAR_WIDTH, formatCell, isStructured, type FormattedCell } from "./cell-format"

/** The cell's horizontal padding, both sides together. */
const CELL_PADDING = 24

const cellVariants = cva(
  "absolute inset-y-0 flex items-center gap-1 border-b border-border-muted px-3 font-mono text-xs text-fg",
  {
    variants: {
      /** Numbers right-align, in tabular figures, so digits line up down the column. */
      numeric: { true: "justify-end tabular-nums" },
      match: { true: "bg-warning-muted" },
      selected: { true: "bg-accent-muted" },
      /** The cell cursor. */
      active: { true: "outline-2 -outline-offset-2 outline-accent" },
    },
  },
)

/** Finds the inspect button, which the grid handles by delegation. */
export const INSPECT_SELECTOR = "[data-inspect]"

interface GridCellProps {
  id: string
  row: number
  /** Display position: what `data-col` carries for the grid's pointer handling. */
  col: number
  /** Source index, for `aria-colindex`. */
  index: number
  column: DataColumn
  /** Left edge within the row's scrolling track. */
  left: number
  width: number
  loaded: boolean
  value: unknown
  selected: boolean
  active: boolean
  match: boolean
}

/**
 * One cell. Memoized, with primitive props only: a scroll re-renders the
 * grid, but a cell whose row is still on screen and whose value has not
 * changed keeps its props, so it skips rendering — the difference between
 * re-rendering a screenful of cells a frame and rendering the one new row.
 * Pointer handling is delegated to the grid, which reads `data-row` and
 * `data-col`.
 */
export const GridCell = memo(function GridCell({
  id,
  row,
  col,
  index,
  column,
  left,
  width,
  loaded,
  value,
  selected,
  active,
  match,
}: GridCellProps) {
  const cell = loaded ? formatCell(value, column) : null
  const overflows =
    cell?.kind === "value" &&
    (cell.clipped ||
      isStructured(value) ||
      cell.text.length * MONO_CHAR_WIDTH > width - CELL_PADDING)
  return (
    <div
      id={id}
      role="gridcell"
      data-row={row}
      data-col={col}
      aria-colindex={index + 2}
      aria-selected={selected}
      aria-busy={!loaded || undefined}
      title={cell?.kind === "absent" ? "Not present in this record" : undefined}
      className={cellVariants({ numeric: column.numeric, match, selected, active })}
      style={{ left, width }}
    >
      {cell ? <CellValue cell={cell} /> : <Skeleton depth="2" className="h-2 w-3/5" />}
      {active && overflows && (
        <button
          type="button"
          data-inspect
          aria-label="Inspect the whole value"
          title="Inspect the whole value (Enter)"
          className="shrink-0 cursor-pointer rounded-sm p-0.5 text-fg-subtle hover:bg-bg-muted hover:text-accent"
        >
          <Maximize2 aria-hidden className="size-3" />
        </button>
      )}
    </div>
  )
})

/**
 * A value as the cell shows it. NULL and an empty string are tokens rather
 * than text, so neither reads as data, and they differ in every channel a
 * glance uses: solid against dashed border, upright capitals against
 * lowercase italic. The empty token's spoken name is `sr-only` text beside an
 * `aria-hidden` word, not an `aria-label` — screen readers ignore a name on a
 * plain span and would read the cell as the word "empty". A key the record
 * does not have is a blank cell, named for screen readers only.
 */
function CellValue({ cell }: { cell: FormattedCell }) {
  switch (cell.kind) {
    case "null":
      return (
        <span
          title="NULL"
          className="rounded-sm border border-border px-1 text-2xs tracking-wider text-fg-subtle"
        >
          NULL
        </span>
      )
    case "empty":
      return (
        <span
          title="Empty string"
          className="rounded-sm border border-dashed border-border px-1 text-2xs font-light text-fg-subtle italic"
        >
          <span aria-hidden>empty</span>
          <span className="sr-only">empty string</span>
        </span>
      )
    case "absent":
      return <span className="sr-only">not present</span>
    case "value":
      return <span className="min-w-0 truncate">{cell.text}</span>
  }
}
