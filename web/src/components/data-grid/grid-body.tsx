import { formatCount } from "@/lib/format"
import { GridCell } from "./grid-cell"
import { cellId, inSelection, type Cell, type Selection } from "./grid-navigation"
import type { ValueAt } from "./loaded-rows"
import { ROW_HEIGHT } from "./scroll-model"
import type { LaidOutColumn } from "./use-grid-columns"

export interface GridBodyProps {
  /** Prefix for cell ids, so the cursor can be the grid's active descendant. */
  gridId: string
  firstRow: number
  lastRow: number
  /** How far the first row's top sits above the viewport. */
  offset: number
  /** Where the rows start: under the header. */
  top: number
  /** The columns in view. */
  columns: readonly LaidOutColumn[]
  rowNumberWidth: number
  scrollLeft: number
  valueAt: ValueAt
  cursor: Cell | null
  selection: Selection | null
  /** `row:col` of every Find match. */
  matches: ReadonlySet<string>
}

/**
 * The rows on screen, and only those: each a pinned row number and a track
 * of cells that slides with the horizontal scroll. The DOM holds a screenful
 * however many rows the file has.
 */
export function GridBody({
  gridId,
  firstRow,
  lastRow,
  offset,
  top,
  columns,
  rowNumberWidth,
  scrollLeft,
  valueAt,
  cursor,
  selection,
  matches,
}: GridBodyProps) {
  const rows = []
  for (let row = firstRow; row <= lastRow; row++) {
    rows.push(
      <div
        key={row}
        role="row"
        aria-rowindex={row + 2}
        className="group absolute inset-x-0"
        style={{ top: top + (row - firstRow) * ROW_HEIGHT - offset, height: ROW_HEIGHT }}
      >
        <div
          className="absolute inset-y-0 overflow-hidden"
          style={{ left: rowNumberWidth, right: 0 }}
        >
          <div className="absolute inset-y-0" style={{ transform: `translateX(${-scrollLeft}px)` }}>
            {columns.map(({ position, index, column, start, width }) => {
              const { loaded, value } = valueAt(row, index)
              return (
                <GridCell
                  key={index}
                  id={cellId(gridId, { row, col: position })}
                  row={row}
                  col={position}
                  index={index}
                  column={column}
                  left={start}
                  width={width}
                  loaded={loaded}
                  value={value}
                  selected={inSelection(selection, row, position)}
                  active={cursor?.row === row && cursor.col === position}
                  match={matches.has(`${row}:${position}`)}
                />
              )
            })}
          </div>
        </div>
        <div
          role="rowheader"
          className="absolute inset-y-0 left-0 flex items-center justify-end border-r border-b border-border-muted bg-bg-elevated pr-3 font-mono text-xs text-fg-subtle tabular-nums select-none group-hover:bg-bg-subtle"
          style={{ width: rowNumberWidth }}
        >
          {formatCount(row + 1)}
        </div>
      </div>,
    )
  }
  return rows
}
