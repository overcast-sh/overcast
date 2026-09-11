import { useCallback, useSyncExternalStore } from "react"

/**
 * Whether a media query matches, kept current as the viewport changes. Read
 * through `useSyncExternalStore` so the first render already has the right
 * answer and a resize re-renders only the subscribers. `false` wherever
 * `matchMedia` is unavailable or throws (a test runner, an old embed).
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      let media: MediaQueryList
      try {
        media = window.matchMedia(query)
      } catch {
        return () => {}
      }
      media.addEventListener("change", onChange)
      return () => media.removeEventListener("change", onChange)
    },
    [query],
  )
  const getSnapshot = useCallback(() => {
    try {
      return window.matchMedia(query).matches
    } catch {
      return false
    }
  }, [query])
  return useSyncExternalStore(subscribe, getSnapshot, () => false)
}
