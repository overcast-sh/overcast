import type {
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
} from "react"
import { INSPECT_SELECTOR } from "./grid-cell"
import { cellForKey, type Cell, type GridBounds } from "./grid-navigation"
import { ROW_HEIGHT } from "./scroll-model"
import type { GridCursor } from "./use-grid-cursor"

interface GridInputOptions {
  cursor: GridCursor
  bounds: GridBounds
  /** Where the cursor starts when a key arrives before any cell was chosen. */
  home: Cell
  /** The logical scroll top, and how to move it: page keys scroll the rows with the cursor. */
  top: number
  scrollToTop: (top: number) => void
  copySelection: () => void
  inspect: (cell: Cell) => void
}

export interface GridInput {
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void
  onDoubleClick: (event: ReactMouseEvent<HTMLElement>) => void
  onClick: (event: ReactMouseEvent<HTMLElement>) => void
}

/** The cell under a pointer, from the `data-row` and `data-col` every cell carries. */
function cellAt(target: EventTarget): Cell | null {
  const element = target instanceof Element ? target.closest<HTMLElement>("[data-row]") : null
  if (!element) return null
  return { row: Number(element.dataset.row), col: Number(element.dataset.col) }
}

/**
 * The grid's keyboard and pointer, handled once at the scroller by
 * delegation, so the memoized cells take no handlers:
 *
 * - arrows, Home and End move the cell cursor (with ⌘/Ctrl, Home and End go
 *   to the first and last row); Shift extends the selection;
 * - PageUp and PageDown move the cursor and the rows together, a viewport at
 *   a time, the way a spreadsheet pages;
 * - ⌘/Ctrl+C copies the selection as TSV; Enter opens the cell inspector;
 * - a click sets the cursor (Shift+click extends), a double-click inspects.
 */
export function useGridInput({
  cursor,
  bounds,
  home,
  top,
  scrollToTop,
  copySelection,
  inspect,
}: GridInputOptions): GridInput {
  const onKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.target !== event.currentTarget) return
    const mod = event.metaKey || event.ctrlKey
    if (mod && event.key.toLowerCase() === "c") {
      event.preventDefault()
      copySelection()
      return
    }
    if (event.key === "Enter" && cursor.cursor) {
      event.preventDefault()
      inspect(cursor.cursor)
      return
    }
    const at = cursor.cursor ?? home
    const next = cellForKey(event.key, at, mod, bounds)
    if (!next) return
    event.preventDefault()
    const paging = event.key === "PageDown" || event.key === "PageUp"
    if (paging) scrollToTop(top + (next.row - at.row) * ROW_HEIGHT)
    cursor.moveTo(next, { extend: event.shiftKey, reveal: !paging })
  }

  const onPointerDown = (event: ReactPointerEvent<HTMLElement>) => {
    if (event.button !== 0) return
    const cell = cellAt(event.target)
    if (!cell) return
    event.currentTarget.focus({ preventScroll: true })
    cursor.moveTo(cell, { extend: event.shiftKey && !!cursor.cursor, reveal: false })
  }

  const onDoubleClick = (event: ReactMouseEvent<HTMLElement>) => {
    const cell = cellAt(event.target)
    if (cell) inspect(cell)
  }

  const onClick = (event: ReactMouseEvent<HTMLElement>) => {
    if (!(event.target instanceof Element) || !event.target.closest(INSPECT_SELECTOR)) return
    const cell = cellAt(event.target)
    if (cell) inspect(cell)
  }

  return { onKeyDown, onPointerDown, onDoubleClick, onClick }
}
