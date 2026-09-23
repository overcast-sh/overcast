import { BaseSource } from "./base-source"
import type { DataColumn, RowBlock, RowSource } from "./row-source"

/**
 * Rows already in memory — a schema, a small query result. Exact and
 * instant, with nothing to fetch and nothing to dispose of.
 */
export function memorySource(
  columns: readonly DataColumn[],
  rows: readonly unknown[][],
): RowSource {
  return new MemorySource(columns, rows)
}

class MemorySource extends BaseSource {
  readonly columns: readonly DataColumn[]
  readonly projects = false
  private readonly rows: readonly unknown[][]

  constructor(columns: readonly DataColumn[], rows: readonly unknown[][]) {
    super()
    this.columns = columns
    this.rows = rows
    this.rowCount = { value: rows.length, exact: true }
  }

  getRows(start: number, end: number): Promise<RowBlock> {
    const slice = this.rows.slice(start, end)
    return Promise.resolve({
      start,
      count: slice.length,
      columns: this.columns.map((_, c) => slice.map((row) => row[c])),
    })
  }
}
