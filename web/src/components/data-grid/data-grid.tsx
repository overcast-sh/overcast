import {
  memo,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react"
import * as PopoverPrimitive from "@radix-ui/react-popover"
import { ChevronDown, ChevronUp, Maximize2, Search, X } from "lucide-react"
import { BlinkingCursor, Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { useToast } from "@/components/ui/toast"
import { writeClipboardText } from "@/lib/clipboard"
import { formatBytes, formatCount } from "@/lib/format"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import {
  cellText,
  clipboardText,
  formatCell,
  isStructured,
  prettyJson,
  sampleColumnWidth,
} from "./cell-format"
import type { GridColumn, RowSource } from "./row-source"
import { columnWindow, rowAt, scrollHeight, scrollTopFor, visibleRowCount } from "./scroll-map"
import { useRowBlocks, useScrollSettle } from "./use-row-blocks"

/**
 * A virtualized grid over any `RowSource`: millions of rows, hundreds of
 * columns, and the same few hundred DOM nodes either way.
 *
 * **Why not `@tanstack/react-virtual`.** It positions items in real pixels,
 * and the row axis here cannot: past the browser's element-height cap the
 * scroll position is a *fraction* of the rows (`scroll-map.ts`), which a
 * pixel-offset virtualizer has no way to express. Both axes therefore share
 * one small windowing function instead of the row axis being the odd one out.
 *
 * **How it draws.** The scroller holds a spacer as tall and wide as the
 * (mapped) content, and inside it one `sticky` layer the size of the viewport.
 * Everything visible — header, row numbers, cells — is absolutely positioned
 * in that layer from the current scroll offsets, so the header and the row
 * numbers never move and the DOM never grows with the file.
 *
 * **What it deliberately does not do.** Sort or filter the file: over a
 * million rows that needs an engine, and doing it over the loaded blocks would
 * misstate the data. Find searches the loaded rows and says so; anything more
 * is a query (see `toolbarEnd`).
 *
 * The component remounts per source (`DataGrid` keys its body on it), so a
 * block cache can never outlive the file it holds.
 */

export const ROW_HEIGHT = 28
const HEADER_HEIGHT = 32
const TYPED_HEADER_HEIGHT = 44
const CHAR_WIDTH = 7.25

export interface DataGridProps {
  source: RowSource
  /** Accessible name of the grid. */
  label: string
  /** Sizing — the grid fills its box, so give it a height. */
  className?: string
  /** Row to open on (0-based), for a deep link. */
  initialRow?: number
  /** Called (debounced by the caller if it likes) as the cursor moves. */
  onCursorChange?: (row: number) => void
  /** Controls at the right of the toolbar — *Query with Athena*, *Open in viewer*. */
  toolbarEnd?: ReactNode
  /** The byte-cap budget for decoded rows; defaults to the device's. */
  cacheBytes?: number
  /** What an empty source says. */
  emptyMessage?: string
  /** Reopens the source — offered when the object changed under it. */
  onReload?: () => void
}

const sourceIds = new WeakMap<RowSource, number>()
let nextSourceId = 1

export function DataGrid(props: DataGridProps) {
  let id = sourceIds.get(props.source)
  if (id === undefined) {
    id = nextSourceId++
    sourceIds.set(props.source, id)
  }
  return <DataGridBody key={id} {...props} />
}

interface Cell {
  row: number
  col: number
}

function useSourceSnapshot(source: RowSource) {
  // The source mutates its own count and status; this re-reads them on every
  // notification. The snapshot is a string so an unchanged state is `===`.
  const subscribe = useCallback((listener: () => void) => source.subscribe(listener), [source])
  const snapshot = useCallback(() => {
    const s = source.indexing
    return `${source.rowCount.value}|${source.rowCount.exact}|${s?.state}|${s?.rows}|${s?.bytes}|${s?.error ?? ""}`
  }, [source])
  useSyncExternalStore(subscribe, snapshot, snapshot)
  return { rowCount: source.rowCount, indexing: source.indexing }
}

function DataGridBody({
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
  const { rowCount, indexing } = useSourceSnapshot(source)
  const count = rowCount.value
  const columns = source.columns

  // ─── Layout ──────────────────────────────────────────────────────────────
  const scrollerRef = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  const [scroll, setScroll] = useState({ top: 0, left: 0 })
  const [direction, setDirection] = useState<1 | -1>(1)
  const frame = useRef(0)

  useLayoutEffect(() => {
    const el = scrollerRef.current
    if (!el) return
    const measure = () => setSize({ width: el.clientWidth, height: el.clientHeight })
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  // jsdom (and a grid not yet laid out) reports zero; a nominal viewport
  // keeps the first screenful rendering rather than rendering nothing.
  const viewWidth = size.width || 960
  const viewHeight = size.height || 480

  const [widths, setWidths] = useState<number[] | null>(null)
  const typed = columns.some((c) => c.type)
  const headerHeight = typed ? TYPED_HEADER_HEIGHT : HEADER_HEIGHT
  const bodyHeight = Math.max(viewHeight - headerHeight, ROW_HEIGHT)
  const rowNumberWidth = Math.round(Math.max(String(Math.max(count, 1)).length, 3) * CHAR_WIDTH + 24)

  const rowWindow = rowAt(scroll.top, bodyHeight, count, ROW_HEIGHT)
  const visibleRows = visibleRowCount(bodyHeight, ROW_HEIGHT)
  const firstRow = rowWindow.first
  const lastRow = Math.min(count - 1, firstRow + visibleRows - 1)

  const colWidths = useMemo(
    () => widths ?? columns.map((c) => sampleColumnWidth(c, undefined)),
    [widths, columns],
  )
  const offsets = useMemo(() => {
    const out = [0]
    for (const w of colWidths) out.push(out[out.length - 1] + w)
    return out
  }, [colWidths])
  const totalWidth = offsets[offsets.length - 1]
  const { first: firstCol, last: lastCol } = columnWindow(
    offsets,
    scroll.left,
    viewWidth - rowNumberWidth,
  )
  const visibleCols = useMemo(() => {
    const out: number[] = []
    for (let c = firstCol; c <= lastCol; c++) out.push(c)
    return out
  }, [firstCol, lastCol])

  const { paused, onScrollRows } = useScrollSettle()
  const { cache, version, error } = useRowBlocks(source, firstRow, lastRow, visibleCols, direction, {
    budget: cacheBytes,
    paused,
  })
  const blockSize = source.blockSize

  const valueAt = useCallback(
    (row: number, col: number): { loaded: boolean; value: unknown } => {
      const block = cache.peek(Math.floor(row / blockSize))
      const column = block?.columns[col]
      const i = row - (block?.start ?? 0)
      if (!block || !column || i >= block.count) return { loaded: false, value: undefined }
      return { loaded: true, value: column[i] }
    },
    // `version` is the cache's change signal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [cache, blockSize, version],
  )

  // Column widths are sampled once, from the first block that arrives.
  useEffect(() => {
    if (widths) return
    const block = cache.peek(0)
    if (!block) return
    setWidths(columns.map((c, i) => sampleColumnWidth(c, block.columns[i])))
  }, [version, widths, cache, columns])

  // A jump the grid made itself (Go to row, Home, End, a deep link) is not a
  // fling: its scroll event fetches at once instead of waiting to settle.
  const programmatic = useRef(false)
  const applyScroll = (top: number, left: number) => {
    const immediate = programmatic.current
    programmatic.current = false
    onScrollRows(rowAt(top, bodyHeight, count, ROW_HEIGHT).first, visibleRows, immediate)
    setScroll((previous) => {
      if (top !== previous.top) setDirection(top > previous.top ? 1 : -1)
      return previous.top === top && previous.left === left ? previous : { top, left }
    })
  }
  const onScroll = () => {
    const el = scrollerRef.current
    if (!el) return
    cancelAnimationFrame(frame.current)
    frame.current = requestAnimationFrame(() => applyScroll(el.scrollTop, el.scrollLeft))
  }
  useEffect(() => () => cancelAnimationFrame(frame.current), [])

  const scrollTo = (top: number | undefined, left: number | undefined) => {
    const el = scrollerRef.current
    if (!el) return
    programmatic.current = true
    if (top !== undefined) el.scrollTop = top
    if (left !== undefined) el.scrollLeft = left
    // jsdom fires no scroll event for a programmatic scroll; apply it here too.
    applyScroll(top ?? el.scrollTop, left ?? el.scrollLeft)
  }

  // ─── Cursor and selection ────────────────────────────────────────────────
  const [cursor, setCursor] = useState<Cell | null>(null)
  const [anchor, setAnchor] = useState<Cell | null>(null)
  const [inspecting, setInspecting] = useState(false)

  const ensureVisible = useCallback(
    (cell: Cell) => {
      let top: number | undefined
      const firstFull = rowWindow.offset > 0 ? firstRow + 1 : firstRow
      const fullRows = Math.max(Math.floor(bodyHeight / ROW_HEIGHT), 1)
      if (cell.row < firstFull) top = scrollTopFor(cell.row, bodyHeight, count, ROW_HEIGHT)
      else if (cell.row >= firstFull + fullRows)
        top = scrollTopFor(cell.row - fullRows + 1, bodyHeight, count, ROW_HEIGHT)
      let left: number | undefined
      const colLeft = offsets[cell.col]
      const colRight = offsets[cell.col + 1]
      const room = viewWidth - rowNumberWidth
      if (colLeft < scroll.left) left = colLeft
      else if (colRight > scroll.left + room) left = colRight - room
      if (top !== undefined || left !== undefined) scrollTo(top, left)
    },
    // `scrollTo` is a fresh closure each render; it reads only refs and setters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rowWindow.offset, firstRow, bodyHeight, count, offsets, viewWidth, rowNumberWidth, scroll.left],
  )

  const moveTo = useCallback(
    (cell: Cell, extend = false) => {
      const next = {
        row: Math.max(0, Math.min(cell.row, count - 1)),
        col: Math.max(0, Math.min(cell.col, columns.length - 1)),
      }
      setCursor(next)
      if (!extend) setAnchor(next)
      ensureVisible(next)
      onCursorChange?.(next.row)
    },
    [count, columns.length, ensureVisible, onCursorChange],
  )

  // A deep link opens on its row, once the source knows it has that many.
  const opened = useRef(false)
  useEffect(() => {
    if (opened.current || initialRow === undefined || count === 0) return
    if (initialRow >= count && !rowCount.exact) return
    opened.current = true
    const row = Math.min(initialRow, count - 1)
    scrollTo(scrollTopFor(row, bodyHeight, count, ROW_HEIGHT), undefined)
    setCursor({ row, col: 0 })
    setAnchor({ row, col: 0 })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialRow, count, rowCount.exact, bodyHeight])

  const selection = useMemo(() => {
    if (!cursor) return null
    const a = anchor ?? cursor
    return {
      r0: Math.min(a.row, cursor.row),
      r1: Math.max(a.row, cursor.row),
      c0: Math.min(a.col, cursor.col),
      c1: Math.max(a.col, cursor.col),
    }
  }, [cursor, anchor])

  const { toast } = useToast()
  const copySelection = useCallback(async () => {
    if (!selection) return
    const MAX_ROWS = 10_000
    const lines: string[] = []
    let skipped = 0
    const r1 = Math.min(selection.r1, selection.r0 + MAX_ROWS - 1)
    for (let r = selection.r0; r <= r1; r++) {
      const cells: string[] = []
      let loaded = true
      for (let c = selection.c0; c <= selection.c1; c++) {
        const cell = valueAt(r, c)
        if (!cell.loaded) loaded = false
        cells.push(clipboardText(cell.value, columns[c]))
      }
      if (loaded) lines.push(cells.join("\t"))
      else skipped++
    }
    try {
      await writeClipboardText(lines.join("\n"))
      toast({
        title: `Copied ${formatCount(lines.length)} ${lines.length === 1 ? "row" : "rows"}`,
        description:
          skipped > 0
            ? `${formatCount(skipped)} selected rows were not loaded and were left out.`
            : undefined,
        descriptionKind: "prose",
        variant: "success",
      })
    } catch {
      toast({ title: "Could not copy the selection", variant: "danger" })
    }
  }, [selection, valueAt, columns, toast])

  // ─── Go to row and Find ─────────────────────────────────────────────────
  const goToRef = useRef<HTMLInputElement>(null)
  const [goTo, setGoTo] = useState("")
  const [goToNote, setGoToNote] = useState<string | null>(null)
  const submitGoTo = () => {
    const n = Number(goTo.replace(/[,_\s]/g, ""))
    if (!Number.isInteger(n) || n < 1) {
      setGoToNote("Enter a row number")
      return
    }
    if (n > count) {
      setGoToNote(
        rowCount.exact
          ? `The file has ${formatCount(count)} rows`
          : `Only ${formatCount(count)} rows are indexed so far`,
      )
      moveTo({ row: count - 1, col: cursor?.col ?? 0 })
    } else {
      setGoToNote(null)
      moveTo({ row: n - 1, col: cursor?.col ?? 0 })
    }
    scrollerRef.current?.focus()
  }

  const [find, setFind] = useState("")
  const [findIndex, setFindIndex] = useState(0)
  const matches = useMemo(() => {
    const q = find.trim().toLowerCase()
    if (q.length === 0) return []
    const out: Cell[] = []
    const blocks = cache.indices().sort((a, b) => a - b)
    for (const b of blocks) {
      const block = cache.peek(b)
      if (!block) continue
      for (let i = 0; i < block.count && out.length < 1000; i++) {
        for (let c = 0; c < columns.length; c++) {
          const column = block.columns[c]
          if (!column) continue
          const cell = formatCell(column[i], columns[c])
          if (cell.kind === "value" && cell.text.toLowerCase().includes(q)) {
            out.push({ row: block.start + i, col: c })
          }
        }
      }
    }
    return out
    // `version` re-runs the search as blocks land.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [find, cache, columns, version])
  const matchKeys = useMemo(() => new Set(matches.map((m) => `${m.row}:${m.col}`)), [matches])
  const stepFind = (delta: number) => {
    if (matches.length === 0) return
    const next = (findIndex + delta + matches.length) % matches.length
    setFindIndex(next)
    moveTo(matches[next])
  }

  // ─── Keyboard ────────────────────────────────────────────────────────────
  const onWrapperKeyDown = (event: ReactKeyboardEvent) => {
    const mod = event.metaKey || event.ctrlKey
    if (mod && event.key.toLowerCase() === "g") {
      event.preventDefault()
      goToRef.current?.focus()
      goToRef.current?.select()
    }
  }

  const onGridKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget) return
    const mod = event.metaKey || event.ctrlKey
    if (mod && event.key.toLowerCase() === "c") {
      event.preventDefault()
      void copySelection()
      return
    }
    if (count === 0 || columns.length === 0) return
    const at = cursor ?? { row: firstRow, col: firstCol }
    const page = Math.max(Math.floor(bodyHeight / ROW_HEIGHT) - 1, 1)
    const extend = event.shiftKey
    let next: Cell | null = null
    switch (event.key) {
      case "ArrowDown":
        next = { row: at.row + 1, col: at.col }
        break
      case "ArrowUp":
        next = { row: at.row - 1, col: at.col }
        break
      case "ArrowRight":
        next = { row: at.row, col: at.col + 1 }
        break
      case "ArrowLeft":
        next = { row: at.row, col: at.col - 1 }
        break
      case "PageDown":
        next = { row: at.row + page, col: at.col }
        break
      case "PageUp":
        next = { row: at.row - page, col: at.col }
        break
      case "Home":
        next = mod ? { row: 0, col: at.col } : { row: at.row, col: 0 }
        break
      case "End":
        next = mod ? { row: count - 1, col: at.col } : { row: at.row, col: columns.length - 1 }
        break
      case "Enter":
        if (cursor) {
          event.preventDefault()
          setInspecting(true)
        }
        return
      default:
        return
    }
    event.preventDefault()
    moveTo(next, extend)
  }

  // ─── Column resize ───────────────────────────────────────────────────────
  const startResize = (col: number, event: ReactPointerEvent) => {
    event.preventDefault()
    event.stopPropagation()
    const startX = event.clientX
    const startWidth = colWidths[col]
    const target = event.currentTarget as HTMLElement
    target.setPointerCapture(event.pointerId)
    const onMove = (e: PointerEvent) => {
      const width = Math.round(Math.min(Math.max(startWidth + e.clientX - startX, 48), 1200))
      setWidths((current) => {
        const next = (current ?? colWidths).slice()
        next[col] = width
        return next
      })
    }
    const onUp = () => {
      target.removeEventListener("pointermove", onMove)
      target.removeEventListener("pointerup", onUp)
    }
    target.addEventListener("pointermove", onMove)
    target.addEventListener("pointerup", onUp)
  }

  // Cells are memoized, so everything they are handed must be stable: the
  // pointer is handled once, here, by delegation, and `inspect` never changes.
  const inspect = useCallback((row: number, col: number) => {
    setCursor({ row, col })
    setAnchor({ row, col })
    setInspecting(true)
  }, [])
  const cellAt = (target: EventTarget | null): Cell | null => {
    const el = target instanceof Element ? target.closest<HTMLElement>("[data-cell]") : null
    if (!el) return null
    return { row: Number(el.dataset.row), col: Number(el.dataset.col) }
  }
  const onPointerDown = (event: ReactPointerEvent) => {
    if (event.button !== 0) return
    const cell = cellAt(event.target)
    if (!cell) return
    scrollerRef.current?.focus({ preventScroll: true })
    setCursor(cell)
    if (!(event.shiftKey && cursor)) setAnchor(cell)
    onCursorChange?.(cell.row)
  }
  const onDoubleClick = (event: ReactMouseEvent) => {
    const cell = cellAt(event.target)
    if (cell) inspect(cell.row, cell.col)
  }

  // ─── Rendering ───────────────────────────────────────────────────────────
  const rows: ReactNode[] = []
  const renderedRows = count === 0 ? 0 : lastRow - firstRow + 1
  for (let i = 0; i < renderedRows; i++) {
    const row = firstRow + i
    const top = headerHeight + i * ROW_HEIGHT - rowWindow.offset
    const cells: ReactNode[] = []
    for (const col of visibleCols) {
      const { loaded, value } = valueAt(row, col)
      const selected =
        !!selection &&
        row >= selection.r0 &&
        row <= selection.r1 &&
        col >= selection.c0 &&
        col <= selection.c1
      const active = cursor?.row === row && cursor.col === col
      cells.push(
        <GridCell
          key={col}
          id={`${gridId}-${row}-${col}`}
          col={col}
          column={columns[col]}
          left={rowNumberWidth + offsets[col] - scroll.left}
          width={colWidths[col]}
          loaded={loaded}
          value={value}
          selected={selected}
          active={active}
          match={matchKeys.has(`${row}:${col}`)}
          row={row}
          onInspect={inspect}
        />,
      )
    }
    rows.push(
      <div
        key={row}
        role="row"
        aria-rowindex={row + 2}
        className="group absolute right-0 left-0"
        style={{ top, height: ROW_HEIGHT }}
      >
        <div
          role="rowheader"
          className="absolute inset-y-0 left-0 z-10 flex items-center justify-end border-r border-b border-border-muted bg-bg-elevated pr-3 font-mono text-xs text-fg-subtle tabular-nums select-none group-hover:bg-bg-subtle"
          style={{ width: rowNumberWidth }}
        >
          {formatCount(row + 1)}
        </div>
        {cells}
      </div>,
    )
  }

  const canScrollLeft = scroll.left > 1
  const canScrollRight = scroll.left + viewWidth - rowNumberWidth < totalWidth - 1
  const activeId = cursor ? `${gridId}-${cursor.row}-${cursor.col}` : undefined
  const activeRendered =
    !!cursor &&
    cursor.row >= firstRow &&
    cursor.row <= lastRow &&
    cursor.col >= firstCol &&
    cursor.col <= lastCol

  const inspected = cursor ? valueAt(cursor.row, cursor.col) : null

  return (
    // The wrapper takes ⌘G wherever focus is inside the grid.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div className={cn("flex min-h-0 flex-col", className)} onKeyDown={onWrapperKeyDown}>
      <GridToolbar
        find={find}
        onFind={(value) => {
          setFind(value)
          setFindIndex(0)
        }}
        matches={matches.length}
        matchIndex={findIndex}
        onStep={stepFind}
        goTo={goTo}
        goToRef={goToRef}
        onGoTo={(value) => {
          setGoTo(value)
          setGoToNote(null)
        }}
        onSubmitGoTo={submitGoTo}
        goToNote={goToNote}
        end={toolbarEnd}
      />
      <div
        ref={scrollerRef}
        role="grid"
        aria-label={label}
        aria-rowcount={count + 1}
        aria-colcount={columns.length + 1}
        aria-multiselectable
        aria-activedescendant={activeRendered ? activeId : undefined}
        tabIndex={0}
        onScroll={onScroll}
        onKeyDown={onGridKeyDown}
        onPointerDown={onPointerDown}
        onDoubleClick={onDoubleClick}
        className="relative min-h-0 flex-1 overflow-auto overscroll-contain focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent"
      >
        <div
          style={{
            height: headerHeight + scrollHeight(count, ROW_HEIGHT),
            width: rowNumberWidth + totalWidth,
          }}
        >
          <div
            className="sticky top-0 left-0 overflow-hidden"
            style={{ width: viewWidth, height: viewHeight }}
          >
            <div
              role="row"
              aria-rowindex={1}
              className="absolute top-0 right-0 left-0 z-20 border-b border-border bg-bg"
              style={{ height: headerHeight }}
            >
              <div
                role="columnheader"
                className={cn(
                  fieldLabel,
                  "absolute inset-y-0 left-0 z-10 flex items-end justify-end border-r border-border-muted bg-bg pr-3 pb-2 text-fg-subtle",
                )}
                style={{ width: rowNumberWidth }}
              >
                <span className="sr-only">Row</span>
                <span aria-hidden>#</span>
              </div>
              {visibleCols.map((col) => (
                <HeaderCell
                  key={col}
                  column={columns[col]}
                  colIndex={col}
                  left={rowNumberWidth + offsets[col] - scroll.left}
                  width={colWidths[col]}
                  onResize={(e) => startResize(col, e)}
                />
              ))}
            </div>
            {count === 0 ? (
              <p
                className="absolute inset-x-0 text-center text-[13px] text-fg-subtle"
                style={{ top: headerHeight + 32 }}
              >
                {emptyMessage}
              </p>
            ) : (
              rows
            )}
            <EdgeShadow side="start" show={canScrollLeft} offset={rowNumberWidth} top={0} />
            <EdgeShadow side="end" show={canScrollRight} offset={0} top={0} />
          </div>
        </div>
        {cursor && inspecting && inspected && (
          <CellInspector
            open
            onOpenChange={(open) => {
              setInspecting(open)
              if (!open) scrollerRef.current?.focus()
            }}
            anchor={document.getElementById(activeId ?? "")}
            row={cursor.row}
            column={columns[cursor.col]}
            loaded={inspected.loaded}
            value={inspected.value}
          />
        )}
      </div>
      <GridFooter
        firstRow={firstRow}
        lastRow={lastRow}
        count={count}
        exact={rowCount.exact}
        indexing={indexing}
        onContinue={source.continueIndexing ? () => source.continueIndexing?.() : undefined}
        error={error}
        selection={selection}
        changed={!!source.changed}
        onReload={onReload}
      />
    </div>
  )
}

// ─── Pieces ──────────────────────────────────────────────────────────────────

function HeaderCell({
  column,
  colIndex,
  left,
  width,
  onResize,
}: {
  column: GridColumn
  colIndex: number
  left: number
  width: number
  onResize: (event: ReactPointerEvent) => void
}) {
  return (
    <div
      role="columnheader"
      aria-colindex={colIndex + 2}
      title={column.type ? `${column.name} · ${column.type}` : column.name}
      className={cn(
        "absolute inset-y-0 flex flex-col justify-end px-3 pb-2 font-mono text-2xs",
        column.numeric && "items-end text-right",
      )}
      style={{ left, width }}
    >
      {/* The file's own names, in its own case: uppercasing `userId` would misreport it. */}
      <span className="max-w-full truncate text-xs font-medium text-fg">{column.name}</span>
      {column.type && <span className="max-w-full truncate text-fg-subtle">{column.type}</span>}
      <span
        aria-hidden
        onPointerDown={onResize}
        className="absolute inset-y-1 -right-1 z-10 w-2 cursor-col-resize rounded-sm hover:bg-accent-muted"
      />
    </div>
  )
}

/**
 * One cell. Memoized: a scroll re-renders the grid, but a cell whose row is
 * still on screen and whose value has not changed only moves with its row,
 * so it skips rendering altogether — the difference between re-rendering a
 * screenful of cells per frame and re-rendering the one new row.
 */
const GridCell = memo(function GridCell({
  id,
  row,
  col,
  column,
  left,
  width,
  loaded,
  value,
  selected,
  active,
  match,
  onInspect,
}: {
  id: string
  row: number
  col: number
  column: GridColumn
  left: number
  width: number
  loaded: boolean
  value: unknown
  selected: boolean
  active: boolean
  match: boolean
  onInspect: (row: number, col: number) => void
}) {
  const cell = loaded ? formatCell(value, column) : null
  const overflows =
    !!cell &&
    cell.kind === "value" &&
    (cell.clipped || isStructured(value) || cell.text.length * CHAR_WIDTH > width - 24)
  return (
    <div
      id={id}
      role="gridcell"
      data-cell
      data-row={row}
      data-col={col}
      aria-colindex={col + 2}
      aria-selected={selected}
      aria-busy={!loaded || undefined}
      title={cell?.kind === "absent" ? "Not present in this record" : undefined}
      className={cn(
        "absolute inset-y-0 flex items-center border-b border-border-muted px-3 font-mono text-xs text-fg",
        column.numeric && "justify-end tabular-nums",
        match && "bg-warning-muted",
        selected && "bg-accent-muted",
        active && "outline-2 -outline-offset-2 outline-accent",
      )}
      style={{ left, width }}
    >
      {!cell ? (
        <Skeleton depth="2" className="h-2 w-3/5" />
      ) : cell.kind === "null" ? (
        <span
          aria-label="null"
          className="rounded-sm border border-border px-1 text-2xs tracking-wider text-fg-subtle"
        >
          NULL
        </span>
      ) : cell.kind === "empty" ? (
        <span
          aria-label="empty string"
          className="rounded-sm border border-dashed border-border px-1 text-2xs font-light text-fg-subtle italic"
        >
          empty
        </span>
      ) : cell.kind === "absent" ? (
        <span className="sr-only">not present</span>
      ) : (
        <span className="min-w-0 truncate">{cell.text}</span>
      )}
      {active && overflows && (
        <button
          type="button"
          aria-label="Inspect the whole value"
          title="Inspect the whole value (Enter)"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => onInspect(row, col)}
          className="ml-1 shrink-0 cursor-pointer rounded-sm p-0.5 text-fg-subtle hover:bg-bg-muted hover:text-accent"
        >
          <Maximize2 aria-hidden className="h-3 w-3" />
        </button>
      )}
    </div>
  )
})

function EdgeShadow({
  side,
  show,
  offset,
  top,
}: {
  side: "start" | "end"
  show: boolean
  offset: number
  top: number
}) {
  return (
    <div
      aria-hidden
      className={cn(
        "pointer-events-none absolute bottom-0 z-30 w-10 from-scrim-edge to-transparent transition-opacity",
        side === "start" ? "bg-linear-to-r" : "bg-linear-to-l",
        show ? "opacity-100" : "opacity-0",
      )}
      style={side === "start" ? { left: offset, top } : { right: offset, top }}
    />
  )
}

function CellInspector({
  open,
  onOpenChange,
  anchor,
  row,
  column,
  loaded,
  value,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  anchor: HTMLElement | null
  row: number
  column: GridColumn
  loaded: boolean
  value: unknown
}) {
  const structured = loaded && isStructured(value)
  const text = !loaded
    ? "Not loaded yet."
    : structured
      ? prettyJson(value)
      : cellText(value, column).text
  const virtualRef = useRef({
    getBoundingClientRect: () => anchor?.getBoundingClientRect() ?? new DOMRect(),
  })
  return (
    <PopoverPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <PopoverPrimitive.Anchor virtualRef={virtualRef} />
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          side="bottom"
          align="start"
          sideOffset={4}
          collisionPadding={12}
          aria-label={`Row ${formatCount(row + 1)}, ${column.name}`}
          className="z-50 flex max-h-80 w-[min(32rem,90vw)] flex-col overflow-hidden rounded-card border border-border bg-bg-elevated shadow-xl"
        >
          <div className="flex items-center gap-2 border-b border-border bg-bg-muted px-3 py-2">
            <span className="min-w-0 truncate font-mono text-xs text-fg">
              {column.name}
              <span className="text-fg-subtle"> · row {formatCount(row + 1)}</span>
              {column.type && <span className="text-fg-subtle"> · {column.type}</span>}
            </span>
            <span className="ml-auto flex items-center gap-1">
              {loaded && <CopyButton value={text} noun="value" tone="inline" />}
              <PopoverPrimitive.Close
                aria-label="Close"
                className="flex h-6 w-6 cursor-pointer items-center justify-center rounded-control text-fg-subtle hover:bg-accent-muted hover:text-accent"
              >
                <X aria-hidden className="h-3.5 w-3.5" />
              </PopoverPrimitive.Close>
            </span>
          </div>
          <pre className="min-h-0 overflow-auto p-3 font-mono text-xs leading-relaxed wrap-break-word whitespace-pre-wrap text-fg">
            {text}
          </pre>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}

function modKey(): string {
  return typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform)
    ? "⌘"
    : "Ctrl "
}

function GridToolbar({
  find,
  onFind,
  matches,
  matchIndex,
  onStep,
  goTo,
  goToRef,
  onGoTo,
  onSubmitGoTo,
  goToNote,
  end,
}: {
  find: string
  onFind: (value: string) => void
  matches: number
  matchIndex: number
  onStep: (delta: number) => void
  goTo: string
  goToRef: React.RefObject<HTMLInputElement | null>
  onGoTo: (value: string) => void
  onSubmitGoTo: () => void
  goToNote: string | null
  end?: ReactNode
}) {
  const findId = useId()
  const goToId = useId()
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
      <div className="relative flex min-w-0 items-center">
        <Search
          aria-hidden
          className="pointer-events-none absolute left-2 h-3.5 w-3.5 text-fg-subtle"
        />
        <label htmlFor={findId} className="sr-only">
          Find in loaded rows
        </label>
        <input
          id={findId}
          type="search"
          value={find}
          onChange={(e) => onFind(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              onStep(e.shiftKey ? -1 : 1)
            }
          }}
          placeholder="Find in loaded rows"
          className="h-7 w-52 rounded-control border border-border bg-bg pr-2 pl-7 font-mono text-xs text-fg placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-none"
        />
      </div>
      {find.trim() !== "" && (
        <span className="flex items-center gap-1 font-mono text-2xs text-fg-muted" aria-live="polite">
          {matches === 0
            ? "no matches in loaded rows"
            : `${formatCount(matchIndex + 1)} of ${matches >= 1000 ? "1,000+" : formatCount(matches)} in loaded rows`}
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Previous match"
            disabled={matches === 0}
            onClick={() => onStep(-1)}
          >
            <ChevronUp aria-hidden className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Next match"
            disabled={matches === 0}
            onClick={() => onStep(1)}
          >
            <ChevronDown aria-hidden className="h-3.5 w-3.5" />
          </Button>
        </span>
      )}
      <form
        className="flex items-center gap-1.5"
        onSubmit={(e) => {
          e.preventDefault()
          onSubmitGoTo()
        }}
      >
        <label htmlFor={goToId} className="font-mono text-2xs text-fg-subtle">
          Go to row
        </label>
        <input
          id={goToId}
          ref={goToRef}
          inputMode="numeric"
          value={goTo}
          onChange={(e) => onGoTo(e.target.value)}
          aria-describedby={goToNote ? `${goToId}-note` : undefined}
          placeholder={`${modKey()}G`}
          className="h-7 w-24 rounded-control border border-border bg-bg px-2 font-mono text-xs text-fg placeholder:text-fg-subtle focus-visible:border-accent focus-visible:outline-none"
        />
        {goToNote && (
          <span id={`${goToId}-note`} className="font-mono text-2xs text-warning">
            {goToNote}
          </span>
        )}
      </form>
      {end && <div className="ml-auto flex items-center gap-2">{end}</div>}
    </div>
  )
}

function GridFooter({
  firstRow,
  lastRow,
  count,
  exact,
  indexing,
  onContinue,
  error,
  selection,
  changed,
  onReload,
}: {
  firstRow: number
  lastRow: number
  count: number
  exact: boolean
  indexing: RowSource["indexing"]
  onContinue?: () => void
  error: Error | null
  selection: { r0: number; r1: number; c0: number; c1: number } | null
  changed: boolean
  onReload?: () => void
}) {
  const span =
    count === 0
      ? "0 rows"
      : `rows ${formatCount(firstRow + 1)}–${formatCount(lastRow + 1)} of ${formatCount(count)}${exact ? "" : "+"}`
  const selected =
    selection && (selection.r1 > selection.r0 || selection.c1 > selection.c0)
      ? `${formatCount(selection.r1 - selection.r0 + 1)} × ${formatCount(selection.c1 - selection.c0 + 1)} selected`
      : null
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border bg-bg-muted px-3 py-1.5 font-mono text-2xs text-fg-muted">
      <span className="tabular-nums">{span}</span>
      {selected && <span className="text-fg-subtle">{selected}</span>}
      {error && (
        <span role="alert" className="text-danger">
          Some rows could not be read: {error.message}
        </span>
      )}
      {changed && (
        // The object was overwritten while open: rows read from here on
        // come from a different file than the rows already on screen.
        <span role="alert" className="flex items-center gap-2 text-warning">
          File changed since it was opened
          {onReload && (
            <Button variant="secondary" size="sm" onClick={onReload}>
              Reload
            </Button>
          )}
        </span>
      )}
      <IndexingStatusLine indexing={indexing} onContinue={onContinue} />
    </div>
  )
}

function IndexingStatusLine({
  indexing,
  onContinue,
}: {
  indexing: RowSource["indexing"]
  onContinue?: () => void
}) {
  if (!indexing) return null
  const rows = formatCount(indexing.rows)
  const bytes = `${formatBytes(indexing.bytes)} of ${formatBytes(indexing.totalBytes)}`
  switch (indexing.state) {
    case "running":
      return (
        <span className="ml-auto flex items-center gap-1.5" aria-live="polite">
          indexing · {rows} rows so far · {bytes}
          <BlinkingCursor />
        </span>
      )
    case "paused-limit":
      return (
        <span className="ml-auto flex items-center gap-2">
          indexed the first {formatBytes(indexing.bytes)} · {rows} rows
          {onContinue && (
            <Button variant="secondary" size="sm" onClick={onContinue}>
              Continue
            </Button>
          )}
        </span>
      )
    case "on-demand":
      return (
        <span className="ml-auto" title="Save-Data is on, so the file is not indexed in the background">
          Save-Data · indexing as you scroll · {rows} rows so far
        </span>
      )
    case "error":
      return (
        <span role="alert" className="ml-auto text-danger">
          Indexing stopped: {indexing.error}
        </span>
      )
    case "done":
      return null
  }
}
