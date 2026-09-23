import { icebergSummary, typeName } from "./preview-iceberg"

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
    { "sequence-number" : 2, "snapshot-id" : 3051729675574597004, "parent-snapshot-id" : 1868154234528417337, "timestamp-ms" : 1790000000000 }
  ]
}`

describe("icebergSummary", () => {
  it("reads the facts a developer checks after a commit", () => {
    const summary = icebergSummary(V2)
    expect(summary).toMatchObject({
      formatVersion: 2,
      tableUuid: "9c12d441-03fe-4693-9a96-a0705ddf69c1",
      location: "s3://warehouse/sales/orders",
      snapshotCount: 2,
      schemaId: 1,
      partitionFields: ["day(ordered_at)"],
    })
    expect(summary?.lastUpdated?.toISOString()).toBe("2026-09-21T14:13:20.000Z")
  })

  it("keeps a 64-bit snapshot id exact rather than rounding it through a double", () => {
    // JSON.parse would give 3051729675574597000, which names no snapshot.
    expect(icebergSummary(V2)?.currentSnapshotId).toBe("3051729675574597004")
  })

  it("lists the current schema's fields, not the first schema's", () => {
    expect(icebergSummary(V2)?.fields).toEqual([
      { id: 1, name: "order_id", type: "long", required: true },
      { id: 2, name: "amount", type: "decimal(10, 2)", required: false },
      { id: 3, name: "ordered_at", type: "timestamptz", required: false },
      { id: 4, name: "tags", type: "list<string>", required: false },
      { id: 5, name: "address", type: "struct<city: string>", required: false },
    ])
  })

  it("reads a v1 file's single schema and inline partition spec", () => {
    const v1 = JSON.stringify({
      "format-version": 1,
      "table-uuid": "u",
      location: "s3://b/t",
      "last-updated-ms": 0,
      schema: { type: "struct", fields: [{ id: 1, name: "id", required: true, type: "int" }] },
      "partition-spec": [{ name: "id", transform: "identity", "source-id": 1 }],
      "current-snapshot-id": -1,
    })
    expect(icebergSummary(v1)).toMatchObject({
      formatVersion: 1,
      fields: [{ name: "id", type: "int", required: true }],
      partitionFields: ["identity(id)"],
      currentSnapshotId: null,
      snapshotCount: 0,
    })
  })

  it("says a freshly created table has no current snapshot", () => {
    const fresh = V2.replace("3051729675574597004,", "-1,")
    expect(icebergSummary(fresh)?.currentSnapshotId).toBeNull()
  })

  it("is null for JSON that is not table metadata", () => {
    expect(icebergSummary('{"name":"x"}')).toBeNull()
    expect(icebergSummary('{"format-version":"2"}')).toBeNull()
    expect(icebergSummary("[1,2]")).toBeNull()
  })

  it("is null for a metadata file the preview window cut short", () => {
    expect(icebergSummary(V2.slice(0, 200))).toBeNull()
  })
})

describe("typeName", () => {
  it("spells nested types the way the spec does", () => {
    expect(
      typeName({ type: "map", "key-id": 1, key: "string", "value-id": 2, value: "long" }),
    ).toBe("map<string, long>")
    expect(typeName({ type: "list", element: { type: "list", element: "int" } })).toBe(
      "list<list<int>>",
    )
  })
})
