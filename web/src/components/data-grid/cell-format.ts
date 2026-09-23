import type { DataColumn } from "@/lib/data-sources/row-source"

/**
 * Turning a cell's value into text, at the last moment.
 *
 * Values stay as the row source produced them (`string`, `number`, `bigint`,
 * `boolean`, `Date`, bytes, arrays, objects). Three kinds of nothing are kept
 * apart because they mean different things in the file:
 *
 * - `null` — a real NULL (Parquet, JSON `null`);
 * - `undefined` — the record has no such field at all (a JSON Lines record
 *   that omits a key another record has);
 * - `""` — an empty string, which in a CSV is the only kind of nothing there is.
 */

/**
 * Characters of one cell kept for display. A 1 MiB single-field row is a
 * legal CSV; drawing it — or putting it in a `title` — would stall the grid.
 * The cell inspector shows up to `INSPECT_CHARS`.
 */
export const CELL_CHARS = 500
export const INSPECT_CHARS = 64 * 1024

/** One character of JetBrains Mono at 12 px: what the grid's widths are measured in. */
export const MONO_CHAR_WIDTH = 7.25

export type CellKind = "value" | "null" | "absent" | "empty"

export interface FormattedCell {
  kind: CellKind
  text: string
  /** The text was shortened. */
  clipped?: boolean
}

/** A cell as the grid draws it: one line, at most `CELL_CHARS`. */
export function formatCell(value: unknown, column?: DataColumn): FormattedCell {
  return cellText(value, column, CELL_CHARS)
}

/** The value as text, up to `limit` characters — `INSPECT_CHARS` for the inspector. */
export function cellText(
  value: unknown,
  column?: DataColumn,
  limit = INSPECT_CHARS,
): FormattedCell {
  if (value === null) return { kind: "null", text: "NULL" }
  if (value === undefined) return { kind: "absent", text: "" }
  if (value === "") return { kind: "empty", text: "empty" }
  const text = valueText(value, column)
  return text.length > limit
    ? { kind: "value", text: `${text.slice(0, limit)}…`, clipped: true }
    : { kind: "value", text }
}

/** What a cell puts on the clipboard: the value, `NULL` for a NULL, nothing for absence. */
export function clipboardText(value: unknown, column?: DataColumn): string {
  const cell = cellText(value, column)
  if (cell.kind !== "value") return cell.kind === "null" ? "NULL" : ""
  // TSV: a tab or a line break inside a value would split it.
  return cell.text.replace(/[\t\n\r]+/g, " ")
}

function valueText(value: unknown, column?: DataColumn): string {
  if (typeof value === "string") return value
  if (typeof value === "number") {
    if (column?.scale !== undefined && Number.isFinite(value)) return value.toFixed(column.scale)
    return String(value)
  }
  if (typeof value === "bigint" || typeof value === "boolean") return String(value)
  if (value instanceof Date) return dateText(value, column?.dateOnly)
  if (value instanceof Uint8Array) return bytesSummary(value)
  return compactJson(value)
}

function dateText(value: Date, dateOnly = false): string {
  if (Number.isNaN(value.getTime())) return "Invalid date"
  const iso = value.toISOString()
  // A DATE is a calendar day with no zone; its midnight-UTC Date is an
  // artefact of how JavaScript carries it.
  return dateOnly ? iso.slice(0, 10) : iso
}

/** `8 B · 0a1b2c3d4e5f6071` — a length and the first bytes in hex. */
function bytesSummary(bytes: Uint8Array): string {
  const head = Array.from(bytes.subarray(0, 16), (b) => b.toString(16).padStart(2, "0")).join("")
  return `${bytes.length} B · ${head}${bytes.length > 16 ? "…" : ""}`
}

/** JSON with the values JSON cannot carry spelled out: bigints as digits, bytes as their summary. */
function jsonReplacer(_key: string, v: unknown): unknown {
  if (typeof v === "bigint") return Number.isSafeInteger(Number(v)) ? Number(v) : v.toString()
  if (v instanceof Uint8Array) return bytesSummary(v)
  if (v === undefined) return null
  return v
}

/** Lists and structs, one line each: `["gift","express"]`, `{"city":"London"}`. */
function compactJson(value: unknown): string {
  try {
    return JSON.stringify(value, jsonReplacer)
  } catch {
    return String(value)
  }
}

/** Whether a value deserves an indented tree in the inspector rather than a line. */
export function isStructured(value: unknown): boolean {
  return (
    typeof value === "object" &&
    value !== null &&
    !(value instanceof Date) &&
    !(value instanceof Uint8Array)
  )
}

/** Indented JSON for the inspector, with the same spellings the cells use. */
export function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value, jsonReplacer, 2)
  } catch {
    return String(value)
  }
}

/**
 * A starting width for a column, from its name, its type and a sample of its
 * values: the longest of them in mono characters, clamped so one long value
 * cannot make a column the width of the screen. The user resizes from there.
 */
export function sampleColumnWidth(
  column: DataColumn,
  sample: ArrayLike<unknown> | undefined,
): number {
  const PADDING = 26
  let longest = Math.max(column.name.length, (column.type?.length ?? 0) * 0.92)
  if (sample) {
    const n = Math.min(sample.length, 200)
    for (let i = 0; i < n; i++) {
      const cell = formatCell(sample[i], column)
      const length = cell.kind === "value" ? cell.text.length : 5
      if (length > longest) longest = length
    }
  }
  return Math.round(Math.min(Math.max(longest * MONO_CHAR_WIDTH + PADDING, 64), 320))
}
