/**
 * The controls around the object listing: the search box, the scope switch,
 * the sortable headers, and the tick boxes and selection bar that a
 * multi-object download is started from.
 *
 * Split out of `bucket-detail` because none of it needs the bucket, the queries
 * or the router — it is all props in, callbacks out, which is also what makes
 * it testable without standing up an S3 listing.
 */

import { useEffect, useRef } from "react"
import { Search, X, Folder, ListTree, Download } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/primitives"
// eslint-disable-next-line local/prefer-resource-table -- borrows `TableHead` for the shared sortable header; it renders no table of its own
import { TableHead } from "@/components/ui/table"
import { Tooltip } from "@/components/ui/tooltip"
import { formatBytes, formatCount, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { ListScope } from "@/features/s3/data"
import type { NameSlice, ObjectSort, SortColumn } from "@/features/s3/object-browser"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

// ─── Search bar ────────────────────────────────────────────────────────────

export interface ObjectSearchBarProps {
  value: string
  onChange: (value: string) => void
  scope: ListScope
  onScopeChange: (scope: ListScope) => void
  /** What the user is looking at: rows left after filtering. */
  matches: number
  /** What it cost: entries pulled back from S3 so far, before filtering. */
  scanned: number
  /** Still pulling pages to make the view complete. */
  isScanning: boolean
  /** Set when the scan stopped at its page cap instead of the end of the listing. */
  capped: boolean
  /** What the rows are — objects in the normal view, versions in history. */
  noun: ListingNoun
}

/** A listing's noun, both ways, for the search box's name and the count beside it. */
export interface ListingNoun {
  one: string
  many: string
}

export const LISTING_NOUNS = {
  objects: { one: "object", many: "objects" },
  versions: { one: "version", many: "versions" },
} as const satisfies Record<string, ListingNoun>

const SCOPES = [
  {
    value: "folder",
    label: "This folder",
    icon: Folder,
    hint: "List only what sits directly in this folder.",
  },
  {
    value: "recursive",
    label: "All nested",
    icon: ListTree,
    hint: "List every object beneath this prefix, folders flattened into full keys.",
  },
] as const satisfies readonly SegmentedOption<ListScope>[]

export function ObjectSearchBar({
  value,
  onChange,
  scope,
  onScopeChange,
  matches,
  scanned,
  isScanning,
  capped,
  noun,
}: ObjectSearchBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="relative min-w-[16rem] flex-1">
        <Search
          className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle"
          aria-hidden
        />
        <Input
          type="search"
          value={value}
          aria-label={`Search ${noun.many}`}
          placeholder="Search by name, or type a prefix like logs/2024/"
          spellCheck={false}
          autoComplete="off"
          // `type="search"` is kept for the searchbox role, but WebKit's own
          // clear glyph is suppressed: it would sit beside ours, and only one
          // of the two is keyboard-reachable or labelled.
          className={cn("pl-8 [&::-webkit-search-cancel-button]:appearance-none", value && "pr-8")}
          onChange={(e) => onChange(e.target.value)}
          // Escape clears rather than blurring: with a filter applied, "get me
          // back to the whole listing" is the thing the key is reached for.
          onKeyDown={(e) => {
            if (e.key !== "Escape" || value === "") return
            e.preventDefault()
            e.stopPropagation()
            onChange("")
          }}
        />
        {value && (
          <button
            type="button"
            aria-label="Clear search"
            className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1 text-fg-subtle transition-colors hover:text-fg"
            onClick={() => onChange("")}
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>

      <SegmentedControl
        label="Search scope"
        value={scope}
        options={SCOPES}
        onChange={onScopeChange}
        size="md"
      />

      <ScanSummary
        matches={matches}
        scanned={scanned}
        isScanning={isScanning}
        capped={capped}
        filtered={value.trim() !== ""}
        noun={noun}
      />
    </div>
  )
}

/**
 * What the listing is showing and what it took to get there.
 *
 * The scanned count is not decoration: a filter or a non-default sort can only
 * be trusted over the pages that have been read, so the number that was read is
 * the only thing that tells the user whether "no matches" means "not in this
 * bucket" or "not in the part of it we have".
 */
function ScanSummary({
  matches,
  scanned,
  isScanning,
  capped,
  filtered,
  noun,
}: {
  matches: number
  scanned: number
  isScanning: boolean
  capped: boolean
  filtered: boolean
  noun: ListingNoun
}) {
  const total = formatQuantity(scanned, noun.one, noun.many)
  if (!filtered && !isScanning && !capped) {
    return <span className="font-mono text-2xs whitespace-nowrap text-fg-subtle">{total}</span>
  }

  return (
    <span className="flex items-center gap-1.5 font-mono text-2xs whitespace-nowrap text-fg-muted">
      {isScanning && <Spinner className="h-3 w-3" />}
      {filtered ? (
        <>
          {formatCount(matches)} of {total}
        </>
      ) : (
        total
      )}
      {isScanning && <span className="text-fg-subtle">scanning…</span>}
      {capped && !isScanning && (
        <Tooltip content="The listing was longer than this view scans. Narrow it with a prefix — type it into the search box — to see the rest.">
          <span className="cursor-help text-warning">partial</span>
        </Tooltip>
      )}
    </span>
  )
}

// ─── Sortable header ───────────────────────────────────────────────────────

export interface SortHeadProps {
  column: SortColumn
  label: string
  sort: ObjectSort
  onSort: (column: SortColumn) => void
  className?: string
}

/**
 * A column header that sorts. The arrow only appears on the active column —
 * a permanent arrow on every header reads as a state rather than an offer.
 */
export function SortHead({ column, label, sort, onSort, className }: SortHeadProps) {
  const active = sort.column === column
  return (
    <TableHead
      className={cn("p-0", className)}
      aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}
    >
      <button
        type="button"
        onClick={() => onSort(column)}
        className={cn(
          "inline-flex h-full w-full cursor-pointer items-center gap-1 px-4 py-2 transition-colors select-none hover:text-fg",
          active && "text-fg",
        )}
        title={`Sort by ${label.toLowerCase()}`}
      >
        {label}
        <span className={cn("text-2xs leading-none", !active && "opacity-0")} aria-hidden>
          {sort.direction === "asc" ? "▲" : "▼"}
        </span>
      </button>
    </TableHead>
  )
}

// ─── Match highlighting ────────────────────────────────────────────────────

/**
 * A row name with the searched-for run picked out. Scanning a filtered listing
 * for *why* a row matched is the slow part of a search, and this removes it.
 */
export function HighlightedName({ slices }: { slices: NameSlice[] }) {
  return (
    <>
      {slices.map((slice, i) =>
        slice.match ? (
          <mark key={i} className="rounded-[2px] bg-warning-muted text-inherit">
            {slice.text}
          </mark>
        ) : (
          <span key={i}>{slice.text}</span>
        ),
      )}
    </>
  )
}

// ─── Selection ─────────────────────────────────────────────────────────────

export interface RowCheckboxProps {
  checked: boolean
  /** Some but not all of what this box covers is ticked. */
  indeterminate?: boolean
  onChange: () => void
  /** What the box ticks, for screen readers — every row's box looks alike. */
  label: string
}

/**
 * The tick box on a row, and the one in the header that covers them all.
 *
 * `indeterminate` is a property rather than an attribute, so it can only be
 * set through the element — which is why this exists as a component instead of
 * three copies of an `<input>`.
 */
export function RowCheckbox({ checked, indeterminate = false, onChange, label }: RowCheckboxProps) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate
  }, [indeterminate])

  return (
    // 24px, not 16: WCAG 2.5.8 measures the control's own box, so padding on a wrapper
    // does not count. The rows are already taller than this, so the only cost is 8px in
    // one narrow selection column — and 24px is the size a checkbox wants on a touch
    // screen anyway.
    <label className="inline-flex cursor-pointer items-center justify-center">
      <input
        ref={ref}
        type="checkbox"
        className="h-6 w-6 cursor-pointer rounded accent-accent"
        checked={checked}
        aria-label={label}
        onChange={onChange}
        onClick={(e) => e.stopPropagation()}
      />
    </label>
  )
}

