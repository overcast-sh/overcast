import { s3tablesCatalog, s3tablesTableRef } from "./athena-sql"

describe("s3tablesCatalog", () => {
  it("names a bucket's catalog the way Athena does", () => {
    expect(s3tablesCatalog("analytics")).toBe("s3tablescatalog/analytics")
  })
})

describe("s3tablesTableRef", () => {
  it("finds a table in its bucket's catalog, with its namespace as the database", () => {
    expect(s3tablesTableRef("analytics", "sales", "orders")).toEqual({
      catalog: "s3tablescatalog/analytics",
      database: "sales",
      table: "orders",
    })
  })
})
