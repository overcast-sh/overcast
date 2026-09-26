import { AlertTriangle } from "lucide-react"
import type { AthenaError } from "@aws-sdk/client-athena"
import { Advisory } from "@/components/ui/advisory"
import { DIALECT_DOCS, dialectDifference } from "../../dialect"

/** `ErrorCategory` as Athena documents it. */
const CATEGORY: Record<number, string> = { 1: "System error", 2: "User error", 3: "Other error" }

/**
 * A failed query, or a `StartQueryExecution` that was refused: the error's
 * category, type and message as Athena reports them, and — when the SQL runs
 * into a known difference between Athena and Overcast's engine — an
 * advisory naming it.
 */
export function QueryError({
  sql,
  error,
  message,
}: {
  sql: string
  /** The execution's `AthenaError`, when it ran and failed. */
  error?: AthenaError
  /** The message: the `AthenaError`'s, its `StateChangeReason`, or the refused request's. */
  message: string
}) {
  const difference = dialectDifference(sql)
  const category = error?.ErrorCategory !== undefined ? CATEGORY[error.ErrorCategory] : undefined
  return (
    <div className="flex flex-col gap-2">
      <div
        role="alert"
        className="flex items-start gap-2.5 rounded-md border border-danger/40 bg-danger-muted px-3 py-2"
      >
        <AlertTriangle aria-hidden className="mt-0.5 size-3.5 shrink-0 text-danger" />
        <div className="flex min-w-0 flex-col gap-1">
          {error && (
            <p className="font-mono text-2xs text-danger">
              {category ?? "Error"}
              {error.ErrorType !== undefined && ` · type ${error.ErrorType}`}
              {error.Retryable && " · retryable"}
            </p>
          )}
          <p className="font-mono text-xs break-words whitespace-pre-wrap text-fg">{message}</p>
        </div>
      </div>
      {difference && (
        <Advisory tone="info" title={difference.title} docsPath={DIALECT_DOCS}>
          {difference.detail}
        </Advisory>
      )}
    </div>
  )
}