export interface SelectionBarProps {
  count: number
  /** Total size of the selected rows on screen; see `selectionSummary`. */
  bytes: number
  /** Set when the selection cannot be downloaded, and why. */
  blockedReason?: string
  onDownload: () => void
  onClear: () => void
}

/**
 * What is ticked, and what can be done with it. Absent until something is.
 *
 * The size is shown next to the count because the download it offers is a
 * single archive: how big that is going to be is the one thing the user cannot
 * work out from the rows themselves.
 */
export function SelectionBar({
  count,
  bytes,
  blockedReason,
  onDownload,
  onClear,
}: SelectionBarProps) {
  if (count === 0) return null
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-accent/40 bg-accent-muted px-3 py-2 text-sm">
      <span className="font-medium">{formatQuantity(count, "object")} selected</span>
      <span className="font-mono text-2xs text-fg-muted">{formatBytes(bytes)}</span>
      <div className="ml-auto flex items-center gap-2">
        {blockedReason && <span className="text-xs text-warning">{blockedReason}</span>}
        <Button size="sm" onClick={onDownload} disabled={!!blockedReason}>
          <Download className="h-3.5 w-3.5" /> Download .zip
        </Button>
        <Button variant="ghost" size="sm" onClick={onClear}>
          Clear
        </Button>
      </div>
    </div>
  )
}
