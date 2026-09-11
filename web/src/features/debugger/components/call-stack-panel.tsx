/**
 * Call stack: one row per frame of the current pause, mapped location
 * shown, generated location in the tooltip, click (or Enter) selects the
 * frame — which re-scopes Locals and Watch and moves the code marker
 * (docs/plans/compute-debugger-console.md § 3.5). Frames outside the
 * deployment — the runtime's bootstrap, Node's internals — are folded
 * into one "n internal frames" row per run, opened on demand, so the
 * reader's own frames are what the list is about.
 */
import { useState, type KeyboardEvent } from "react"
import { ChevronRight } from "lucide-react"
import { cn } from "@/lib/utils"
import { foldFrames } from "../call-stack"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import type { StackFrame } from "../session/session"
import { PanelEmpty } from "./panel-empty"

export function CallStackPanel() {
  const session = useDebugSession()
  const pause = useDebugSessionState((s) => s.pause)
  const hasSourceMaps = useDebugSessionState((s) => s.hasSourceMaps)
  const [openFolds, setOpenFolds] = useState<ReadonlySet<string>>(() => new Set())

  if (!pause) return <PanelEmpty>Not paused.</PanelEmpty>

  const rows = foldFrames(pause.frames)
  const select = (index: number) => session.selectFrame(index)
  const onKeyDown = (index: number) => (e: KeyboardEvent<HTMLLIElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault()
      select(index)
    }
  }

  const frameRow = (index: number, frame: StackFrame, folded: boolean) => {
    const selected = index === pause.selectedFrame
    const location = `${frame.location.path}:${frame.location.line}:${frame.location.column}`
    const generated = `${frame.generated.path}:${frame.generated.line}:${frame.generated.column}`
    const unmapped = hasSourceMaps && !frame.mapped && !frame.internal
    return (
      <li
        key={frame.id}
        role="option"
        aria-selected={selected}
        tabIndex={0}
        onClick={() => select(index)}
        onKeyDown={onKeyDown(index)}
        title={frame.mapped ? `Compiled: ${generated}` : generated}
        className={cn(
          "flex cursor-pointer flex-col gap-0 rounded-sm px-1.5 py-1 font-mono text-2xs leading-snug outline-none",
          "hover:bg-bg-muted focus-visible:ring-1 focus-visible:ring-accent",
          selected && "bg-accent-muted text-accent",
          folded && "pl-5 text-fg-muted",
        )}
      >
        <span className="truncate">{frame.functionName}</span>
        <span className={cn("truncate", selected ? "text-accent/80" : "text-fg-muted")}>
          {location}
          {unmapped && <span className="ml-1 text-warning">(no source map)</span>}
        </span>
      </li>
    )
  }

  return (
    <ul role="listbox" aria-label="Call stack" className="m-0 flex list-none flex-col p-0">
      {rows.map((row) => {
        if (row.kind === "frame") return frameRow(row.index, row.frame, false)
        const selectedInside = row.frames.some(({ index }) => index === pause.selectedFrame)
        const open = openFolds.has(row.key) || selectedInside
        return (
          <li key={row.key} role="presentation" className="flex flex-col">
            <button
              type="button"
              aria-expanded={open}
              onClick={() =>
                setOpenFolds((prev) => {
                  const next = new Set(prev)
                  if (next.has(row.key)) next.delete(row.key)
                  else next.add(row.key)
                  return next
                })
              }
              className="flex items-center gap-1 rounded-sm px-1.5 py-1 text-left font-mono text-2xs text-fg-muted italic hover:bg-bg-muted focus-visible:ring-1 focus-visible:ring-accent focus-visible:outline-none"
            >
              <ChevronRight
                aria-hidden
                className={cn("h-3 w-3 transition-transform", open && "rotate-90")}
              />
              {row.frames.length} internal frame{row.frames.length === 1 ? "" : "s"}
            </button>
            {open && (
              <ul role="group" className="m-0 flex list-none flex-col p-0">
                {row.frames.map(({ index, frame }) => frameRow(index, frame, true))}
              </ul>
            )}
          </li>
        )
      })}
    </ul>
  )
}
