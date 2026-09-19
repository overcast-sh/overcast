import { HighlightedCode } from "@/components/ui/highlighted-code"
import { CopyButton } from "@/components/ui/copy-button"
import { SectionLabel } from "@/components/ui/primitives"
import { cn } from "@/lib/utils"
import { prettyJSON } from "../format"

/**
 * A labelled, copyable, syntax-highlighted JSON payload — the shape every
 * input, output and definition fragment on the Step Functions pages takes.
 * Text that is not JSON renders as it came.
 */
export function JsonPane({
  label,
  value,
  empty = "—",
  className,
  bodyClassName,
}: {
  label?: string
  value: string | undefined
  empty?: string
  className?: string
  bodyClassName?: string
}) {
  const text = prettyJSON(value)
  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      {label && (
        <div className="flex items-center justify-between gap-2">
          <SectionLabel>{label}</SectionLabel>
          {text && <CopyButton value={text} noun={label.toLowerCase()} tone="inline" />}
        </div>
      )}
      <div
        tabIndex={0}
        className={cn(
          "min-h-10 overflow-auto rounded-md border border-border bg-bg-muted p-3 font-mono text-xs text-fg",
          bodyClassName,
        )}
      >
        {text ? (
          <HighlightedCode
            text={text}
            language={looksLikeJson(text) ? "json" : null}
            className="whitespace-pre"
          />
        ) : (
          <span className="text-fg-subtle">{empty}</span>
        )}
      </div>
    </div>
  )
}

function looksLikeJson(text: string): boolean {
  const first = text.trimStart()[0]
  return first === "{" || first === "[" || first === '"'
}
