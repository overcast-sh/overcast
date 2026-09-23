import { useMemo } from "react"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { CopyButton } from "@/components/ui/copy-button"
import { ArnLink } from "@/components/ui/arn-link"
import { findLinkableArns } from "@/components/ui/arn-routes"
import { SectionLabel } from "@/components/ui/primitives"
import { cn } from "@/lib/utils"
import { prettyJSON } from "../format"

/**
 * A labelled, copyable, syntax-highlighted JSON payload — the shape every
 * input, output and definition fragment on the Step Functions pages takes.
 * Text that is not JSON renders as it came. Any ARN in it that the console
 * has a page for is listed under it as a link, since the highlighted text
 * itself cannot carry one.
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
  const arns = useMemo(() => findLinkableArns(text).slice(0, MAX_LINKED_ARNS), [text])
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
      {arns.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs">
          <span className="text-fg-subtle">Resources:</span>
          {arns.map((arn) => (
            <span key={arn} title={arn}>
              <ArnLink arn={arn} label={shortName(arn)} className="text-2xs" />
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

/** How many ARNs a pane links before it stops — a list of hundreds helps nobody. */
const MAX_LINKED_ARNS = 8

/** An ARN's last name-like segment: a function's name, not its qualifier; a role's name, not its path. */
function shortName(arn: string): string {
  const parts = arn.split(":")
  const resource = parts.slice(5)
  // function:name[:qualifier], stateMachine:name[:version], execution:machine:name
  if (resource.length >= 2 && /^(function|stateMachine|log-group)$/.test(resource[0]))
    return resource[1]
  return (resource.at(-1) ?? arn).split("/").at(-1) || arn
}

function looksLikeJson(text: string): boolean {
  const first = text.trimStart()[0]
  return first === "{" || first === "[" || first === '"'
}
