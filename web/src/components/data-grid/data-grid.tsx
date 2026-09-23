import { useCallback, useId, useMemo, useRef, useState, type ReactNode } from "react"
import { EmptyState } from "@/components/ui/primitives"
import { ScrollEdge } from "@/components/ui/scroll-x"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { useElementSize } from "@/hooks/use-element-size"
import { useOverflowEdges } from "@/hooks/use-overflow-edges"
import type { RowSource } from "@/lib/data-sources/row-source"
import { formatCount, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import { CellInspector } from "./cell-inspector"
import { GridBody } from "./grid-body"
import { FileChangedNotice, GridFooter } from "./grid-footer"
import { GridHeader } from "./grid-header"
import {
  cellId,
  leftToShowColumn,
  topToShowRow,
  type Cell,
  type GridBounds,
} from "./grid-navigation"
import { GridToolbar } from "./grid-toolbar"
import { selectionTsv } from "./loaded-rows"
import { ROW_HEIGHT, rowWindow } from "./scroll-model"
import { useColumnWindow } from "./use-column-window"
import { useGridColumns } from "./use-grid-columns"
import { useGridCursor } from "./use-grid-cursor"
import { useGridFind } from "./use-grid-find"
import { useGridInput } from "./use-grid-input"
import { useHybridScroll } from "./use-hybrid-scroll"
import { useRowJumps } from "./use-row-jumps"
import { useBlockView, useRowBlocks } from "./use-row-blocks"
import { useSourceStatus } from "./use-source-status"

/**
 * A virtualized grid over any `RowSource`: millions of rows, hundreds of
 * columns, and the same few hundred DOM nodes either way. The design is
 * *Large data in the browser* in `docs/plans/data-lake-console.md`.
 *
 * This file only composes the parts, each of which is tested on its own:
 *
 * - rows scroll by the hybrid model (`scroll-model.ts`, `use-hybrid-scroll.ts`),
 *   which keeps a wheel notch one notch at five million rows;
 * - columns are sized, resized, pinned and hidden by headless TanStack Table
 *   (`use-grid-columns.ts`) and windowed by TanStack Virtual (`use-column-window.ts`);
 * - rows are read a block at a time into a byte-capped cache
 *   (`block-loader.ts`), and nothing while the scroll is flying;
 * - the cursor, the keyboard, Find and copying work over what is loaded
 *   (`grid-navigation.ts`, `use-grid-input.ts`, `loaded-rows.ts`).
 *
 * The scroller holds a spacer as tall (up to the cap) and as wide as the
 * content, and inside it one `sticky` layer the size of the viewport where
 * the header, the row numbers and the cells are drawn — so the header and
 * row numbers stay put and the DOM never grows with the file.
 *
 * It does not sort or filter: over millions of rows that needs an engine,
 * and doing it over the loaded rows would misstate the data. Find says it
 * searches the loaded rows; a query does the rest (`toolbarEnd`).
 */

export interface DataGridProps {
  source: RowSource
  /** Accessible name of the grid. */
  label: string
  /** Sizing: the grid fills its box, so give it a height. */
  className?: string
  /** Row to open on (0-based), for a deep link. */
  initialRow?: number
  /** Called as the cursor moves to a row. */
  onCursorChange?: (row: number) => void
  /** Controls at the end of the toolbar: *Query with Athena*, *Open in viewer*. */
  toolbarEnd?: ReactNode
  /** The decoded-row budget in bytes; the device's by default. */
  cacheBytes?: number
  /** What a source with no rows says. */
  emptyMessage?: string
  /** Reopens the source, offered when the object changed under it. */
  onReload?: () => void
}

export function DataGrid(props: DataGridProps) {
  // A new source is a new file: remount, so no cache, cursor or scroll
  // position outlives the file it belongs to.
  const [current, setCurrent] = useState({ source: props.source, generation: 0 })
  if (current.source !== props.source) {
    setCurrent({ source: props.source, generation: current.generation + 1 })
  }
  return <DataGridView key={current.generation} {...props} />
}

function DataGridView({
  source,
  label,
  className,
  initialRow,
  onCursorChange,
  toolbarEnd,
  cacheBytes,
  emptyMessage = "No rows.",
  onReload,
}: DataGridProps) {
  const gridId = useId()
  const { ref: scroller, start: fadeStart, end: fadeEnd } = useOverflowEdges<HTMLDivElement>()
  const size = useElementSize(scroller)
  const status = useSourceStatus(source)
  const rowCount = status.rowCount.value

  // ─── Rows, columns and the blocks under them ────────────────────────────
  const blocks = useRowBlocks(source, cacheBytes)
  const layout = useGridColumns(source.columns, { rowCount, sample: blocks.loader.lastLanded })
  const header = layout.headerHeight
  const viewport = Math.max(size.height - header, ROW_HEIGHT)
  const scroll = useHybridScroll(scroller, { rowCount, viewport })
  const { first: firstRow, offset } = rowWindow(scroll.top, ROW_HEIGHT)
  const lastRow = Math.min(rowCount - 1, Math.floor((scroll.top + viewport - 1) / ROW_HEIGHT))
  const inView = useColumnWindow(scroller, layout.columns, layout.rowNumberWidth)
  useBlockView(
    blocks,
    {
      firstRow,
      lastRow,
      columns: inView.map((column) => column.index),
      direction: scroll.direction,
      fast: scroll.fast,
    },
    rowCount,
  )
  const valueAt = (row: number, index: number) => blocks.loader.valueAt(row, index)

  // ─── Cursor, keyboard, Find, copy ───────────────────────────────────────
  const bounds = useMemo<GridBounds>(
    () => ({
      rowCount,
      colCount: layout.columns.length,
      pageRows: Math.max(Math.floor(viewport / ROW_HEIGHT), 1),
    }),
    [rowCount, layout.columns.length, viewport],
  )
  const { top, scrollToTop } = scroll
  const reveal = useCallback(
    ({ row, col }: Cell) => {
      const rowTop = topToShowRow(row, { top, viewport, rowHeight: ROW_HEIGHT })
      if (rowTop !== null) scrollToTop(rowTop)
      const column = layout.columns.at(col)
      const element = scroller.current
      if (!column || !element) return
      const left = leftToShowColumn(column, {
        left: element.scrollLeft,
        viewport: element.clientWidth - layout.rowNumberWidth,
      })
      if (left !== null) element.scrollTo({ left })
    },
    [top, viewport, scrollToTop, layout, scroller],
  )
  const cursor = useGridCursor({
    bounds,
    initial: initialRow === undefined ? undefined : { row: initialRow, col: 0 },
    reveal,
    onRowChange: onCursorChange,
  })
  const find = useGridFind(blocks.loader, layout.columns, blocks.version, (cell) =>
    cursor.moveTo(cell),
  )
  const [inspecting, setInspecting] = useState<{ cell: Cell; anchor: HTMLElement | null } | null>(
    null,
  )
  const inspect = (cell: Cell) => {
    // Enter inspects the cursor's cell and keeps the selection it anchors.
    if (cursor.cursor?.row !== cell.row || cursor.cursor.col !== cell.col) cursor.moveTo(cell)
    setInspecting({ cell, anchor: document.getElementById(cellId(gridId, cell)) })
  }
  const { copy } = useCopyToClipboard()
  const copySelection = () => {
    if (!cursor.selection) return
    const copied = selectionTsv(cursor.selection, layout.columns, valueAt)
    const rows = formatQuantity(copied.rows + copied.skipped, "row")
    copy(copied.text, {
      noun: copied.skipped > 0 ? `${formatCount(copied.rows)} of ${rows}` : rows,
    })
  }
  const input = useGridInput({
    cursor,
    bounds,
    home: { row: firstRow, col: inView[0]?.position ?? 0 },
    top,
    scrollToTop,
    copySelection,
    inspect,
  })
  const goToRef = useRef<HTMLInputElement>(null)
  const { goTo } = useRowJumps({
    rowCount: status.rowCount,
    initialRow,
    scrollToTop,
    cursor,
    focusGrid: () => scroller.current?.focus(),
  })

  const inspected = inspecting && layout.columns[inspecting.cell.col]
  const active = cursor.cursor
  const activeInView =
    !!active &&
    active.row >= firstRow &&
    active.row <= lastRow &&
    inView.some((column) => column.position === active.col)

  return (
    <div
      className={cn("flex min-h-0 flex-col", className)}
      // ⌘G reaches Go to row from anywhere in the grid, the toolbar included.
      onKeyDown={(event) => {
        if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "g") return
        event.preventDefault()
        goToRef.current?.focus()
        goToRef.current?.select()
      }}
    >
      <GridToolbar
        find={find}
        goToRef={goToRef}
        onGoTo={goTo}
        visibility={layout.visibility}
        end={toolbarEnd}
      />
      {status.changed && <FileChangedNotice onReload={onReload} />}
      <div
        ref={scroller}
        role="grid"
        aria-label={label}
        // Unknown (-1) while the file is still being counted.
        aria-rowcount={status.rowCount.exact ? rowCount + 1 : -1}
        aria-colcount={source.columns.length + 1}
        aria-multiselectable
        aria-activedescendant={activeInView ? cellId(gridId, active) : undefined}
        tabIndex={0}
        onScroll={scroll.onScroll}
        {...input}
        className="relative min-h-0 flex-1 overflow-auto overscroll-contain focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
      >
        <div
          style={{
            height: header + scroll.spacerHeight,
            width: layout.rowNumberWidth + layout.totalWidth,
          }}
        >
          <div
            className="sticky top-0 left-0 overflow-hidden"
            style={{ width: size.width, height: size.height }}
          >
            <GridHeader
              columns={inView}
              rowNumberWidth={layout.rowNumberWidth}
              scrollLeft={scroll.left}
              height={header}
            />
            {rowCount === 0 ? (
              <EmptyState className="absolute inset-x-0 py-10" title={emptyMessage} />
            ) : (
              <GridBody
                gridId={gridId}
                firstRow={firstRow}
                lastRow={lastRow}
                offset={offset}
                top={header}
                columns={inView}
                rowNumberWidth={layout.rowNumberWidth}
                scrollLeft={scroll.left}
                valueAt={valueAt}
                cursor={active}
                selection={cursor.selection}
                matches={find.keys}
              />
            )}
            <ScrollEdge side="start" show={fadeStart} style={{ left: layout.rowNumberWidth }} />
            <ScrollEdge side="end" show={fadeEnd} />
          </div>
        </div>
      </div>
      {inspecting && inspected && (
        <CellInspector
          anchor={inspecting.anchor}
          row={inspecting.cell.row}
          column={inspected.column}
          {...valueAt(inspecting.cell.row, inspected.index)}
          onClose={() => {
            setInspecting(null)
            scroller.current?.focus()
          }}
        />
      )}
      <GridFooter
        firstRow={firstRow}
        lastRow={lastRow}
        rowCount={status.rowCount}
        selection={cursor.selection}
        error={blocks.error}
        indexing={status.indexing}
        onContinue={source.continueIndexing && (() => source.continueIndexing?.())}
      />
    </div>
  )
}
