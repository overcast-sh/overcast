import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import type { LaidOutColumn } from "./use-grid-columns"

/**
 * The header row: the pinned row-number corner, then the scrolling column
 * headers — each a name in the file's own case (upper-casing `userId` would
 * misreport it), its declared type under it, and a resize handle on its edge.
 */
export function GridHeader({
  columns,
  rowNumberWidth,
  scrollLeft,
  height,
}: {
  columns: readonly LaidOutColumn[]
  rowNumberWidth: number
  scrollLeft: number
  height: number
}) {
  return (
    <div
      role="row"
      aria-rowindex={1}
      className="absolute inset-x-0 top-0 z-20 border-b border-border bg-bg"
      style={{ height }}
    >
      <div
        className="absolute inset-y-0 overflow-hidden"
        style={{ left: rowNumberWidth, right: 0 }}
      >
        <div className="absolute inset-y-0" style={{ transform: `translateX(${-scrollLeft}px)` }}>
          {columns.map((column) => (
            <HeaderCell key={column.index} column={column} />
          ))}
        </div>
      </div>
      <div
        role="columnheader"
        aria-colindex={1}
        className={cn(
          fieldLabel,
          "absolute inset-y-0 left-0 flex items-end justify-end border-r border-border-muted bg-bg pr-3 pb-2 text-fg-subtle",
        )}
        style={{ width: rowNumberWidth }}
      >
        <span className="sr-only">Row</span>
        <span aria-hidden>#</span>
      </div>
    </div>
  )
}

function HeaderCell({ column: laidOut }: { column: LaidOutColumn }) {
  const { column, index, start, width, onResizeStart, resizing } = laidOut
  return (
    <div
      role="columnheader"
      aria-colindex={index + 2}
      title={column.type ? `${column.name} · ${column.type}` : column.name}
      className={cn(
        "absolute inset-y-0 flex flex-col justify-end border-r border-border-muted px-3 pb-2 font-mono",
        column.numeric && "items-end text-right",
      )}
      style={{ left: start, width }}
    >
      <span className="max-w-full truncate text-xs font-medium text-fg">{column.name}</span>
      {column.type && (
        <span className="max-w-full truncate text-2xs text-fg-subtle">{column.type}</span>
      )}
      <span
        aria-hidden
        onMouseDown={onResizeStart}
        onTouchStart={onResizeStart}
        className={cn(
          "absolute inset-y-1 -right-1 z-10 w-2 cursor-col-resize touch-none rounded-sm hover:bg-accent-muted",
          resizing && "bg-accent-muted",
        )}
      />
    </div>
  )
}
