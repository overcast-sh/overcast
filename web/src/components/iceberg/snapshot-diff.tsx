import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { SectionLabel } from "@/components/ui/primitives"
import { formatBytes, formatCount } from "@/lib/format"
import type { IcebergSnapshot } from "./metadata"
import { otherSummaryFields, totalsDelta, type MetricDelta } from "./snapshot-changes"

/**
 * *Diff with previous*: what one commit changed, as the table's running totals
 * before and after it, then whatever else its summary says — the engine-
 * specific keys (a Spark app id, a changed-partition count) the totals skip.
 */
export function SnapshotDiff({
  snapshot,
  parent,
}: {
  snapshot: IcebergSnapshot
  parent?: IcebergSnapshot
}) {
  const totals = totalsDelta(snapshot, parent)
  const others = otherSummaryFields(snapshot)
  return (
    <div className="flex flex-col gap-4 py-1">
      <section className="flex flex-col gap-2">
        <SectionLabel>
          {parent ? `Since snapshot ${parent.snapshotId}` : "First snapshot — nothing before it"}
          {snapshot.schemaId !== undefined && ` · written with schema ${snapshot.schemaId}`}
        </SectionLabel>
        {totals.length > 0 ? (
          <DefinitionList columns={3}>
            {totals.map((delta) => (
              <Definition key={delta.label} label={delta.label} value={<DeltaValue {...delta} />} />
            ))}
          </DefinitionList>
        ) : (
          <p className="text-xs text-fg-subtle">This snapshot's summary carries no totals.</p>
        )}
      </section>
      {others.length > 0 && (
        <section className="flex flex-col gap-2">
          <SectionLabel>More from the summary</SectionLabel>
          <DefinitionList columns={3}>
            {others.map(([key, value]) => (
              <Definition key={key} label={key} value={value} />
            ))}
          </DefinitionList>
        </section>
      )}
    </div>
  )
}

/** The tone of a change by its sign: growth, shrinkage, none. */
const DIRECTION_TONE: Record<number, string> = {
  1: "text-success",
  [-1]: "text-danger",
  0: "text-fg-subtle",
}

function format(value: number, unit: MetricDelta["unit"]): string {
  return unit === "bytes" ? formatBytes(value) : formatCount(value)
}

/** `1,200 → 2,060 +860`, the change tinted by its direction. */
function DeltaValue({ before, after, unit }: MetricDelta) {
  if (after === undefined) return <span className="text-fg-subtle">not reported</span>
  if (before === undefined) return <span>{format(after, unit)}</span>
  const change = after - before
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-1.5">
      <span className="text-fg-muted">{format(before, unit)}</span>
      <span aria-hidden className="text-fg-subtle">
        →
      </span>
      <span className="sr-only">to</span>
      <span>{format(after, unit)}</span>
      <span className={DIRECTION_TONE[Math.sign(change)]}>
        {change === 0 ? "unchanged" : `${change > 0 ? "+" : "−"}${format(Math.abs(change), unit)}`}
      </span>
    </span>
  )
}
