/**
 * One table in a Glue database or S3 Tables bucket node — the one row
 * component both share, so an Iceberg table looks and behaves the same in
 * either: a write flash when it is written, a burst of the records a commit
 * appended, and a ghost when it is dropped (see data-lake-overlay.ts).
 */

import type { ReactNode } from "react"
import type { LucideIcon } from "lucide-react"
import { formatCount, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import { ROW_GHOST_TTL, type RowOverlay } from "./data-lake-overlay"
import type { DisplayTable } from "./data-lake-layout"
import { toSweep } from "./map-theme"

/** How long a write flash sweeps across the row (ms) — the node sweep's length. */
const FLASH_TTL = 2_000

interface DataTableRowProps {
  row: DisplayTable
  overlay?: RowOverlay
  /** The service's colour, which the write flash sweeps in. */
  color: string
  /** The facts on the right: a format badge, a partition or snapshot count. */
  meta?: ReactNode
  /** The node's peek for this table, and what it is called. */
  onPeek: () => void
  peekLabel: string
  PeekIcon: LucideIcon
}

export function DataTableRow({
  row,
  overlay,
  color,
  meta,
  onPeek,
  peekLabel,
  PeekIcon,
}: DataTableRowProps) {
  if (row.ghost) return <GhostTableRow name={row.name} />
  const flashes = overlay?.flashes ?? 0
  const records = overlay?.records ?? 0
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        onPeek()
      }}
      aria-label={`${peekLabel}: ${row.key}`}
      className="group relative flex h-6 w-full items-center gap-2 overflow-hidden rounded px-1.5 text-left text-xs hover:bg-bg-muted focus-visible:bg-bg-muted"
    >
      {flashes > 0 && (
        <span
          key={flashes}
          aria-hidden
          className="pointer-events-none absolute inset-0"
          style={{
            background: `linear-gradient(90deg, transparent 0%, ${toSweep(color)} 50%, transparent 100%)`,
            animation: `overcastSweep ${FLASH_TTL}ms ease-out forwards`,
          }}
        />
      )}
      <span className="min-w-0 flex-1 truncate font-mono">{row.name}</span>
      {records > 0 && (
        <span
          className="shrink-0 rounded-full bg-accent-muted px-1.5 font-mono text-2xs font-semibold text-accent tabular-nums"
          title={`${formatQuantity(records, "record")} appended`}
        >
          +{formatCount(records)}
        </span>
      )}
      <span className="flex shrink-0 items-center gap-1.5 text-2xs text-fg-subtle group-hover:hidden group-focus-visible:hidden">
        {meta}
      </span>
      <PeekIcon
        aria-hidden
        className="hidden size-3.5 shrink-0 text-fg-muted group-hover:block group-focus-visible:block"
      />
    </button>
  )
}

/** A table that was just dropped: struck through and fading, not clickable. */
function GhostTableRow({ name }: { name: string }) {
  return (
    <div
      className="flex h-6 items-center gap-2 px-1.5 text-xs text-fg-subtle"
      style={{ animation: `overcastGhostFade ${ROW_GHOST_TTL}ms linear forwards` }}
    >
      <span className="min-w-0 flex-1 truncate font-mono line-through">{name}</span>
      <span className="shrink-0 text-2xs">dropped</span>
    </div>
  )
}

/** A count that pops each time `ticks` goes up — a Glue table's partitions changing. */
export function TickCount({ ticks = 0, children }: { ticks?: number; children: ReactNode }) {
  return (
    <span
      key={ticks}
      className={cn("inline-block tabular-nums", ticks > 0 && "origin-right")}
      style={ticks > 0 ? { animation: "overcastTick 600ms ease-out" } : undefined}
    >
      {children}
    </span>
  )
}
