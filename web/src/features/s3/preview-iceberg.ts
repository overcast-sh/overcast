/**
 * The at-a-glance facts of an Iceberg table metadata file (`*.metadata.json`),
 * for the summary card the S3 preview puts above the JSON.
 *
 * Deliberately a summary, not a viewer: the fields a developer checks after a
 * commit — which format version, which schema, which snapshot is current and
 * how many there are. The full metadata viewer (snapshot history, manifests,
 * partition evolution) is a separate, shared component (#2087).
 *
 * Field names follow the Iceberg table spec's "Table Metadata Fields":
 * https://iceberg.apache.org/spec/#table-metadata-fields
 */

export interface IcebergField {
  id?: number
  name: string
  type: string
  required: boolean
}

export interface IcebergSummary {
  formatVersion: number
  tableUuid?: string
  location?: string
  /** `last-updated-ms`, as a Date. */
  lastUpdated?: Date
  /**
   * The current snapshot's id as written in the file, or null when the table
   * has none yet (`-1` or absent — both appear in the wild).
   */
  currentSnapshotId: string | null
  snapshotCount: number
  schemaId?: number
  fields: IcebergField[]
  /** The default partition spec, as `transform(column)` — `day(ordered_at)`, `identity(region)`. */
  partitionFields: string[]
}

type Json = Record<string, unknown>

function isObject(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/**
 * Null when `text` is not Iceberg table metadata — including when it is not
 * JSON at all, which the preview window can cause: a metadata file past 1 MiB
 * arrives cut short and cannot be parsed. The JSON view underneath still shows
 * what arrived.
 */
export function icebergSummary(text: string): IcebergSummary | null {
  let doc: unknown
  try {
    doc = JSON.parse(text)
  } catch {
    return null
  }
  if (!isObject(doc)) return null
  const formatVersion = doc["format-version"]
  if (typeof formatVersion !== "number") return null
  if (typeof doc["table-uuid"] !== "string" && typeof doc.location !== "string") return null

  const schema = currentSchema(doc)
  const fieldsById = new Map<number, string>()
  const fields = (Array.isArray(schema?.fields) ? schema.fields : []).filter(isObject).map((f) => {
    const field: IcebergField = {
      id: typeof f.id === "number" ? f.id : undefined,
      name: String(f.name ?? ""),
      type: typeName(f.type),
      required: f.required === true,
    }
    if (field.id !== undefined) fieldsById.set(field.id, field.name)
    return field
  })

  const lastUpdatedMs = doc["last-updated-ms"]
  return {
    formatVersion,
    tableUuid: typeof doc["table-uuid"] === "string" ? doc["table-uuid"] : undefined,
    location: typeof doc.location === "string" ? doc.location : undefined,
    lastUpdated: typeof lastUpdatedMs === "number" ? new Date(lastUpdatedMs) : undefined,
    currentSnapshotId: currentSnapshotId(text),
    snapshotCount: Array.isArray(doc.snapshots) ? doc.snapshots.length : 0,
    schemaId: typeof schema?.["schema-id"] === "number" ? schema["schema-id"] : undefined,
    fields,
    partitionFields: partitionFields(doc, fieldsById),
  }
}

/** v2 and later list every schema and name the current one; v1 carries a single `schema`. */
function currentSchema(doc: Json): Json | undefined {
  const schemas = Array.isArray(doc.schemas) ? doc.schemas.filter(isObject) : []
  const currentId = doc["current-schema-id"]
  const current = schemas.find((s) => s["schema-id"] === currentId)
  if (current) return current
  if (isObject(doc.schema)) return doc.schema
  return schemas.at(-1)
}

/**
 * Read from the text, not the parsed document. Snapshot ids are random 64-bit
 * integers, far past the 2^53 a JavaScript number holds exactly, so
 * `JSON.parse` would round `3051729675574597004` to `3051729675574597000` — a
 * snapshot id that does not exist. `current-snapshot-id` appears once, at the
 * top level; snapshots themselves carry `snapshot-id`.
 */
function currentSnapshotId(text: string): string | null {
  const match = /"current-snapshot-id"\s*:\s*(-?\d+)/.exec(text)
  if (!match || match[1] === "-1") return null
  return match[1]
}

/** Iceberg types as their spec spellings: `long`, `decimal(10, 2)`, `list<string>`, `map<string, long>`, `struct<…>`. */
export function typeName(type: unknown): string {
  if (typeof type === "string") return type
  if (!isObject(type)) return "unknown"
  switch (type.type) {
    case "list":
      return `list<${typeName(type.element)}>`
    case "map":
      return `map<${typeName(type.key)}, ${typeName(type.value)}>`
    case "struct": {
      const fields = Array.isArray(type.fields) ? type.fields.filter(isObject) : []
      return `struct<${fields.map((f) => `${String(f.name)}: ${typeName(f.type)}`).join(", ")}>`
    }
    default:
      return typeof type.type === "string" ? type.type : "unknown"
  }
}

function partitionFields(doc: Json, fieldsById: Map<number, string>): string[] {
  const specs = Array.isArray(doc["partition-specs"]) ? doc["partition-specs"].filter(isObject) : []
  const defaultId = doc["default-spec-id"]
  const spec = specs.find((s) => s["spec-id"] === defaultId)
  // v1 also carries the spec inline as `partition-spec`: a bare field list.
  const fields = Array.isArray(spec?.fields)
    ? spec.fields
    : Array.isArray(doc["partition-spec"])
      ? doc["partition-spec"]
      : []
  return fields.filter(isObject).map((f) => {
    const sourceId = f["source-id"]
    const source =
      (typeof sourceId === "number" ? fieldsById.get(sourceId) : undefined) ?? String(f.name ?? "?")
    return `${String(f.transform ?? "identity")}(${source})`
  })
}
