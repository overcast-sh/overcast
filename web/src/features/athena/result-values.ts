import type { ColumnInfo, GetQueryResultsOutput } from "@aws-sdk/client-athena"
import type { AthenaResultPage } from "@/lib/data-sources/athena-result-source"
import type { DataColumn } from "@/lib/data-sources/row-source"

/**
 * `GetQueryResults` pages in the grid's terms: typed columns, and values
 * turned back from the text Athena renders them as into what they are, so a
 * number right-aligns, a NULL reads as NULL rather than as an empty string,
 * and an array opens as a tree in the cell inspector.
 */

const INTEGER_TYPES = new Set(["tinyint", "smallint", "integer", "int", "bigint"])
const FLOAT_TYPES = new Set(["real", "float", "double"])
const COMPLEX_TYPES = new Set(["array", "map", "row"])

export function resultColumn(info: ColumnInfo): DataColumn {
  const type = (info.Type ?? "").toLowerCase()
  return {
    name: info.Name ?? info.Label ?? "",
    type: type === "decimal" ? `decimal(${info.Precision ?? 38},${info.Scale ?? 0})` : type,
    numeric: INTEGER_TYPES.has(type) || FLOAT_TYPES.has(type) || type === "decimal",
    dateOnly: type === "date" || undefined,
  }
}

/** An integer that fits a double exactly stays a number; a larger one is a bigint. */
function integer(text: string): number | bigint | string {
  const n = Number(text)
  if (Number.isSafeInteger(n)) return n
  try {
    return BigInt(text)
  } catch {
    return text
  }
}

/** `01 ff 2a` — Athena's rendering of a varbinary — as bytes. */
function hexBytes(text: string): Uint8Array | string {
  const pairs = text.trim() === "" ? [] : text.trim().split(/\s+/)
  if (pairs.some((p) => !/^[\da-f]{2}$/i.test(p))) return text
  return Uint8Array.from(pairs, (p) => parseInt(p, 16))
}

function json(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return text
  }
}

/**
 * One value, from the text `GetQueryResults` carries. A missing
 * `VarCharValue` is a NULL. A DECIMAL stays the text Athena wrote — a
 * `decimal(38,2)` has more digits than a double holds — and so do the types
 * the grid has nothing to add to: dates, timestamps with their zone,
 * intervals.
 */
export function resultValue(text: string | undefined, type: string): unknown {
  if (text === undefined) return null
  if (INTEGER_TYPES.has(type)) return integer(text)
  if (FLOAT_TYPES.has(type)) return Number(text)
  if (type === "boolean") return text === "true"
  if (type === "varbinary") return hexBytes(text)
  if (type === "json") return json(text)
  if (COMPLEX_TYPES.has(type)) return parseComplexValue(text) ?? text
  return text
}

/**
 * A page of `GetQueryResults` as the result source takes it. A `SELECT`'s
 * first page opens with a header row of column names, which `hasHeader`
 * drops; DDL and utility statements have none.
 */
export function resultPage(output: GetQueryResultsOutput, hasHeader: boolean): AthenaResultPage {
  const info = output.ResultSet?.ResultSetMetadata?.ColumnInfo ?? []
  const columns = info.map(resultColumn)
  const types = info.map((c) => (c.Type ?? "").toLowerCase())
  const rows = (output.ResultSet?.Rows ?? []).slice(hasHeader ? 1 : 0)
  return {
    columns,
    rows: rows.map((row) => types.map((type, i) => resultValue(row.Data?.[i]?.VarCharValue, type))),
    nextToken: output.NextToken,
  }
}

// ─── Arrays, maps and rows ─────────────────────────────────────────────────

/**
 * Athena renders an array as `[a, b]` and a map or a row as `{k=v, …}`, with
 * nothing quoted. This reads that back into a list or an object so the cell
 * inspector can show it as a tree.
 *
 * It is a best effort, because the text is ambiguous: a string element that
 * itself contains `, ` or a bracket reads differently from how it was
 * written. Elements stay strings (the column type does not say what they
 * are), `null` reads as null, and text that does not parse to the end is
 * left alone — `undefined` — so the cell shows it as Athena wrote it.
 */
export function parseComplexValue(text: string): unknown[] | Record<string, unknown> | undefined {
  const reader = { text, at: 0 }
  const value = readContainer(reader)
  return value !== undefined && reader.at === text.length ? value : undefined
}

interface Reader {
  text: string
  at: number
}

function readContainer(r: Reader): unknown[] | Record<string, unknown> | undefined {
  const open = r.text[r.at]
  if (open !== "[" && open !== "{") return undefined
  const close = open === "[" ? "]" : "}"
  r.at++
  const list: unknown[] = []
  const object: Record<string, unknown> = {}
  if (r.text[r.at] === close) {
    r.at++
    return open === "[" ? list : object
  }
  for (;;) {
    if (open === "[") {
      const item = readElement(r, close)
      if (item === READ_FAILED) return undefined
      list.push(item)
    } else {
      const eq = r.text.indexOf("=", r.at)
      if (eq < 0) return undefined
      const key = r.text.slice(r.at, eq)
      r.at = eq + 1
      const item = readElement(r, close)
      if (item === READ_FAILED) return undefined
      object[key] = item
    }
    if (r.text.startsWith(", ", r.at)) {
      r.at += 2
      continue
    }
    if (r.text[r.at] !== close) return undefined
    r.at++
    return open === "[" ? list : object
  }
}

const READ_FAILED = Symbol("read failed")

/** One element: a nested container, or text up to the next `, ` or the container's end. */
function readElement(r: Reader, close: string): unknown {
  const first = r.text[r.at]
  if (first === "[" || first === "{") return readContainer(r) ?? READ_FAILED
  let end = r.at
  while (end < r.text.length && r.text[end] !== close && !r.text.startsWith(", ", end)) end++
  const leaf = r.text.slice(r.at, end)
  r.at = end
  return leaf === "null" ? null : leaf
}
