/**
 * The cost-bounded LRU three caches lean on (highlight markup, token ranges,
 * formatted JSON documents). What matters is the *cost* accounting — entry
 * counts are meaningless when one consumer stores 40-char strings and another
 * stores 16k-object arrays.
 */
import { describe, expect, it } from "vitest"
import { LruCache } from "./lru-cache"

describe("LruCache", () => {
  it("returns the identical value on a hit", () => {
    const cache = new LruCache<number[]>(100)
    const value = [1, 2, 3]
    cache.put("k", value, 10)
    expect(cache.get("k")).toBe(value)
  })

  it("evicts least-recently-used entries to fit the budget", () => {
    // Costs stay at or under budget/4 — the quarter rule refuses anything
    // bigger outright (tested below), it never evicts to make room for it.
    const cache = new LruCache<string>(40)
    cache.put("a", "A", 10)
    cache.put("b", "B", 10)
    cache.put("c", "C", 10)
    cache.put("d", "D", 10) // budget exactly full
    cache.get("a") // freshen: b is now the oldest
    cache.put("e", "E", 10) // needs room — evicts b, not a
    expect(cache.get("a")).toBe("A")
    expect(cache.get("b")).toBeUndefined()
    expect(cache.get("e")).toBe("E")
  })

  it("refuses an entry costing more than a quarter of the budget", () => {
    const cache = new LruCache<string>(100)
    cache.put("giant", "G", 26)
    expect(cache.get("giant")).toBeUndefined()
  })

  it("replaces an existing key without double-counting its cost", () => {
    const cache = new LruCache<string>(20)
    cache.put("k", "v1", 5)
    cache.put("k", "v2", 5)
    cache.put("other", "O", 5)
    // 5 + 5 fits in 20 twice over; nothing should have been evicted.
    expect(cache.get("k")).toBe("v2")
    expect(cache.get("other")).toBe("O")
  })

  it("stores null and distinguishes it from a miss", () => {
    const cache = new LruCache<string | null>(100)
    cache.put("not-json", null, 1)
    expect(cache.get("not-json")).toBeNull()
    expect(cache.get("never-seen")).toBeUndefined()
  })

  it("stores an entry past a quarter of the budget when the share limit is lifted", () => {
    const cache = new LruCache<string>(100, { maxEntryShare: Infinity })
    cache.put("giant", "G", 90)
    expect(cache.get("giant")).toBe("G")
  })

  it("never evicts a pinned entry, even when it is the oldest", () => {
    // Given: a full cache whose oldest entry is pinned
    const cache = new LruCache<string, number>(20, {
      maxEntryShare: 1,
      isPinned: (key) => key === 1,
    })
    cache.put(1, "pinned", 10)
    cache.put(2, "B", 10)
    // When: another entry needs room
    cache.put(3, "C", 10)
    // Then: the next-oldest unpinned entry goes instead
    expect(cache.keys()).toEqual([1, 3])
  })

  it("reads an entry with peek without making it the most recent", () => {
    const cache = new LruCache<string>(100)
    cache.put("a", "A", 1)
    cache.put("b", "B", 1)
    cache.peek("a")
    expect(cache.keys()).toEqual(["a", "b"])
  })
})
