import { useState } from "react"
import { Filter } from "lucide-react"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

interface ExpressionFilterProps {
  /** The expression in effect. */
  value: string
  /** Applies an expression — on Enter, not per keystroke, so a half-typed one never errors. */
  onSubmit: (expression: string) => void
  /** GetPartitions' error for `value`, shown word for word. */
  error?: unknown
  /** A starting point for the placeholder, from the table's first key. */
  example: string
  className?: string
}

/**
 * The Glue `Expression` box. What is typed is sent to GetPartitions exactly
 * as an SDK would send it, so what works here works in code, and the
 * service's own parse error is shown under it.
 */
export function ExpressionFilter({
  value,
  onSubmit,
  error,
  example,
  className,
}: ExpressionFilterProps) {
  const [draft, setDraft] = useState(value)
  const message = error instanceof Error ? error.message : error ? String(error) : undefined
  return (
    <form
      role="search"
      className={cn("flex flex-col gap-1", className)}
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(draft.trim())
      }}
    >
      <div className="relative">
        <Filter className="absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
        <Input
          aria-label="Partition expression"
          aria-invalid={message ? true : undefined}
          aria-describedby={message ? "partition-expression-error" : undefined}
          placeholder={`Glue expression, e.g. ${example} — ⏎ to apply`}
          className="pl-8 font-mono text-xs"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
      </div>
      {message && (
        <p id="partition-expression-error" role="alert" className="font-mono text-xs text-danger">
          {message}
        </p>
      )}
    </form>
  )
}
