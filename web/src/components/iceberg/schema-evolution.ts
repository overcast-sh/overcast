import { flattenFields, typeName, type FieldAtPath, type IcebergSchema } from "./metadata"

/**
 * What changed between two schemas of one table.
 *
 * Iceberg tracks columns by field id, never by name, so the comparison does
 * too: a renamed column keeps its id and reads as a rename, not as one column
 * dropped and another added — which is exactly the distinction a developer
 * checking a migration needs, since a rename keeps the data and a drop loses it.
 */
export type SchemaChange =
  | { kind: "added"; path: string; type: string }
  | { kind: "dropped"; path: string }
  | { kind: "renamed"; from: string; path: string }
  | { kind: "type"; path: string; from: string; to: string }
  | { kind: "required"; path: string; required: boolean }

/** One schema of a table and how it differs from the schema before it. */
export interface SchemaVersion {
  schema: IcebergSchema
  /** Empty for the first schema, which has nothing to differ from. */
  changes: SchemaChange[]
}

/**
 * The type a field's own change is judged by. A struct's fields are compared
 * one by one under their own ids, so a struct is "the same type" whatever its
 * children do — otherwise one added child would report twice.
 */
function ownType(field: FieldAtPath): string {
  const name = typeName(field.field.type)
  return name.startsWith("struct<") ? "struct" : name
}

export function schemaChanges(before: IcebergSchema, after: IcebergSchema): SchemaChange[] {
  const old = new Map(flattenFields(before.fields).map((f) => [f.field.id, f]))
  const next = flattenFields(after.fields)
  const changes: SchemaChange[] = []
  for (const f of next) {
    const prev = old.get(f.field.id)
    if (!prev) {
      changes.push({ kind: "added", path: f.path, type: typeName(f.field.type) })
      continue
    }
    if (prev.path !== f.path) changes.push({ kind: "renamed", from: prev.path, path: f.path })
    if (ownType(prev) !== ownType(f)) {
      changes.push({ kind: "type", path: f.path, from: ownType(prev), to: ownType(f) })
    }
    if (prev.field.required !== f.field.required) {
      changes.push({ kind: "required", path: f.path, required: f.field.required })
    }
  }
  const kept = new Set(next.map((f) => f.field.id))
  for (const [fieldId, f] of old) {
    if (!kept.has(fieldId)) changes.push({ kind: "dropped", path: f.path })
  }
  return changes
}

/** Every schema in the order the table added them, each with its changes from the one before. */
export function schemaHistory(schemas: IcebergSchema[]): SchemaVersion[] {
  return schemas.map((schema, index) => ({
    schema,
    changes: index === 0 ? [] : schemaChanges(schemas[index - 1], schema),
  }))
}
