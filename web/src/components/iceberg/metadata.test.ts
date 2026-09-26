import { flattenFields, parseIcebergMetadata, partitionLabel, typeName } from "./metadata"

// A v2 table metadata file as Iceberg writes it after two commits, trimmed to
// the fields the summary reads plus enough of the rest to be realistic. The
// snapshot ids are past 2^53 on purpose — real ones are random longs.
const V2 = `{
  "format-version" : 2,
  "table-uuid" : "9c12d441-03fe-4693-9a96-a0705ddf69c1",
  "location" : "s3://warehouse/sales/orders",
  "last-sequence-number" : 2,
  "last-updated-ms" : 1790000000000,
  "last-column-id" : 5,
  "current-schema-id" : 1,
  "schemas" : [ {
    "type" : "struct", "schema-id" : 0,
    "fields" : [ { "id" : 1, "name" : "order_id", "required" : true, "type" : "long" } ]
  }, {
    "type" : "struct", "schema-id" : 1,
    "fields" : [
      { "id" : 1, "name" : "order_id", "required" : true, "type" : "long" },
      { "id" : 2, "name" : "amount", "required" : false, "type" : "decimal(10, 2)" },
      { "id" : 3, "name" : "ordered_at", "required" : false, "type" : "timestamptz" },
      { "id" : 4, "name" : "tags", "required" : false,
        "type" : { "type" : "list", "element-id" : 6, "element" : "string", "element-required" : false } },
      { "id" : 5, "name" : "address", "required" : false,
        "type" : { "type" : "struct", "fields" : [ { "id" : 7, "name" : "city", "required" : false, "type" : "string" } ] } }
    ]
  } ],
  "default-spec-id" : 0,
  "partition-specs" : [ { "spec-id" : 0, "fields" : [
    { "name" : "ordered_at_day", "transform" : "day", "source-id" : 3, "field-id" : 1000 } ] } ],
  "properties" : { "write.format.default" : "parquet" },
  "current-snapshot-id" : 3051729675574597004,
  "snapshots" : [
    { "sequence-number" : 1, "snapshot-id" : 1868154234528417337, "timestamp-ms" : 1789990000000 },
    { "sequence-number" : 2, "snapshot-id" : 3051729675574597004, "parent-snapshot-id" : 1868154234528417337, "timestamp-ms" : 1790000000000,
      "summary" : { "operation" : "append", "added-records" : "3", "total-records" : "5" } }
  ],
  "refs" : { "main" : { "snapshot-id" : 3051729675574597004, "type" : "branch" } },
  "metadata-log" : [
    { "timestamp-ms" : 1789980000000, "metadata-file" : "s3://warehouse/sales/orders/metadata/00000-a.metadata.json" },
    { "timestamp-ms" : 1789990000000, "metadata-file" : "s3://warehouse/sales/orders/metadata/00001-b.metadata.json" }
  ]
}`

describe("parseIcebergMetadata", () => {
  const meta = parseIcebergMetadata(V2)

  it("reads the table's identity and format", () => {
    expect(meta).toMatchObject({
      formatVersion: 2,
      tableUuid: "9c12d441-03fe-4693-9a96-a0705ddf69c1",
      location: "s3://warehouse/sales/orders",
      lastUpdatedMs: 1790000000000,
      lastSequenceNumber: 2,
      properties: { "write.format.default": "parquet" },
    })
  })

  it("keeps a 64-bit snapshot id exact rather than rounding it through a double", () => {
    // JSON.parse would give 3051729675574597000, which names no snapshot.
    expect(meta?.currentSnapshotId).toBe("3051729675574597004")
  })

  it("reads each snapshot with its parent, operation and summary", () => {
    expect(meta?.snapshots[1]).toEqual({
      snapshotId: "3051729675574597004",
      parentSnapshotId: "1868154234528417337",
      sequenceNumber: 2,
      timestampMs: 1790000000000,
      operation: "append",
      summary: { "added-records": "3", "total-records": "5" },
      manifestList: undefined,
      schemaId: undefined,
    })
  })

  it("picks the current schema by id, not by position", () => {
    expect(meta?.currentSchema?.schemaId).toBe(1)
  })

  it("keeps every schema, for the evolution between them", () => {
    expect(meta?.schemas.map((s) => s.schemaId)).toEqual([0, 1])
  })

  it("reads the metadata log oldest first", () => {
    expect(meta?.metadataLog.map((e) => e.metadataFile)).toEqual([
      "s3://warehouse/sales/orders/metadata/00000-a.metadata.json",
      "s3://warehouse/sales/orders/metadata/00001-b.metadata.json",
    ])
  })

  it("reads the branches and tags", () => {
    expect(meta?.refs).toEqual([
      { name: "main", type: "branch", snapshotId: "3051729675574597004" },
    ])
  })

  it("reads a v1 file's single schema and inline partition spec", () => {
    const v1 = parseIcebergMetadata(
      JSON.stringify({
        "format-version": 1,
        "table-uuid": "u",
        location: "s3://b/t",
        "last-updated-ms": 0,
        schema: { type: "struct", fields: [{ id: 1, name: "id", required: true, type: "int" }] },
        "partition-spec": [{ name: "id", transform: "identity", "source-id": 1 }],
        "current-snapshot-id": -1,
      }),
    )
    expect(v1).toMatchObject({
      formatVersion: 1,
      currentSchema: { fields: [{ name: "id", type: "int", required: true }] },
      defaultSpec: { fields: [{ sourceId: 1, transform: "identity" }] },
      currentSnapshotId: null,
      snapshots: [],
    })
  })

  it("says a freshly created table has no current snapshot", () => {
    const fresh = V2.replace("3051729675574597004,", "-1,")
    expect(parseIcebergMetadata(fresh)?.currentSnapshotId).toBeNull()
  })

  it.each([['{"name":"x"}'], ['{"format-version":"2"}'], ["[1,2]"]])(
    "is null for %s, which is not table metadata",
    (text) => {
      expect(parseIcebergMetadata(text)).toBeNull()
    },
  )

  it("is null for a metadata file the preview window cut short", () => {
    expect(parseIcebergMetadata(V2.slice(0, 200))).toBeNull()
  })
})

describe("flattenFields", () => {
  it("lists nested struct fields after their parent, with their path", () => {
    const fields = parseIcebergMetadata(V2)?.currentSchema?.fields ?? []
    expect(flattenFields(fields).map((f) => [f.path, f.depth])).toEqual([
      ["order_id", 0],
      ["amount", 0],
      ["ordered_at", 0],
      ["tags", 0],
      ["address", 0],
      ["address.city", 1],
    ])
  })
})

describe("partitionLabel", () => {
  it("names the source column the way Iceberg's DDL does", () => {
    const meta = parseIcebergMetadata(V2)
    const [field] = meta?.defaultSpec?.fields ?? []
    expect(partitionLabel(field, meta?.currentSchema)).toBe("day(ordered_at)")
  })
})

describe("typeName", () => {
  it.each([
    [
      { type: "map", "key-id": 1, key: "string", "value-id": 2, value: "long" },
      "map<string, long>",
    ],
    [{ type: "list", element: { type: "list", element: "int" } }, "list<list<int>>"],
    [{ type: "struct", fields: [{ id: 7, name: "city", type: "string" }] }, "struct<city: string>"],
  ])("spells %j as %s", (type, name) => {
    expect(typeName(type)).toBe(name)
  })
})
