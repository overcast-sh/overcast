import { snapshotQuerySql } from "./athena-sql"

describe("snapshotQuerySql", () => {
  it("reads the table at one snapshot through the bucket's catalog", () => {
    expect(snapshotQuerySql("analytics", "sales", "orders", "3051729675574597004")).toBe(
      'SELECT * FROM "s3tablescatalog/analytics"."sales"."orders" FOR VERSION AS OF 3051729675574597004 LIMIT 100',
    )
  })

  it("doubles a quote inside an identifier", () => {
    expect(snapshotQuerySql("b", "n", 'we"ird', "1")).toContain('"we""ird"')
  })
})
