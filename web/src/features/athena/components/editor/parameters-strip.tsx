import { Input } from "@/components/ui/input"
import { FieldLabel } from "@/components/ui/primitives"

/**
 * One field per `?` in the SQL, sent in order as `ExecutionParameters`.
 * Each value is one SQL literal, written as SQL writes it: `42`,
 * `'2026-09-26'`.
 */
export function ParametersStrip({
  count,
  values,
  onChange,
}: {
  count: number
  values: readonly string[]
  onChange: (values: string[]) => void
}) {
  if (count === 0) return null
  return (
    <fieldset className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md border border-border bg-bg-muted px-3 py-2">
      <legend className="sr-only">Execution parameters</legend>
      <FieldLabel aria-hidden>parameters</FieldLabel>
      {Array.from({ length: count }, (_, i) => (
        <label key={i} className="flex items-center gap-1.5 font-mono text-2xs text-fg-subtle">
          ?{i + 1}
          <Input
            aria-label={`Parameter ${i + 1}`}
            placeholder="'value' or 42"
            value={values[i] ?? ""}
            onChange={(e) => {
              const next = Array.from({ length: count }, (_, j) => values[j] ?? "")
              next[i] = e.target.value
              onChange(next)
            }}
            className="h-7 w-36"
          />
        </label>
      ))}
    </fieldset>
  )
}
