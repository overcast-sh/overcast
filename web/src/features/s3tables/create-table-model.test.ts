import {
  draftProblems,
  maxDepth,
  partitionableColumns,
  toCdk,
  toCreateTableInput,
  type SchemaRow,
  type TableDraft,
} from "./create-table-model"

const row = (name: string, type: SchemaRow["type"], depth = 0, required = false): SchemaRow => ({
  key: name,
  name,
  type,
  required,
  depth,
})

const flat: TableDraft = {
  tableBucketARN: "arn:aws:s3tables:us-east-1:000000000000:bucket/analytics",
  namespace: "sales",
  name: "orders",
  rows: [row("order_id", "long", 0, true), row("ordered_at", "timestamptz")],
  partitions: [{ key: "p", column: "ordered_at", transform: "day" }],
}

const nested: TableDraft = {
  ...flat,
  rows: [
    row("order_id", "long", 0, true),
    row("shipping", "struct"),
    row("carrier", "string", 1),
    row("shipped_at", "timestamptz", 1),
    row("channel", "string"),
  ],
  partitions: [],
}

describe("toCreateTableInput", () => {
  it("sends a flat schema as metadata.iceberg.schema, with Iceberg's ids", () => {
    expect(toCreateTableInput(flat).metadata?.iceberg?.schema).toEqual({
      fields: [
        { id: 1, name: "order_id", type: "long", required: true },
        { id: 2, name: "ordered_at", type: "timestamptz", required: false },
      ],
    })
  })

  it("points each partition field at its source column's id", () => {
    expect(toCreateTableInput(flat).metadata?.iceberg?.partitionSpec).toEqual({
      specId: 0,
      fields: [{ sourceId: 2, fieldId: 1000, name: "ordered_at_day", transform: "day" }],
    })
  })

  it("sends nested structs as schemaV2, fields numbered depth-first", () => {
    expect(toCreateTableInput(nested).metadata?.iceberg?.schemaV2).toEqual({
      type: "struct",
      schemaId: 0,
      fields: [
        { id: 1, name: "order_id", required: true, type: "long" },
        {
          id: 2,
          name: "shipping",
          required: false,
          type: {
            type: "struct",
            fields: [
              { id: 3, name: "carrier", required: false, type: "string" },
              { id: 4, name: "shipped_at", required: false, type: "timestamptz" },
            ],
          },
        },
        { id: 5, name: "channel", required: false, type: "string" },
      ],
    })
  })

  it("leaves the partition spec out when there is none", () => {
    expect(toCreateTableInput(nested).metadata?.iceberg).not.toHaveProperty("partitionSpec")
  })
})

describe("draftProblems", () => {
  it("has none for a complete draft", () => {
    expect(draftProblems(flat)).toEqual([])
  })

  it("asks for a field under an empty struct", () => {
    expect(draftProblems({ ...flat, rows: [row("shipping", "struct")], partitions: [] })).toEqual([
      "Struct shipping needs at least one field indented under it.",
    ])
  })

  it("refuses two columns with one name at the same level", () => {
    expect(
      draftProblems({ ...flat, rows: [row("a", "long"), row("a", "int")], partitions: [] }),
    ).toEqual(["Two fields are called a."])
  })

  it("allows the same name in two different structs", () => {
    const rows = [row("a", "struct"), row("x", "int", 1), row("b", "struct"), row("x", "int", 1)]
    expect(draftProblems({ ...flat, rows, partitions: [] })).toEqual([])
  })

  it("asks for a column on a partition whose column is gone", () => {
    const partitions = [{ key: "p", column: "dropped", transform: "day" as const }]
    expect(draftProblems({ ...flat, partitions })).toEqual(["Partition 1 needs a column."])
  })

  it("refuses a table name S3 Tables would", () => {
    expect(draftProblems({ ...flat, name: "Orders" })[0]).toMatch(/^Table name:/)
  })
})

describe("maxDepth", () => {
  it("lets a row indent one level under a struct, and no further", () => {
    expect(maxDepth(nested.rows, 2)).toBe(1)
    expect(maxDepth(nested.rows, 3)).toBe(1)
  })

  it("keeps the first row at the top level", () => {
    expect(maxDepth(nested.rows, 0)).toBe(0)
  })
})

describe("partitionableColumns", () => {
  it("offers the top-level columns that are not structs", () => {
    expect(partitionableColumns(nested.rows)).toEqual(["order_id", "channel"])
  })
})

describe("toCdk", () => {
  it("declares the table as a CfnTable with the same schema and partition spec", () => {
    expect(toCdk(flat)).toBe(`import { CfnTable } from "aws-cdk-lib/aws-s3tables"

new CfnTable(this, "OrdersTable", {
  tableBucketArn: "arn:aws:s3tables:us-east-1:000000000000:bucket/analytics",
  namespace: "sales",
  tableName: "orders",
  openTableFormat: "ICEBERG",
  icebergMetadata: {
    icebergSchema: {
      schemaFieldList: [
        {
          name: "order_id",
          type: "long",
          required: true
        },
        {
          name: "ordered_at",
          type: "timestamptz",
          required: false
        }
      ]
    },
    icebergPartitionSpec: {
      specId: 0,
      fields: [
        {
          sourceId: 2,
          fieldId: 1000,
          name: "ordered_at_day",
          transform: "day"
        }
      ]
    }
  }
})`)
  })
})
