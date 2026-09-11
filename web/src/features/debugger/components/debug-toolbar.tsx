/**
 * The execution-control strip over the Code tab while a console session is
 * open (docs/plans/compute-debugger-console.md § 3.5): Continue, Step over,
 * Step into, Step out, Pause on exceptions, Restart container, Stop. The
 * keys are VS Code's — F5 / F10 / F11 / Shift+F11 — and are bound by
 * `DebugCodeBrowser` on the pane, never on the window, so they act only
 * while the code pane has focus.
 *
 * Every control is a labelled button; the key is in the tooltip, not the
 * only way in.
 */
import {
  ArrowDownToDot,
  ArrowUpFromDot,
  OctagonAlert,
  Play,
  Redo2,
  RotateCcw,
  Square,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Tooltip } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import { sessionStatusLine } from "../session/status"
import { DebugStateBadge } from "./debug-state-badge"

function ToolbarButton({
  label,
  hint,
  onClick,
  disabled,
  pressed,
  children,
}: {
  label: string
  hint: string
  onClick?: () => void
  disabled?: boolean
  pressed?: boolean
  children: React.ReactNode
}) {
  return (
    <Tooltip content={hint}>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        aria-label={label}
        aria-pressed={pressed}
        onClick={onClick}
        disabled={disabled}
        className={cn(pressed && "bg-accent-muted text-accent")}
      >
        {children}
      </Button>
    </Tooltip>
  )
}

export function DebugToolbar() {
  const session = useDebugSession()
  const status = useDebugSessionState((s) => s.status)
  const error = useDebugSessionState((s) => s.error)
  const pause = useDebugSessionState((s) => s.pause)
  const pauseOnExceptions = useDebugSessionState((s) => s.pauseOnExceptions)
  const hasSourceMaps = useDebugSessionState((s) => s.hasSourceMaps)
  const paused = pause !== null
  const frame = pause?.frames.at(pause.selectedFrame)

  return (
    <div
      role="toolbar"
      aria-label="Debug controls"
      className="flex flex-wrap items-center gap-1 rounded-md bg-bg-muted px-2 py-1"
    >
      <ToolbarButton
        label="Continue"
        hint="Continue (F5)"
        onClick={() => session.resume()}
        disabled={!paused}
      >
        <Play aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      <ToolbarButton
        label="Step over"
        hint="Step over (F10)"
        onClick={() => session.stepOver()}
        disabled={!paused}
      >
        <Redo2 aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      <ToolbarButton
        label="Step into"
        hint="Step into (F11)"
        onClick={() => session.stepInto()}
        disabled={!paused}
      >
        <ArrowDownToDot aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      <ToolbarButton
        label="Step out"
        hint="Step out (Shift+F11)"
        onClick={() => session.stepOut()}
        disabled={!paused}
      >
        <ArrowUpFromDot aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      <span aria-hidden className="mx-1 h-4 w-px bg-border" />
      <ToolbarButton
        label="Pause on uncaught exceptions"
        hint={
          pauseOnExceptions === "none"
            ? "Pause on uncaught exceptions: off"
            : `Pause on exceptions: ${pauseOnExceptions}`
        }
        pressed={pauseOnExceptions !== "none"}
        onClick={() =>
          session.setPauseOnExceptions(pauseOnExceptions === "none" ? "uncaught" : "none")
        }
      >
        <OctagonAlert aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      {/* The retire-and-recreate path is phase-2 "later" (§ 6); the control is
          here so the strip reads as VS Code's, disabled until it exists. */}
      <ToolbarButton
        label="Restart container"
        hint="Restart container — not available yet"
        disabled
      >
        <RotateCcw aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>
      <ToolbarButton label="Stop debugging" hint="Stop debugging" onClick={() => session.stop()}>
        <Square aria-hidden className="h-3.5 w-3.5" />
      </ToolbarButton>

      <div className="ml-2 flex min-w-0 flex-1 flex-wrap items-center gap-2 text-xs text-fg-muted">
        <DebugStateBadge state={paused ? "paused" : status} />
        {paused && frame ? (
          <span className="truncate font-mono">
            {frame.functionName} · {frame.location.path}:{frame.location.line}
            {pause.exception && <> · {pause.exception}</>}
          </span>
        ) : (
          <span className="truncate">{sessionStatusLine(status, error)}</span>
        )}
        {paused && frame && hasSourceMaps && !frame.mapped && (
          <Badge
            variant="warning"
            title="The map does not resolve this frame; showing the compiled file"
          >
            no source map for this frame
          </Badge>
        )}
      </div>
    </div>
  )
}
