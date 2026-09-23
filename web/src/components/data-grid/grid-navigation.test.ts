import {
  cellForKey,
  clampCell,
  inSelection,
  leftToShowColumn,
  selectionOf,
  topToShowRow,
} from "./grid-navigation"

const bounds = { rowCount: 100, colCount: 5, pageRows: 20 }
const at = { row: 50, col: 2 }

describe("cellForKey", () => {
  it.each([
    ["ArrowDown", false, { row: 51, col: 2 }],
    ["ArrowUp", false, { row: 49, col: 2 }],
    ["ArrowRight", false, { row: 50, col: 3 }],
    ["ArrowLeft", false, { row: 50, col: 1 }],
    ["PageDown", false, { row: 70, col: 2 }],
    ["PageUp", false, { row: 30, col: 2 }],
    ["Home", false, { row: 50, col: 0 }],
    ["End", false, { row: 50, col: 4 }],
    ["Home", true, { row: 0, col: 2 }],
    ["End", true, { row: 99, col: 2 }],
  ])("moves %s (mod %s) to %o", (key, mod, expected) => {
    expect(cellForKey(key, at, mod, bounds)).toEqual(expected)
  })

  it("ignores a key that is not a move", () => {
    expect(cellForKey("a", at, false, bounds)).toBeNull()
  })
})

describe("clampCell", () => {
  it("keeps the cursor inside the grid", () => {
    expect(clampCell({ row: 120, col: -1 }, bounds)).toEqual({ row: 99, col: 0 })
  })
})

describe("selectionOf and inSelection", () => {
  const selection = selectionOf({ row: 2, col: 3 }, { row: 5, col: 1 })

  it("spans the rectangle between the anchor and the cursor, whichever way round", () => {
    expect(selection).toEqual({ top: 2, bottom: 5, left: 1, right: 3 })
  })

  it.each([
    [3, 2, true],
    [6, 2, false],
    [3, 4, false],
  ])("counts row %i, column %i as inside: %s", (row, col, expected) => {
    expect(inSelection(selection, row, col)).toBe(expected)
  })
})

describe("topToShowRow", () => {
  const view = { top: 280, viewport: 280, rowHeight: 28 }

  it.each([
    ["above the view goes to the top", 5, 140],
    ["below the view goes to the bottom", 25, 448],
    ["already in view stays put", 12, null],
  ])("a row %s", (_, row, expected) => {
    expect(topToShowRow(row, view)).toBe(expected)
  })
})

describe("leftToShowColumn", () => {
  const view = { left: 100, viewport: 300 }

  it.each([
    ["left of the view scrolls left to it", { start: 50, width: 80 }, 50],
    ["right of the view scrolls right to it", { start: 350, width: 100 }, 150],
    ["in view stays put", { start: 150, width: 100 }, null],
  ])("a column %s", (_, column, expected) => {
    expect(leftToShowColumn(column, view)).toBe(expected)
  })
})
