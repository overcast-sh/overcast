/**
 * The small editor a right-click (or modifier-click) in the gutter opens:
 * set or clear a breakpoint's condition, or remove it. It sits inline above
 * the code pane rather than floating at the click — a Monaco gutter click
 * has no DOM element to anchor a popover to, and an inline strip is the
 * same for keyboard and mouse. Enter saves, Escape closes.
 */
import { useId, useState } from "react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import type { Breakpoint } from "../session/session"

export interface BreakpointConditionEditorProps {
  path: string
  line: number
  /** The breakpoint already on the line, when there is one. */
  breakpoint: Breakpoint | undefined
  /** Save the condition — adding the breakpoint when there is none. */
  onSave: (condition: string) => void
  onRemove: () => void
  onClose: () => void
}

export function BreakpointConditionEditor({
  path,
  line,
  breakpoint,
  onSave,
  onRemove,
  onClose,
}: BreakpointConditionEditorProps) {
  const [condition, setCondition] = useState(breakpoint?.condition ?? "")
  const inputId = useId()
  const title = `Breakpoint at ${path}:${line}`

  return (
    <Card className="px-3 py-2 shadow-none">
      <form
        aria-label={title}
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          onSave(condition.trim())
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault()
            onClose()
          }
        }}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <label htmlFor={inputId} className={cn(fieldLabel, "text-fg-muted")}>
            {title} — condition
          </label>
          <Input
            id={inputId}
            autoFocus
            value={condition}
            onChange={(e) => setCondition(e.target.value)}
            placeholder="Pause when this expression is truthy, e.g. event.count > 3"
            className="font-mono"
          />
        </div>
        <div className="flex gap-2">
          <Button type="submit" size="sm">
            {breakpoint ? "Save" : "Add breakpoint"}
          </Button>
          {breakpoint && (
            <Button type="button" size="sm" variant="danger-ghost" onClick={onRemove}>
              Remove
            </Button>
          )}
          <Button type="button" size="sm" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  )
}
