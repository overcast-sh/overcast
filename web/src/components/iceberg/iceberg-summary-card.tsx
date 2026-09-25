import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { formatCount, formatDate, formatQuantity } from "@/lib/format"
import { partitionLabel, typeName, type IcebergMetadata } from "./metadata"

/**
 * The at-a-glance facts of an Iceberg table's metadata: which format version,
 * which table, which schema, which snapshot is current. The S3 preview puts it
 * above a metadata file's JSON; a table page puts it on its overview, with the
 * snapshot, schema and metadata tabs beside it for the rest.
 */
export function IcebergSummaryCard({
  metadata,
  className,
}: {
  metadata: IcebergMetadata
  className?: string
}) {
  const snapshot = metadata.currentSnapshotId
  const schema = metadata.currentSchema
  const fields = schema?.fields ?? []
  const partitions = (metadata.defaultSpec?.fields ?? []).map((f) => partitionLabel(f, schema))
  const schemaName = schema ? `Schema ${schema.schemaId}` : "Schema"
  return (
    <DefinitionCard
      aria-label="Iceberg table metadata"
      title="Iceberg table"
      columns={2}
      cardClassName={className}
      contentClassName="p-3"
    >
      <Definition label="Format version" value={`v${metadata.formatVersion}`} />
      <Definition
        label="Last updated"
        value={metadata.lastUpdatedMs === undefined ? undefined : formatDate(metadata.lastUpdatedMs)}
      />
      <Definition label="Table UUID" value={metadata.tableUuid} copyable />
      <Definition
        label="Current snapshot"
        value={snapshot ?? "none yet"}
        valueClassName={snapshot ? undefined : "text-fg-subtle"}
        copyable={snapshot ?? undefined}
      />
      <Definition label="Snapshots" value={formatCount(metadata.snapshots.length)} />
      <Definition
        label="Partitioned by"
        value={partitions.length > 0 ? partitions.join(", ") : "unpartitioned"}
        valueClassName={partitions.length > 0 ? undefined : "text-fg-subtle"}
      />
      <Definition label="Location" value={metadata.location} copyable full />
      <Definition
        full
        label={`${schemaName} · ${formatQuantity(fields.length, "field")}`}
        value={
          // A run of name/type pairs rather than a table: this is a glance at
          // what the columns are, and the schema view has the rest.
          <ul className="flex flex-wrap gap-1.5">
            {fields.map((field) => (
              <li
                key={field.id}
                title={`${field.name}: ${typeName(field.type)}${field.required ? " (required)" : ""}`}
                className="inline-flex max-w-full min-w-0 items-baseline gap-1.5 rounded-control border border-border bg-bg-muted px-2 py-0.5"
              >
                <span className="truncate text-fg">{field.name}</span>
                <span className="truncate text-fg-subtle">{typeName(field.type)}</span>
                {field.required && <span className="text-2xs text-fg-muted">required</span>}
              </li>
            ))}
          </ul>
        }
      />
    </DefinitionCard>
  )
}
