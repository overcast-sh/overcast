import { searchLargeId, searchOneOf, searchText } from "./search-params"

describe("searchText", () => {
  it.each([
    ["orders", "orders"],
    [42, "42"],
    [true, "true"],
    [undefined, undefined],
    [{ a: 1 }, undefined],
  ])("reads %j as %j", (value, text) => {
    expect(searchText(value)).toBe(text)
  })
})

describe("searchOneOf", () => {
  it("keeps a listed value and drops any other", () => {
    expect(searchOneOf(["a", "b"] as const, "b")).toBe("b")
    expect(searchOneOf(["a", "b"] as const, "c")).toBeUndefined()
  })
})

describe("searchLargeId", () => {
  it("keeps a quoted id exactly", () => {
    expect(searchLargeId("3051729675574628680")).toBe("3051729675574628680")
  })

  it("drops a bare id the router already rounded", () => {
    expect(searchLargeId(Number("3051729675574628680"))).toBeUndefined()
  })

  it("keeps a small bare id", () => {
    expect(searchLargeId(42)).toBe("42")
  })
})
