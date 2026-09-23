import { formatCount } from "@/lib/format"

/**
 * The tabular model every data preview renders through — CSV, TSV, JSON Lines
 * and Parquet all reduce to columns plus the first rows, and the table on
 * screen does not know which of them it is showing.
 *
 * Cell values stay as the reader produced them (`string`, `number`, `bigint`,
 * `boolean`, `Date`, bytes, arrays, objects) and are turned into text here, at
 * the last moment, by `formatPreviewCell`. Three kinds of nothing are kept
 * apart because they mean different things in the file:
 *
 * - `null` — a real NULL (Parquet, JSON `null`);
 * - `undefined` — the record has no such field at all (a JSON Lines record
 *   that omits a key another record has);
 * - `""` — an empty string, which in a CSV is the only kind of nothing there is.
 */

/** Rows shown by every tabular preview. The table is a peek, not a browser. */
export const PREVIEW_ROW_LIMIT = 200

/** Columns shown before the rest are summarised as "and N more". */
export const PREVIEW_COLUMN_LIMIT = 200

/**
 * Characters of one cell kept for display. A 1 MiB single-field row is a
 * legal CSV; drawing it — or putting it in a `title` — would stall the dialog.
 */
export const PREVIEW_CELL_CHARS = 500

export interface PreviewColumn {
  name: string
  /** Declared type, where the format has one (Parquet); shown in the header's title. */
  type?: string
  /** Right-align: every value in the column is a number. */
  numeric: boolean
  /**
   * Digits after the point for a DECIMAL column. The reader hands decimals
   * over as doubles, and `19.99` must not render as `19.990000000000002`.
   */
  scale?: number
  /** Parquet DATE: a calendar day, rendered without a time of day. */
  dateOnly?: boolean
}

export interface PreviewTableModel {
  columns: PreviewColumn[]
  /** At most `PREVIEW_ROW_LIMIT` rows, each as long as `columns`. */
  rows: unknown[][]
  /**
   * Rows in the whole object, when that can be said. Exact for Parquet (the
   * footer counts them) and for a text file read to its end; an estimate when
   * the text was cut at the byte cap — see `totalIsEstimate`.
   */
  totalRows?: number
  totalIsEstimate: boolean
  /** The text behind the table was cut at the preview's byte cap. */
  truncatedByBytes: boolean
  /** Columns left out past `PREVIEW_COLUMN_LIMIT`. */
  hiddenColumns: number
}

export type PreviewCellKind = "value" | "null" | "absent" | "empty"

export interface FormattedCell {
  kind: PreviewCellKind
  text: string
  /** Set when the text had to be shortened, so the full value (up to the cap) is still reachable. */
  title?: string
}

/**
 * The caption above a table: how many rows are on screen and of how many.
 * `first 200 rows of 1,204`, `first 200 rows of ~48,000`, `first 200 rows`,
 * or `12 rows` when the whole object fitted.
 */
export function describeRowCount(model: PreviewTableModel): string {
  const shown = model.rows.length
  const noun = (n: number) => (n === 1 ? "row" : "rows")
  const total = model.totalRows
  if (total !== undefined && !model.totalIsEstimate && total <= shown) {
    return `${formatCount(shown)} ${noun(shown)}`
  }
  if (total !== undefined && total > shown) {
    const approx = model.totalIsEstimate ? "~" : ""
    return `first ${formatCount(shown)} ${noun(shown)} of ${approx}${formatCount(total)}`
  }
  return `first ${formatCount(shown)} ${noun(shown)}`
}

/**
 * Rounds an estimate to two significant figures — `~48,000`, not `~48,217`,
 * which would claim a precision a byte-ratio extrapolation does not have.
 */
export function roundEstimate(n: number): number {
  if (n < 100) return Math.round(n)
  const magnitude = 10 ** (Math.floor(Math.log10(n)) - 1)
  return Math.round(n / magnitude) * magnitude
}

/** A number as a CSV spells it: optional sign, digits, point, exponent. */
const NUMERIC_TEXT = /^[-+]?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$/

/**
 * True when every non-empty value in the column is a number (and there is at
 * least one), so the column right-aligns. Leading zeros make it text: `007`
 * and `02134` are identifiers, and right-aligning them reads as arithmetic.
 *
 * `numericText: false` is for typed sources (JSON), where `"42"` is a string
 * someone chose to quote and is not a number however it reads.
 */
export function isNumericColumn(
  values: readonly unknown[],
  { numericText = true }: { numericText?: boolean } = {},
): boolean {
  let seen = false
  for (const value of values) {
    if (value === null || value === undefined || value === "") continue
    if (typeof value === "number" || typeof value === "bigint") {
      seen = true
      continue
    }
    if (typeof value !== "string" || !numericText) return false
    const trimmed = value.trim()
    if (!NUMERIC_TEXT.test(trimmed) || /^[-+]?0\d/.test(trimmed)) return false
    seen = true
  }
  return seen
}

export function formatPreviewCell(value: unknown, column?: PreviewColumn): FormattedCell {
  if (value === null) return { kind: "null", text: "NULL" }
  if (value === undefined) return { kind: "absent", text: "" }
  if (value === "") return { kind: "empty", text: "empty" }
  return clip(valueText(value, column))
}

function clip(text: string): FormattedCell {
  if (text.length <= PREVIEW_CELL_CHARS) return { kind: "value", text }
  return {
    kind: "value",
    text: `${text.slice(0, PREVIEW_CELL_CHARS)}…`,
    title: text.slice(0, 4000),
  }
}

function valueText(value: unknown, column?: PreviewColumn): string {
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

/**
 * Lists and structs, one line each: `["gift","express"]`, `{"city":"London"}`.
 * Values JSON cannot carry are spelled out rather than dropped — a bigint as
 * its digits, a date as ISO text, bytes as their summary.
 */
function compactJson(value: unknown): string {
  try {
    return JSON.stringify(value, (_key, v: unknown) => {
      if (typeof v === "bigint") return Number.isSafeInteger(Number(v)) ? Number(v) : v.toString()
      if (v instanceof Uint8Array) return bytesSummary(v)
      if (v === undefined) return null
      return v
    })
  } catch {
    return String(value)
  }
}
