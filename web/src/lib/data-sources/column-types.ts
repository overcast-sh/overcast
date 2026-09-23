import type { DataColumn } from "./row-source"

/**
 * What a text file's columns are, inferred from a sample of their values.
 *
 * Text formats declare no types, so the only thing inferred is the one the
 * grid needs to lay a column out: whether it is numeric, and so right-aligned.
 * Anything more (dates, booleans) would be a guess the file never made.
 */

/** A number as a CSV spells it: optional sign, digits, point, exponent. */
const NUMERIC_TEXT = /^[-+]?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$/

/** `007`, `02134`: digits with a leading zero are identifiers, not quantities. */
const LEADING_ZERO = /^[-+]?0\d/

/**
 * True when every non-empty value is a number (and there is at least one), so
 * the column right-aligns. Leading zeros make it text: right-aligning `007`
 * reads as arithmetic.
 *
 * `numericText: false` is for typed sources (JSON), where `"42"` is a string
 * someone chose to quote and is not a number however it reads.
 */
export function isNumericColumn(
  values: ArrayLike<unknown>,
  { numericText = true }: { numericText?: boolean } = {},
): boolean {
  let seen = false
  for (let i = 0; i < values.length; i++) {
    const value = values[i]
    if (value === null || value === undefined || value === "") continue
    if (typeof value === "number" || typeof value === "bigint") {
      seen = true
      continue
    }
    if (typeof value !== "string" || !numericText) return false
    const trimmed = value.trim()
    if (!NUMERIC_TEXT.test(trimmed) || LEADING_ZERO.test(trimmed)) return false
    seen = true
  }
  return seen
}

/** Named columns over sampled values, numeric where every value is. */
export function inferColumns(
  names: readonly string[],
  samples: readonly ArrayLike<unknown>[],
  options?: { numericText?: boolean },
): DataColumn[] {
  return names.map((name, i) => ({ name, numeric: isNumericColumn(samples[i] ?? [], options) }))
}
