import { useEffect, useState } from "react"

/**
 * A clock that ticks every `intervalMs` while `active` and stays still
 * otherwise, so a running duration counts up on screen and a finished one
 * costs no re-renders.
 */
export function useNow(active: boolean, intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const id = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(id)
  }, [active, intervalMs])
  return now
}
