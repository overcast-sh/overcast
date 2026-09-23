import type { LucideIcon } from "lucide-react"
import { ShieldOff, BookOpen, ExternalLink, AlertCircle } from "lucide-react"
import { Tooltip } from "@/components/ui/tooltip"

export interface PlaceholderPageProps {
  /** AWS service display name, e.g. "CloudFormation" */
  serviceName: string
  /** One-line description of what this service does */
  description: string
  /** Optional Lucide icon component */
  Icon?: LucideIcon
  /** Optional AWS docs URL for this service */
  docsUrl?: string
  /**
   * Current emulation tier.
   * "stub" = registered in backend with a small subset; the rest returns 501.
   * "unsupported" = not in Overcast at all.
   * Omit for legacy use where tier is unknown.
   */
  tier?: "stub" | "unsupported"
  /**
   * Aspirational tier. When different from `tier`, a WIP badge is shown.
   */
  goalTier?: "unsupported" | "stub" | "partial" | "full"
  /** Why Overcast doesn't support this service, or for a stub, what it does answer */
  reason?: string
}

const TIER_LABELS: Record<string, string> = {
  full: "Full",
  partial: "Partial",
  inert: "Inert",
  stub: "Stub",
  unsupported: "Not supported",
}

const TIER_DESCRIPTIONS: Record<string, string> = {
  full: "All operations are implemented and behave like real AWS.",
  partial: "Core operations work. Some endpoints return 501 or have limited behaviour.",
  inert:
    "Service accepts requests but operations have no side effects — always returns success without storing state.",
  stub: "Registered with only a small subset of operations; every other operation returns 501 Not Implemented.",
  unsupported: "Not registered in Overcast. Requests will fall through to the 501 handler.",
}

/**
 * PlaceholderPage is rendered for stub and unsupported services.
 * It communicates current support status, aspirational tier, and
 * links to AWS documentation.
 */
export function PlaceholderPage({
  serviceName,
  description,
  Icon = ShieldOff,
  docsUrl,
  tier,
  goalTier,
  reason,
}: PlaceholderPageProps) {
  const isWip = tier !== undefined && goalTier !== undefined && tier !== goalTier
  const isStub = tier === "stub"

  return (
    <div className="mx-auto max-w-2xl py-16 text-center">
      <div className="mb-6 flex justify-center">
        <div className="flex h-16 w-16 items-center justify-center rounded-2xl border border-border bg-bg-elevated">
          <Icon className="h-8 w-8 text-fg-subtle" />
        </div>
      </div>

      <div className="flex items-baseline justify-center gap-2">
        <h1 className="font-mono text-2xl font-semibold text-fg">{serviceName}</h1>
        {isWip && (
          <Tooltip content="This service is a work in progress — support is being actively improved.">
            <span className="mb-0.5 inline-flex cursor-default items-center gap-1 rounded-full bg-warning-muted px-2 py-0.5 font-mono text-xs font-medium text-warning">
              <AlertCircle className="h-3 w-3" />
              WIP
            </span>
          </Tooltip>
        )}
      </div>
      <p className="mt-2 text-sm text-fg-muted">{description}</p>

      {tier !== undefined && (
        <div className="mt-4 flex items-center justify-center gap-4 text-xs text-fg-subtle">
          <span>
            Current:{" "}
            <Tooltip content={TIER_DESCRIPTIONS[tier] ?? tier}>
              <span className="cursor-default font-medium text-fg-muted underline decoration-fg-subtle decoration-dotted underline-offset-2">
                {TIER_LABELS[tier] ?? tier}
              </span>
            </Tooltip>
          </span>
          {goalTier && goalTier !== tier && (
            <>
              <span className="text-border">→</span>
              <span>
                Goal:{" "}
                <Tooltip content={TIER_DESCRIPTIONS[goalTier] ?? goalTier}>
                  <span className="cursor-default font-medium text-fg-muted underline decoration-fg-subtle decoration-dotted underline-offset-2">
                    {TIER_LABELS[goalTier] ?? goalTier}
                  </span>
                </Tooltip>
              </span>
            </>
          )}
        </div>
      )}

      <div className="mt-8 space-y-4 rounded-xl border border-border-muted bg-bg-elevated p-6 text-left">
        {isStub ? (
          <>
            <p className="text-sm font-medium text-fg">Stub — most operations return 501</p>
            {/* Which operations a stub answers differs per service, so the
                entry's own reason says what works. */}
            {reason && <p className="text-sm text-fg-muted">{reason}</p>}
          </>
        ) : (
          <>
            <p className="text-sm font-medium text-fg">Not supported in Overcast</p>
            {reason && <p className="text-sm text-fg-muted">{reason}</p>}
          </>
        )}

        <p className="text-sm text-fg-subtle">
          API responses and behaviour are subject to change as support improves.{" "}
          <a
            href="https://github.com/overcast-sh/overcast/issues"
            target="_blank"
            rel="noreferrer"
            className="text-accent hover:underline"
          >
            Open a GitHub issue
          </a>{" "}
          to request or track support for this service.
        </p>

        {docsUrl && (
          <a
            href={docsUrl}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 text-sm font-medium text-accent hover:underline"
          >
            <BookOpen className="h-3.5 w-3.5" />
            AWS {serviceName} documentation
            <ExternalLink className="h-3 w-3" />
          </a>
        )}
      </div>
    </div>
  )
}
