import { formatQuantity } from "@/lib/format"
import type { IcebergSnapshot } from "./metadata"

/**
 * What a snapshot did, read from its summary.
 *
 * The summary is optional in the spec beyond `operation`, and engines differ
 * in which keys they write, so everything here is "when present". The keys
 * are the ones Iceberg's own `SnapshotSummary` writes:
 * https://iceberg.apache.org/spec/#optional-snapshot-summary-fields
 */

export type MetricUnit = "count" | "bytes"

interface TotalMetric {
  key: string
  label: string
  unit: MetricUnit
}

/** The running totals a snapshot carries, in the order a reader scans them. */
const TOTALS: TotalMetric[] = [
  { key: "total-records", label: "Records", unit: "count" },
  { key: "total-data-files", label: "Data files", unit: "count" },
  { key: "total-files-size", label: "Size", unit: "bytes" },
  { key: "total-delete-files", label: "Delete files", unit: "count" },
  { key: "total-position-deletes", label: "Position deletes", unit: "count" },
  { key: "total-equality-deletes", label: "Equality deletes", unit: "count" },
]

export interface MetricDelta {
  label: string
  unit: MetricUnit
  before?: number
  after?: number
}

/** A numeric summary value, or undefined when the snapshot does not report it. */
export function metric(
  summary: Record<string, string> | undefined,
  key: string,
): number | undefined {
  const raw = summary?.[key]
  if (raw === undefined || raw.trim() === "") return undefined
  const value = Number(raw)
  return Number.isFinite(value) ? value : undefined
}

/** The snapshot this one was committed on top of: its parent, or none for the first. */
export function parentOf(
  snapshots: IcebergSnapshot[],
  snapshot: IcebergSnapshot,
): IcebergSnapshot | undefined {
  if (snapshot.parentSnapshotId === undefined) return undefined
  return snapshots.find((s) => s.snapshotId === snapshot.parentSnapshotId)
}

/**
 * Each running total before and after the snapshot — the parent's totals are
 * "before". Totals neither side reports are left out.
 */
export function totalsDelta(snapshot: IcebergSnapshot, parent?: IcebergSnapshot): MetricDelta[] {
  return TOTALS.flatMap(({ key, label, unit }) => {
    const before = metric(parent?.summary, key)
    const after = metric(snapshot.summary, key)
    return before === undefined && after === undefined ? [] : [{ label, unit, before, after }]
  })
}

/** The summary keys the totals and the commit's changes already show. */
const SHOWN_KEYS = new Set([
  ...TOTALS.map((t) => t.key),
  "added-records",
  "deleted-records",
  "added-data-files",
  "deleted-data-files",
])

/** The rest of the summary — engine-specific keys, sizes, partition counts — as the file writes it. */
export function otherSummaryFields(snapshot: IcebergSnapshot): [string, string][] {
  return Object.entries(snapshot.summary).filter(([key]) => !SHOWN_KEYS.has(key))
}

export interface CommitChange {
  text: string
  tone: "added" | "removed"
}

/** What the commit itself added and removed: `+860 records`, `−120 records`, `+2 files`. */
export function commitChanges(snapshot: IcebergSnapshot): CommitChange[] {
  const change = (key: string, tone: CommitChange["tone"], noun: string): CommitChange[] => {
    const n = metric(snapshot.summary, key)
    return n ? [{ text: `${tone === "added" ? "+" : "−"}${formatQuantity(n, noun)}`, tone }] : []
  }
  return [
    ...change("added-records", "added", "record"),
    ...change("deleted-records", "removed", "record"),
    ...change("added-data-files", "added", "file"),
    ...change("deleted-data-files", "removed", "file"),
  ]
}
