/**
 * The Debug tab's *Debug in console* start/stop control and the session's
 * state line (docs/plans/compute-debugger-console.md § 3.5). Rendered only
 * when the descriptor offers a console session (`consoleDebug`); the phase 1
 * panel beside it keeps rendering the editor configurations either way.
 */
import { BugPlay, Square } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { useDebugTarget } from "../hooks"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import { isSessionOpen, sessionStatusLine } from "../session/status"
import { bridgeUrl, consoleDebugOf } from "../target"
import { DebugStateBadge } from "./debug-state-badge"

export function DebugSessionControls({ service, resource }: { service: string; resource: string }) {
  const { data: target } = useDebugTarget(service, resource)
  const session = useDebugSession()
  const status = useDebugSessionState((s) => s.status)
  const error = useDebugSessionState((s) => s.error)
  const pause = useDebugSessionState((s) => s.pause)

  if (!target) return null
  const console = consoleDebugOf(target)
  if (!console.available || console.bridgePath === null) return null
  const { bridgePath } = console
  const open = isSessionOpen(status)

  return (
    <Card className="flex flex-wrap items-center gap-3 px-3 py-2 shadow-none">
      {open ? (
        <Button type="button" size="sm" variant="secondary" onClick={() => session.stop()}>
          <Square aria-hidden className="h-3.5 w-3.5" />
          Stop debugging
        </Button>
      ) : (
        <Button
          type="button"
          size="sm"
          onClick={() => session.start(bridgeUrl(bridgePath))}
          disabled={!target.enabled}
          title={target.enabled ? undefined : "Turn the debugger on for this resource first"}
        >
          <BugPlay aria-hidden className="h-3.5 w-3.5" />
          Debug in console
        </Button>
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
