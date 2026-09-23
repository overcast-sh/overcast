import type { RowBlock } from "@/lib/data-sources/row-source"
import { BlockCache, blockBytes } from "./block-cache"

const block = (start: number, text = "x".repeat(100), count = 10): RowBlock => ({
  start,
  count,
  columns: [Array.from({ length: count }, () => text)],
})

describe("blockBytes", () => {
  it("counts strings by length and numbers by width", () => {
    const strings = blockBytes(block(0, "x".repeat(1000)))
    const numbers = blockBytes({ start: 0, count: 10, columns: [new Array(10).fill(1)] })
    expect(strings).toBeGreaterThan(20_000)
    expect(numbers).toBeLessThan(200)
  })
})

describe("BlockCache", () => {
  const one = blockBytes(block(0))

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
})
