import type { RowBlock } from "@/lib/data-sources/row-source"
import { columnBytes } from "@/lib/data-sources/value-bytes"
import { LruCache } from "@/lib/lru-cache"

/**
 * The decoded rows a grid holds: an LRU of row blocks, capped by bytes.
 *
 * Capped by bytes rather than by rows because rows are not a unit of memory —
 * a thousand rows of three numbers and a thousand rows of forty free-text
 * columns differ by a factor of hundreds. Sizes are the estimates of
 * `columnBytes`, kept per column so a column that lands into a block adds
 * only its own cost.
 *
 * The blocks on screen are pinned while they are, so eviction never takes
 * away a row being looked at; everything else goes least recently used
 * first. Keyed by block index: the grid remounts on a new source (a new ETag
 * is a new source), so a cache never holds two files' rows.
 */

/** Per block, beyond its columns: the object and its arrays. */
const BLOCK_OVERHEAD = 64

interface Entry {
  block: RowBlock
  /** Estimated bytes of each column held, by column index. */
  columnCosts: number[]
}

export class BlockCache {
  private readonly lru: LruCache<Entry, number>
  private pinned = new Set<number>()

  constructor(budget: number) {
    this.lru = new LruCache(budget, {
      // A block on screen is kept however large it is.
      maxEntryShare: Infinity,
      isPinned: (index) => this.pinned.has(index),
    })
  }

  /** Estimated bytes held. */
  get bytes(): number {
    return this.lru.cost
  }

  /** Reads a block and marks it most recently used. */
  get(index: number): RowBlock | undefined {
    return this.lru.get(index)?.block
  }

  /** Reads without touching recency — for Find, which must not reorder the cache. */
  peek(index: number): RowBlock | undefined {
    return this.lru.peek(index)?.block
  }

  /** Every cached block index, least recently used first. */
  indices(): number[] {
    return this.lru.keys()
  }

  /**
   * Stores a block — merged with what is cached for it already when it holds
   * the same rows, so a projecting source can fill in columns as they scroll
   * into view.
   */
  set(index: number, block: RowBlock): void {
    const existing = this.lru.peek(index)
    const base = existing && existing.block.count === block.count ? existing : undefined
    const columns = base ? base.block.columns.slice() : []
    const columnCosts = base ? base.columnCosts.slice() : []
    block.columns.forEach((values, i) => {
      if (!values) return
      columns[i] = values
      columnCosts[i] = columnBytes(values)
    })
    const cost = columnCosts.reduce((sum, bytes) => sum + bytes, BLOCK_OVERHEAD)
    this.lru.put(index, { block: { ...block, columns }, columnCosts }, cost)
  }

  /** The blocks on screen, which eviction must leave alone until the next call. */
  pin(indices: Iterable<number>): void {
    this.pinned = new Set(indices)
    this.lru.evict()
  }
}
