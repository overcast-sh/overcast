/**
 * Debug console: the session's console buffer — the program's own
 * `console.*` output and thrown exceptions as they arrive, the pause and
 * resume markers, and the REPL's echoes and answers — over an input that
 * evaluates in the selected frame while paused and globally while running
 * (docs/plans/compute-debugger-console.md § 3.5). ArrowUp and ArrowDown
 * walk the history the session keeps; an object answer expands through
 * the same `ValueTree` as a local. The list is a live region so a screen
 * reader hears new output without leaving the input.
 */
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react"
import { ChevronRight, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { formatLogTime } from "@/lib/log-format"
import { cn } from "@/lib/utils"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import type { ConsoleEntry, ConsoleEntryKind } from "../session/session"
import { ValueTree } from "./value-tree"

const KIND_CLASS: Record<ConsoleEntryKind, string> = {
  log: "text-fg",
  info: "text-fg",
  debug: "text-fg-muted",
  warn: "text-warning",
  error: "text-danger",
  exception: "text-danger",
  marker: "text-accent italic",
  input: "text-fg-muted",
  result: "text-fg",
}

export function DebugConsole() {
  const session = useDebugSession()
  const entries = useDebugSessionState((s) => s.console)
  const history = useDebugSessionState((s) => s.consoleHistory)
  const paused = useDebugSessionState((s) => s.pause !== null)
  const status = useDebugSessionState((s) => s.status)
  const [draft, setDraft] = useState("")
  // Where in the history the arrows are; `history.length` is "the draft".
  const [cursor, setCursor] = useState<number | null>(null)
  const listRef = useRef<HTMLOListElement>(null)
  const inputId = useId()

  // New output keeps the newest line in view, as a terminal does.
  useEffect(() => {
    const list = listRef.current
    if (list) list.scrollTop = list.scrollHeight
  }, [entries.length])

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault()
      const expression = draft.trim()
      if (!expression) return
      setDraft("")
      setCursor(null)
      void session.runConsoleCommand(expression)
      return
    }
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      if (history.length === 0) return
      e.preventDefault()
      const at = cursor ?? history.length
      const next = e.key === "ArrowUp" ? Math.max(0, at - 1) : Math.min(history.length, at + 1)
      setCursor(next === history.length ? null : next)
      setDraft(next === history.length ? "" : history[next])
    }
  }

  const attached = status === "attached"

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-1">
      <div className="flex items-center justify-between gap-2">
        <span className="text-2xs text-fg-muted">
          {paused ? "Evaluating in the selected frame." : "Evaluating globally."}
        </span>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => session.clearConsole()}
          disabled={entries.length === 0}
        >
          <Trash2 aria-hidden className="h-3 w-3" />
          Clear
        </Button>
      </div>
      <ol
        ref={listRef}
        role="log"
        aria-label="Debug console output"
        aria-live="polite"
        className="m-0 min-h-0 flex-1 list-none overflow-auto rounded-md border border-border bg-bg-elevated p-2 font-mono text-2xs leading-relaxed"
      >
        {entries.length === 0 && (
          <li className="text-fg-muted italic">
            Nothing yet — evaluations, exceptions and pause markers appear here. The
            function&rsquo;s own console.log lines are on the Logs tab.
          </li>
        )}
        {entries.map((entry) => (
          <ConsoleLine key={entry.id} entry={entry} />
        ))}
      </ol>
      <div className="flex items-center gap-1 rounded-md border border-border bg-bg px-2">
        <ChevronRight aria-hidden className="h-3.5 w-3.5 shrink-0 text-accent" />
        <label htmlFor={inputId} className="sr-only">
          Debug console input
        </label>
        <input
          id={inputId}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            setCursor(null)
          }}
          onKeyDown={onKeyDown}
          disabled={!attached}
          placeholder={attached ? "Evaluate an expression — ↑ ↓ for history" : "Not attached"}
          autoComplete="off"
          spellCheck={false}
          className="h-7 min-w-0 flex-1 bg-transparent font-mono text-2xs text-fg outline-none placeholder:text-fg-subtle disabled:cursor-not-allowed"
        />
      </div>
    </div>
  )
}

function ConsoleLine({ entry }: { entry: ConsoleEntry }) {
  const session = useDebugSession()
  return (
    <li className={cn("flex items-start gap-2", KIND_CLASS[entry.kind])}>
      <time
        dateTime={new Date(entry.timestamp).toISOString()}
        className="shrink-0 text-fg-subtle tabular-nums select-none"
      >
        {formatLogTime(entry.timestamp)}
      </time>
      <span aria-hidden className="w-2 shrink-0 text-fg-subtle select-none">
        {entry.kind === "input" ? ">" : entry.kind === "result" ? "<" : ""}
      </span>
      {entry.value ? (
        <ValueTree
          className="min-w-0 flex-1"
          aria-label="Result"
          loadChildren={(objectId) => session.getProperties(objectId)}
          roots={[{ key: String(entry.id), name: "", value: entry.value }]}
        />
      ) : (
        <span className="min-w-0 break-all whitespace-pre-wrap">{entry.text}</span>
      )}
    </li>
  )
}
