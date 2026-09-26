import { DiffEditor } from "@monaco-editor/react"
import { useIsDarkTheme } from "@/hooks/use-theme"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { SkeletonRows } from "./skeleton"

/**
 * A read-only, side-by-side diff of two texts — two table versions, two
 * metadata files, two definitions — on Monaco's diff editor, the same editor
 * and themes `CodeBrowser` uses. Each side is captioned, so the reader knows
 * which is older without counting columns.
 *
 * ```tsx
 * <DiffViewer
 *   original={JSON.stringify(v3, null, 2)}
 *   modified={JSON.stringify(v4, null, 2)}
 *   originalLabel="Version 3"
 *   modifiedLabel="Version 4"
 * />
 * ```
 */
export interface DiffViewerProps {
  original: string
  modified: string
  originalLabel: string
  modifiedLabel: string
  /** Monaco language id. JSON by default. */
  language?: string
  /** Editor height, any CSS length. */
  height?: string
  className?: string
}

export function DiffViewer({
  original,
  modified,
  originalLabel,
  modifiedLabel,
  language = "json",
  height = "28rem",
  className,
}: DiffViewerProps) {
  const isDark = useIsDarkTheme()
  return (
    <div
      className={cn("overflow-hidden rounded-card border border-border bg-bg-elevated", className)}
    >
      <div className="grid grid-cols-2 border-b border-border bg-bg-muted">
        {[originalLabel, modifiedLabel].map((label) => (
          <span key={label} className={cn(fieldLabel, "px-3 py-2 text-fg-subtle")}>
            {label}
          </span>
        ))}
      </div>
      <DiffEditor
        height={height}
        language={language}
        original={original}
        modified={modified}
        theme={isDark ? "vs-dark" : "light"}
        loading={<SkeletonRows rows={8} className="w-full" />}
        options={{
          readOnly: true,
          originalEditable: false,
          renderSideBySide: true,
          // Wide enough for two JSON documents at 1024px; below that Monaco
          // stacks the sides, which reads better than two slivers.
          renderSideBySideInlineBreakpoint: 720,
          // A table input is mostly unchanged between versions: fold the
          // runs that match so the change is on screen without scrolling.
          hideUnchangedRegions: { enabled: true, contextLineCount: 3 },
          minimap: { enabled: false },
          scrollBeyondLastLine: false,
          fontSize: 12,
          automaticLayout: true,
          renderOverviewRuler: false,
        }}
      />
    </div>
  )
}
