import type { RowBlock } from "@/lib/data-sources/row-source"
import { BlockCache } from "./block-cache"

const block = (start: number, text = "x".repeat(100), count = 10): RowBlock => ({
  start,
  count,
  columns: [Array.from({ length: count }, () => text)],
})

describe("BlockCache", () => {
  /** What one test block costs: measured, so the budgets below are in blocks. */
  const one = (() => {
    const probe = new BlockCache(Infinity)
    probe.set(0, block(0))
    return probe.bytes
  })()

  it("evicts the least recently used block once over budget", () => {
    const cache = new BlockCache(one * 3)
    cache.set(0, block(0))
    cache.set(1, block(10))
    cache.set(2, block(20))
    cache.get(0) // 0 is now the most recent
    cache.set(3, block(30))
    expect(cache.indices().sort()).toEqual([0, 2, 3])
    expect(cache.bytes).toBeLessThanOrEqual(one * 3)
  })

  it("never evicts a pinned block", () => {
    // Given: the oldest block is on screen
    const cache = new BlockCache(one * 2)
    cache.set(0, block(0))
    cache.pin([0])
    // When: more blocks arrive than fit
    cache.set(1, block(10))
    cache.set(2, block(20))
    // Then: the pinned block is still there
    expect(cache.peek(0)).toBeDefined()
  })

  it("keeps an on-screen block larger than the whole budget", () => {
    const cache = new BlockCache(10)
    cache.pin([0])
    cache.set(0, block(0))
    expect(cache.peek(0)).toBeDefined()
  })

  it("stays within its budget however many blocks pass through", () => {
    const cache = new BlockCache(one * 5)
    for (let i = 0; i < 1000; i++) {
      cache.pin([i])
      cache.set(i, block(i * 10))
    }
    expect(cache.indices().length).toBeLessThanOrEqual(5)
    expect(cache.bytes).toBeLessThanOrEqual(one * 5)
  })

  it("merges the columns of a projecting source into one block", () => {
    const cache = new BlockCache(1024 * 1024)
    cache.set(0, { start: 0, count: 2, columns: [[1, 2]] })
    cache.set(0, { start: 0, count: 2, columns: [undefined, ["a", "b"]] })
    expect(cache.peek(0)?.columns).toEqual([
      [1, 2],
      ["a", "b"],
    ])
  })

  it("replaces a block that has grown", () => {
    const cache = new BlockCache(1024 * 1024)
    cache.set(0, { start: 0, count: 1, columns: [[1]] })
    cache.set(0, { start: 0, count: 2, columns: [[1, 2]] })
    expect(cache.peek(0)?.count).toBe(2)
  })

  it("adds only a landing column's cost when it merges into a block", () => {
    // Given: a block holding one column
    const cache = new BlockCache(Infinity)
    cache.set(0, { start: 0, count: 2, columns: [["a", "b"]] })
    const before = cache.bytes
    // When: a second column lands
    cache.set(0, { start: 0, count: 2, columns: [undefined, [1, 2]] })
    // Then: the block grew by that column's cost alone
    expect(cache.bytes - before).toBe(16 + 2 * 8)
  })
})
