import { parseJsonKeepingLargeIntegers } from "@/lib/json-text"
import { isRecord } from "@/lib/utils"

/**
 * A typed reader for an Iceberg table metadata file (`*.metadata.json`),
 * shared by everything that shows one: the S3 object preview, the S3 Tables
 * table page and the Glue table page.
 *
 * Tolerant by design. A file written by any engine, any format version, or
 * cut short by a preview window either reads as table metadata or is `null` —
 * a missing optional field becomes an absent property rather than an error,
 * because a viewer that refuses a file shows the developer less than one that
 * shows what it could read.
 *
 * Snapshot ids are random 64-bit integers, far past the 2^53 a JavaScript
 * number holds exactly, so they are read as decimal strings (see
 * `parseJsonKeepingLargeIntegers`).
 *
 * Field names follow the Iceberg table spec's "Table Metadata Fields":
 * https://iceberg.apache.org/spec/#table-metadata-fields
 */

/** A primitive type's spelling (`long`, `decimal(10, 2)`), or a nested type as the file writes it. */
export type IcebergType = string | Record<string, unknown>

export interface IcebergField {
  id: number
  name: string
  required: boolean
  type: IcebergType
  doc?: string
}

export interface IcebergSchema {
  schemaId: number
  fields: IcebergField[]
}

export interface IcebergPartitionField {
  sourceId: number
  fieldId?: number
  name: string
  transform: string
}

export interface IcebergPartitionSpec {
  specId: number
  fields: IcebergPartitionField[]
}

export interface IcebergSnapshot {
  snapshotId: string
  parentSnapshotId?: string
  sequenceNumber?: number
  timestampMs: number
  /** `summary.operation`: `append`, `overwrite`, `replace` or `delete`. */
  operation?: string
  /** The rest of the summary: `added-records`, `total-records`… as the file writes them. */
  summary: Record<string, string>
  manifestList?: string
  schemaId?: number
}

export interface IcebergMetadataLogEntry {
  timestampMs: number
  metadataFile: string
}

export interface IcebergRef {
  name: string
  type: "branch" | "tag"
  snapshotId: string
}

export interface IcebergMetadata {
  formatVersion: number
  tableUuid?: string
  location?: string
  lastUpdatedMs?: number
  lastSequenceNumber?: number
  schemas: IcebergSchema[]
  /** The schema new data is written with: `current-schema-id`'s, or v1's single `schema`. */
  currentSchema?: IcebergSchema
  partitionSpecs: IcebergPartitionSpec[]
  defaultSpec?: IcebergPartitionSpec
  properties: Record<string, string>
  /** Null when the table has no snapshot yet (`-1` or absent — both appear in the wild). */
  currentSnapshotId: string | null
  snapshots: IcebergSnapshot[]
  /** Earlier metadata files of this table, oldest first. Kept up to `write.metadata.previous-versions-max`. */
  metadataLog: IcebergMetadataLogEntry[]
  refs: IcebergRef[]
}

type Json = Record<string, unknown>

/** The objects in a JSON array, or none when `value` is not an array. */
function objectsIn(value: unknown): Json[] {
  return Array.isArray(value) ? value.filter(isRecord) : []
}

function num(value: unknown): number | undefined {
  return typeof value === "number" ? value : undefined
}

function str(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined
}

/** An id that may arrive as a small number or, past 2^53, as the string the parser kept. */
function id(value: unknown): string | undefined {
  if (typeof value === "string" && /^-?\d+$/.test(value)) return value
  if (typeof value === "number" && Number.isInteger(value)) return String(value)
  return undefined
}

function stringMap(value: unknown): Record<string, string> {
  if (!isRecord(value)) return {}
  return Object.fromEntries(
    Object.entries(value).flatMap(([k, v]) => (typeof v === "string" ? [[k, v]] : [])),
  )
}

/**
 * The file's metadata, or null when `text` is not Iceberg table metadata —
 * including when it is not JSON at all, which a preview window can cause: a
 * metadata file past its limit arrives cut short and cannot be parsed.
 */
export function parseIcebergMetadata(text: string): IcebergMetadata | null {
  let doc: unknown
  try {
    doc = parseJsonKeepingLargeIntegers(text)
  } catch {
    return null
  }
  if (!isRecord(doc)) return null
  const formatVersion = doc["format-version"]
  if (typeof formatVersion !== "number") return null
  if (typeof doc["table-uuid"] !== "string" && typeof doc.location !== "string") return null

  const schemas = readSchemas(doc)
  const partitionSpecs = readPartitionSpecs(doc)
  const currentSnapshotId = id(doc["current-snapshot-id"])
  return {
    formatVersion,
    tableUuid: str(doc["table-uuid"]),
    location: str(doc.location),
    lastUpdatedMs: num(doc["last-updated-ms"]),
    lastSequenceNumber: num(doc["last-sequence-number"]),
    schemas,
    currentSchema: schemas.find((s) => s.schemaId === doc["current-schema-id"]) ?? schemas.at(-1),
    partitionSpecs,
    defaultSpec:
      partitionSpecs.find((s) => s.specId === doc["default-spec-id"]) ?? partitionSpecs.at(-1),
    properties: stringMap(doc.properties),
    currentSnapshotId:
      currentSnapshotId === undefined || currentSnapshotId === "-1" ? null : currentSnapshotId,
    snapshots: objectsIn(doc.snapshots).flatMap(readSnapshot),
    metadataLog: objectsIn(doc["metadata-log"]).flatMap((e) => {
      const file = str(e["metadata-file"])
      const ts = num(e["timestamp-ms"])
      return file && ts !== undefined ? [{ metadataFile: file, timestampMs: ts }] : []
    }),
    refs: readRefs(doc.refs),
  }
}

