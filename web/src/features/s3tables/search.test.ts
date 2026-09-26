import { validateTableBucketSearch, validateTableSearch } from "./search"

describe("validateTableSearch", () => {
  it("keeps a quoted snapshot id exactly", () => {
    expect(validateTableSearch({ snapshot: "3051729675574628680" }).snapshot).toBe(
      "3051729675574628680",
    )
  })

  it("drops a bare snapshot id the router already rounded", () => {
    expect(
      validateTableSearch({ snapshot: Number("3051729675574628680") }).snapshot,
    ).toBeUndefined()
  })

  it("keeps a small bare snapshot id", () => {
    expect(validateTableSearch({ snapshot: 42 }).snapshot).toBe("42")
  })

  it("ignores a tab the page does not have", () => {
    expect(validateTableSearch({ tab: "partitions" }).tab).toBeUndefined()
  })
})

describe("validateTableBucketSearch", () => {
  it("reads the tab and the filter", () => {
    expect(validateTableBucketSearch({ tab: "policy", q: "sales" })).toEqual({
      tab: "policy",
      q: "sales",
    })
  })
})
