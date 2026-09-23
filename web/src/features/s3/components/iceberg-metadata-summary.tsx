import { useMemo } from "react"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { formatCount, formatDate, formatQuantity } from "@/lib/format"
import { icebergSummary } from "../preview-iceberg"

/**
 * The facts of an Iceberg table metadata file, above its JSON: which format
 * version, which table, which schema, which snapshot is current. Modest on
 * purpose — the shared metadata viewer with snapshot history and manifests is
 * #2087, and will replace this card rather than grow out of it.
 *
 * Renders nothing when the file does not read as table metadata (a name that
 * merely ends in `.metadata.json`, or one cut short by the preview window);
 * the JSON below still shows whatever arrived.
 */
export function IcebergMetadataSummary({ text }: { text: string }) {
  const summary = useMemo(() => icebergSummary(text), [text])
  if (!summary) return null
  const snapshot = summary.currentSnapshotId
  const partitioned = summary.partitionFields.length > 0
  const schemaName = summary.schemaId === undefined ? "Schema" : `Schema ${summary.schemaId}`
  return (
    <DefinitionCard
      aria-label="Iceberg table metadata"
      title="Iceberg table"
      columns={2}
      cardClassName="shrink-0 shadow-none"
      contentClassName="p-3"
    >
      <Definition label="Format version" value={`v${summary.formatVersion}`} />
      <Definition
        label="Last updated"
        value={summary.lastUpdated ? formatDate(summary.lastUpdated) : undefined}
      />
      <Definition label="Table UUID" value={summary.tableUuid} copyable />
      <Definition
        label="Current snapshot"
        value={snapshot ?? "none yet"}
        valueClassName={snapshot ? undefined : "text-fg-subtle"}
        copyable={snapshot ?? undefined}
      />
      <Definition label="Snapshots" value={formatCount(summary.snapshotCount)} />
      <Definition
        label="Partitioned by"
        value={partitioned ? summary.partitionFields.join(", ") : "unpartitioned"}
        valueClassName={partitioned ? undefined : "text-fg-subtle"}
      />
      <Definition label="Location" value={summary.location} copyable full />
      <Definition
        full
        label={`${schemaName} · ${formatQuantity(summary.fields.length, "field")}`}
        value={
          // A run of name/type pairs rather than a table: the schema here is
          // a glance at what the columns are, and the JSON below has the rest.
          <ul className="flex flex-wrap gap-1.5">
            {summary.fields.map((field, index) => (
              <li
                key={field.id ?? index}
                title={`${field.name}: ${field.type}${field.required ? " (required)" : ""}`}
                className="inline-flex max-w-full min-w-0 items-baseline gap-1.5 rounded-control border border-border bg-bg-muted px-2 py-0.5"
              >
                <span className="truncate text-fg">{field.name}</span>
                <span className="truncate text-fg-subtle">{field.type}</span>
                {field.required && <span className="text-2xs text-fg-muted">required</span>}
              </li>
            ))}
          </ul>
        }
      />
    </DefinitionCard>
  )
}
