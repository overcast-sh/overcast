import { describe, expect, it } from "vitest"
import { previewSql, trinoIdentifier } from "./sql-quote"

describe("trinoIdentifier", () => {
  it("double-quotes a name and doubles any quote in it", () => {
    expect(trinoIdentifier('order "date"')).toBe('"order ""date"""')
  })
})

describe("previewSql", () => {
  it("selects the first rows of the table, both names quoted", () => {
    expect(previewSql("sales", "orders")).toBe('SELECT * FROM "sales"."orders" LIMIT 10;')
  })
})
