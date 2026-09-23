import { act, renderHook } from "@/test/render"
import type { DataColumn, RowBlock } from "@/lib/data-sources/row-source"
import { sampleColumnWidth } from "./cell-format"
import { rowNumberWidth, useGridColumns } from "./use-grid-columns"

const COLUMNS: DataColumn[] = [
  { name: "id", numeric: true },
  { name: "comment", numeric: false },
]

function render(columns = COLUMNS, sample?: RowBlock) {
  return renderHook(
    ({ sample: block }: { sample?: RowBlock }) =>
      useGridColumns(columns, { rowCount: 1000, sample: block }),
    { initialProps: { sample } },
  )
}

describe("rowNumberWidth", () => {
  it("fits the last row number with its thousands separators", () => {
    expect(rowNumberWidth(5_000_000)).toBeGreaterThan(rowNumberWidth(9_999))
    expect(rowNumberWidth(5_000_000) - rowNumberWidth(500_000)).toBeGreaterThan(
      rowNumberWidth(500_000) - rowNumberWidth(50_000),
    )
  })
})

describe("useGridColumns", () => {
  it("lays the columns out side by side after the pinned row numbers", () => {
    const { result } = render()
    const [first, second] = result.current.columns
    expect(result.current.rowNumberWidth).toBe(rowNumberWidth(1000))
    expect(second.start).toBe(first.width)
    expect(result.current.totalWidth).toBe(first.width + second.width)
  })

  it("hands back the same layout when nothing about the columns changed", () => {
    // The grid renders every scroll frame; a new layout each time would redo
    // everything downstream of it, Find included.
    const { result, rerender } = render()
    const first = result.current
    rerender({ sample: undefined })
    expect(result.current).toBe(first)
  })

  it("sizes a column from its first values once they arrive", () => {
    // Given: no rows yet, so widths come from the names
    const { result, rerender } = render()
    const before = result.current.columns[1].width
    // When: the first block arrives with a long comment
    const long = "a rather long comment that needs room"
    rerender({ sample: { start: 0, count: 1, columns: [[1], [long]] } })
    // Then: the comment column widens to it
    expect(result.current.columns[1].width).toBe(sampleColumnWidth(COLUMNS[1], [long]))
    expect(result.current.columns[1].width).toBeGreaterThan(before)
  })

  it("samples a column a projecting source delivers later, when it arrives", () => {
    const { result, rerender } = render()
    rerender({ sample: { start: 0, count: 1, columns: [[1]] } })
    const long = "arrived after the first column"
    rerender({ sample: { start: 0, count: 1, columns: [[1], [long]] } })
    expect(result.current.columns[1].width).toBe(sampleColumnWidth(COLUMNS[1], [long]))
  })

  it("hides a column from the layout, keeping its source index on the rest", () => {
    const { result } = render()
    act(() => result.current.visibility.toggle("0"))
    expect(result.current.columns.map((column) => [column.index, column.position])).toEqual([
      [1, 0],
    ])
    expect(result.current.visibility.hidden).toEqual(new Set(["0"]))
  })

  it("gives the header a second line when the format declares types", () => {
    const plain = render().result.current.headerHeight
    const typed = render([{ name: "id", type: "INT64", numeric: true }]).result.current.headerHeight
    expect(typed).toBeGreaterThan(plain)
  })
})
