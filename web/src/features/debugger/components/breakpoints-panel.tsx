/**
 * Breakpoints: every breakpoint the session holds, with its enabled
 * checkbox, its condition, and the pause-on-exceptions mode
 * (docs/plans/compute-debugger-console.md § 3.5). Editing a condition
 * opens the same `BreakpointConditionEditor` the gutter's right-click
 * does, under the row, so the two paths cannot disagree about what a
 * condition is. A row that is not yet bound says so in its title: the
 * script has not parsed on this connection, or the original line has no
 * mapping.
 */
import { useId, useState } from "react"
import { Pencil, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Select } from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import type { Breakpoint, PauseOnExceptionsMode } from "../session/session"
import { BreakpointConditionEditor } from "./breakpoint-condition-editor"
import { PanelEmpty } from "./panel-empty"

const EXCEPTION_MODES: Array<{ value: PauseOnExceptionsMode; label: string }> = [
  { value: "none", label: "Never" },
  { value: "uncaught", label: "Uncaught" },
  { value: "all", label: "Caught and uncaught" },
]

function bindingTitle(bp: Breakpoint): string {
  if (!bp.enabled) return "Disabled"
  return bp.bound ? "Set in the runtime" : "Not yet bound — waiting for the script to load"
}

export function BreakpointsPanel() {
  const session = useDebugSession()
  const breakpoints = useDebugSessionState((s) => s.breakpoints)
  const pauseOnExceptions = useDebugSessionState((s) => s.pauseOnExceptions)
  const [editingId, setEditingId] = useState<string | null>(null)
  const modeId = useId()

  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={modeId} className="flex items-center gap-2 text-xs text-fg-muted">
        <span className="shrink-0">Pause on exceptions</span>
        <Select
          id={modeId}
          value={pauseOnExceptions}
          onChange={(e) => session.setPauseOnExceptions(e.target.value as PauseOnExceptionsMode)}
          className="h-6 w-auto min-w-0 flex-1 px-1.5 text-2xs"
        >
          {EXCEPTION_MODES.map((mode) => (
            <option key={mode.value} value={mode.value}>
              {mode.label}
            </option>
          ))}
        </Select>
      </label>

      {breakpoints.length === 0 ? (
        <PanelEmpty>No breakpoints. Click a line's gutter in the code to add one.</PanelEmpty>
      ) : (
        <ul aria-label="Breakpoints" className="m-0 flex list-none flex-col p-0">
          {breakpoints.map((bp) => {
            const label = `${bp.path}:${bp.line}`
            const editing = editingId === bp.id
            return (
              <li key={bp.id} className="flex flex-col gap-1">
                <div className="group/bp flex items-center gap-1.5 rounded-sm px-1 py-0.5 hover:bg-bg-muted">
                  <input
                    type="checkbox"
                    aria-label={`Enable breakpoint at ${label}`}
                    checked={bp.enabled}
                    onChange={(e) => session.updateBreakpoint(bp.id, { enabled: e.target.checked })}
                    className="accent-accent"
                  />
                  <span
                    title={bindingTitle(bp)}
                    className={cn(
                      "flex min-w-0 flex-1 flex-col font-mono text-2xs leading-snug",
                      !bp.enabled && "text-fg-muted line-through",
                      bp.enabled && !bp.bound && "text-fg-muted",
                    )}
                  >
                    <span className="truncate">{label}</span>
                    {bp.condition && (
                      <span className="truncate text-fg-muted" title={bp.condition}>
                        when {bp.condition}
                      </span>
                    )}
                  </span>
                  <span className="flex shrink-0 items-center opacity-0 transition-opacity group-focus-within/bp:opacity-100 group-hover/bp:opacity-100">
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Edit condition of breakpoint at ${label}`}
                      aria-expanded={editing}
                      onClick={() => setEditingId(editing ? null : bp.id)}
                    >
                      <Pencil aria-hidden className="h-3 w-3" />
                    </Button>
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Remove breakpoint at ${label}`}
                      onClick={() => session.removeBreakpoint(bp.id)}
                    >
                      <X aria-hidden className="h-3 w-3" />
                    </Button>
                  </span>
                </div>
                {editing && (
                  <BreakpointConditionEditor
                    path={bp.path}
                    line={bp.line}
                    breakpoint={bp}
                    onSave={(condition) => {
                      session.updateBreakpoint(bp.id, { condition })
                      setEditingId(null)
                    }}
                    onRemove={() => {
                      session.removeBreakpoint(bp.id)
                      setEditingId(null)
                    }}
                    onClose={() => setEditingId(null)}
                  />
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
