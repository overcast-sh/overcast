import { withDeclaredColumns, type DeclaredColumns } from "./declared-columns"
import { memorySource } from "./memory-source"
import type { RowSource } from "./row-source"
import { readRows } from "./testing/source-helpers"

const TEXT = [
  { name: "id", numeric: true },
  { name: "tags", numeric: false },
]

const DECLARED: DeclaredColumns = {
  columns: [
    { name: "id", type: "bigint", numeric: true },
    { name: "tags", type: "array", numeric: false },
  ],
  value: (text, index) => (text === null ? null : index === 0 ? Number(text) : text.split("|")),
}

const textSource = () =>
  memorySource(TEXT, [
    ["1", "a|b"],
    [null, "c"],
  ])

describe("withDeclaredColumns", () => {
  it("replaces the columns and reads every value through the declaration", async () => {
    const source = withDeclaredColumns(textSource(), DECLARED)
    expect(source.columns).toBe(DECLARED.columns)
    expect((await readRows(source, 0, 2)).columns).toEqual([
      [1, null],
      [["a", "b"], ["c"]],
    ])
  })

  it("types a partial block as well as the whole one", async () => {
    // Given: a source that reports its first column before the block
    const inner: RowSource = Object.assign(textSource(), {
      getRows: (...[start, , , , onPartial]: Parameters<RowSource["getRows"]>) => {
        onPartial?.({ start, count: 1, columns: [["7"]] })
        return Promise.resolve({ start, count: 1, columns: [["7"], ["x"]] })
      },
    })
    const partials: unknown[] = []
    await withDeclaredColumns(inner, DECLARED).getRows(
      0,
      1,
      [0, 1],
      new AbortController().signal,
      (block) => partials.push(block.columns),
    )
    expect(partials).toEqual([[[7]]])
  })

  it("leaves a source alone when it is not as wide as the declaration", () => {
    const narrow = memorySource([{ name: "id", numeric: false }], [["1"]])
    expect(withDeclaredColumns(narrow, DECLARED)).toBe(narrow)
  })

  it("passes status, subscriptions, indexing and disposal through to the source", () => {
    // Given: a source that indexes
    const inner = textSource()
    const continueIndexing = vi.fn()
    const dispose = vi.spyOn(inner, "dispose")
    const indexing = { state: "paused-limit" as const, rows: 2, bytes: 10, totalBytes: 20 }
    Object.assign(inner, { continueIndexing, indexing })
    const source = withDeclaredColumns(inner, DECLARED)
    // Then: what the grid reads of it is the source's own
    expect(source.rowCount).toEqual({ value: 2, exact: true })
    expect(source.indexing).toBe(indexing)
    source.continueIndexing?.()
    expect(continueIndexing).toHaveBeenCalled()
    source.dispose()
    expect(dispose).toHaveBeenCalled()
  })

  it("offers no Continue for a source that does not index", () => {
    expect(withDeclaredColumns(textSource(), DECLARED)).toMatchObject({
      continueIndexing: undefined,
    })
  })
})
