import { tableFormat } from "./table-format"

describe("tableFormat", () => {
  it.each([
    [{ table_type: "ICEBERG" }, "ICEBERG"],
    [{ inputformat: "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat" }, "PARQUET"],
    [{ inputformat: "org.apache.hadoop.hive.ql.io.orc.OrcInputFormat" }, "ORC"],
    [{ "serde.serialization.lib": "org.openx.data.jsonserde.JsonSerDe" }, "JSON"],
    [{ "serde.serialization.lib": "org.apache.hadoop.hive.serde2.OpenCSVSerde" }, "CSV"],
    [{ inputformat: "org.apache.hadoop.mapred.TextInputFormat" }, "CSV"],
    [{ classification: "avro" }, "AVRO"],
    [
      {
        inputformat: "org.apache.hadoop.mapred.TextInputFormat",
        "serde.serialization.lib": "org.openx.data.jsonserde.JsonSerDe",
      },
      "JSON",
    ],
  ])("reads %j as %s", (Parameters, format) => {
    expect(tableFormat({ Name: "t", Parameters })).toBe(format)
  })

  it("names a view", () => {
    expect(tableFormat({ Name: "v", TableType: "VIRTUAL_VIEW" })).toBe("VIEW")
  })

  it("is undefined when nothing says", () => {
    expect(tableFormat({ Name: "t" })).toBeUndefined()
  })
})
