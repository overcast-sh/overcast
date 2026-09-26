import { useMemo, useState } from "react"
import { ChevronsUpDown } from "lucide-react"
import { diffLines, foldUnchanged, type DiffLine } from "@/lib/line-diff"
import { formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"

/**
 * A unified line diff of two texts: removed lines on a danger wash, added on a
 * success wash, each with its line number on both sides, and long unchanged
 * runs folded behind a *Show N unchanged lines* control.
 *
 * Generic — two versions of a metadata file, of a table definition, of a
 * policy. The caller formats both sides the same way first (pretty-printed
 * JSON diffs line by line; one-line JSON would be a single changed line).
 *
 * A plain `<table>` rather than `ResourceTable`: its rows are the lines of a
 * diff, not resources — nothing to sort, filter or act on.
 */
export function TextDiff({
  before,
  after,
  className,
  "aria-label": ariaLabel,
}: {
  before: string
  after: string
  className?: string
  "aria-label"?: string
}) {
  const segments = useMemo(() => foldUnchanged(diffLines(before, after)), [before, after])
  const changed = segments.some((s) => s.lines.some((l) => l.kind !== "same"))
  return (
    <div
      role="region"
      aria-label={ariaLabel}
      className={cn(
        "overflow-auto rounded-md border border-border bg-bg font-mono text-xs leading-5",
        className,
      )}
    >
      {!changed ? (
        <p className="px-3 py-6 text-center text-fg-subtle">The two versions are identical.</p>
      ) : (
        <table className="w-full border-collapse">
          <tbody>
            {segments.map((segment) =>
              segment.kind === "folded" ? (
                <FoldedRun key={lineKey(segment.lines[0])} lines={segment.lines} />
              ) : (
                segment.lines.map((line) => <Line key={lineKey(line)} line={line} />)
              ),
            )}
          </tbody>
        </table>
      )}
    </div>
  )
}

function lineKey(line: DiffLine): string {
  switch (line.kind) {
    case "same":
      return `s${line.oldLine}`
    case "removed":
      return `r${line.oldLine}`
    case "added":
      return `a${line.newLine}`
  }
}

const TONE = {
  same: { row: "", sign: " " },
  removed: { row: "bg-danger-muted text-danger", sign: "−" },
  added: { row: "bg-success-muted text-success", sign: "+" },
} as const

function Line({ line }: { line: DiffLine }) {
  const tone = TONE[line.kind]
  const oldLine = line.kind === "added" ? "" : line.oldLine
  const newLine = line.kind === "removed" ? "" : line.newLine
  return (
    <tr className={tone.row}>
      <td className="w-10 px-2 text-right text-fg-subtle select-none">{oldLine}</td>
      <td className="w-10 px-2 text-right text-fg-subtle select-none">{newLine}</td>
      <td aria-hidden className="w-4 text-center select-none">
        {tone.sign}
      </td>
      <td className={cn("pr-3 whitespace-pre", line.kind === "same" && "text-fg")}>
        {line.kind !== "same" && <span className="sr-only">{line.kind} </span>}
        {line.text}
      </td>
    </tr>
  )
}

/** Unchanged lines out of view until asked for; opening shows them in place. */
function FoldedRun({ lines }: { lines: DiffLine[] }) {
  const [open, setOpen] = useState(false)
  if (open) return lines.map((line) => <Line key={lineKey(line)} line={line} />)
  return (
    <tr className="bg-bg-muted">
      <td colSpan={4} className="px-2 py-0.5">
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="inline-flex cursor-pointer items-center gap-1.5 text-fg-subtle transition-colors hover:text-accent"
        >
          <ChevronsUpDown aria-hidden className="size-3" />
          Show {formatQuantity(lines.length, "unchanged line")}
        </button>
      </td>
    </tr>
  )
}
