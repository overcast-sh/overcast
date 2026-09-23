import { BaseSource } from "@/lib/data-sources/base-source"
import {
  abortError,
  type DataColumn,
  type IndexingStatus,
  type RowBlock,
} from "@/lib/data-sources/row-source"

export interface FakeRead {
  start: number
  end: number
  cols: readonly number[]
  aborted: boolean
}

/**
 * A row source of `rows` generated rows — cell text `r<row>c<col>` — that
 * records every read. Reads answer on the next microtask, or are held until
 * `release()` when `hold` is set, so a test can watch what gets aborted.
 */
export class FakeSource extends BaseSource {
  readonly columns: DataColumn[]
  readonly projects: boolean
  readonly reads: FakeRead[] = []
  private readonly hold: boolean
  private readonly held: (() => void)[] = []

  constructor(
    rows: number,
    {
      columns = 4,
      projects = false,
      hold = false,
      indexing,
    }: { columns?: number; projects?: boolean; hold?: boolean; indexing?: IndexingStatus } = {},
  ) {
    super()
    this.columns = Array.from({ length: columns }, (_, c) => ({ name: `col_${c}`, numeric: false }))
    this.projects = projects
    this.hold = hold
    this.indexing = indexing
    this.rowCount = { value: rows, exact: true }
  }

  continueIndexing = vi.fn()

  /** Moves the index on, as a text source's worker would. */
  setIndexing(indexing: IndexingStatus): void {
    this.indexing = indexing
    this.notify()
  }

  /** The object was overwritten under the open file, as a worker would report it. */
  overwrite(): void {
    this.markChanged()
  }

  /** Answers every read held so far. */
  release(): void {
    for (const answer of this.held.splice(0)) answer()
  }

  /** Reads still waiting for an answer, neither released nor aborted. */
  pending(): FakeRead[] {
    return this.reads.filter((read) => !read.aborted)
  }

  getRows(start: number, end: number, cols: readonly number[], signal: AbortSignal) {
    const read: FakeRead = { start, end, cols, aborted: false }
    this.reads.push(read)
    return new Promise<RowBlock>((resolve, reject) => {
      signal.addEventListener("abort", () => {
        read.aborted = true
        reject(abortError())
      })
      const answer = () => {
        if (signal.aborted) return
        const which = this.projects ? cols : this.columns.map((_, c) => c)
        const columns: string[][] = []
        for (const c of which) {
          columns[c] = Array.from({ length: end - start }, (_, i) => `r${start + i}c${c}`)
        }
        resolve({ start, count: end - start, columns })
      }
      if (this.hold) this.held.push(answer)
      else queueMicrotask(answer)
    })
  }
}
