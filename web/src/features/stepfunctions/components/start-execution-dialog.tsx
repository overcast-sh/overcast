/**
 * Start an execution. The input is checked as JSON while it is typed, can be
 * seeded from the last run's input in one click, and the dialog hands back the
 * new execution's name so the caller can open its live view straight away.
 */
import { useState } from "react"
import { History } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { JsonEditor } from "@/components/ui/json-editor"
import { SectionLabel } from "@/components/ui/primitives"

const NAME_PATTERN = /^[A-Za-z0-9_-]{1,80}$/

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Pre-filled input — a re-run of an earlier execution passes its input here. */
  initialInput?: string
  /** The most recent execution's input, offered as a one-click fill. */
  lastInput?: string
  pending: boolean
  onStart: (vars: { input: string; name?: string }) => void
}

export function StartExecutionDialog(props: Props) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      {props.open && <StartBody {...props} />}
    </Dialog>
  )
}

function jsonError(text: string): string | null {
  if (!text.trim()) return null
  try {
    JSON.parse(text)
    return null
  } catch (err) {
    return (err as Error).message
  }
}

function StartBody({ onOpenChange, initialInput, lastInput, pending, onStart }: Props) {
  const [input, setInput] = useState(() => pretty(initialInput) || "{}")
  const [name, setName] = useState("")
  const error = jsonError(input)
  const nameError =
    name && !NAME_PATTERN.test(name) ? "Use 1–80 letters, digits, hyphens or underscores." : null
  const disabled = pending || !!error || !!nameError

  const start = () => {
    if (disabled) return
    onStart({ input: input.trim() || "{}", name: name || undefined })
  }

  return (
    <DialogContent
      size="lg"
      onPrimaryAction={start}
      primaryActionDisabled={disabled}
      aria-describedby={undefined}
    >
      <DialogHeader>
        <DialogTitle>Start execution</DialogTitle>
      </DialogHeader>
      <div
        className="mt-3 flex flex-col gap-3"
        onKeyDown={(e) => {
          // ⌘/Ctrl+Enter starts from inside the editor, where Enter is a newline.
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            start()
          }
        }}
      >
        <label className="flex flex-col gap-1.5">
          <SectionLabel>Name (optional)</SectionLabel>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Generated if left blank"
            aria-invalid={!!nameError}
          />
          {nameError && <span className="text-2xs text-danger">{nameError}</span>}
        </label>
        <div className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between gap-2">
            <SectionLabel>Input (JSON)</SectionLabel>
            <div className="flex gap-1">
              {lastInput !== undefined && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setInput(pretty(lastInput) || "{}")}
                >
                  <History className="mr-1 h-3.5 w-3.5" />
                  Last input
                </Button>
              )}
              <Button
                size="sm"
                variant="ghost"
                disabled={!!error}
                onClick={() => setInput(pretty(input))}
              >
                Format
              </Button>
            </div>
          </div>
          <div className="max-h-80 overflow-auto rounded-md">
            <JsonEditor value={input} onChange={setInput} error={error} minHeight={180} />
          </div>
          <p className="text-2xs text-fg-subtle">
            Opens the live view when it starts. Press Ctrl/⌘ + Enter to start from the editor.
          </p>
        </div>
      </div>
      <DialogFooter className="mt-4">
        <Button size="sm" variant="ghost" onClick={() => onOpenChange(false)}>
          Cancel
        </Button>
        <Button size="sm" disabled={disabled} onClick={start}>
          Start
        </Button>
      </DialogFooter>
    </DialogContent>
  )
}

function pretty(text: string | undefined): string {
  if (!text) return ""
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}
