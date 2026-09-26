import { ddlSummary } from "./ddl-summary"

describe("ddlSummary", () => {
  it.each([
    [
      "CREATE EXTERNAL TABLE IF NOT EXISTS sales.Orders (id int) LOCATION 's3://b/'",
      { verb: "Created", kind: "table", database: "sales", name: "orders" },
    ],
    [
      "-- make it\nCREATE TABLE events (id int)",
      { verb: "Created", kind: "table", database: "default", name: "events" },
    ],
    [
      'DROP TABLE IF EXISTS "My DB"."My Table"',
      { verb: "Dropped", kind: "table", database: "My DB", name: "My Table" },
    ],
    ["CREATE DATABASE sales", { verb: "Created", kind: "database", database: "sales" }],
    [
      "ALTER TABLE `orders` ADD PARTITION (dt='2026-09-26')",
      { verb: "Altered", kind: "table", database: "default", name: "orders" },
    ],
    [
      "CREATE OR REPLACE VIEW daily AS SELECT 1",
      { verb: "Created", kind: "view", database: "default", name: "daily" },
    ],
  ])("reads %s", (sql, summary) => {
    expect(ddlSummary(sql, "default")).toEqual(summary)
  })

  it("is null for DDL it does not summarise", () => {
    expect(ddlSummary("MSCK REPAIR TABLE orders", "default")).toBeNull()
  })
})
