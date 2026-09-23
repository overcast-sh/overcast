import { inferColumns, isNumericColumn } from "./column-types"

describe("isNumericColumn", () => {
  it("accepts integers, decimals, signs and exponents, ignoring blanks", () => {
    expect(isNumericColumn(["1", "-2.5", "+3", "1e6", "", null, undefined, ".5"])).toBe(true)
  })

  it.each([
    ["an identifier with a leading zero", ["007"]],
    ["any text among the numbers", ["1", "n/a"]],
    ["nothing at all", ["", null]],
  ])("is false for %s", (_, values) => {
    expect(isNumericColumn(values)).toBe(false)
  })

  it("does not read a quoted JSON string as a number", () => {
    expect(isNumericColumn(["42"], { numericText: false })).toBe(false)
  })

  it("accepts typed numbers from a typed source", () => {
    expect(isNumericColumn([42, 7n, null], { numericText: false })).toBe(true)
  })
})

describe("inferColumns", () => {
  it("names each column and marks the numeric ones", () => {
    expect(
      inferColumns(
        ["id", "name"],
        [
          ["1", "2"],
          ["a", "b"],
        ],
      ),
    ).toEqual([
      { name: "id", numeric: true },
      { name: "name", numeric: false },
    ])
  })
})
