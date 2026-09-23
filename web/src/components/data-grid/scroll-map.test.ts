import {
  MAX_SCROLL_PX,
  columnWindow,
  isMapped,
  rowAt,
  scrollHeight,
  scrollTopFor,
} from "./scroll-map"

const ROW = 28
const VIEW = 560 // 20 rows

describe("scroll mapping below the height cap", () => {
  it("is plain pixel arithmetic", () => {
    expect(isMapped(1000, ROW)).toBe(false)
    expect(scrollHeight(1000, ROW)).toBe(28_000)
    expect(rowAt(28 * 10 + 5, VIEW, 1000, ROW)).toEqual({ first: 10, offset: 5 })
    expect(scrollTopFor(10, VIEW, 1000, ROW)).toBe(280)
  })

  it("stops at the last screenful", () => {
    expect(scrollTopFor(999, VIEW, 1000, ROW)).toBe(28_000 - VIEW)
  })
})

describe("scroll mapping above the height cap", () => {
  const rows = 5_000_000 // 140M px unmapped — eight times Firefox's cap

  it("keeps the spacer under the cap", () => {
    expect(isMapped(rows, ROW)).toBe(true)
    expect(scrollHeight(rows, ROW)).toBe(MAX_SCROLL_PX)
  })

  it("maps the top of the scrollbar to row 0 and the bottom to the last screenful", () => {
    expect(rowAt(0, VIEW, rows, ROW).first).toBe(0)
    const bottom = rowAt(MAX_SCROLL_PX - VIEW, VIEW, rows, ROW)
    expect(bottom.first).toBe(rows - VIEW / ROW)
  })

  it("reaches every row: scrollTopFor and rowAt round-trip", () => {
    for (const row of [0, 1, 17, 12_345, 2_500_000, 4_999_000, rows - 20]) {
      const top = scrollTopFor(row, VIEW, rows, ROW)
      expect(top).toBeLessThanOrEqual(MAX_SCROLL_PX - VIEW)
      expect(rowAt(top, VIEW, rows, ROW).first, `row ${row}`).toBe(row)
    }
  })

  it("moves monotonically with the scroll position", () => {
    let previous = -1
    for (let top = 0; top <= MAX_SCROLL_PX - VIEW; top += 97_531) {
      const { first } = rowAt(top, VIEW, rows, ROW)
      expect(first).toBeGreaterThanOrEqual(previous)
      previous = first
    }
  })
})

describe("columnWindow", () => {
  const offsets = [0, 100, 200, 300, 400, 500]

  it("returns the columns intersecting the viewport, plus overscan", () => {
    expect(columnWindow(offsets, 0, 150, 0)).toEqual({ first: 0, last: 1 })
    expect(columnWindow(offsets, 250, 100, 1)).toEqual({ first: 1, last: 4 })
  })

  it("clamps at the edges", () => {
    expect(columnWindow(offsets, 450, 400, 1)).toEqual({ first: 3, last: 4 })
    expect(columnWindow([0], 0, 100)).toEqual({ first: 0, last: -1 })
  })
})
