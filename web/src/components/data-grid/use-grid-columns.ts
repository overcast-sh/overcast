import { useMemo, useState } from "react"
import {
  columnOrderingFeature,
  columnPinningFeature,
  columnResizingFeature,
  columnSizingFeature,
  columnVisibilityFeature,
  tableFeatures,
  useTable,
  type ColumnDef,
} from "@tanstack/react-table"
import type { DataColumn, RowBlock } from "@/lib/data-sources/row-source"
import { MONO_CHAR_WIDTH, sampleColumnWidth } from "./cell-format"

/**
 * The grid's column state on TanStack Table v9, headless: sizes, resizing,
 * the pinned row-number column, visibility and order — the same engine
 * `ResourceTable` uses, with no row model at all. Rows are the row source's
 * business; the table only knows columns (`data` is always empty).
 *
 * Each data column's id is its index in the source, so a reordered or
 * hidden column still reads its values from the right place.
 */

const gridFeatures = tableFeatures({
  columnSizingFeature,
  columnResizingFeature,
  columnPinningFeature,
  columnVisibilityFeature,
  columnOrderingFeature,
})

type GridFeatures = typeof gridFeatures

/** The row-number column's id: pinned to the start, never resized or hidden. */
const ROW_NUMBER_ID = "row-number"

const NO_ROWS: never[] = []

/** Resize limits, in px: narrow enough for a flag, wide enough for a sentence. */
const MIN_WIDTH = 48
const MAX_WIDTH = 1200

/** A header of names, and one with each name's declared type under it. */
const HEADER_HEIGHT = 32
const TYPED_HEADER_HEIGHT = 44

/** The row number's padding either side of its digits. */
const ROW_NUMBER_PADDING = 24

export interface LaidOutColumn {
  /** Place in display order, among the visible columns: what the cell cursor moves over. */
  position: number
  /** Index in the source's columns: where its values are. */
  index: number
  column: DataColumn
  width: number
  /** Left edge within the scrolling (unpinned) region. */
  start: number
  /** Starts a resize drag from a pointer or touch on the column's edge. */
  onResizeStart: (event: unknown) => void
  resizing: boolean
}

export interface GridColumns {
  /** The visible data columns, in display order. */
  columns: LaidOutColumn[]
  /** Width of the pinned row-number column. */
  rowNumberWidth: number
  /** Width of every visible data column together. */
  totalWidth: number
  /** The header's height: one line for names, two when the format declares types. */
  headerHeight: number
  /** For the Columns menu: every data column, whether shown, and how to toggle it. */
  visibility: {
    items: { id: string; label: string }[]
    hidden: Set<string>
    toggle: (id: string) => void
    showAll: () => void
    hideAll: () => void
  }
}

/** Wide enough for the row count's digits (at least three). */
export function rowNumberWidth(rowCount: number): number {
  const digits = Math.max(String(Math.max(rowCount, 1)).length, 3)
  return Math.round(digits * MONO_CHAR_WIDTH + ROW_NUMBER_PADDING)
}

export function useGridColumns(
  columns: readonly DataColumn[],
  { rowCount, sample }: { rowCount: number; sample: RowBlock | undefined },
): GridColumns {
  // Widths are sampled once, from the first rows to arrive: a column that
  // resized itself under the reader as more rows loaded would be worse than
  // a starting width that is a little off. Adjusted during render, guarded
  // so it runs once.
  const [sampled, setSampled] = useState<number[] | null>(null)
  if (!sampled && sample) {
    setSampled(columns.map((column, i) => sampleColumnWidth(column, sample.columns[i])))
  }
  const numbersWidth = rowNumberWidth(rowCount)

  const definitions = useMemo<ColumnDef<GridFeatures, never>[]>(
    () => [
      { id: ROW_NUMBER_ID, size: numbersWidth, enableResizing: false, enableHiding: false },
      ...columns.map((column, index) => ({
        id: String(index),
        size: sampled?.[index] ?? sampleColumnWidth(column, undefined),
        minSize: MIN_WIDTH,
        maxSize: MAX_WIDTH,
      })),
    ],
    [columns, sampled, numbersWidth],
  )

  const table = useTable<GridFeatures, never>({
    features: gridFeatures,
    data: NO_ROWS,
    columns: definitions,
    columnResizeMode: "onChange",
    initialState: { columnPinning: { start: [ROW_NUMBER_ID], end: [] } },
  })

  const { state } = table
  return useMemo(() => {
    const headers = new Map(table.getCenterFlatHeaders().map((header) => [header.id, header]))
    const laidOut = table.getCenterVisibleLeafColumns().map((column, position) => {
      const index = Number(column.id)
      return {
        position,
        index,
        column: columns[index],
        width: column.getSize(),
        start: column.getStart("center"),
        onResizeStart: headers.get(column.id)?.getResizeHandler() ?? (() => {}),
        resizing: column.getIsResizing(),
      }
    })
    const hideable = table.getAllLeafColumns().filter((column) => column.getCanHide())
    return {
      columns: laidOut,
      rowNumberWidth: table.getStartTotalSize(),
      totalWidth: table.getCenterTotalSize(),
      headerHeight: columns.some((column) => column.type) ? TYPED_HEADER_HEIGHT : HEADER_HEIGHT,
      visibility: {
        items: hideable.map((column) => ({
          id: column.id,
          label: columns[Number(column.id)].name,
        })),
        hidden: new Set(hideable.filter((column) => !column.getIsVisible()).map((c) => c.id)),
        toggle: (id: string) => table.getColumn(id)?.toggleVisibility(),
        showAll: () => table.toggleAllColumnsVisible(true),
        hideAll: () => table.toggleAllColumnsVisible(false),
      },
    }
    // `state` is the table's change signal: sizes, visibility and order live in it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [table, columns, state, definitions])
}
