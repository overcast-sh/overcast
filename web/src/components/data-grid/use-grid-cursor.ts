import { useCallback, useState } from "react"
import {
  clampCell,
  selectionOf,
  type Cell,
  type GridBounds,
  type Selection,
} from "./grid-navigation"

export interface GridCursor {
  cursor: Cell | null
  /** The rectangle between the anchor and the cursor; a single cell when they coincide. */
  selection: Selection | null
  /** Moves the cursor — extending the selection from its anchor, or starting a new one. */
  moveTo: (cell: Cell, options?: { extend?: boolean; reveal?: boolean }) => void
}

/**
 * The cell cursor and the selection it anchors. A move clamps to the grid,
 * brings the cell into view (`reveal`, unless the caller has already scrolled
 * for it) and reports the row, which the full-page viewer keeps in its URL.
 */
export function useGridCursor({
  bounds,
  initial,
  reveal,
  onRowChange,
}: {
  bounds: GridBounds
  initial?: Cell
  reveal: (cell: Cell) => void
  onRowChange?: (row: number) => void
}): GridCursor {
  const [cursor, setCursor] = useState<Cell | null>(initial ?? null)
  const [anchor, setAnchor] = useState<Cell | null>(initial ?? null)

  const moveTo = useCallback(
    (
      cell: Cell,
      { extend = false, reveal: show = true }: { extend?: boolean; reveal?: boolean } = {},
    ) => {
      if (bounds.rowCount === 0 || bounds.colCount === 0) return
      const next = clampCell(cell, bounds)
      setCursor(next)
      if (!extend) setAnchor(next)
      if (show) reveal(next)
      onRowChange?.(next.row)
    },
    [bounds, reveal, onRowChange],
  )

  const selection = cursor ? selectionOf(cursor, anchor ?? cursor) : null
  return { cursor, selection, moveTo }
}
