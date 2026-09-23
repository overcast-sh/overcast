import { isAbortError, type RowBlock, type RowSource } from "@/lib/data-sources/row-source"
import { BlockCache } from "./block-cache"

/**
 * Keeps the blocks under the viewport loaded, and little else.
 *
 * Each `update` names what is on screen. The loader pins those blocks in the
 * cache, reads the ones missing (or missing a visible column, for a source
 * that projects), and once they are all there, **one block ahead in the
 * direction of scroll** — enough that a steady scroll finds the next block
 * already loaded.
 *
 * **Nothing is read while the view is `fast`** — the user is dragging the
 * scrollbar or has flung the grid. The rows flying past show skeletons, and
 * only the blocks where the scroll comes to rest are read: without that, a
 * drag from row 0 to row 4,000,000 would queue a read for every block it
 * crossed. A read whose block has left the view before it lands is aborted,
 * fast or not, so fast scrolling costs the reads for where the user stopped.
 */

export interface BlockView {
  firstRow: number
  lastRow: number
  /** Source indices of the columns on screen. */
  columns: readonly number[]
  direction: 1 | -1
  /** Scrolling too fast for reads to be worth starting. */
  fast: boolean
}

interface Read {
  controller: AbortController
  columns: ReadonlySet<number>
}

export class BlockLoader {
  readonly cache: BlockCache
  private readonly source: RowSource
  private readonly inflight = new Map<number, Read[]>()
  private readonly onChange: () => void
  private readonly onError: (error: Error) => void

  constructor(
    source: RowSource,
    budget: number,
    { onChange, onError }: { onChange: () => void; onError: (error: Error) => void },
  ) {
    this.source = source
    this.cache = new BlockCache(budget)
    this.onChange = onChange
    this.onError = onError
  }

  /** The value at a row and column, or `loaded: false` when its block is not here yet. */
  valueAt(row: number, column: number): { loaded: boolean; value: unknown } {
    const block = this.cache.peek(Math.floor(row / this.source.blockSize))
    const values = block?.columns[column]
    const i = row - (block?.start ?? 0)
    if (!block || !values || i >= block.count) return { loaded: false, value: undefined }
    return { loaded: true, value: values[i] }
  }

  /** Every block held, without touching their recency. */
  loadedBlocks(): RowBlock[] {
    return this.cache.indices().flatMap((index) => this.cache.peek(index) ?? [])
  }

  update(view: BlockView): void {
    const visible = this.visibleBlocks(view)
    const ahead = this.aheadBlock(visible, view.direction)
    this.cache.pin(visible)
    // Touch what is on screen, so least recently used means least recently seen.
    for (const index of visible) this.cache.get(index)
    this.abortExcept(new Set(ahead === undefined ? visible : [...visible, ahead]))
    if (view.fast) return
    let allLoaded = true
    for (const index of visible) {
      if (!this.load(index, view.columns)) allLoaded = false
    }
    if (allLoaded && ahead !== undefined) this.load(ahead, view.columns)
  }

  /** Aborts every read — the grid is unmounting. */
  dispose(): void {
    this.abortExcept(new Set())
  }

  private visibleBlocks({ firstRow, lastRow }: BlockView): number[] {
    const rowCount = this.source.rowCount.value
    if (rowCount === 0) return []
    const size = this.source.blockSize
    const first = Math.floor(Math.max(firstRow, 0) / size)
    const last = Math.floor(Math.min(Math.max(lastRow, 0), rowCount - 1) / size)
    return Array.from({ length: Math.max(last - first + 1, 0) }, (_, i) => first + i)
  }

  private aheadBlock(visible: readonly number[], direction: 1 | -1): number | undefined {
    if (visible.length === 0) return undefined
    const ahead = direction > 0 ? visible[visible.length - 1] + 1 : visible[0] - 1
    const blocks = Math.ceil(this.source.rowCount.value / this.source.blockSize)
    return ahead >= 0 && ahead < blocks ? ahead : undefined
  }

  private abortExcept(wanted: ReadonlySet<number>): void {
    for (const [index, reads] of this.inflight) {
      if (wanted.has(index)) continue
      for (const read of reads) read.controller.abort()
      this.inflight.delete(index)
    }
  }

  /** True when the block is loaded; otherwise starts whatever read it still needs. */
  private load(index: number, visibleColumns: readonly number[]): boolean {
    const needed = this.neededColumns(index, visibleColumns)
    if (needed === null) return true
    const reads = this.inflight.get(index) ?? []
    const covered = new Set(reads.flatMap((read) => [...read.columns]))
    // A non-projecting source reads whole blocks: one read covers everything.
    const pending = this.source.projects ? needed.filter((c) => !covered.has(c)) : needed
    if (reads.length === 0 || (this.source.projects && pending.length > 0)) {
      this.start(index, pending)
    }
    return false
  }

  /**
   * The columns a block still needs, or null when it has them all: every
   * visible column it lacks for a projecting source, or everything (`[]`)
   * when the block is missing or was read short while the file was indexing.
   */
  private neededColumns(index: number, visibleColumns: readonly number[]): number[] | null {
    const { blockSize, rowCount, projects } = this.source
    const start = index * blockSize
    const expected = Math.min(start + blockSize, rowCount.value) - start
    const cached = this.cache.peek(index)
    const whole = !cached || cached.count < expected
    if (!projects) return whole ? [] : null
    const missing = whole ? [...visibleColumns] : visibleColumns.filter((c) => !cached.columns[c])
    return missing.length > 0 ? missing : null
  }

  private start(index: number, columns: readonly number[]): void {
    const { blockSize, rowCount } = this.source
    const start = index * blockSize
    const end = Math.min(start + blockSize, rowCount.value)
    const read: Read = { controller: new AbortController(), columns: new Set(columns) }
    this.inflight.set(index, [...(this.inflight.get(index) ?? []), read])
    const live = () => !read.controller.signal.aborted
    const store = (block: RowBlock) => {
      if (!live()) return
      this.cache.set(index, block)
      this.onChange()
    }
    this.source.getRows(start, end, columns, read.controller.signal, store).then(
      (block) => {
        this.forget(index, read)
        store(block)
      },
      (reason: unknown) => {
        this.forget(index, read)
        if (!isAbortError(reason))
          this.onError(reason instanceof Error ? reason : new Error(String(reason)))
      },
    )
  }

  private forget(index: number, read: Read): void {
    const remaining = (this.inflight.get(index) ?? []).filter((r) => r !== read)
    if (remaining.length > 0) this.inflight.set(index, remaining)
    else this.inflight.delete(index)
  }
}
