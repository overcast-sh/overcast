import { Bug } from "lucide-react"
import { useDebugTarget } from "../hooks"
import { ATTACHED_STATES } from "../target"

/**
 * The one line above Invoke that says what the debugger does to the timeout
 * (docs/plans/compute-debugger.md § 7). Nothing renders while the feature
 * is off for this resource — an untagged function, a server without the
 * flag — so the Test tab looks as it always did until the debugger is in
 * play. The state names are the server's; the timeout policy is the one the
 * server reports, spelled as it is in OVERCAST_DEBUGGER_TIMEOUT.
 */
export function InvokeDebugHint({
  service,
  resource,
  timeoutSeconds,
}: {
  service: string
  resource: string
  /** The resource's configured timeout, when the caller knows it. */
  timeoutSeconds?: number
}) {
  const { data: target } = useDebugTarget(service, resource)
  if (!target?.enabled) return null

  const timeout =
    timeoutSeconds === undefined ? "the configured timeout" : `the ${timeoutSeconds} s timeout`
  let text: string
  if (ATTACHED_STATES.has(target.state)) {
    if (target.timeoutPolicy === "strict") {
      text = `Debugger attached — ${timeout} still applies (timeout policy: strict).`
    } else if (target.timeoutPolicy === "paused" && target.state !== "paused") {
      text = `Debugger attached — ${timeout} applies until execution pauses at a breakpoint.`
    } else {
      text = "Debugger attached — the timeout clock is suspended."
    }
  } else {
    text = `No debugger attached — ${timeout} applies.`
  }

  return (
    <p role="status" className="flex items-center gap-1.5 text-xs text-fg-muted">
      <Bug aria-hidden className="h-3.5 w-3.5 shrink-0" />
      {text}
    </p>
  )
}
