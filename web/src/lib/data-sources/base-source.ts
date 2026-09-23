import type { DataColumn, IndexingStatus, RowBlock, RowCount, RowSource } from "./row-source"

/** Rows per block — and per index point, for text. */
export const BLOCK_ROWS = 1000

/**
 * What every row source shares: the subscription the grid re-reads its
 * count and status through, and the `changed` flag. Subclasses set the
 * fields and call `notify()`.
 */
export abstract class BaseSource implements RowSource {
  abstract readonly columns: readonly DataColumn[]
  abstract readonly projects: boolean
  readonly blockSize: number = BLOCK_ROWS
  rowCount: RowCount = { value: 0, exact: false }
  indexing?: IndexingStatus
  changed = false
  protected disposed = false
  private readonly listeners = new Set<() => void>()

  abstract getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
    onPartial?: (block: RowBlock) => void,
  ): Promise<RowBlock>

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  dispose(): void {
    this.disposed = true
    this.listeners.clear()
  }

  /** The object was overwritten while open. */
  protected markChanged(): void {
    if (this.changed || this.disposed) return
    this.changed = true
    this.notify()
  }

  protected notify(): void {
    for (const listener of this.listeners) listener()
  }
}

/** Rows `[start, end)` of a block that may begin before `start` or run past `end`. */
export function sliceBlock(block: RowBlock, start: number, end: number): RowBlock {
  const last = Math.min(end, block.start + block.count)
  if (block.start === start && block.start + block.count === last) return block
  const from = start - block.start
  const to = last - block.start
  return {
    start,
    count: Math.max(to - from, 0),
    columns: block.columns.map((c) => (c ? Array.prototype.slice.call(c, from, to) : c)),
  }
}
