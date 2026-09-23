import { AlertTriangle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { BlinkingCursor } from "@/components/ui/skeleton"
import { formatBytes, formatCount, formatQuantity } from "@/lib/format"
import type { IndexingStatus, RowCount } from "@/lib/data-sources/row-source"
import type { Selection } from "./grid-navigation"

/**
 * The status line under the grid: which rows are on screen of how many, the
 * selection's size, a read that failed, and how far indexing has got.
 */
export function GridFooter({
  firstRow,
  lastRow,
  rowCount,
  selection,
  error,
  indexing,
  onContinue,
}: {
  firstRow: number
  lastRow: number
  rowCount: RowCount
  selection: Selection | null
  error: Error | null
  indexing?: IndexingStatus
  onContinue?: () => void
}) {
  const total = `${formatCount(rowCount.value)}${rowCount.exact ? "" : "+"}`
  const span =
    rowCount.value === 0
      ? "no rows"
      : `rows ${formatCount(firstRow + 1)}–${formatCount(lastRow + 1)} of ${total}`
  const rows = selection ? selection.bottom - selection.top + 1 : 0
  const columns = selection ? selection.right - selection.left + 1 : 0
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border bg-bg-muted px-3 py-1.5 font-mono text-2xs text-fg-muted">
      <span className="tabular-nums">{span}</span>
      {rows * columns > 1 && (
        <span className="text-fg-subtle">
          {formatQuantity(rows, "row")} × {formatQuantity(columns, "column")} selected
        </span>
      )}
      {error && (
        <span role="alert" className="text-danger">
          Some rows could not be read: {error.message}
        </span>
      )}
      {indexing && <IndexingLine indexing={indexing} onContinue={onContinue} />}
    </div>
  )
}

function IndexingLine({
  indexing,
  onContinue,
}: {
  indexing: IndexingStatus
  onContinue?: () => void
}) {
  const rows = formatQuantity(indexing.rows, "row")
  switch (indexing.state) {
    case "running":
      return (
        <span className="ml-auto flex items-center gap-1.5">
          indexing · {rows} so far · {formatBytes(indexing.bytes)} of{" "}
          {formatBytes(indexing.totalBytes)}
          <BlinkingCursor />
        </span>
      )
    case "paused-limit":
      return (
        <span className="ml-auto flex items-center gap-2">
          indexed the first {formatBytes(indexing.bytes)} of {formatBytes(indexing.totalBytes)} ·{" "}
          {rows}
          {onContinue && (
            <Button variant="secondary" size="sm" onClick={onContinue}>
              Continue
            </Button>
          )}
        </span>
      )
    case "on-demand":
      return (
        <span className="ml-auto">Save-Data is on: indexing as you scroll · {rows} so far</span>
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

/**
 * The object was overwritten while open. Rows read from now on come from a
 * different file than the rows already on screen, so the grid says so where
 * it cannot be missed, and offers to start again.
 */
export function FileChangedNotice({ onReload }: { onReload?: () => void }) {
  return (
    <div
      role="alert"
      className="flex items-center gap-2 border-b border-warning/40 bg-warning-muted px-3 py-2 text-xs text-fg-muted"
    >
      <AlertTriangle aria-hidden className="size-3.5 shrink-0 text-warning" />
      <span className="min-w-0 flex-1">
        This file changed since it was opened, so new rows may not match the ones on screen.
      </span>
      {onReload && (
        <Button variant="secondary" size="sm" onClick={onReload}>
          Reload
        </Button>
      )}
    </div>
  )
}
