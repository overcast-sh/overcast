import { searchOneOf, searchText } from "./search-params"

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
