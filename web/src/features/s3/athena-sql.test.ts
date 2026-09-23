import { athenaSql } from "./athena-sql"

describe("athenaSql", () => {
  it("makes a Parquet table over the object's folder, typed from the schema", () => {
    const sql = athenaSql({
      bucket: "lake",
      objectKey: "sales/2026/orders.parquet",
      format: "parquet",
      columns: [
        { name: "order_id", type: "INT64", numeric: true },
        { name: "amount", type: "DECIMAL(10,2)", numeric: true },
        { name: "ordered_at", type: "TIMESTAMP(MICROS, UTC)", numeric: false },
        { name: "tags", type: "LIST<STRING>", numeric: false },
      ],
    })
    expect(sql).toContain("CREATE EXTERNAL TABLE orders (")
    expect(sql).toContain(
      "  `order_id` bigint,\n  `amount` decimal(10,2),\n  `ordered_at` timestamp,\n  `tags` array<string>",
    )
    expect(sql).toContain("STORED AS PARQUET\nLOCATION 's3://lake/sales/2026/'")
  })

  it("reads CSV columns as strings through OpenCSVSerde, skipping the header", () => {
    const sql = athenaSql({
      bucket: "lake",
      objectKey: "prices.csv",
      format: "csv",
      columns: [{ name: "price", numeric: true }],
      delimiter: ";",
    })
    expect(sql).toContain("`price` string")
    expect(sql).toContain("'separatorChar' = ';'")
    expect(sql).toContain("TBLPROPERTIES ('skip.header.line.count' = '1')")
  })

  it("spells a tab separator the way the SerDe reads it", () => {
    const sql = athenaSql({ bucket: "b", objectKey: "a.tsv", format: "tsv", columns: [] })
    expect(sql).toContain("'separatorChar' = '\\t'")
  })

  it("types JSON Lines numbers as double and reads them with the JSON SerDe", () => {
    const sql = athenaSql({
      bucket: "b",
      objectKey: "events.jsonl",
      format: "jsonl",
      columns: [{ name: "latency", numeric: true }],
    })
    expect(sql).toContain("`latency` double")
    expect(sql).toContain("ROW FORMAT SERDE 'org.openx.data.jsonserde.JsonSerDe'")
  })

  it.each([
    ["2026-orders.csv", "t_2026_orders"],
    ["My Data!.csv", "my_data"],
  ])("names the table for %s as Athena accepts it: %s", (file, table) => {
    const sql = athenaSql({ bucket: "b", objectKey: file, format: "csv", columns: [] })
    expect(sql).toContain(`CREATE EXTERNAL TABLE ${table} (`)
  })

  it("quotes a column name holding a backquote", () => {
    const sql = athenaSql({
      bucket: "b",
      objectKey: "a.csv",
      format: "csv",
      columns: [{ name: "odd`name", numeric: false }],
    })
    expect(sql).toContain("`odd``name` string")
  })
})
