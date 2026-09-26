import { s3tablesCatalog } from "./athena-sql"

describe("s3tablesCatalog", () => {
  it("names a bucket's catalog the way Athena does", () => {
    expect(s3tablesCatalog("analytics")).toBe("s3tablescatalog/analytics")
  })
})
