/**
 * Reading a route's search params in `validateSearch`.
 *
 * The router parses each value as JSON before a route sees it, so `q=42`
 * arrives as the number 42 and `q=true` as a boolean. A deep link's values
 * are text, so these read them back as the text they were written as.
 */

/** The value as text, or undefined when it is absent or not a scalar. */
export function searchText(value: unknown): string | undefined {
  if (typeof value === "string") return value
  if (typeof value === "number" || typeof value === "boolean") return String(value)
  return undefined
}

/** The value when it is one of `options` — a tab id, a mode — or undefined. */
export function searchOneOf<T extends string>(
  options: readonly T[],
  value: unknown,
): T | undefined {
  return (options as readonly unknown[]).includes(value) ? (value as T) : undefined
}
