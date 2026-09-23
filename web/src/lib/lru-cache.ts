/**
 * Insertion-ordered LRU bounded by *cost*, not entry count — because
 * consumers' values have wildly different weights per entry: the highlight
 * kernel caches HTML strings (cost: characters) and token arrays (cost:
 * token count), the log formatter caches pretty-printed documents, the data
 * grid caches decoded row blocks (cost: estimated bytes). An entry-count bound
 * sized for one is either useless or ruinous for another, and a size
 * *refusal* would deny caching to exactly the large values whose
 * recomputation is most expensive. By default one entry may take at most a
 * quarter of the budget, so a single pathological value cannot evict
 * everything else; `maxEntryShare: Infinity` lifts that for a cache whose entries
 * must be kept however large (the rows on screen).
 *
 * `isPinned` names entries eviction must skip — the grid's on-screen blocks.
 * Pinned entries still count against the budget, which a cache of nothing
 * but pinned entries may therefore exceed.
 */
export interface LruCacheOptions<K> {
  /** Largest share of the budget one entry may take; bigger ones are not stored. */
  maxEntryShare?: number
  /** Entries eviction leaves alone. */
  isPinned?: (key: K) => boolean
}

export class LruCache<V, K = string> {
  private readonly entries = new Map<K, { value: V; cost: number }>()
  private total = 0
  private readonly budget: number
  private readonly maxEntryCost: number
  private readonly isPinned: (key: K) => boolean

  constructor(
    budget: number,
    { maxEntryShare = 1 / 4, isPinned = () => false }: LruCacheOptions<K> = {},
  ) {
    this.budget = budget
    this.maxEntryCost = budget * maxEntryShare
    this.isPinned = isPinned
  }

  /** The summed cost of every entry. */
  get cost(): number {
    return this.total
  }

  /** Re-inserts on hit so the map's insertion order is least-recent first. */
  get(key: K): V | undefined {
    const entry = this.entries.get(key)
    if (entry === undefined) return undefined
    this.entries.delete(key)
    this.entries.set(key, entry)
    return entry.value
  }

  /** Reads without touching recency — for a scan that must not reorder the cache. */
  peek(key: K): V | undefined {
    return this.entries.get(key)?.value
  }

  /** Every key, least recently used first. */
  keys(): K[] {
    return [...this.entries.keys()]
  }

  put(key: K, value: V, cost: number): void {
    if (cost > this.maxEntryCost) return
    this.delete(key)
    this.entries.set(key, { value, cost })
    this.total += cost
    this.evict()
  }

  delete(key: K): void {
    const prior = this.entries.get(key)
    if (prior === undefined) return
    this.entries.delete(key)
    this.total -= prior.cost
  }

  /** Evicts least-recently-used, unpinned entries until the cache fits its budget. */
  evict(): void {
    for (const [key, entry] of this.entries) {
      if (this.total <= this.budget) return
      if (this.isPinned(key)) continue
      this.entries.delete(key)
      this.total -= entry.cost
    }
  }
}
