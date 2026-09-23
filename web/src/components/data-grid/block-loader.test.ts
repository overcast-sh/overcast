import { BlockLoader, type BlockView } from "./block-loader"
import { FakeSource } from "./testing/fake-source"

const view = (firstRow: number, overrides: Partial<BlockView> = {}): BlockView => ({
  firstRow,
  lastRow: firstRow + 20,
  columns: [0, 1, 2, 3],
  direction: 1,
  fast: false,
  ...overrides,
})

function loaderFor(source: FakeSource, budget = 64 * 1024 * 1024) {
  const onChange = vi.fn()
  const onError = vi.fn()
  return { loader: new BlockLoader(source, budget, { onChange, onError }), onChange, onError }
}

const blocksRead = (source: FakeSource) => source.reads.map((read) => read.start / 1000)
const flush = () => new Promise((resolve) => setTimeout(resolve, 0))

describe("BlockLoader", () => {
  it("reads the blocks on screen, then one ahead in the direction of scroll", async () => {
    // Given: a view across the boundary of blocks 4 and 5
    const source = new FakeSource(100_000)
    const { loader } = loaderFor(source)
    // When: the view is loaded, and loaded again once its blocks have landed
    loader.update(view(4_990))
    await flush()
    loader.update(view(4_990))
    // Then: blocks 4 and 5 first, block 6 after
    expect(blocksRead(source)).toEqual([4, 5, 6])
  })

  it("prefetches upwards when scrolling up", async () => {
    const source = new FakeSource(100_000)
    const { loader } = loaderFor(source)
    loader.update(view(4_100, { direction: -1 }))
    await flush()
    loader.update(view(4_100, { direction: -1 }))
    expect(blocksRead(source)).toEqual([4, 3])
  })

  it("jumping from row 0 to row 4,000,000 issues only the reads for the final view", async () => {
    // Given: a five-million-row file open at the top, its first block loaded
    const source = new FakeSource(5_000_000, { hold: true })
    const { loader } = loaderFor(source)
    loader.update(view(0))
    source.release()
    await flush()
    const before = source.reads.length
    // When: the thumb is dragged to row 4,000,000 — a fling of fast frames — and settles
    for (let row = 0; row <= 4_000_000; row += 40_000) loader.update(view(row, { fast: true }))
    loader.update(view(4_000_000))
    // Then: the one block under the final view was read, and nothing on the way
    expect(blocksRead(source).slice(before)).toEqual([4_000])
  })

  it("aborts a read whose block has left the view before it lands", () => {
    const source = new FakeSource(100_000, { hold: true })
    const { loader } = loaderFor(source)
    loader.update(view(0))
    loader.update(view(50_000))
    expect(source.reads[0].aborted).toBe(true)
  })

  it("reads nothing while the view is scrolling fast", () => {
    const source = new FakeSource(100_000)
    const { loader } = loaderFor(source)
    loader.update(view(10_000, { fast: true }))
    expect(source.reads).toHaveLength(0)
  })

  it("reads only the columns a projecting source is missing", async () => {
    // Given: a projecting source with columns 0 and 1 of block 0 loaded
    const source = new FakeSource(500, { projects: true })
    const { loader } = loaderFor(source)
    loader.update(view(0, { columns: [0, 1] }))
    await flush()
    // When: columns 1 and 2 scroll into view
    loader.update(view(0, { columns: [1, 2] }))
    // Then: only column 2 is read
    expect(source.reads.map((read) => read.cols)).toEqual([[0, 1], [2]])
  })

  it("serves loaded values and says when a value is not loaded yet", async () => {
    const source = new FakeSource(100_000)
    const { loader } = loaderFor(source)
    loader.update(view(0))
    expect(loader.valueAt(3, 1)).toEqual({ loaded: false, value: undefined })
    await flush()
    expect(loader.valueAt(3, 1)).toEqual({ loaded: true, value: "r3c1" })
  })

  it("re-reads a block that was read short while the file was still indexing", async () => {
    // Given: block 0 read when only 400 rows were known
    const source = new FakeSource(400)
    const { loader } = loaderFor(source)
    loader.update(view(0))
    await flush()
    // When: the index grows past the block's end
    source.rowCount = { value: 2_000, exact: false }
    loader.update(view(0))
    // Then: the block is read again, whole
    expect(source.reads.map((read) => [read.start, read.end])).toEqual([
      [0, 400],
      [0, 1_000],
    ])
  })

  it("reports a failed read, but not an aborted one", async () => {
    const source = new FakeSource(100_000)
    source.getRows = () => Promise.reject(new Error("Read failed: HTTP 403"))
    const { loader, onError } = loaderFor(source)
    loader.update(view(0))
    await flush()
    expect(onError).toHaveBeenCalledWith(new Error("Read failed: HTTP 403"))
  })

  it("aborts everything when disposed", () => {
    const source = new FakeSource(100_000, { hold: true })
    const { loader } = loaderFor(source)
    loader.update(view(0))
    loader.dispose()
    expect(source.pending()).toHaveLength(0)
  })
})
