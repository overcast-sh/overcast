import { findInBlocks, MAX_MATCHES, selectionTsv, type ValueAt } from "./loaded-rows"
import type { LaidOutColumn } from "./use-grid-columns"

const laidOut = (names: string[], order = names.map((_, i) => i)): LaidOutColumn[] =>
  order.map((index, position) => ({
    position,
    index,
    column: { name: names[index], numeric: false },
    width: 100,
    start: position * 100,
    onResizeStart: () => {},
    resizing: false,
  }))

describe("selectionTsv", () => {
  const values: ValueAt = (row, index) =>
    row === 2 ? { loaded: false, value: undefined } : { loaded: true, value: `r${row}c${index}` }

  it("copies the selected rectangle as tab-separated lines, in display order", () => {
    // Given: columns shown in the order 2, 0, 1
    const columns = laidOut(["a", "b", "c"], [2, 0, 1])
    // When: rows 0–1 of the first two displayed columns are copied
    const copied = selectionTsv({ top: 0, bottom: 1, left: 0, right: 1 }, columns, values)
    // Then: each line holds source columns 2 then 0
    expect(copied.text).toBe("r0c2\tr0c0\nr1c2\tr1c0")
  })

  it("leaves out rows that are not loaded, and counts them", () => {
    const copied = selectionTsv({ top: 1, bottom: 3, left: 0, right: 0 }, laidOut(["a"]), values)
    expect(copied).toEqual({ text: "r1c0\nr3c0", rows: 2, skipped: 1 })
  })

  it("spells NULL out and keeps tabs and line breaks from splitting a value", () => {
    const tricky: ValueAt = (_, index) => ({ loaded: true, value: index === 0 ? null : "a\tb\nc" })
    const copied = selectionTsv(
      { top: 0, bottom: 0, left: 0, right: 1 },
      laidOut(["x", "y"]),
      tricky,
    )
    expect(copied.text).toBe("NULL\ta b c")
  })
})

describe("findInBlocks", () => {
  const columns = laidOut(["name", "city"])
  const blocks = [
    { start: 1000, count: 1, columns: [["Grace"], ["Arlington"]] },
    {
      start: 0,
      count: 2,
      columns: [
        ["Ada", "Alan"],
        ["London", null],
      ],
    },
  ]

  it("finds matches in row then column order, whatever order the blocks are cached in", () => {
    expect(findInBlocks(blocks, columns, "a")).toEqual([
      { row: 0, col: 0 },
      { row: 1, col: 0 },
      { row: 1000, col: 0 },
      { row: 1000, col: 1 },
    ])
  })

  it("ignores case and surrounding spaces", () => {
    expect(findInBlocks(blocks, columns, "  LONDON ")).toEqual([{ row: 0, col: 1 }])
  })

  it("never matches the NULL token", () => {
    expect(findInBlocks(blocks, columns, "null")).toEqual([])
  })

  it("stops at the match limit", () => {
    const many = [{ start: 0, count: 2000, columns: [Array.from({ length: 2000 }, () => "x")] }]
    expect(findInBlocks(many, laidOut(["x"]), "x")).toHaveLength(MAX_MATCHES)
  })
})
