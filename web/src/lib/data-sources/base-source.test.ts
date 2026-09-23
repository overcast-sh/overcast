import { sliceBlock } from "./base-source"

describe("sliceBlock", () => {
  const block = { start: 1000, count: 4, columns: [["a", "b", "c", "d"], undefined] }

  it("returns the block itself when the range covers it exactly", () => {
    expect(sliceBlock(block, 1000, 1004)).toBe(block)
  })

  it("cuts the rows asked for out of a block that starts earlier", () => {
    expect(sliceBlock(block, 1001, 1003)).toEqual({
      start: 1001,
      count: 2,
      columns: [["b", "c"], undefined],
    })
  })

  it("stops at the block's end when the range runs past it", () => {
    expect(sliceBlock(block, 1002, 2000)).toMatchObject({
      count: 2,
      columns: [["c", "d"], undefined],
    })
  })
})
