import { csvTable } from "./__fixtures__/tables"
import { buildPartitionInput, buildTableInput, tableInputOf, type TableDraft } from "./table-input"

const draft: TableDraft = {
  name: "orders",
  location: "s3://lake/orders/",
  format: "parquet",
  columns: [{ Name: "id", Type: "bigint" }],
  partitionKeys: [],
}

describe("buildTableInput", () => {
  it("registers Parquet with the SerDe and formats a crawler uses", () => {
    const input = buildTableInput(draft)
    expect(input.StorageDescriptor?.SerdeInfo?.SerializationLibrary).toBe(
      "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe",
    )
    expect(input.Parameters).toMatchObject({ EXTERNAL: "TRUE", classification: "parquet" })
  })

  it("registers quoted CSV with OpenCSVSerde and skips the header", () => {
    const input = buildTableInput({ ...draft, format: "csv", quoted: true, delimiter: "|" })
    expect(input.StorageDescriptor?.SerdeInfo).toEqual({
      SerializationLibrary: "org.apache.hadoop.hive.serde2.OpenCSVSerde",
      Parameters: { separatorChar: "|", quoteChar: '"', escapeChar: "\\" },
    })
    expect(input.Parameters?.["skip.header.line.count"]).toBe("1")
  })

  it("registers JSON Lines with the OpenX SerDe, naming its paths", () => {
    const input = buildTableInput({ ...draft, format: "jsonl" })
    expect(input.StorageDescriptor?.SerdeInfo?.Parameters).toEqual({ paths: "id" })
  })
})

describe("buildPartitionInput", () => {
  it("points the table's storage at the partition's prefix", () => {
    const input = buildPartitionInput(csvTable, ["2026-09-01"], "s3://lake/orders/dt=2026-09-01/")
    expect(input.StorageDescriptor?.Location).toBe("s3://lake/orders/dt=2026-09-01/")
    expect(input.StorageDescriptor?.SerdeInfo).toEqual(csvTable.StorageDescriptor?.SerdeInfo)
  })
})

describe("tableInputOf", () => {
  it("leaves out what the service assigns, so a version diff shows what the writer changed", () => {
    const input = tableInputOf(csvTable)
    expect(input).not.toHaveProperty("VersionId")
    expect(input).not.toHaveProperty("UpdateTime")
    expect(input.Name).toBe("orders")
  })
})
