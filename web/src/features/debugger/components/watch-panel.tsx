/**
 * Watch: expressions the session re-evaluates on every pause and frame
 * change (docs/plans/compute-debugger-console.md § 3.5). Each row is a
 * one-root `ValueTree`, so an object result expands exactly as a local
 * does; an error shows in the value's place. The expression is edited in
 * place — Enter saves, Escape cancels — and a new one is typed into the
 * strip at the bottom. Values are cleared while the program runs: there is
 * no frame to evaluate them in.
 */
import { useCallback, useId, useState, type KeyboardEvent } from "react"
import { Pencil, Plus, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import type { Property, Watch } from "../session/session"
import { PanelEmpty } from "./panel-empty"
import { ValueTree } from "./value-tree"

export function WatchPanel() {
  const session = useDebugSession()
  const watches = useDebugSessionState((s) => s.watches)
  const paused = useDebugSessionState((s) => s.pause !== null)
  const loadChildren = useCallback((objectId: string) => session.getProperties(objectId), [session])

  return (
    <div className="flex flex-col gap-1">
      {watches.length === 0 ? (
        <PanelEmpty>No watch expressions. Add one below.</PanelEmpty>
      ) : (
        <ul aria-label="Watch expressions" className="m-0 flex list-none flex-col p-0">
          {watches.map((watch) => (
            <WatchRow
              key={watch.id}
              watch={watch}
              paused={paused}
              loadChildren={loadChildren}
              onChange={(expression) => session.updateWatch(watch.id, expression)}
              onRemove={() => session.removeWatch(watch.id)}
            />
          ))}
        </ul>
      )}
      <AddWatch onAdd={(expression) => session.addWatch(expression)} />
    </div>
  )
}

function WatchRow({
  watch,
  paused,
  loadChildren,
  onChange,
  onRemove,
}: {
  watch: Watch
  paused: boolean
  loadChildren: (objectId: string) => Promise<Property[]>
  onChange: (expression: string) => void
  onRemove: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(watch.expression)

  const save = () => {
    const next = draft.trim()
    if (next && next !== watch.expression) onChange(next)
    setEditing(false)
  }

  return (
    <li className="group/watch flex items-start gap-1">
      {editing ? (
        <form
          aria-label={`Edit watch ${watch.expression}`}
          className="flex min-w-0 flex-1"
          onSubmit={(e) => {
            e.preventDefault()
            save()
          }}
        >
          <Input
            autoFocus
            aria-label="Expression"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={save}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault()
                setDraft(watch.expression)
                setEditing(false)
              }
            }}
            className="h-6 px-1.5 font-mono text-2xs"
          />
        </form>
      ) : (
        <ValueTree
          // A value left over from the last pause is still shown — it is
          // often what the reader wants to compare against — but dimmed,
          // and the row says where it came from.
          className={cn("min-w-0 flex-1", !paused && watch.result && "opacity-60")}
          title={!paused && watch.result ? "From the last pause; re-evaluated at the next" : undefined}
          aria-label={`Watch ${watch.expression}`}
          loadChildren={loadChildren}
          roots={[
            {
              key: watch.id,
              name: watch.expression,
              value: watch.result,
              error: watch.error,
              note: paused || watch.result ? null : "not available",
            },
          ]}
        />
      )}
      <span
        className={cn(
          "flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity",
          "group-focus-within/watch:opacity-100 group-hover/watch:opacity-100",
        )}
      >
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={`Edit watch ${watch.expression}`}
          onClick={() => {
            setDraft(watch.expression)
            setEditing(true)
          }}
        >
          <Pencil aria-hidden className="h-3 w-3" />
        </Button>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={`Remove watch ${watch.expression}`}
          onClick={onRemove}
        >
          <X aria-hidden className="h-3 w-3" />
        </Button>
      </span>
    </li>
  )
}

function AddWatch({ onAdd }: { onAdd: (expression: string) => void }) {
  const [draft, setDraft] = useState("")
  const id = useId()
  const submit = () => {
    const expression = draft.trim()
    if (!expression) return
    onAdd(expression)
    setDraft("")
  }
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") setDraft("")
  }
  return (
    <form
      className="flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <label htmlFor={id} className="sr-only">
        Add watch expression
      </label>
      <Input
        id={id}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Add expression…"
        className="h-6 px-1.5 font-mono text-2xs"
      />
      <Button
        type="submit"
        size="icon-sm"
        variant="ghost"
        aria-label="Add watch"
        disabled={draft.trim() === ""}
      >
        <Plus aria-hidden className="h-3.5 w-3.5" />
      </Button>
    </form>
  )
}
