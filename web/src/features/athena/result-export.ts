import { clipboardText } from "@/components/data-grid/cell-format"
import type { DataColumn, RowSource } from "@/lib/data-sources/row-source"

/**
 * A query result copied as CSV, TSV, JSON or Markdown. Only a result held in
 * memory (paged through `GetQueryResults`) is copied whole; a large one is
 * downloaded as the CSV Athena wrote instead.
 */

export type ResultFormat = "csv" | "tsv" | "json" | "markdown"

export const RESULT_FORMATS: { format: ResultFormat; label: string }[] = [
  { format: "csv", label: "Copy as CSV" },
  { format: "tsv", label: "Copy as TSV" },
  { format: "json", label: "Copy as JSON" },
  { format: "markdown", label: "Copy as Markdown" },
]

export interface ResultRows {
  columns: readonly DataColumn[]
  rows: readonly (readonly unknown[])[]
}

/** Every row of a source, block by block: for a result small enough to hold. */
export async function readAllRows(source: RowSource, signal: AbortSignal): Promise<ResultRows> {
  const count = source.rowCount.value
  const cols = source.columns.map((_, i) => i)
  const rows: unknown[][] = []
  for (let start = 0; start < count; start += source.blockSize) {
    const block = await source.getRows(
      start,
      Math.min(start + source.blockSize, count),
      cols,
      signal,
    )
    for (let r = 0; r < block.count; r++) rows.push(cols.map((c) => block.columns[c]?.[r]))
  }
  return { columns: source.columns, rows }
}

/** RFC 4180: quoted when it holds a comma, a quote or a line break. */
function csvField(text: string): string {
  return /[",\r\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}

function markdownField(text: string): string {
  return text.replaceAll("|", "\\|").replace(/\r?\n/g, " ")
}

/** A value as JSON carries it: bigints as numbers when exact, bytes as their summary text. */
function jsonValue(value: unknown, column: DataColumn): unknown {
  if (value === null || value === undefined) return null
  if (typeof value === "bigint") {
    return Number.isSafeInteger(Number(value)) ? Number(value) : value.toString()
  }
  if (value instanceof Uint8Array || value instanceof Date) return clipboardText(value, column)
  return value
}

export function formatResult({ columns, rows }: ResultRows, format: ResultFormat): string {
  const names = columns.map((c) => c.name)
  const cells = rows.map((row) => columns.map((column, i) => clipboardText(row[i], column)))
  switch (format) {
    case "csv":
      return [names, ...cells].map((line) => line.map(csvField).join(",")).join("\n")
    case "tsv":
      return [names, ...cells].map((line) => line.join("\t")).join("\n")
    case "json":
      return JSON.stringify(
        rows.map((row) =>
          Object.fromEntries(columns.map((c, i) => [c.name, jsonValue(row[i], c)])),
        ),
        null,
        2,
      )
    case "markdown": {
      const line = (fields: string[]) => `| ${fields.map(markdownField).join(" | ")} |`
      const rule = columns.map((c) => (c.numeric ? "---:" : "---"))
      return [line(names), `| ${rule.join(" | ")} |`, ...cells.map(line)].join("\n")
    }
  }
}
