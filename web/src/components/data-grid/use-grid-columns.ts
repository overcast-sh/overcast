import { useEffect, useEffectEvent, useMemo, useRef, useState } from "react"
import {
  columnPinningFeature,
  columnResizingFeature,
  columnSizingFeature,
  columnVisibilityFeature,
  tableFeatures,
  useTable,
  type ColumnDef,
  type ColumnSizingState,
} from "@tanstack/react-table"
import type { DataColumn, RowBlock } from "@/lib/data-sources/row-source"
import { formatCount } from "@/lib/format"
import { MONO_CHAR_WIDTH, sampleColumnWidth } from "./cell-format"

/**
 * The grid's column state on TanStack Table v9, headless: sizes, resizing,
 * the pinned row-number column and visibility — the same engine
 * `ResourceTable` uses, with no row model at all. Rows are the row source's
 * business; the table only knows columns (`data` is always empty).
 *
 * Each data column's id is its index in the source, so a hidden column's
 * neighbours still read their values from the right place. Widths the caller
 * keeps (`ColumnWidths`) are by name instead, so they outlive the result they
 * were set on: a query run again, or edited, keeps the widths of the columns
 * it still has.
 */

const gridFeatures = tableFeatures({
  columnSizingFeature,
  columnResizingFeature,
  columnPinningFeature,
  columnVisibilityFeature,
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

/**
 * Column widths the user set, in px, by column name. Two columns with the
 * same name share one width.
 */
export type ColumnWidths = Readonly<Record<string, number>>

export interface ColumnWidthsOptions {
  /** Widths to open with, for the columns they name; the rest are sampled. */
  initialWidths?: ColumnWidths
  /** Called with every resized column's width when a resize ends. */
  onWidthsChange?: (widths: ColumnWidths) => void
}

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

/** Wide enough for the last row's number as the grid prints it, separators included. */
export function rowNumberWidth(rowCount: number): number {
  const characters = Math.max(formatCount(Math.max(rowCount, 1)).length, 3)
  return Math.round(characters * MONO_CHAR_WIDTH + ROW_NUMBER_PADDING)
}

/** Widths by name as the table's sizing state, by column index. */
function sizingByIndex(columns: readonly DataColumn[], widths: ColumnWidths): ColumnSizingState {
  const sizing: ColumnSizingState = {}
  columns.forEach((column, index) => {
    const width = widths[column.name]
    if (Number.isFinite(width)) {
      sizing[String(index)] = Math.min(Math.max(width, MIN_WIDTH), MAX_WIDTH)
    }
  })
  return sizing
}

/** The table's sizing state as widths by name. */
function widthsByName(columns: readonly DataColumn[], sizing: ColumnSizingState): ColumnWidths {
  return Object.fromEntries(
    Object.entries(sizing).flatMap(([id, width]) => {
      const column = columns.at(Number(id))
      return column ? [[column.name, width]] : []
    }),
  )
}

export function useGridColumns(
  columns: readonly DataColumn[],
  {
    rowCount,
    sample,
    initialWidths,
    onWidthsChange,
  }: { rowCount: number; sample: RowBlock | undefined } & ColumnWidthsOptions,
): GridColumns {
  // Each column's width is sampled once, from the first of its values to
  // arrive, wherever the grid opened (a projecting source delivers columns
  // one at a time): a column that resized itself under the reader as more
  // rows loaded would be worse than a starting width that is a little off.
  // Adjusted during render, guarded so it only runs when a column has new
  // values to sample.
  const [sampled, setSampled] = useState<ReadonlyMap<number, number>>(() => new Map())
  const fresh = columns.flatMap((column, i) => {
    const values = sample?.columns[i]
    return values && !sampled.has(i) ? [[i, sampleColumnWidth(column, values)] as const] : []
  })
  if (fresh.length > 0) setSampled(new Map([...sampled, ...fresh]))
  const numbersWidth = rowNumberWidth(rowCount)

  const definitions = useMemo<ColumnDef<GridFeatures, never>[]>(
    () => [
      { id: ROW_NUMBER_ID, size: numbersWidth, enableResizing: false, enableHiding: false },
      ...columns.map((column, index) => ({
        id: String(index),
        size: sampled.get(index) ?? sampleColumnWidth(column, undefined),
        minSize: MIN_WIDTH,
        maxSize: MAX_WIDTH,
      })),
    ],
    [columns, sampled, numbersWidth],
  )

  // The caller's widths are read once, when the table is made: the grid
  // remounts for each source, and after that the table owns its sizes.
  const [columnSizing] = useState(() => initialWidths && sizingByIndex(columns, initialWidths))

  // The options object is memoized because `useTable` hands back a new table
  // for new options: inline, every render (a frame, while scrolling) would
  // rebuild the layout below and everything downstream of it.
  const options = useMemo(
    () => ({
      features: gridFeatures,
      data: NO_ROWS,
      columns: definitions,
      columnResizeMode: "onChange" as const,
      initialState: { columnPinning: { start: [ROW_NUMBER_ID], end: [] }, columnSizing },
    }),
    [definitions, columnSizing],
  )
  const table = useTable<GridFeatures, never>(options)

  const { state } = table
  useReportWidths(columns, state, onWidthsChange)
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

/**
 * Reports the widths when a resize ends — once per drag, not every frame of
 * it — so a caller that stores them writes once.
 */
function useReportWidths(
  columns: readonly DataColumn[],
  state: { columnSizing: ColumnSizingState; columnResizing: { isResizingColumn: false | string } },
  onWidthsChange: ColumnWidthsOptions["onWidthsChange"],
) {
  const resizing = state.columnResizing.isResizingColumn !== false
  const wasResizing = useRef(false)
  const report = useEffectEvent(() => onWidthsChange?.(widthsByName(columns, state.columnSizing)))
  useEffect(() => {
    if (wasResizing.current && !resizing) report()
    wasResizing.current = resizing
  }, [resizing])
}
