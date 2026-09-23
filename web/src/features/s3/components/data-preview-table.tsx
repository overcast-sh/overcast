// eslint-disable-next-line local/prefer-resource-table -- a data file's first rows: columns named by the file, no row identity, nothing to act on
import {
  Table,
  TableBody,
  TableCell,
  TableEmpty,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { ThHTMLAttributes } from "react"
import { formatPreviewCell, type PreviewColumn, type PreviewTableModel } from "../preview-table"
import { cn } from "@/lib/utils"

/**
 * The first rows of a data file — CSV, TSV, JSON Lines, Parquet — as a grid.
 *
 * ResourceTable didn't fit because this is not a list of resources: the
 * columns are whatever the file declares (hundreds, possibly, and named by
 * someone else), rows have no identity to key, click or delete by, and the
 * states ResourceTable owns — loading, filtered-empty, row actions — belong to
 * the preview panel around it. It is the same exception as the Athena result
 * grid the data-lake design names, composed from the same primitives so the
 * header, cell and scroll treatment match every other table.
 *
 * What it adds to those primitives:
 * - column names keep the file's own case — they are data, and uppercasing
 *   `userId` to `USERID` would misreport it — with the declared type, when
 *   there is one, on a second line;
 * - numbers right-align in tabular figures;
 * - NULL, an empty string and a missing field each read differently;
 * - the header sticks while the body scrolls, inside the table's own scroller,
 *   and a wide table scrolls sideways there rather than widening the dialog.
 */
export function DataPreviewTable({
  table,
  label,
  emptyMessage = "No rows.",
  className,
}: {
  table: PreviewTableModel
  /** Accessible name — the table has no visible caption of its own. */
  label: string
  /** What an empty body says: a CSV with only a header is not a Parquet file with no rows. */
  emptyMessage?: string
  className?: string
}) {
  return (
    <Table
      aria-label={label}
      // `border-separate` because a sticky cell cannot carry a collapsed
      // border: it scrolls away from the cell it belonged to. Every rule is
      // drawn on the cells instead.
      className="border-separate border-spacing-0"
      scrollerClassName={cn("max-h-[55vh]", className)}
    >
      <TableHeader className="border-0">
        <TableRow className="border-0">
          <HeaderCell className="w-px text-right">
            <span className="sr-only">Row</span>#
          </HeaderCell>
          {table.columns.map((column, index) => (
            <HeaderCell
              key={index}
              title={column.type ? `${column.name} · ${column.type}` : column.name}
              className={cn(column.numeric && "text-right")}
            >
              <span className="block max-w-60 truncate text-fg">{column.name}</span>
              {column.type && (
                <span className="block max-w-60 truncate text-2xs tracking-normal text-fg-subtle">
                  {column.type}
                </span>
              )}
            </HeaderCell>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {table.rows.length === 0 ? (
          <TableEmpty colSpan={table.columns.length + 1}>{emptyMessage}</TableEmpty>
        ) : (
          table.rows.map((row, r) => (
            <TableRow
              key={r}
              className="border-0 *:border-b *:border-border-muted last:*:border-b-0 hover:bg-bg-subtle"
            >
              <TableCell className="w-px px-3 py-1.5 text-right text-fg-subtle tabular-nums select-none">
                {r + 1}
              </TableCell>
              {table.columns.map((column, c) => (
                <DataCell key={c} value={row[c]} column={column} />
              ))}
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  )
}

function HeaderCell({ className, ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <TableHead
      className={cn(
        "sticky top-0 z-10 border-b border-border bg-bg px-3 py-2 align-bottom font-medium",
        // The field-label spec's uppercase is for labels the console wrote;
        // these are the file's own names.
        "tracking-normal normal-case",
        className,
      )}
      {...props}
    />
  )
}

function DataCell({ value, column }: { value: unknown; column: PreviewColumn }) {
  const cell = formatPreviewCell(value, column)
  return (
    <TableCell
      title={cell.kind === "absent" ? "Not present in this record" : undefined}
      className={cn("px-3 py-1.5 whitespace-nowrap", column.numeric && "text-right tabular-nums")}
    >
      {cell.kind === "null" ? (
        <span
          title="NULL"
          className="rounded-sm border border-border px-1 text-2xs tracking-wider text-fg-subtle"
        >
          NULL
        </span>
      ) : cell.kind === "empty" ? (
        <span title="Empty string" className="text-fg-subtle opacity-70">
          {cell.text}
        </span>
      ) : cell.kind === "absent" ? (
        <span className="sr-only">not present</span>
      ) : (
        // One line per row whatever the value holds; the title carries the
        // rest, embedded newlines included.
        <span title={cell.title ?? cell.text} className="block max-w-80 truncate">
          {cell.text}
        </span>
      )}
    </TableCell>
  )
}
