/**
 * The cell cursor and the selection, as pure functions of positions: where a
 * key moves the cursor, which rectangle the cursor and its anchor span, and
 * where the viewport must move to keep a cell in view.
 *
 * Columns are counted in display order (the visible columns, as reordered),
 * not in source order: the cursor moves over what is on screen.
 */

export interface Cell {
  row: number
  col: number
}

/** A rectangle of cells, inclusive at both ends. */
export interface Selection {
  top: number
  bottom: number
  left: number
  right: number
}

export interface GridBounds {
  rowCount: number
  colCount: number
  /** Rows a page key moves: one viewport. */
  pageRows: number
}

/** A cell's DOM id, so the cursor can be the grid's `aria-activedescendant`. */
export function cellId(gridId: string, { row, col }: Cell): string {
  return `${gridId}-${row}-${col}`
}

export function clampCell({ row, col }: Cell, { rowCount, colCount }: GridBounds): Cell {
  return {
    row: Math.max(0, Math.min(row, rowCount - 1)),
    col: Math.max(0, Math.min(col, colCount - 1)),
  }
}

/**
 * Where a key moves the cursor from `at`, or null for a key that is not a
 * move. `mod` is ⌘ on a Mac and Ctrl elsewhere: with Home and End it jumps
 * to the first or last row rather than the first or last column.
 */
export function cellForKey(key: string, at: Cell, mod: boolean, bounds: GridBounds): Cell | null {
  const { row, col } = at
  switch (key) {
    case "ArrowDown":
      return { row: row + 1, col }
    case "ArrowUp":
      return { row: row - 1, col }
    case "ArrowRight":
      return { row, col: col + 1 }
    case "ArrowLeft":
      return { row, col: col - 1 }
    case "PageDown":
      return { row: row + bounds.pageRows, col }
    case "PageUp":
      return { row: row - bounds.pageRows, col }
    case "Home":
      return mod ? { row: 0, col } : { row, col: 0 }
    case "End":
      return mod ? { row: bounds.rowCount - 1, col } : { row, col: bounds.colCount - 1 }
    default:
      return null
  }
}

export function selectionOf(cursor: Cell, anchor: Cell): Selection {
  return {
    top: Math.min(anchor.row, cursor.row),
    bottom: Math.max(anchor.row, cursor.row),
    left: Math.min(anchor.col, cursor.col),
    right: Math.max(anchor.col, cursor.col),
  }
}

export function inSelection(selection: Selection | null, row: number, col: number): boolean {
  return (
    !!selection &&
    row >= selection.top &&
    row <= selection.bottom &&
    col >= selection.left &&
    col <= selection.right
  )
}

/**
 * The logical scroll top that brings `row` fully into view — or null when
 * it already is. A row above the view goes to the top; one below goes to the
 * bottom, so arrowing down moves the view a row at a time.
 */
export function topToShowRow(
  row: number,
  { top, viewport, rowHeight }: { top: number; viewport: number; rowHeight: number },
): number | null {
  const rowTop = row * rowHeight
  if (rowTop < top) return rowTop
  if (rowTop + rowHeight > top + viewport) return rowTop + rowHeight - viewport
  return null
}

/** The same for a column across: the `scrollLeft` that shows `[start, start + width)`, or null. */
export function leftToShowColumn(
  { start, width }: { start: number; width: number },
  { left, viewport }: { left: number; viewport: number },
): number | null {
  if (start < left) return start
  if (start + width > left + viewport) return start + width - viewport
  return null
}
