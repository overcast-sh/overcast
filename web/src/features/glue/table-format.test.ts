import { csvTable, icebergTable } from "./__fixtures__/tables"
import { isIcebergTable, metadataLocation, tableFormat, tableLocation } from "./table-format"

describe("tableFormat", () => {
  it("reads the Iceberg marker first", () => {
    expect(tableFormat(icebergTable)).toBe("ICEBERG")
  })

  it("reads a CSV table from its SerDe", () => {
    expect(tableFormat(csvTable)).toBe("CSV")
  })

  it.each([
    ["org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe", "PARQUET"],
    ["org.apache.hadoop.hive.ql.io.orc.OrcSerde", "ORC"],
    ["org.openx.data.jsonserde.JsonSerDe", "JSON"],
    ["org.apache.hadoop.hive.serde2.avro.AvroSerDe", "AVRO"],
  ])("reads %s as %s", (serde, format) => {
    expect(tableFormat({ StorageDescriptor: { SerdeInfo: { SerializationLibrary: serde } } })).toBe(
      format,
    )
  })

  it("falls back to the crawler's classification", () => {
    expect(tableFormat({ Parameters: { classification: "orc" } })).toBe("ORC")
  })

  it("says nothing when nothing on the table says", () => {
    expect(tableFormat({})).toBeUndefined()
  })

  it("reads table_type in any case, as PyIceberg writes it", () => {
    expect(isIcebergTable({ Parameters: { TABLE_TYPE: "iceberg" } })).toBe(true)
  })
})

describe("locations", () => {
  it("reads the storage descriptor's location", () => {
    expect(tableLocation(csvTable)).toBe("s3://lake/orders/")
  })

  it("reads an Iceberg table's current metadata file", () => {
    expect(metadataLocation(icebergTable)).toBe("s3://lake/events/metadata/00001-abc.metadata.json")
  })
})
