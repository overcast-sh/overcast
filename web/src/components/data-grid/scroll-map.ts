/**
 * Mapping a scroll position onto a row index, past the browser's cap on how
 * tall an element can be.
 *
 * A naive virtual list makes its spacer `rowCount × rowHeight` pixels tall.
 * Browsers stop honouring an element's height somewhere around 17.9 million
 * pixels in Firefox and 33.5 million in Chromium, so at 28 px a row a naive
 * spacer breaks past roughly 640 thousand rows: the scrollbar stops short and
 * the last rows are unreachable. Past `MAX_SCROLL_PX`, then, the spacer stays
 * at that height and the scroll position is read as a *fraction* of the way
 * through the rows. Every row is still reachable, the scrollbar still says
 * where you are, and one pixel of scroll moves more than one pixel of rows —
 * which at a million rows is the only way a scrollbar can work at all. Exact
 * positioning is what Go-to-row is for.
 *
 * Pure functions, so the boundary cases are testable without layout.
 */

/** Well under Firefox's cap, with room for the header and rounding. */
export const MAX_SCROLL_PX = 8_000_000

export interface RowWindow {
  /** First row intersecting the viewport. */
  first: number
  /** How far that row's top sits above the viewport's top, in px (≥ 0). */
  offset: number
}

/** Height of the scrollable body (rows only, no header). */
export function scrollHeight(rowCount: number, rowHeight: number): number {
  return Math.min(rowCount * rowHeight, MAX_SCROLL_PX)
}

/** True when the grid is past the cap and scrolling is proportional. */
export function isMapped(rowCount: number, rowHeight: number): boolean {
  return rowCount * rowHeight > MAX_SCROLL_PX
}

/**
 * The row at the top of the viewport for a given `scrollTop`.
 *
 * `viewport` is the height available to rows. When mapped, the scroll range
 * `[0, MAX - viewport]` maps linearly onto the row range `[0, total - viewport]`
 * in virtual pixels, so the top of the scrollbar is row 0 and the bottom is
 * exactly the last screenful.
 */
export function rowAt(
  scrollTop: number,
  viewport: number,
  rowCount: number,
  rowHeight: number,
): RowWindow {
  const real = rowCount * rowHeight
  let virtualTop: number
  if (real <= MAX_SCROLL_PX) {
    virtualTop = scrollTop
  } else {
    const range = Math.max(MAX_SCROLL_PX - viewport, 1)
    const fraction = Math.min(Math.max(scrollTop / range, 0), 1)
    virtualTop = fraction * Math.max(real - viewport, 0)
  }
  virtualTop = Math.max(0, Math.min(virtualTop, Math.max(real - rowHeight, 0)))
  const first = Math.floor(virtualTop / rowHeight)
  return { first, offset: virtualTop - first * rowHeight }
}

/**
 * The `scrollTop` that brings `row` to the top of the viewport — the inverse
 * of `rowAt`, for Go-to-row and for keeping the cursor in view.
 */
export function scrollTopFor(
  row: number,
  viewport: number,
  rowCount: number,
  rowHeight: number,
): number {
  const real = rowCount * rowHeight
  const virtualTop = Math.max(0, Math.min(row * rowHeight, Math.max(real - viewport, 0)))
  if (real <= MAX_SCROLL_PX) return virtualTop
  const range = Math.max(MAX_SCROLL_PX - viewport, 1)
  const span = Math.max(real - viewport, 1)
  return (virtualTop / span) * range
}

/** How many rows can be at least partly visible in `viewport` px. */
export function visibleRowCount(viewport: number, rowHeight: number): number {
  return Math.ceil(viewport / rowHeight) + 1
}

/**
 * The columns intersecting `[left, left + width)`, by binary search over the
 * running offsets — `offsets[i]` is column i's left edge, `offsets[n]` the
 * total width. The same windowing as the rows, for the other axis.
 */
export function columnWindow(
  offsets: readonly number[],
  left: number,
  width: number,
  overscan = 1,
): { first: number; last: number } {
  const n = offsets.length - 1
  if (n <= 0) return { first: 0, last: -1 }
  const find = (x: number) => {
    let lo = 0
    let hi = n - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (offsets[mid] <= x) lo = mid
      else hi = mid - 1
    }
    return lo
  }
  const first = Math.max(0, find(left) - overscan)
  const last = Math.min(n - 1, find(left + width) + overscan)
  return { first, last }
}
