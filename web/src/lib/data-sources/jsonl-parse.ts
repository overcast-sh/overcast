import { isRecord } from "@/lib/utils"

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

export type JsonlRecords =
  | { ok: true; records: unknown[]; recordCount: number; consumedChars: number }
  | { ok: false; reason: string }

/**
 * Parses up to `maxRecords` lines, counting (not keeping) the rest. A line
 * that is not JSON makes the whole file not JSON Lines — except the last line
 * of a truncated window, which the cut went through.
 */
export function parseJsonl(text: string, maxRecords: number, truncated: boolean): JsonlRecords {
  const records: unknown[] = []
  let recordCount = 0
  let consumedChars = 0
  let start = text.charCodeAt(0) === 0xfeff ? 1 : 0
  while (start < text.length) {
    let end = text.indexOf("\n", start)
    const lastLine = end === -1
    if (lastLine) end = text.length
    // A window that ends without a newline ends mid-record.
    if (lastLine && truncated) break
    const line = text.slice(start, end).trim()
    if (line !== "") {
      recordCount++
      if (records.length < maxRecords) {
        try {
          records.push(JSON.parse(line))
        } catch {
          return { ok: false, reason: `Line ${lineNumber(text, start)} is not valid JSON.` }
        }
      }
    }
    consumedChars = end + 1
    start = end + 1
  }
  return { ok: true, records, recordCount, consumedChars: Math.min(consumedChars, text.length) }
}

function lineNumber(text: string, offset: number): number {
  let line = 1
  for (let i = 0; i < offset; i++) if (text.charCodeAt(i) === 10) line++
  return line
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
