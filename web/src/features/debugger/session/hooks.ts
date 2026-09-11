/**
 * Hooks over the page's `DebugSession` (see `provider.tsx`). Components read
 * state through `useDebugSessionState` and act through the session object
 * itself; neither names a protocol.
 */
import { useContext, useSyncExternalStore } from "react"
import { DebugSessionContext } from "./context"
import type { DebugSession, DebugSessionState } from "./session"

/** The page's session. Throws outside a provider — a surface that can live without one uses the optional form. */
export function useDebugSession(): DebugSession {
  const session = useContext(DebugSessionContext)
  if (!session) throw new Error("useDebugSession must be used within <DebugSessionProvider>")
  return session
}

/** The page's session, or `null` where none is mounted (a list page, a test without the provider). */
export function useOptionalDebugSession(): DebugSession | null {
  return useContext(DebugSessionContext)
}

/**
 * A slice of the session state, re-rendering only when the slice changes.
 * The selector must return something referentially stable for an unchanged
 * state — a field of it, not a fresh object — or `useSyncExternalStore`
 * will re-render without end.
 */
export function useDebugSessionState<T>(selector: (state: DebugSessionState) => T): T {
  const session = useDebugSession()
  return useSyncExternalStore(session.subscribe, () => selector(session.getState()))
}
