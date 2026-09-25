import { Columns3, GitCommitVertical } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { SectionLabel } from "@/components/ui/primitives"
import { ResourceTable } from "@/components/ui/resource-table"
import { formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  flattenFields,
  typeName,
  type FieldAtPath,
  type IcebergMetadata,
  type IcebergSchema,
} from "./metadata"
import { schemaHistory, type SchemaChange, type SchemaVersion } from "./schema-evolution"

/**
 * A table's current schema, field by field with nested structs under their
 * parent, and its evolution: every schema the table has had, each with what
 * changed from the one before. Columns are compared by field id, so a rename
 * reads as a rename.
 */
export function IcebergSchemaView({ metadata }: { metadata: IcebergMetadata }) {
  const current = metadata.currentSchema
  const history = schemaHistory(metadata.schemas).reverse()
  const partitionSources = new Map(
    (metadata.defaultSpec?.fields ?? []).map((f) => [f.sourceId, f.transform]),
  )
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <SectionLabel>
          {current
            ? `Current schema · id ${current.schemaId} · ${formatQuantity(current.fields.length, "column")}`
            : "Current schema"}
        </SectionLabel>
        <SchemaFields schema={current} partitionSources={partitionSources} />
      </section>
      {history.length > 1 && (
        <section className="flex flex-col gap-2">
          <SectionLabel>Evolution · {formatQuantity(history.length, "schema")}</SectionLabel>
          <ResourceTable
            variant="embedded"
            query={{ data: history, isLoading: false }}
            noun="schemas"
            rowKey={(v) => v.schema.schemaId}
            expandLabel="fields"
            expandedContent={(v) => <SchemaFields schema={v.schema} />}
            columns={[
              {
                header: "Schema",
                cell: (v) => (
                  <span className="inline-flex items-center gap-1.5">
                    <GitCommitVertical aria-hidden className="size-3.5 text-fg-subtle" />
                    id {v.schema.schemaId}
                    {v.schema.schemaId === current?.schemaId && (
                      <Badge variant="accent">current</Badge>
                    )}
                  </span>
                ),
              },
              {
                header: "Columns",
                cellClassName: "text-fg-muted tabular-nums",
                cell: (v) => v.schema.fields.length,
              },
              { header: "Changes from the previous schema", cell: (v) => <Changes version={v} /> },
            ]}
          />
        </section>
      )}
    </div>
  )
}

function SchemaFields({
  schema,
  partitionSources,
}: {
  schema?: IcebergSchema
  /** Field id → the transform partitioning by it, for the current spec's source columns. */
  partitionSources?: Map<number, string>
}) {
  return (
    <ResourceTable
      variant="embedded"
      query={{ data: schema ? flattenFields(schema.fields) : [], isLoading: false }}
      noun="columns"
      emptyIcon={Columns3}
      emptyTitle="No columns"
      emptyDescription="This schema declares no columns."
      rowKey={(f) => f.field.id}
      columns={[
        {
          header: "Column",
          cell: (f) => <FieldName entry={f} />,
        },
        { header: "Type", cellClassName: "text-fg-muted", cell: (f) => fieldType(f) },
        {
          header: "Required",
          cell: (f) =>
            f.field.required ? (
              <Badge variant="default">required</Badge>
            ) : (
              <span className="text-fg-subtle">optional</span>
            ),
        },
        {
          header: "Field id",
          cellClassName: "text-fg-muted tabular-nums",
          cell: (f) => f.field.id,
        },
        ...(partitionSources && partitionSources.size > 0
          ? [
              {
                header: "Partition",
                cell: (f: FieldAtPath) => {
                  const transform = partitionSources.get(f.field.id)
                  return transform ? <Badge variant="accent">{transform}</Badge> : null
                },
              },
            ]
          : []),
        {
          header: "Doc",
          prose: true,
          cellClassName: "text-fg-muted",
          cell: (f) => f.field.doc ?? "",
        },
      ]}
    />
  )
}

/** A nested field under its parent: indented, with a tree elbow, named by its own name. */
function FieldName({ entry }: { entry: FieldAtPath }) {
  return (
    <span
      className="inline-flex items-center gap-1.5"
      // One indent step per level; the path is on the title for a deep struct.
      style={{ paddingLeft: `${entry.depth * 1.25}rem` }}
      title={entry.depth > 0 ? entry.path : undefined}
    >
      {entry.depth > 0 && (
        <span aria-hidden className="text-fg-subtle">
          └
        </span>
      )}
      <span className="text-fg">{entry.field.name}</span>
    </span>
  )
}

/** A struct's own type is its children, listed beneath it; everything else by its spec spelling. */
function fieldType(entry: FieldAtPath): string {
  const name = typeName(entry.field.type)
  return name.startsWith("struct<") ? "struct" : name
}

const CHANGE_TONE: Record<SchemaChange["kind"], string> = {
  added: "text-success",
  dropped: "text-danger",
  renamed: "text-accent",
  type: "text-warning",
  required: "text-warning",
}

function describe(change: SchemaChange): string {
  switch (change.kind) {
    case "added":
      return `+ ${change.path} ${change.type.startsWith("struct<") ? "struct" : change.type}`
    case "dropped":
      return `− ${change.path}`
    case "renamed":
      return `${change.from} → ${change.path}`
    case "type":
      return `${change.path}: ${change.from} → ${change.to}`
    case "required":
      return `${change.path} ${change.required ? "now required" : "now optional"}`
  }
}

function Changes({ version }: { version: SchemaVersion }) {
  if (version.changes.length === 0) {
    return <span className="text-fg-subtle">first schema</span>
  }
  return (
    <ul className="flex flex-wrap gap-x-3 gap-y-1">
      {version.changes.map((change) => {
        const text = describe(change)
        return (
          <li key={text} className={cn(CHANGE_TONE[change.kind])}>
            {text}
          </li>
        )
      })}
    </ul>
  )
}
