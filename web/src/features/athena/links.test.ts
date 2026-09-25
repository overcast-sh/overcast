import { defaultParseSearch, defaultStringifySearch } from "@tanstack/react-router"
import { describe, expect, it } from "vitest"
import { athenaEditorLink } from "./links"
import { validateAthenaSearch } from "./search"

const SQL = `SELECT "order_id", amount FROM "sales"."orders" WHERE note = 'a & b?' LIMIT 10`

describe("athenaEditorLink", () => {
  it("targets the editor tab with the catalog, database and SQL", () => {
    expect(athenaEditorLink({ catalog: "AwsDataCatalog", database: "sales", sql: SQL })).toEqual({
      to: "/athena",
      search: { tab: "editor", catalog: "AwsDataCatalog", database: "sales", sql: SQL },
    })
  })

  it("round-trips the SQL through the URL unchanged", () => {
    const { search } = athenaEditorLink({ database: "sales", sql: SQL })
    const parsed = validateAthenaSearch(defaultParseSearch(defaultStringifySearch(search)))
    expect(parsed).toMatchObject({ tab: "editor", database: "sales", sql: SQL })
  })

  it("reads a hand-written URL-encoded link", () => {
    const parsed = validateAthenaSearch(
      defaultParseSearch(`?tab=editor&catalog=s3tablescatalog%2Fwarehouse&database=ns&sql=${encodeURIComponent(SQL)}`),
    )
    expect(parsed).toMatchObject({ catalog: "s3tablescatalog/warehouse", database: "ns", sql: SQL })
  })

  it("keeps SQL that looks like JSON as text", () => {
    expect(validateAthenaSearch(defaultParseSearch("?sql=42")).sql).toBe("42")
  })
})

describe("validateAthenaSearch", () => {
  it("drops an unknown tab", () => {
    expect(validateAthenaSearch({ tab: "nope" }).tab).toBeUndefined()
  })
})