function readField(f: Json): IcebergField {
  return {
    id: num(f.id) ?? -1,
    name: String(f.name ?? ""),
    required: f.required === true,
    type: typeof f.type === "string" || isRecord(f.type) ? f.type : "unknown",
    doc: str(f.doc),
  }
}

/** v2 and later list every schema; v1 carries a single `schema`. */
function readSchemas(doc: Json): IcebergSchema[] {
  const listed = objectsIn(doc.schemas)
  const raw = listed.length > 0 ? listed : isRecord(doc.schema) ? [doc.schema] : []
  return raw.map((s, index) => ({
    schemaId: num(s["schema-id"]) ?? index,
    fields: objectsIn(s.fields).map(readField),
  }))
}

/** v2 lists every spec; v1 carries the spec inline as `partition-spec`, a bare field list. */
function readPartitionSpecs(doc: Json): IcebergPartitionSpec[] {
  const readFields = (fields: unknown) =>
    objectsIn(fields).map((f) => ({
      sourceId: num(f["source-id"]) ?? -1,
      fieldId: num(f["field-id"]),
      name: String(f.name ?? ""),
      transform: String(f.transform ?? "identity"),
    }))
  const listed = objectsIn(doc["partition-specs"])
  if (listed.length > 0) {
    return listed.map((s, index) => ({
      specId: num(s["spec-id"]) ?? index,
      fields: readFields(s.fields),
    }))
  }
  return Array.isArray(doc["partition-spec"])
    ? [{ specId: 0, fields: readFields(doc["partition-spec"]) }]
    : []
}

function readSnapshot(s: Json): IcebergSnapshot[] {
  const snapshotId = id(s["snapshot-id"])
  const timestampMs = num(s["timestamp-ms"])
  if (snapshotId === undefined || timestampMs === undefined) return []
  const { operation, ...summary } = stringMap(s.summary)
  return [
    {
      snapshotId,
      parentSnapshotId: id(s["parent-snapshot-id"]),
      sequenceNumber: num(s["sequence-number"]),
      timestampMs,
      operation,
      summary,
      manifestList: str(s["manifest-list"]),
      schemaId: num(s["schema-id"]),
    },
  ]
}

function readRefs(value: unknown): IcebergRef[] {
  if (!isRecord(value)) return []
  return Object.entries(value).flatMap(([name, ref]): IcebergRef[] => {
    if (!isRecord(ref)) return []
    const snapshotId = id(ref["snapshot-id"])
    if (snapshotId === undefined) return []
    return [{ name, type: ref.type === "tag" ? "tag" : "branch", snapshotId }]
  })
}

/** Iceberg types as their spec spellings: `long`, `decimal(10, 2)`, `list<string>`, `map<string, long>`, `struct<…>`. */
export function typeName(type: unknown): string {
  if (typeof type === "string") return type
  if (!isRecord(type)) return "unknown"
  switch (type.type) {
    case "list":
      return `list<${typeName(type.element)}>`
    case "map":
      return `map<${typeName(type.key)}, ${typeName(type.value)}>`
    case "struct": {
      const fields = objectsIn(type.fields).map((f) => `${String(f.name)}: ${typeName(f.type)}`)
      return `struct<${fields.join(", ")}>`
    }
    default:
      return typeof type.type === "string" ? type.type : "unknown"
  }
}

/** The struct fields nested inside a type — a struct's own, or a list's or map's struct element. */
export function nestedFields(type: unknown): IcebergField[] {
  if (!isRecord(type)) return []
  if (type.type === "struct") return objectsIn(type.fields).map(readField)
  if (type.type === "list") return nestedFields(type.element)
  if (type.type === "map") return nestedFields(type.value)
  return []
}

/** A field in a schema, with where it sits: `shipping.carrier` is `shipping`'s child at depth 1. */
export interface FieldAtPath {
  field: IcebergField
  path: string
  depth: number
}

/** Every field in `fields`, nested ones after their parent, depth-first in declaration order. */
export function flattenFields(fields: IcebergField[], parent?: FieldAtPath): FieldAtPath[] {
  return fields.flatMap((field) => {
    const here: FieldAtPath = {
      field,
      path: parent ? `${parent.path}.${field.name}` : field.name,
      depth: parent ? parent.depth + 1 : 0,
    }
    return [here, ...flattenFields(nestedFields(field.type), here)]
  })
}

/** A partition field as `transform(column)` — `day(ordered_at)`, `bucket[16](id)`. */
export function partitionLabel(field: IcebergPartitionField, schema?: IcebergSchema): string {
  const source =
    flattenFields(schema?.fields ?? []).find((f) => f.field.id === field.sourceId)?.path ??
    field.name
  return `${field.transform}(${source})`
}
