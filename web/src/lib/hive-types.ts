/**
 * Hive column types — the type strings Glue tables and Athena's Hive DDL use
 * (`bigint`, `double`, `array<string>`, `struct<a:int>`) — for data the
 * console has read: a Parquet schema, or sampled CSV and JSON Lines values.
 *
 * What is inferred follows what a Glue crawler would register for the same
 * file, and stops where the file stops saying anything: a text value is only
 * typed when every sampled value agrees, and `string` is always safe.
 */

import { isRecord } from "@/lib/utils"

/** An integer as CSV spells it. Leading zeros (`007`) make it an identifier, not a number. */
const INTEGER_TEXT = /^[-+]?(0|[1-9]\d*)$/
const DECIMAL_TEXT = /^[-+]?(\d+\.\d*|\.\d+|\d+)([eE][-+]?\d+)?$/
const LEADING_ZERO = /^[-+]?0\d/
const DATE_TEXT = /^\d{4}-\d{2}-\d{2}$/
/**
 * Hive's timestamp text, `yyyy-MM-dd HH:mm:ss[.f]`. An ISO `T` between date
 * and time is not it: LazySimpleSerDe reads such a value as NULL, so a
 * crawler leaves that column a string, and so does this.
 */
const TIMESTAMP_TEXT = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{1,9})?$/
const BOOLEAN_TEXT = /^(true|false)$/i

/** Integers are `bigint`, as a crawler registers them: `int` would break on the first large value. */
type TextType = "boolean" | "bigint" | "double" | "date" | "timestamp" | "string"

/** The narrowest type every non-empty value reads as, in the order a crawler tries them. */
const TEXT_TYPES: readonly [TextType, (value: string) => boolean][] = [
  ["boolean", (v) => BOOLEAN_TEXT.test(v)],
  ["bigint", (v) => INTEGER_TEXT.test(v) && Number.isSafeInteger(Number(v))],
  ["double", (v) => DECIMAL_TEXT.test(v) && !LEADING_ZERO.test(v)],
  ["date", (v) => DATE_TEXT.test(v)],
  ["timestamp", (v) => TIMESTAMP_TEXT.test(v)],
]

/**
 * The Hive type of a text column (CSV, TSV) from sampled values. Empty
 * values say nothing and are skipped; a column with no values is `string`.
 */
export function inferTextHiveType(values: ArrayLike<unknown>): string {
  const texts: string[] = []
  for (let i = 0; i < values.length; i++) {
    const value = values[i]
    if (typeof value === "string" && value.trim() !== "") texts.push(value.trim())
  }
  if (texts.length === 0) return "string"
  return TEXT_TYPES.find(([, reads]) => texts.every(reads))?.[0] ?? "string"
}

/**
 * The Hive type of a JSON column from sampled values, as the OpenX JSON SerDe
 * reads them: numbers, booleans, objects as `struct`s and arrays as `array`s,
 * nested as deep as the values go. Values of different kinds make it `string`.
 */
export function inferJsonHiveType(values: ArrayLike<unknown>): string {
  const present: unknown[] = []
  for (let i = 0; i < values.length; i++) {
    if (values[i] !== null && values[i] !== undefined) present.push(values[i])
  }
  if (present.length === 0) return "string"
  if (present.every((v) => typeof v === "boolean")) return "boolean"
  if (present.every((v) => typeof v === "number")) {
    return present.every((v) => Number.isInteger(v)) ? "bigint" : "double"
  }
  if (present.every((v) => typeof v === "string")) return "string"
  if (present.every(Array.isArray)) {
    return `array<${inferJsonHiveType((present as unknown[][]).flat())}>`
  }
  if (present.every(isRecord)) return structType(present)
  return "string"
}

/** `struct<a:bigint,b:string>` over the union of the objects' fields, in first-seen order. */
function structType(objects: readonly Record<string, unknown>[]): string {
  const fields = [...new Set(objects.flatMap((o) => Object.keys(o)))]
  const typed = fields.map((f) => `${f}:${inferJsonHiveType(objects.map((o) => o[f]))}`)
  return `struct<${typed.join(",")}>`
}

/**
 * The Hive type for a Parquet column, from the type the Parquet reader
 * describes it by (`INT64`, `DECIMAL(10,2)`, `TIMESTAMP(MICROS, UTC)`,
 * `LIST<STRING>`, `STRUCT<a, b>`). Nested element types the description does
 * not carry fall back to `string`.
 */
export function parquetHiveType(declared: string | undefined): string {
  const t = (declared ?? "").toUpperCase()
  if (t.startsWith("DECIMAL")) return t.toLowerCase().replace(/\s/g, "")
  if (t.startsWith("TIMESTAMP")) return "timestamp"
  if (t === "DATE") return "date"
  if (t === "INT64" || t === "UINT64") return "bigint"
  if (t === "INT32" || t === "UINT32" || t === "INT16" || t === "INT8") return "int"
  if (t === "DOUBLE") return "double"
  if (t === "FLOAT") return "float"
  if (t === "BOOLEAN") return "boolean"
  if (t.startsWith("LIST<")) return "array<string>"
  if (t.startsWith("STRUCT<")) {
    const fields = (declared ?? "")
      .slice(7, -1)
      .split(",")
      .map((f) => f.trim())
    return `struct<${fields.map((f) => `${f}:string`).join(",")}>`
  }
  if (t.startsWith("MAP<")) return "map<string,string>"
  return "string"
}
