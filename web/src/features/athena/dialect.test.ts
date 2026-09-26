import { dialectDifference } from "./dialect"

describe("dialectDifference", () => {
  it.each([
    ["OPTIMIZE events REWRITE DATA USING BIN_PACK", "OPTIMIZE and VACUUM"],
    ["-- tidy\nVACUUM events", "OPTIMIZE and VACUUM"],
    ["UNLOAD (SELECT 1) TO 's3://b/' WITH (format = 'PARQUET')", "UNLOAD"],
    ["USING EXTERNAL FUNCTION f(x int) RETURNS int LAMBDA 'fn' SELECT f(1)", "Lambda"],
    ["ALTER TABLE orders ADD COLUMNS (note string)", "Hive DDL"],
    ["DESCRIBE orders amount", "DESCRIBE"],
  ])("names the difference %s runs into", (sql, title) => {
    expect(dialectDifference(sql)?.title).toContain(title)
  })

  it.each([
    "SELECT * FROM orders",
    "ALTER TABLE orders ADD PARTITION (dt = '2026-09-26')",
    "SELECT 'UNLOAD' AS word",
    "DESCRIBE orders",
    "DESCRIBE EXTENDED orders",
  ])("finds none in %s", (sql) => {
    expect(dialectDifference(sql)).toBeNull()
  })
})
