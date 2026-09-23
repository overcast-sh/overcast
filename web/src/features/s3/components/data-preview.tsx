import type { ReactNode } from "react"
import { AlertTriangle, Download, Info, type LucideIcon } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { EmptyState } from "@/components/ui/primitives"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { SkeletonRows } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

/**
 * The frame every S3 preview shares — the panel, its notices, the view toggle,
 * the raw text — and the Avro notice. The data-file previews themselves (a
 * `DataGrid` over CSV, TSV, JSON Lines or Parquet) are in `data-file-preview.tsx`.
 */

// ─── Frame ────────────────────────────────────────────────────────────────

/**
 * One frame for every preview: a header strip naming what is shown and how
 * much of it, an optional control on the right, notes about what was left
 * out, then the body. The body owns its own scrolling.
 */
export function PreviewPanel({
  format,
  meta,
  control,
  notices,
  children,
  className,
}: {
  /** The format, as a badge — "CSV", "Parquet". Omit for plain text. */
  format?: string
  /** What is on screen and how much of the object it is. */
  meta?: ReactNode
  control?: ReactNode
  notices?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    // The card surface, flat: it sits inside a dialog that already floats.
    <Card
      role="region"
      aria-label={format ? `${format} preview` : "Preview"}
      className={cn("flex min-h-0 flex-col overflow-hidden shadow-none", className)}
    >
      <div className="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border bg-bg-muted px-3 py-2">
        {format && <Badge variant="outline">{format}</Badge>}
        <span className="min-w-0 font-mono text-2xs text-fg-muted">{meta}</span>
        {control && <div className="ml-auto">{control}</div>}
      </div>
      {notices}
      <div className="flex min-h-0 flex-1 flex-col">{children}</div>
    </Card>
  )
}

/**
 * A note under the panel's header — what the preview left out and why.
 * `info` for the expected limits (the byte cap), `warning` for a file that
 * did not read the way its name promised.
 */
export function PreviewNotice({
  tone = "info",
  children,
}: {
  tone?: "info" | "warning"
  children: ReactNode
}) {
  const Icon = tone === "warning" ? AlertTriangle : Info
  return (
    <p
      className={cn(
        "flex items-start gap-2 border-b border-border px-3 py-2 text-xs text-fg-muted",
        tone === "warning" ? "bg-warning-muted" : "bg-bg-elevated",
      )}
    >
      <Icon
        aria-hidden
        className={cn(
          "mt-px h-3.5 w-3.5 shrink-0",
          tone === "warning" ? "text-warning" : "text-fg-subtle",
        )}
      />
      <span>{children}</span>
    </p>
  )
}

export interface ToggleOption<T extends string> {
  value: T
  label: string
  icon: LucideIcon
}

/**
 * Two or three views of one object, as a segmented control. Each segment is
 * a real button with `aria-pressed`, so Tab reaches it, Space and Enter work,
 * and a screen reader hears which view is on.
 */
export function ViewToggle<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: readonly ToggleOption<T>[]
  onChange: (value: T) => void
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="flex items-center gap-0.5 rounded-control border border-border bg-bg-elevated p-0.5"
    >
      {options.map((option) => {
        const Icon = option.icon
        const pressed = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={pressed}
            onClick={() => onChange(option.value)}
            className={cn(
              "inline-flex h-6 cursor-pointer items-center gap-1.5 rounded-sm px-2 font-mono text-2xs transition-colors",
              "focus-visible:outline-2 focus-visible:outline-accent",
              pressed ? "bg-accent-muted text-accent" : "text-fg-muted hover:text-fg",
            )}
          >
            <Icon aria-hidden className="h-3.5 w-3.5" strokeWidth={1.9} />
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

/** The raw text of an object, as the dialog has always shown it. */
export function RawText({
  text,
  language,
  keepLines = language != null,
}: {
  text: string
  language: string | null
  /**
   * Keep each line on one line and scroll sideways. The default follows the
   * language; a CSV or JSON Lines file has none but is still line-shaped —
   * one record per line — and wrapping it would blur where records end.
   */
  keepLines?: boolean
}) {
  return (
    <HighlightedCode
      text={text}
      language={language}
      className={cn(
        "max-h-[55vh] min-h-0 overflow-auto bg-bg-muted p-3 font-mono text-xs leading-relaxed text-fg",
        // Policy, not accident (kept from the dialog's first commit): a
        // document we chose a language for is code — it keeps its line shape
        // and scrolls horizontally — while arbitrary plain text wraps. The
        // too-large-to-format case (a 1 MiB minified bundle) has no language
        // and so wraps, where it would otherwise be one mile-wide line.
        keepLines ? "whitespace-pre" : "wrap-break-word whitespace-pre-wrap",
      )}
    />
  )
}

/** The preview body while bytes are on their way: static, never a spinner. */
export function PreviewSkeleton({ noun = "preview" }: { noun?: string }) {
  return <SkeletonRows rows={6} noun={noun} />
}

// ─── Avro and other unreadable objects ─────────────────────────────────────

/**
 * Avro is binary, and the console does not decode it. Saying so beats the
 * generic "no preview" line: an Iceberg table's manifest list and manifests
 * are Avro, so a developer opening one is mid-investigation and needs to know
 * this is a limit of the console, not a broken file.
 */
export function AvroNotice({ downloadHref }: { downloadHref: string }) {
  return (
    <PreviewPanel format="Avro" meta="binary">
      <UnreadableObject
        title="Avro — not previewed"
        description="The console does not decode Avro. Iceberg manifests and manifest lists are Avro files; download this one to read it with avro-tools."
        downloadHref={downloadHref}
      />
    </PreviewPanel>
  )
}

export function UnreadableObject({
  title,
  description,
  secondary,
  downloadHref,
}: {
  title: string
  description: string
  secondary?: string
  downloadHref: string
}) {
  return (
    <EmptyState
      className="px-6 py-10"
      title={title}
      description={secondary ? `${description} ${secondary}` : description}
      action={
        <Button asChild variant="secondary" size="sm">
          <a href={downloadHref} download>
            <Download aria-hidden className="h-3.5 w-3.5" /> Download instead
          </a>
        </Button>
      }
    />
  )
}
