import type { RowBlock } from "./row-source"

/**
 * The decoded rows a grid holds: an LRU of blocks, capped by bytes.
 *
 * Capped by bytes rather than by rows because rows are not a unit of memory —
 * a thousand rows of three numbers and a thousand rows of forty free-text
 * columns differ by a factor of hundreds. The size of each block is an
 * estimate (strings at two bytes a character plus an object header, numbers
 * at eight), which is what the budget needs: an upper bound that tracks the
 * data, not a heap profile.
 *
 * Blocks the grid is showing are pinned while it shows them, so eviction can
 * never take away a row on screen; everything else goes least-recently-used
 * first.
 */

/** The default budget on a capable desktop. */
export const DEFAULT_CACHE_BYTES = 64 * 1024 * 1024

/**
 * The budget for this device: 64 MB, halved when `navigator.deviceMemory`
 * reports 4 GB or less (a Chromium-only signal; elsewhere nothing is assumed),
 * and halved again when the primary pointer is coarse — a tablet or a phone,
 * where the browser is the first thing the OS reclaims.
 */
export function cacheBudget(
  env: { deviceMemory?: number; coarsePointer?: boolean } = deviceEnvironment(),
): number {
  let budget = DEFAULT_CACHE_BYTES
  if (env.deviceMemory !== undefined && env.deviceMemory <= 4) budget /= 2
  if (env.coarsePointer) budget /= 2
  return budget
}

function deviceEnvironment(): { deviceMemory?: number; coarsePointer?: boolean } {
  if (typeof navigator === "undefined") return {}
  const deviceMemory = (navigator as Navigator & { deviceMemory?: number }).deviceMemory
  const coarsePointer =
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia("(pointer: coarse)").matches
      : false
  return { deviceMemory, coarsePointer }
}

/** Estimated bytes one block holds. */
export function blockBytes(block: RowBlock): number {
  let bytes = 64
  for (const column of block.columns) {
    if (!column) continue
    bytes += 16
    if (ArrayBuffer.isView(column)) {
      bytes += (column as unknown as ArrayBufferView).byteLength
      continue
    }
    for (let i = 0; i < column.length; i++) bytes += valueBytes(column[i])
  }
  return bytes
}

function valueBytes(value: unknown): number {
  if (typeof value === "string") return 16 + value.length * 2
  if (typeof value === "number" || typeof value === "boolean") return 8
  if (value === null || value === undefined) return 8
  if (typeof value === "bigint") return 24
  if (value instanceof Uint8Array) return 32 + value.byteLength
  if (value instanceof Date) return 32
  // Arrays and structs: their JSON length is a fair proxy for their size.
  try {
    return 32 + JSON.stringify(value, (_k, v: unknown) => (typeof v === "bigint" ? 0 : v)).length * 2
  } catch {
    return 64
  }
}

interface Entry {
  block: RowBlock
  bytes: number
}

export class BlockCache {
  private readonly entries = new Map<number, Entry>()
  private total = 0
  private pinned = new Set<number>()
  readonly budget: number

  constructor(budget: number = cacheBudget()) {
    this.budget = budget
  }

  get bytes(): number {
    return this.total
  }

  get size(): number {
    return this.entries.size
  }

  has(index: number): boolean {
    return this.entries.has(index)
  }

  /** Reads a block and marks it most recently used. */
  get(index: number): RowBlock | undefined {
    const entry = this.entries.get(index)
    if (!entry) return undefined
    this.entries.delete(index)
    this.entries.set(index, entry)
    return entry.block
  }

  /** Reads without touching recency — for Find, which must not reorder the cache. */
  peek(index: number): RowBlock | undefined {
    return this.entries.get(index)?.block
  }

  /**
   * Stores a block — merged with what is cached for it already, so a
   * projecting source can fill in columns as they scroll into view.
   */
  set(index: number, block: RowBlock): void {
    const existing = this.entries.get(index)
    let merged = block
    if (existing && existing.block.count === block.count) {
      const columns = existing.block.columns.slice()
      block.columns.forEach((column, i) => {
        if (column) columns[i] = column
      })
      merged = { ...block, columns }
      this.total -= existing.bytes
      this.entries.delete(index)
    } else if (existing) {
      this.total -= existing.bytes
      this.entries.delete(index)
    }
    const bytes = blockBytes(merged)
    this.entries.set(index, { block: merged, bytes })
    this.total += bytes
    this.evict()
  }

  /** The blocks on screen: never evicted while pinned. */
  pin(indices: Iterable<number>): void {
    this.pinned = new Set(indices)
    this.evict()
  }

  /** Every cached block, oldest first. */
  indices(): number[] {
    return [...this.entries.keys()]
  }

  clear(): void {
    this.entries.clear()
    this.total = 0
  }

  private evict(): void {
    if (this.total <= this.budget) return
    for (const [index, entry] of this.entries) {
      if (this.total <= this.budget) break
      if (this.pinned.has(index)) continue
      this.entries.delete(index)
      this.total -= entry.bytes
    }
  }
}
