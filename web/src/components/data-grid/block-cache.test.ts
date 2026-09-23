import { BlockCache, DEFAULT_CACHE_BYTES, blockBytes, cacheBudget } from "./block-cache"
import type { RowBlock } from "./row-source"

const block = (start: number, text = "x".repeat(100), count = 10): RowBlock => ({
  start,
  count,
  columns: [Array.from({ length: count }, () => text)],
})

describe("cacheBudget", () => {
  it("is 64 MB on a capable desktop", () => {
    expect(cacheBudget({ deviceMemory: 16, coarsePointer: false })).toBe(DEFAULT_CACHE_BYTES)
    expect(cacheBudget({})).toBe(DEFAULT_CACHE_BYTES)
  })

  it("halves for 4 GB or less, and again for a coarse pointer", () => {
    expect(cacheBudget({ deviceMemory: 4 })).toBe(DEFAULT_CACHE_BYTES / 2)
    expect(cacheBudget({ deviceMemory: 2, coarsePointer: true })).toBe(DEFAULT_CACHE_BYTES / 4)
    expect(cacheBudget({ coarsePointer: true })).toBe(DEFAULT_CACHE_BYTES / 2)
  })
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
    const cache = new BlockCache(one * 2)
    cache.set(0, block(0))
    cache.pin([0])
    cache.set(1, block(10))
    cache.set(2, block(20))
    expect(cache.has(0)).toBe(true)
  })

  it("stays within its budget however many blocks pass through", () => {
    const cache = new BlockCache(one * 5)
    for (let i = 0; i < 1000; i++) {
      cache.pin([i])
      cache.set(i, block(i * 10))
    }
    expect(cache.size).toBeLessThanOrEqual(5)
    expect(cache.bytes).toBeLessThanOrEqual(one * 5)
  })

  it("merges the columns of a projecting source into one block", () => {
    const cache = new BlockCache()
    cache.set(0, { start: 0, count: 2, columns: [[1, 2]] })
    cache.set(0, { start: 0, count: 2, columns: [undefined, ["a", "b"]] })
    expect(cache.peek(0)?.columns).toEqual([[1, 2], ["a", "b"]])
  })

  it("replaces a block that has grown", () => {
    const cache = new BlockCache()
    cache.set(0, { start: 0, count: 1, columns: [[1]] })
    cache.set(0, { start: 0, count: 2, columns: [[1, 2]] })
    expect(cache.peek(0)?.count).toBe(2)
  })
})
