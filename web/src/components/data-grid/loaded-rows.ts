import type { RowBlock } from "@/lib/data-sources/row-source"
import { clipboardText, formatCell } from "./cell-format"
import type { Cell, Selection } from "./grid-navigation"
import type { LaidOutColumn } from "./use-grid-columns"

/**
 * What the grid can do with the rows it has loaded, and only those: copy a
 * selection, and find text. Neither fetches — a selection or a search across
 * five million rows would mean reading the file, which is a query's job.
 */

/** The most rows one copy takes: a TSV of more is not a paste anyone wants. */
export const MAX_COPY_ROWS = 10_000

/** The most matches Find lists. */
export const MAX_MATCHES = 1000

export type ValueAt = (row: number, index: number) => { loaded: boolean; value: unknown }

export interface CopiedRows {
  /** Tab-separated values, a line per row. */
  text: string
  rows: number
  /** Selected rows left out because they were not loaded. */
  skipped: number
}

/** The selection as TSV, in display order, leaving out rows not loaded. */
export function selectionTsv(
  selection: Selection,
  columns: readonly LaidOutColumn[],
  valueAt: ValueAt,
): CopiedRows {
  const selected = columns.slice(selection.left, selection.right + 1)
  const bottom = Math.min(selection.bottom, selection.top + MAX_COPY_ROWS - 1)
  const lines: string[] = []
  let skipped = 0
  for (let row = selection.top; row <= bottom; row++) {
    const cells = selected.map(({ index, column }) => ({ ...valueAt(row, index), column }))
    if (cells.every((cell) => cell.loaded)) {
      lines.push(cells.map(({ value, column }) => clipboardText(value, column)).join("\t"))
    } else {
      skipped++
    }
  }
  return { text: lines.join("\n"), rows: lines.length, skipped }
}

/**
 * Cells whose text holds `query` (case-insensitive), in row then column
 * order, across the loaded blocks and the visible columns — at most
 * `MAX_MATCHES`. NULL, empty and absent cells are tokens, not text, and
 * never match.
 */
export function findInBlocks(
  blocks: readonly RowBlock[],
  columns: readonly LaidOutColumn[],
  query: string,
): Cell[] {
  const needle = query.trim().toLowerCase()
  if (needle === "") return []
  const matches: Cell[] = []
  for (const block of [...blocks].sort((a, b) => a.start - b.start)) {
    for (let i = 0; i < block.count; i++) {
      for (const { position, index, column } of columns) {
        const values = block.columns[index]
        if (!values) continue
        const cell = formatCell(values[i], column)
        if (cell.kind === "value" && cell.text.toLowerCase().includes(needle)) {
          matches.push({ row: block.start + i, col: position })
          if (matches.length === MAX_MATCHES) return matches
        }
      }
    }
  }
  return matches
}
