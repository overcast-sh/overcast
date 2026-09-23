import { useEffect, useRef } from "react"
import type { RowCount } from "@/lib/data-sources/row-source"
import { formatQuantity } from "@/lib/format"
import { ROW_HEIGHT } from "./scroll-model"
import type { GridCursor } from "./use-grid-cursor"

/**
 * The two ways the grid jumps straight to a row: Go to row, and a deep link
 * that opens on one. Both bring the row to the top of the view and put the
 * cursor on it; a deep link waits until the source knows it has that row.
 */
export function useRowJumps({
  rowCount,
  initialRow,
  scrollToTop,
  cursor,
  focusGrid,
}: {
  rowCount: RowCount
  initialRow?: number
  scrollToTop: (top: number) => void
  cursor: GridCursor
  focusGrid: () => void
}): { goTo: (row: number) => string | null } {
  const { value: count, exact } = rowCount

  const opened = useRef(initialRow === undefined)
  useEffect(() => {
    if (opened.current || initialRow === undefined) return
    if (initialRow >= count && !exact) return
    opened.current = true
    scrollToTop(Math.min(initialRow, count - 1) * ROW_HEIGHT)
  }, [initialRow, count, exact, scrollToTop])

  /** Moves to a 1-based row; says why when it could not go exactly there. */
  const goTo = (row: number): string | null => {
    const target = Math.min(row, count) - 1
    scrollToTop(target * ROW_HEIGHT)
    cursor.moveTo({ row: target, col: cursor.cursor?.col ?? 0 }, { reveal: false })
    focusGrid()
    if (row <= count) return null
    return exact
      ? `The file has ${formatQuantity(count, "row")}`
      : `Only ${formatQuantity(count, "row")} indexed so far`
  }

  return { goTo }
}
