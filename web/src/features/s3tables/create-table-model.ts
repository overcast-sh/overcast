import { createId } from "@/lib/id"
import type { CreateTableInput } from "@/services/api/s3tables"
import { identifierProblem } from "./names"

/**
 * The create-table dialog's model: a flat list of schema rows, where a row
 * indented under a `struct` row is one of its fields, turned into the
 * `CreateTable` request and into the `CfnTable` a CDK app would declare.
 *
 * A flat, primitive schema goes as `metadata.iceberg.schema`, the shape every
 * S3 Tables client sends. Nested structs need `schemaV2`, whose field types
 * are full Iceberg types.
 */

export const PRIMITIVE_TYPES = [
  "boolean",
  "int",
  "long",
  "float",
  "double",
  "decimal(10,2)",
  "date",
  "time",
  "timestamp",
  "timestamptz",
  "string",
  "uuid",
  "binary",
] as const

export type ColumnType = (typeof PRIMITIVE_TYPES)[number] | "struct"

export interface SchemaRow {
  /** Stable React key; not an Iceberg field id. */
  key: string
  name: string
  type: ColumnType
  required: boolean
  /** 0 for a column, 1 for a field of the struct above it, and so on. */
  depth: number
}

export const PARTITION_TRANSFORMS = [
  "identity",
  "year",
  "month",
  "day",
  "hour",
  "bucket[16]",
  "truncate[10]",
] as const

export type PartitionTransform = (typeof PARTITION_TRANSFORMS)[number]

export interface PartitionRow {
  key: string
  /** The top-level column partitioned by, by name. */
  column: string
  transform: PartitionTransform
}

export interface TableDraft {
  tableBucketARN: string
  namespace: string
  name: string
  rows: SchemaRow[]
  partitions: PartitionRow[]
}

/**
 * An Iceberg field as `schemaV2` carries it: a nested type is a full Iceberg
 * type. A type alias, not an interface, so it is assignable to the SDK's
 * `DocumentType`.
 */
type NestedField = {
  id: number
  name: string
  required: boolean
  type: string | { type: "struct"; fields: NestedField[] }
}

/** An empty string column at `depth`, ready to be named. */
export function newSchemaRow(depth = 0): SchemaRow {
  return { key: createId(), name: "", type: "string", required: false, depth }
}

/** The deepest a row may sit: one level below the row above it. */
export function maxDepth(rows: SchemaRow[], index: number): number {
  if (index === 0) return 0
  const above = rows[index - 1]
  return above.type === "struct" ? above.depth + 1 : above.depth
}

/** The rows as a tree, field ids assigned depth-first from 1 as Iceberg assigns them. */
function toFields(rows: SchemaRow[]): NestedField[] {
  let nextId = 1
  const build = (start: number, depth: number): [NestedField[], number] => {
    const fields: NestedField[] = []
    let i = start
    while (i < rows.length && rows[i].depth === depth) {
      const row = rows[i]
      const id = nextId++
      if (row.type === "struct") {
        const [children, end] = build(i + 1, depth + 1)
        fields.push({
          id,
          name: row.name,
          required: row.required,
          type: { type: "struct", fields: children },
        })
        i = end
      } else {
        fields.push({ id, name: row.name, required: row.required, type: row.type })
        i++
      }
    }
    return [fields, i]
  }
  return build(0, 0)[0]
}

export function hasNestedFields(rows: SchemaRow[]): boolean {
  return rows.some((r) => r.type === "struct")
}

/** What stops the draft being created, one line each; empty when it can be. */
export function draftProblems(draft: TableDraft): string[] {
  const problems: string[] = []
  const tableProblem = identifierProblem(draft.name, "table")
  if (draft.name === "") problems.push("Name the table.")
  else if (tableProblem) problems.push(`Table name: ${tableProblem}`)
  if (draft.namespace === "") problems.push("Pick a namespace.")
  if (draft.rows.length === 0) problems.push("Add at least one column.")
  draft.rows.forEach((row, i) => {
    if (row.name.trim() === "") problems.push(`Column ${i + 1} has no name.`)
    const next = draft.rows.at(i + 1)
    if (row.type === "struct" && (!next || next.depth <= row.depth)) {
      problems.push(`Struct ${row.name || i + 1} needs at least one field indented under it.`)
    }
  })
  const siblings = new Map<string, number>()
  draft.rows.forEach((row, i) => {
    const parent = draft.rows.slice(0, i).findLastIndex((r) => r.depth < row.depth)
    const key = `${parent}:${row.name}`
    if (row.name && siblings.has(key)) problems.push(`Two fields are called ${row.name}.`)
    siblings.set(key, i)
  })
  const partitionable = partitionableColumns(draft.rows)
  draft.partitions.forEach((p, i) => {
    if (!partitionable.includes(p.column)) problems.push(`Partition ${i + 1} needs a column.`)
  })
  return problems
}

