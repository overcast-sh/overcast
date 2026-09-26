import { csvTable, icebergTable } from "./__fixtures__/tables"
import { tableDdl } from "./table-ddl"

describe("tableDdl", () => {
  it("recreates a Hive table as Athena's SHOW CREATE TABLE prints it", () => {
    expect(tableDdl(csvTable)).toBe(
      [
        "CREATE EXTERNAL TABLE `sales`.`orders` (",
        "  `id` bigint,",
        "  `amount` double COMMENT 'in cents'",
        ")",
        "PARTITIONED BY (",
        "  `dt` string",
        ")",
        "ROW FORMAT SERDE 'org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe'",
        "WITH SERDEPROPERTIES (",
        "  'field.delim' = ','",
        ")",
        "STORED AS INPUTFORMAT 'org.apache.hadoop.mapred.TextInputFormat'",
        "OUTPUTFORMAT 'org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat'",
        "LOCATION 's3://lake/orders/'",
        "TBLPROPERTIES (",
        "  'classification' = 'csv',",
        "  'skip.header.line.count' = '1'",
        ");",
        "",
      ].join("\n"),
    )
  })

  it("recreates an Iceberg table with its table_type and without Glue's metadata pointer", () => {
    const ddl = tableDdl(icebergTable) ?? ""
    expect(ddl).toContain("CREATE TABLE `sales`.`events` (")
    expect(ddl).toContain("'table_type' = 'ICEBERG'")
    expect(ddl).not.toContain("metadata_location")
  })

  it("has no DDL for a view", () => {
    expect(tableDdl({ ...csvTable, TableType: "VIRTUAL_VIEW" })).toBeUndefined()
  })
})
