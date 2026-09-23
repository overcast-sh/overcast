/**
 * Estimated memory for decoded values, for the caches that are capped by
 * bytes: the grid's block cache and the worker's decoded Parquet chunks.
 *
 * An estimate, not a heap profile: strings at two bytes a character plus a
 * header, numbers at eight, and lists and structs by their size without
 * walking into them — cheap enough to run on every block that lands, and an
 * upper bound that tracks the data.
 */

/** The bytes one column of values holds. */
export function columnBytes(values: ArrayLike<unknown>): number {
  if (ArrayBuffer.isView(values)) return values.byteLength
  let bytes = 16
  for (let i = 0; i < values.length; i++) bytes += valueBytes(values[i])
  return bytes
}

function valueBytes(value: unknown): number {
  switch (typeof value) {
    case "string":
      return 16 + value.length * 2
    case "bigint":
      return 24
    case "object":
      if (value === null) return 8
      if (value instanceof Uint8Array) return 32 + value.byteLength
      if (Array.isArray(value)) return 32 + value.length * 16
      if (value instanceof Date) return 32
      return 32 + Object.keys(value).length * 32
    default:
      return 8
  }
}
