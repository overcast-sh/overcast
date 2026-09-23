import { columnBytes } from "./value-bytes"

describe("columnBytes", () => {
  it("counts strings by their length", () => {
    expect(columnBytes(["x".repeat(1000)])).toBeGreaterThan(columnBytes(["x"]) + 1900)
  })

  it("counts a typed array by its buffer", () => {
    expect(columnBytes(new Int32Array(1000))).toBe(4000)
  })

  it("sizes lists and structs by their shape, without walking into them", () => {
    const nested = [{ a: { deep: "x".repeat(10_000) } }]
    expect(columnBytes(nested)).toBeLessThan(200)
  })

  it.each([
    ["numbers", [1, 2]],
    ["NULLs", [null, null]],
    ["booleans", [true, false]],
  ])("gives %s eight bytes each", (_, values) => {
    expect(columnBytes(values)).toBe(16 + 16)
  })
})
