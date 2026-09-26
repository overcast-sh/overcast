import { useState, useCallback } from "react"

/**
 * Generic hook for state backed by localStorage with JSON serialisation.
 *
 * Handles corrupt/missing values gracefully and catches write failures
 * (e.g. QuotaExceededError in private browsing).
 *
 * `defaultValue` may be a function, called once when nothing is stored — for
 * a default that must not be rebuilt each render (one with generated ids).
 * `restore` checks and repairs what was stored, which may be from an older
 * shape, before it is trusted; it runs once, on the first render.
 */
export function useLocalStorage<T>(
  key: string,
  defaultValue: T | (() => T),
  restore?: (stored: unknown) => T,
): [T, (value: T | ((prev: T) => T)) => void] {
  const [stored, setStored] = useState<T>(() => {
    const fallback = () => (defaultValue instanceof Function ? defaultValue() : defaultValue)
    try {
      const raw = localStorage.getItem(key)
      if (raw === null) return fallback()
      const parsed: unknown = JSON.parse(raw)
      return restore ? restore(parsed) : (parsed as T)
    } catch {
      return fallback()
    }
  })

  const setValue = useCallback(
    (value: T | ((prev: T) => T)) => {
      setStored((prev) => {
        const next = value instanceof Function ? value(prev) : value
        try {
          localStorage.setItem(key, JSON.stringify(next))
        } catch {
          // QuotaExceededError or SecurityError in private browsing — state
          // still updates in-memory, next page load falls back to default.
        }
        return next
      })
    },
    [key],
  )

  return [stored, setValue]
}
