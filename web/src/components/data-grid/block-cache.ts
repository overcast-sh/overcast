import type { RowBlock } from "@/lib/data-sources/row-source"
import { LruCache } from "@/lib/lru-cache"

/**
 * The decoded rows a grid holds: an LRU of row blocks, capped by bytes.
 *
 * Capped by bytes rather than by rows because rows are not a unit of memory —
 * a thousand rows of three numbers and a thousand rows of forty free-text
 * columns differ by a factor of hundreds. Each block's size is an estimate
 * (strings at two bytes a character plus a header, numbers at eight), which
 * is what a budget needs: an upper bound that tracks the data, not a heap
 * profile.
 *
 * The blocks on screen are pinned while they are, so eviction never takes
 * away a row being looked at; everything else goes least recently used
 * first. Keyed by block index: the grid remounts on a new source (a new ETag
 * is a new source), so a cache never holds two files' rows.
 */
export class BlockCache {
  private readonly lru: LruCache<RowBlock, number>
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
    return this.lru.get(index)
  }

  /** Reads without touching recency — for Find, which must not reorder the cache. */
  peek(index: number): RowBlock | undefined {
    return this.lru.peek(index)
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
    const merged =
      existing && existing.count === block.count ? mergeColumns(existing, block) : block
    this.lru.put(index, merged, blockBytes(merged))
  }

  /** The blocks on screen, which eviction must leave alone until the next call. */
  pin(indices: Iterable<number>): void {
    this.pinned = new Set(indices)
    this.lru.evict()
  }
}

function mergeColumns(existing: RowBlock, update: RowBlock): RowBlock {
  const columns = existing.columns.slice()
  update.columns.forEach((column, i) => {
    if (column) columns[i] = column
  })
  return { ...update, columns }
}

/** Estimated bytes one block holds. */
export function blockBytes(block: RowBlock): number {
  let bytes = 64
  for (const column of block.columns) {
    if (!column) continue
    bytes += 16
    if (ArrayBuffer.isView(column)) {
      bytes += column.byteLength
      continue
    }
    for (let i = 0; i < column.length; i++) bytes += valueBytes(column[i])
  }
  return bytes
}

function valueBytes(value: unknown): number {
  switch (typeof value) {
    case "string":
      return 16 + value.length * 2
    case "number":
    case "boolean":
    case "undefined":
      return 8
    case "bigint":
      return 24
  }
  if (value === null) return 8
  if (value instanceof Uint8Array) return 32 + value.byteLength
  if (value instanceof Date) return 32
  // Lists and structs: their JSON length is a fair proxy for their size.
  try {
    return (
      32 + JSON.stringify(value, (_key, v: unknown) => (typeof v === "bigint" ? 0 : v)).length * 2
    )
  } catch {
    return 64
  }
}
