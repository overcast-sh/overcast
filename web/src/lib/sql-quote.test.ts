import { hiveIdentifier, hiveString, tableIdentifier, trinoIdentifier } from "./sql-quote"

describe("sql-quote", () => {
  it("backquotes a Hive name, doubling any backquote", () => {
    expect(hiveIdentifier("a`b")).toBe("`a``b`")
  })

  it("double-quotes a Trino name, doubling any double quote", () => {
    expect(trinoIdentifier('a"b')).toBe('"a""b"')
  })

  it("escapes a Hive string with backslashes, a tab as \\t", () => {
    expect(hiveString("it's\t\\")).toBe("'it\\'s\\t\\\\'")
  })

  it.each([
    ["Orders 2026.csv", "orders_2026_csv"],
    ["2026-data", "t_2026_data"],
    ["---", "data"],
  ])("makes a table name of %s", (text, name) => {
    expect(tableIdentifier(text)).toBe(name)
  })
})
