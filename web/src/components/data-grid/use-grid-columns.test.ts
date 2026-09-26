import { act, fireEvent, renderHook } from "@/test/render"
import type { DataColumn, RowBlock } from "@/lib/data-sources/row-source"
import { sampleColumnWidth } from "./cell-format"
import { rowNumberWidth, useGridColumns, type ColumnWidthsOptions } from "./use-grid-columns"

const COLUMNS: DataColumn[] = [
  { name: "id", numeric: true },
  { name: "comment", numeric: false },
]

function render(columns = COLUMNS, sample?: RowBlock, widths: ColumnWidthsOptions = {}) {
  return renderHook(
    ({ sample: block }: { sample?: RowBlock }) =>
      useGridColumns(columns, { rowCount: 1000, sample: block, ...widths }),
    { initialProps: { sample } },
  )
}

/** Drags a column's edge `by` px, as the header's resize handle would. */
function drag(onResizeStart: (event: unknown) => void, by: number) {
  act(() => onResizeStart(new MouseEvent("mousedown", { clientX: 100 })))
  act(() => {
    fireEvent.mouseMove(document, { clientX: 100 + by / 2 })
    fireEvent.mouseMove(document, { clientX: 100 + by })
  })
  act(() => {
    fireEvent.mouseUp(document, { clientX: 100 + by })
  })
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

  it("opens with the widths the caller kept, by name, over the sampled ones", () => {
    // Given: a width kept for "comment", and one for a column this result lacks
    const initialWidths = { comment: 300, gone: 90 }
    // When: the columns are laid out
    const { result } = render(COLUMNS, undefined, { initialWidths })
    // Then: "comment" opens at its kept width, and "id" is sampled as usual
    expect(result.current.columns.map((column) => column.width)).toEqual([
      sampleColumnWidth(COLUMNS[0], undefined),
      300,
    ])
  })

  it("clamps a kept width to the resize limits", () => {
    const { result } = render(COLUMNS, undefined, { initialWidths: { id: 1, comment: 1e6 } })
    expect(result.current.columns.map((column) => column.width)).toEqual([48, 1200])
  })

  it("reports the widths by name once a resize ends, not on every frame of it", () => {
    // Given: a caller keeping widths
    const onWidthsChange = vi.fn()
    const { result } = render(COLUMNS, undefined, {
      initialWidths: { comment: 200 },
      onWidthsChange,
    })
    const before = result.current.columns[0].width
    // When: the reader drags the id column 60 px wider
    drag(result.current.columns[0].onResizeStart, 60)
    // Then: one report, with the kept width and the new one
    expect(onWidthsChange).toHaveBeenCalledTimes(1)
    expect(onWidthsChange).toHaveBeenCalledWith({ id: before + 60, comment: 200 })
  })

  it("gives the header a second line when the format declares types", () => {
    const plain = render().result.current.headerHeight
    const typed = render([{ name: "id", type: "INT64", numeric: true }]).result.current.headerHeight
    expect(typed).toBeGreaterThan(plain)
  })
})
