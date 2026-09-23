import { isRecord } from "@/lib/utils"
import { bomLength } from "./byte-order-mark"

/**
 * JSON Lines as a table — when, and only when, it is one.
 *
 * A JSON Lines file is a table when its records are objects describing the
 * same thing. Many are not: a log of heterogeneous events, a stream of arrays,
 * one record per line of wildly different shapes. Tabulating those produces a
 * grid that is mostly holes and says less than the raw lines, so the table is
 * offered only for records `uniformColumns` accepts and the raw view stays the
 * answer for the rest.
 */

export type JsonlRecords = { ok: true; records: unknown[] } | { ok: false; reason: string }

interface JsonlOptions {
  /** Records read; parsing stops once this many are complete. */
  maxRecords?: number
  /**
   * The text is the opening window of a longer object, so its last line is
   * the one the cut went through: dropped rather than failed on.
   */
  truncated?: boolean
}

/**
 * One JSON value per non-blank line. A line that is not JSON makes the text
 * not JSON Lines — except the last line of a truncated window.
 */
export function parseJsonl(
  text: string,
  { maxRecords = Infinity, truncated = false }: JsonlOptions = {},
): JsonlRecords {
  const records: unknown[] = []
  let start = bomLength(text)
  while (start < text.length && records.length < maxRecords) {
    let end = text.indexOf("\n", start)
    if (end === -1) {
      if (truncated) break
      end = text.length
    }
    const line = text.slice(start, end).trim()
    if (line !== "") {
      try {
        records.push(JSON.parse(line))
      } catch {
        return { ok: false, reason: `Line ${lineNumber(text, start)} is not valid JSON.` }
      }
    }
    start = end + 1
  }
  return { ok: true, records }
}

function lineNumber(text: string, offset: number): number {
  let line = 1
  for (let i = 0; i < offset; i++) if (text.charCodeAt(i) === 10) line++
  return line
}

/**
 * Records to columns: one array per key, holding each record's value for it.
 * A key a record omits is `undefined` — "not present", which the grid tells
 * apart from a JSON `null`.
 */
export function recordColumns(records: readonly unknown[], keys: readonly string[]): unknown[][] {
  return keys.map((key) => records.map((record) => (isRecord(record) ? record[key] : undefined)))
}

/**
 * The columns a set of records shares, in first-seen order — or null when the
 * records are not a table.
 *
 * Uniform means every record is a JSON object and every record carries more
 * than half of the union of keys. That admits the common shape of optional
 * fields (a key present on most records and omitted on some, which renders as
 * a blank cell) and refuses a mixed stream of event types, where each record
 * would fill a different corner of the grid.
 */
export function uniformColumns(records: readonly unknown[]): string[] | null {
  if (records.length === 0) return null
  const union = new Set<string>()
  const widths: number[] = []
  for (const record of records) {
    if (!isRecord(record)) return null
    const keys = Object.keys(record)
    for (const key of keys) union.add(key)
    widths.push(keys.length)
  }
  if (union.size === 0) return null
  return widths.every((width) => width > union.size / 2) ? [...union] : null
}
