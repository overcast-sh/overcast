import { useEffect } from "react"
import { useDebugSession } from "../session/hooks"
import type { PauseState } from "../session/session"

/**
 * Runs `onPause` for every pause of the page's session and renders nothing.
 * The function route uses it to switch to the Code tab when execution stops
 * (docs/plans/compute-debugger-console.md § 3.5, Test tab), which keeps the
 * tab state where it lives — in the route — and the session where it lives.
 * Pass a stable callback; a new identity re-subscribes.
 */
export function OnPause({ onPause }: { onPause: (pause: PauseState) => void }) {
  const session = useDebugSession()
  useEffect(() => session.onPause(onPause), [session, onPause])
  return null
}
