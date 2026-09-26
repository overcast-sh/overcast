import type { ReactNode } from "react"
import { AlertTriangle, ExternalLink, Info } from "lucide-react"
import { Link } from "@tanstack/react-router"
import { cn } from "@/lib/utils"

/**
 * An advisory: something a developer must know where they are working,
 * usually where Overcast differs from AWS — the query engine is off, a
 * dialect difference, a bucket S3 Tables manages. Never a tooltip: the point
 * is that it is on screen when it matters.
 *
 * Compact, in sentence case, with an optional link to the docs page that
 * explains it (a path under `docs/`, anchor allowed) and an optional action
 * at the end of the row.
 *
 * ```tsx
 * <Advisory title="The query engine is off" docsPath="services/athena/limitations.md#the-engine">
 *   Queries succeed with empty results. Unset <code>ATHENA_ENGINE</code> to run them.
 * </Advisory>
 * ```
 */
export interface AdvisoryProps {
  /** `warning` for something that changes an outcome; `info` for a difference to be aware of. */
  tone?: "warning" | "info"
  /** One short sentence-case line. */
  title: ReactNode
  /** The detail: what it means here and what to do. */
  children?: ReactNode
  /** A page under `docs/`, e.g. `services/athena.md#differences-from-aws`. */
  docsPath?: string
  /** A control at the end of the row — *Set a location*, *Retry*. */
  action?: ReactNode
  className?: string
}

const TONES = {
  warning: {
    icon: AlertTriangle,
    box: "border-warning/40 bg-warning-muted",
    iconClass: "text-warning",
  },
  info: { icon: Info, box: "border-accent/30 bg-accent-muted", iconClass: "text-accent" },
} as const

export function Advisory({
  tone = "warning",
  title,
  children,
  docsPath,
  action,
  className,
}: AdvisoryProps) {
  const style = TONES[tone]
  const Icon = style.icon
  const [path, hash] = docsPath?.split("#") ?? []
  return (
    <div
      role="note"
      className={cn(
        "flex items-start gap-2.5 rounded-md border px-3 py-2 text-xs text-fg-muted",
        style.box,
        className,
      )}
    >
      <Icon aria-hidden className={cn("mt-0.5 size-3.5 shrink-0", style.iconClass)} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="font-medium text-fg">{title}</p>
        {children && <div className="leading-relaxed">{children}</div>}
        {path && (
          <Link
            to="/docs"
            search={{ path }}
            hash={hash}
            className="inline-flex w-fit items-center gap-1 text-accent underline underline-offset-2"
          >
            Read more
            <ExternalLink aria-hidden className="size-3" />
          </Link>
        )}
      </div>
      {action && <div className="shrink-0 self-center">{action}</div>}
    </div>
  )
}
