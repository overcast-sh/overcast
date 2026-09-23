import { useLayoutEffect, type RefObject } from "react"
import { useVirtualizer } from "@tanstack/react-virtual"
import type { LaidOutColumn } from "./use-grid-columns"

/** Columns rendered past either edge, so a horizontal scroll never shows a gap. */
const OVERSCAN = 2

/**
 * The columns in view, windowed by `@tanstack/react-virtual` on the
 * horizontal axis: there is no height cap across, so the stock pixel
 * virtualizer fits, as it does in the app's other virtual lists. The pinned
 * row-number column sits over the first `rowNumberWidth` pixels of the
 * scroller, so the scrolling columns start after it.
 */
export function useColumnWindow(
  scroller: RefObject<HTMLElement | null>,
  columns: readonly LaidOutColumn[],
  rowNumberWidth: number,
): LaidOutColumn[] {
  const virtualizer = useVirtualizer({
    horizontal: true,
    // The grid re-renders a frame at a time as it is; a synchronous render
    // per scroll event on top would be wasted work.
    useFlushSync: false,
    count: columns.length,
    getScrollElement: () => scroller.current,
    estimateSize: (position) => columns[position]?.width ?? 0,
    paddingStart: rowNumberWidth,
    overscan: OVERSCAN,
  })

  // Widths are estimates the virtualizer caches; a resize, or a column shown
  // or hidden, changes them under it.
  const widths = columns.map((column) => column.width).join(",")
  useLayoutEffect(() => virtualizer.measure(), [virtualizer, widths])

  return virtualizer.getVirtualItems().flatMap((item) => columns[item.index] ?? [])
}
