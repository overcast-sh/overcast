import { snapshotQuerySql } from "./snapshot-sql"

describe("snapshotQuerySql", () => {
  it("reads an S3 Tables table at one snapshot through its bucket's catalog", () => {
    const ref = { catalog: "s3tablescatalog/analytics", database: "sales", table: "orders" }
    expect(snapshotQuerySql(ref, "3051729675574597004")).toBe(
      'SELECT * FROM "s3tablescatalog/analytics"."sales"."orders" FOR VERSION AS OF 3051729675574597004 LIMIT 100',
    )
  })

  it("reads a Glue table through AwsDataCatalog", () => {
    const ref = { catalog: "AwsDataCatalog", database: "lake", table: "events" }
    expect(snapshotQuerySql(ref, "1")).toBe(
      'SELECT * FROM "AwsDataCatalog"."lake"."events" FOR VERSION AS OF 1 LIMIT 100',
    )
  })

  it("doubles a quote inside an identifier", () => {
    expect(snapshotQuerySql({ catalog: "c", database: "d", table: 'we"ird' }, "1")).toContain(
      '"we""ird"',
    )
  })
})
