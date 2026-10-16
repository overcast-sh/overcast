import { Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { EnvEntry } from "../models"

interface KeyValueFieldsProps {
  entries: EnvEntry[]
  onChange: (entries: EnvEntry[]) => void
  /** What an entry is called, for the accessible names: `environment variable`. */
  noun: string
  addLabel: string
}

/** A list of key/value rows with an add button — a revision's environment variables. */
export function KeyValueFields({ entries, onChange, noun, addLabel }: KeyValueFieldsProps) {
  const set = (index: number, patch: Partial<EnvEntry>) =>
    onChange(entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)))
  return (
    <div className="flex flex-col gap-2">
      {entries.map((entry, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            aria-label={`${noun} ${i + 1} name`}
            className="flex-1 font-mono text-xs"
            placeholder="KEY"
            value={entry.key}
            onChange={(event) => set(i, { key: event.target.value })}
          />
          <Input
            aria-label={`${noun} ${i + 1} value`}
            className="flex-1 font-mono text-xs"
            placeholder="value"
            value={entry.value}
            onChange={(event) => set(i, { value: event.target.value })}
          />
          <Button
            type="button"
            variant="danger-ghost"
            size="sm"
            aria-label={`Remove ${noun} ${i + 1}`}
            onClick={() => onChange(entries.filter((_, j) => j !== i))}
          >
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="self-start"
        onClick={() => onChange([...entries, { key: "", value: "" }])}
      >
        <Plus className="size-3.5" />
        {addLabel}
      </Button>
    </div>
  )
}
