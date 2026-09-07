import { useEffect, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { DebuggerTarget } from "@/types"
import { debuggerTargetQueryOptions, debuggerTargetsQueryOptions } from "./data"
import { ATTACHED_STATES } from "./target"

/**
 * Every debug target the emulator knows, polled while mounted. A server
 * without the endpoint answers with an error; callers treat that exactly
 * like an empty list, so a badge never becomes a red banner on a list page.
 */
export function useDebugTargets() {
  return useQuery(debuggerTargetsQueryOptions())
}

/**
 * One target, polled while mounted. `data` is `null` for a resource the
 * server has no entry for at all (a 404), which surfaces render as "off".
 */
export function useDebugTarget(service: string, resource: string, container?: string) {
  return useQuery(debuggerTargetQueryOptions(service, resource, container ?? ""))
}

/**
 * A clock for the relative times on the live line, ticking once a second
 * while `active` and idle otherwise, so a panel with nothing time-based to
 * show — inert, unbound, listening, error — re-renders only when its data
 * does.
 */
export function useNow(active: boolean, intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [active, intervalMs])
  return now
}

/**
 * Remembers the container an attached editor was talking to, so the panel
 * can say when it has been swapped out from under it — hot reload recycling
 * the container, a redeploy — before the editor notices.
 *
 * The remembered pair is adopted on every *new* attach session
 * (`attachedSince` changes when the client count goes 0 → 1), which is what
 * lets the notice clear on its own: an editor that reconnected to the new
 * container starts a new session, one that is still holding the old splice
 * does not. Returns the previous container id while a replacement is
 * pending, null otherwise.
 */
export function useReplacedContainer(target: DebuggerTarget): string | null {
  const attachedNow = ATTACHED_STATES.has(target.state) && target.containerId !== ""
  const [session, setSession] = useState<{ containerId: string; since: string } | null>(null)

  // State adjusted from a prop during render — the React-documented form,
  // guarded so it settles in one extra render rather than an effect's two.
  if (attachedNow && (session === null || session.since !== target.attachedSince)) {
    setSession({ containerId: target.containerId, since: target.attachedSince })
    return null
  }

  if (session === null || target.containerId === "" || target.containerId === session.containerId) {
    return null
  }
  return session.containerId
}
