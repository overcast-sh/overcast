/**
 * The Debug tab's *Debug in console* start/stop control and the session's
 * state line (docs/plans/compute-debugger-console.md § 3.5). Rendered when
 * the descriptor offers a console session (`consoleDebug`); a target the
 * debugger is on for but that speaks a protocol the console cannot yet —
 * Python's, Java's — gets one line saying so, above the editor
 * configurations the phase 1 panel keeps rendering either way.
 *
 * `StartConsoleDebugButton` is the start control on its own, so the Code
 * tab's idle strip offers exactly the same start as this card.
 */
import { BugPlay, Square } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import type { DebuggerTarget } from "@/types"
import { useDebugTarget } from "../hooks"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import { isSessionOpen, sessionStatusLine } from "../session/status"
import { bridgeUrl, consoleDebugOf } from "../target"
import { DebugStateBadge } from "./debug-state-badge"

/** Starts a session on the target's bridge; disabled, saying why, while the debugger is off for it. */
export function StartConsoleDebugButton({ target }: { target: DebuggerTarget }) {
  const session = useDebugSession()
  const { bridgePath } = consoleDebugOf(target)
  return (
    <Button
      type="button"
      size="sm"
      onClick={() => bridgePath !== null && session.start(bridgeUrl(bridgePath))}
      disabled={!target.enabled || bridgePath === null}
      title={target.enabled ? undefined : "Turn the debugger on for this resource first"}
    >
      <BugPlay aria-hidden className="h-3.5 w-3.5" />
      Debug in console
    </Button>
  )
}

export function DebugSessionControls({ service, resource }: { service: string; resource: string }) {
  const { data: target } = useDebugTarget(service, resource)
  const session = useDebugSession()
  const status = useDebugSessionState((s) => s.status)
  const error = useDebugSessionState((s) => s.error)
  const pause = useDebugSessionState((s) => s.pause)

  if (!target) return null
  if (!consoleDebugOf(target).available) {
    if (!target.enabled) return null
    return (
      <p role="note" className="text-sm text-fg-muted">
        Debugging in the console is not available for the {target.protocol || "target's"} protocol
        yet — attach an editor with the configurations below.
      </p>
    )
  }
  const open = isSessionOpen(status)

  return (
    <Card className="flex flex-wrap items-center gap-3 px-3 py-2 shadow-none">
      {open ? (
        <Button type="button" size="sm" variant="secondary" onClick={() => session.stop()}>
          <Square aria-hidden className="h-3.5 w-3.5" />
          Stop debugging
        </Button>
      ) : (
        <StartConsoleDebugButton target={target} />
      )}
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        {open && <DebugStateBadge state={pause ? "paused" : status} />}
        <span role="status" className="text-sm text-fg-muted">
          {sessionStatusLine(status, error)}
        </span>
      </div>
    </Card>
  )
}
