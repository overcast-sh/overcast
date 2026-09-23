import { memorySource } from "./memory-source"
import { readRows } from "./testing/source-helpers"

describe("memorySource", () => {
  const source = memorySource([{ name: "a", numeric: false }], [["x"], ["y"]])

  it("counts its rows exactly", () => {
    expect(source.rowCount).toEqual({ value: 2, exact: true })
  })

  it("serves any range of them, columnar", async () => {
    expect(await readRows(source, 1, 2)).toEqual({ start: 1, count: 1, columns: [["y"]] })
  })
})