/** Top-level columns a partition can use: not structs. */
export function partitionableColumns(rows: SchemaRow[]): string[] {
  return rows.filter((r) => r.depth === 0 && r.type !== "struct").map((r) => r.name)
}

/** `ordered_at_day`, `id_bucket` — the partition field names Iceberg's own API picks. */
function partitionFieldName(p: PartitionRow): string {
  const transform = p.transform.replace(/\[\d+\]$/, "")
  return transform === "identity" ? p.column : `${p.column}_${transform}`
}

/** The partition fields, each pointing at its source column's field id. */
function partitionFields(fields: NestedField[], partitions: PartitionRow[]) {
  return partitions.flatMap((p, i) => {
    const source = fields.find((f) => f.name === p.column)
    if (!source) return []
    return [
      {
        sourceId: source.id,
        fieldId: 1000 + i,
        name: partitionFieldName(p),
        transform: p.transform,
      },
    ]
  })
}

export function toCreateTableInput(draft: TableDraft): CreateTableInput {
  const fields = toFields(draft.rows)
  const partition = partitionFields(fields, draft.partitions)
  const schema = hasNestedFields(draft.rows)
    ? { schemaV2: { type: "struct" as const, schemaId: 0, fields } }
    : {
        schema: {
          fields: fields.map((f) => ({
            id: f.id,
            name: f.name,
            type: f.type as string,
            required: f.required,
          })),
        },
      }
  return {
    tableBucketARN: draft.tableBucketARN,
    namespace: draft.namespace,
    name: draft.name,
    format: "ICEBERG",
    metadata: {
      iceberg: {
        ...schema,
        ...(partition.length > 0 ? { partitionSpec: { specId: 0, fields: partition } } : {}),
      },
    },
  }
}

/** JSON as a TypeScript object literal: the same text, with identifier keys unquoted. */
function objectLiteral(value: unknown): string {
  return JSON.stringify(value, null, 2).replace(/^(\s*)"([A-Za-z_$][\w$]*)":/gm, "$1$2:")
}

/** `orders` → `OrdersTable`: the construct id a CDK app would give it. */
function constructId(name: string): string {
  return `${name.replace(/(^|_)([a-z0-9])/g, (_, __, c: string) => c.toUpperCase())}Table`
}

/**
 * The same table as a CDK app declares it: `CfnTable` from
 * `aws-cdk-lib/aws-s3tables`, whose `icebergMetadata` mirrors the API's.
 */
export function toCdk(draft: TableDraft): string {
  const input = toCreateTableInput(draft)
  const iceberg = input.metadata?.iceberg
  const metadata: Record<string, unknown> = {}
  if (iceberg?.schema) {
    metadata.icebergSchema = {
      schemaFieldList: iceberg.schema.fields?.map(({ name, type, required }) => ({
        name,
        type,
        required,
      })),
    }
  }
  if (iceberg?.schemaV2) metadata.icebergSchemaV2 = iceberg.schemaV2
  if (iceberg?.partitionSpec) metadata.icebergPartitionSpec = iceberg.partitionSpec
  const props = {
    tableBucketArn: draft.tableBucketARN,
    namespace: draft.namespace,
    tableName: draft.name,
    openTableFormat: "ICEBERG",
    icebergMetadata: metadata,
  }
  return [
    `import { CfnTable } from "aws-cdk-lib/aws-s3tables"`,
    "",
    `new CfnTable(this, ${JSON.stringify(constructId(draft.name))}, ${objectLiteral(props)})`,
  ].join("\n")
}
