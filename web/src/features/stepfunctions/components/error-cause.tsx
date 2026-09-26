/**
 * An error as a developer wants to read it: the error name, the message the
 * function threw rather than the JSON it arrived wrapped in, what the name
 * means when that is not obvious, and the stack trace behind a disclosure.
 */
import { Lightbulb } from "lucide-react"
import { CopyButton } from "@/components/ui/copy-button"
import { LinkifiedText } from "@/components/ui/arn-link"
import { cn } from "@/lib/utils"
import { errorHint, parseCause } from "../lambda-invocations"
import { formatQuantity } from "@/lib/format"

interface Props {
  error: string | undefined
  cause: string | undefined
  /** `caught` for a failure a Catch absorbed, `retried` for one a Retry did. */
  tone?: "failed" | "caught" | "retried"
  /** A line under the error, such as "Caught — the execution moved on through a Catch." */
  note?: string
  className?: string
}

export function ErrorCause({ error, cause, tone = "failed", note, className }: Props) {
  const parsed = parseCause(cause)
  const hint = errorHint(error, cause)
  // The function's own error type is worth showing only when the ASL name
  // is not already it — Lambda.Unknown hides a TypeError, a custom name does not.
  const errorType = parsed?.errorType && parsed.errorType !== error ? parsed.errorType : undefined
  return (
    <div
      className={cn(
        "flex flex-col gap-1.5 rounded-md border px-3 py-2 text-xs",
        tone === "failed"
          ? "border-danger/30 bg-danger-muted text-danger"
          : "border-warning/30 bg-warning-muted text-warning",
        className,
      )}
    >
      <div className="flex items-start gap-2">
        <p className="min-w-0 flex-1 font-mono font-semibold wrap-anywhere">{error ?? "Error"}</p>
        {cause && <CopyButton value={cause} noun="error cause" tone="inline" />}
      </div>
      {parsed && (
        <p className="wrap-anywhere whitespace-pre-wrap text-fg">
          {errorType && <span className="font-mono font-semibold">{errorType}: </span>}
          <LinkifiedText text={parsed.errorMessage} />
        </p>
      )}
      {note && <p className="text-fg-muted">{note}</p>}
      {hint && (
        <p className="flex gap-1.5 text-fg-muted">
          <Lightbulb className="mt-0.5 h-3 w-3 shrink-0" />
          <span>{hint}</span>
        </p>
      )}
      {parsed && parsed.stack.length > 0 && (
        <details className="group">
          <summary className="cursor-pointer text-fg-muted select-none hover:text-fg">
            Stack trace · {formatQuantity(parsed.stack.length, "frame")}
          </summary>
          <pre className="mt-1.5 max-h-60 overflow-auto rounded border border-border bg-bg-muted p-2 font-mono text-2xs leading-relaxed whitespace-pre text-fg">
            {parsed.stack.join("\n")}
          </pre>
        </details>
      )}
    </div>
  )
}
