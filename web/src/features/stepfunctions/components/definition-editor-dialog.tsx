/**
 * Create a state machine, or edit an existing one's definition, with the flow
 * diagram redrawn beside the JSON on every keystroke. Structural problems — a
 * `Next` that names no state, a missing `StartAt`, a duplicated name — are
 * listed under the editor before the emulator ever sees the definition.
 */
import { useMemo, useState } from "react"
import { AlertTriangle, CheckCircle2 } from "lucide-react"
import type { StateMachineType } from "@aws-sdk/client-sfn"
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
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { cn } from "@/lib/utils"
import { parseDefinition } from "../asl"
import { DEFINITION_TEMPLATES, templateDefinition } from "../templates"
import { FlowDiagram } from "./flow-diagram"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

const NAME_PATTERN = /^[A-Za-z0-9_-]{1,80}$/

const TYPES = [
  { value: "STANDARD", label: "Standard" },
  { value: "EXPRESS", label: "Express" },
] as const satisfies readonly SegmentedOption<StateMachineType>[]

export type DefinitionEditorResult =
  | { mode: "create"; name: string; type: StateMachineType; definition: string }
  | { mode: "edit"; definition: string }

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: "create" | "edit"
  /** Existing definition, when editing. */
  initialDefinition?: string
  /** Machine name, shown in the title when editing. */
  name?: string
  pending: boolean
  onSubmit: (result: DefinitionEditorResult) => void
}

export function DefinitionEditorDialog(props: Props) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      {/* Remount on open so a cancelled edit never leaks into the next one. */}
      {props.open && <EditorBody {...props} />}
    </Dialog>
  )
}

function EditorBody({
  onOpenChange,
  mode,
  initialDefinition,
  name: machineName,
  pending,
  onSubmit,
}: Props) {
  const [name, setName] = useState("")
  const [type, setType] = useState<StateMachineType>("STANDARD")
  const [template, setTemplate] = useState(DEFINITION_TEMPLATES[0].id)
  const [text, setText] = useState(() =>
    mode === "edit"
      ? prettify(initialDefinition ?? "")
      : templateDefinition(DEFINITION_TEMPLATES[0].id),
  )
  const [selected, setSelected] = useState<string>()

  const debounced = useDebouncedValue(text, 150)
  const parsed = useMemo(() => parseDefinition(debounced), [debounced])
  // Keep the last good diagram on screen while the JSON is mid-edit and invalid.
  const [lastModel, setLastModel] = useState(parsed.model)
  if (parsed.model && parsed.model !== lastModel) setLastModel(parsed.model)
  const model = parsed.model ?? lastModel

  const nameError =
    mode === "create" && name !== "" && !NAME_PATTERN.test(name)
      ? "Use 1–80 letters, digits, hyphens or underscores."
      : undefined
  const issues = parsed.error ? [parsed.error] : (parsed.model?.issues ?? [])
  const canSubmit = !pending && !parsed.error && (mode === "edit" || (name !== "" && !nameError))
  const dirty = mode === "create" || prettify(initialDefinition ?? "") !== text

  const submit = () => {
    if (!canSubmit) return
    if (mode === "create") onSubmit({ mode, name, type, definition: text })
    else onSubmit({ mode, definition: text })
  }

  return (
    <DialogContent
      className="max-w-6xl"
      onPrimaryAction={submit}
      primaryActionDisabled={!canSubmit}
      aria-describedby={undefined}
    >
      <DialogHeader>
        <DialogTitle>
          {mode === "create" ? "Create state machine" : `Edit ${machineName ?? "definition"}`}
        </DialogTitle>
      </DialogHeader>

      {mode === "create" && (
        <div className="mt-3 flex flex-col gap-3">
          <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
            <label className="flex flex-col gap-1.5">
              <SectionLabel>Name</SectionLabel>
              <Input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="order-workflow"
                aria-invalid={!!nameError}
              />
              {nameError && <span className="text-2xs text-danger">{nameError}</span>}
            </label>
            <div className="flex flex-col gap-1.5">
              <SectionLabel>Type</SectionLabel>
              <SegmentedControl
                label="State machine type"
                value={type}
                options={TYPES}
                onChange={setType}
                size="md"
                className="self-start"
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <SectionLabel>Start from</SectionLabel>
            <div className="grid gap-2 sm:grid-cols-3">
              {DEFINITION_TEMPLATES.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  aria-pressed={template === t.id}
                  onClick={() => {
                    setTemplate(t.id)
                    setText(templateDefinition(t.id))
                    setSelected(undefined)
                  }}
                  className={cn(
                    "flex flex-col gap-0.5 rounded-lg border px-3 py-2 text-left transition-colors",
                    template === t.id
                      ? "border-accent bg-accent-muted/60"
                      : "border-border hover:bg-bg-muted",
                  )}
                >
                  <span className="text-xs font-semibold text-fg">{t.label}</span>
                  <span className="text-2xs text-fg-muted">{t.description}</span>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="mt-3 grid min-h-0 gap-3 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1.5">
          <div className="flex items-center justify-between">
            <SectionLabel>Definition (Amazon States Language)</SectionLabel>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setText(prettify(text))}
              disabled={!!parsed.error}
            >
              Format
            </Button>
          </div>
          <div className="h-[28rem] overflow-auto rounded-md">
            <JsonEditor value={text} onChange={setText} minHeight={440} />
          </div>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <SectionLabel>Preview</SectionLabel>
          <div className="h-[28rem] overflow-hidden rounded-md border border-border">
            {model ? (
              <FlowDiagram
                model={model}
                compact
                selectedState={selected}
                onSelectState={setSelected}
              />
            ) : (
              <div className="flex h-full items-center justify-center px-6 text-center text-xs text-fg-muted">
                The diagram appears once the definition parses.
              </div>
            )}
          </div>
        </div>
      </div>

      <div
        className={cn(
          "mt-3 rounded-md border px-3 py-2 text-xs",
          issues.length
            ? "border-warning/30 bg-warning-muted text-warning"
            : "border-success/30 bg-success-muted text-success",
        )}
        role="status"
      >
        {issues.length === 0 ? (
          <span className="flex items-center gap-1.5">
            <CheckCircle2 className="h-3.5 w-3.5" /> The definition is well-formed.
          </span>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {issues.slice(0, 6).map((issue) => (
              <li key={issue} className="flex items-start gap-1.5">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                {issue}
              </li>
            ))}
            {issues.length > 6 && <li className="pl-5">…and {issues.length - 6} more.</li>}
          </ul>
        )}
      </div>

      <DialogFooter className="mt-4">
        <Button size="sm" variant="ghost" onClick={() => onOpenChange(false)}>
          Cancel
        </Button>
        <Button size="sm" disabled={!canSubmit || !dirty} onClick={submit}>
          {mode === "create" ? "Create" : "Save definition"}
        </Button>
      </DialogFooter>
    </DialogContent>
  )
}

function prettify(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}
