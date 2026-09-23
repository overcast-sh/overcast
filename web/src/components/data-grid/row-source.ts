/**
 * The contract between `DataGrid` and whatever supplies its rows.
 *
 * The grid knows nothing about formats. It asks a row source for blocks of
 * rows, a column subset at a time, and renders what comes back; the source
 * decides how to get them — a byte range of a CSV between two index points,
 * a `rowStart`/`rowEnd` read of a Parquet file with a column projection, an
 * array already in memory. The design is *Large data in the browser* in
 * `docs/plans/data-lake-console.md`.
 *
 * Rows come back **columnar**: one array per requested column rather than one
 * object per row, which is what a Parquet reader produces anyway and saves the
 * per-row object churn a million-row scroll would otherwise make.
 */

export interface GridColumn {
  name: string
  /** Declared type, where the format has one — shown under the name. */
  type?: string
  /** Right-align: every value is a number. */
  numeric: boolean
  /** Digits after the point for a DECIMAL, which arrives as a double. */
  scale?: number
  /** A calendar day: rendered without a time of day. */
  dateOnly?: boolean
}

export interface RowCount {
  value: number
  /** False while the source is still counting (indexing a text file). */
  exact: boolean
}

export interface RowBlock {
  /** First row of the block. */
  start: number
  /** Rows in the block. */
  count: number
  /**
   * One array per column index. A column the caller did not ask for may be
   * absent — a projecting source (Parquet) reads only what is on screen.
   */
  columns: (ArrayLike<unknown> | undefined)[]
}

/** Where a text source's background indexing has got to. */
export interface IndexingStatus {
  state:
    | "running"
    /** Stopped at the per-run byte limit; `continueIndexing()` reads on. */
    | "paused-limit"
    /** `Save-Data` is on: the file is indexed only as far as it is scrolled. */
    | "on-demand"
    | "done"
    | "error"
  rows: number
  bytes: number
  totalBytes: number
  error?: string
}

export interface RowSource {
  readonly columns: readonly GridColumn[]
  readonly rowCount: RowCount
  /** Rows per block. Blocks start at multiples of it. */
  readonly blockSize: number
  /** Whether `getRows` honours its column list; if not, every block carries every column. */
  readonly projects: boolean
  /** Background indexing, for sources that index. */
  readonly indexing?: IndexingStatus
  /**
   * The object changed under the open file (its ETag moved): rows read from
   * here on may not match rows already on screen, so the grid says so.
   */
  readonly changed?: boolean
  /**
   * Rows `[start, end)` — never across a block boundary — with the given
   * columns. Rejects with an `AbortError` when `signal` aborts.
   *
   * `onPartial`, when the source can, is called with part of the block — one
   * column that has already decoded — before the whole block resolves, so a
   * slow column does not hold up the others.
   */
  getRows(
    start: number,
    end: number,
    cols: readonly number[],
    signal: AbortSignal,
    onPartial?: (block: RowBlock) => void,
  ): Promise<RowBlock>
  /** Called when the row count or indexing status changes. Returns an unsubscribe. */
  subscribe(listener: () => void): () => void
  /** Reads on past a paused index — the byte limit, or on-demand mode near its end. */
  continueIndexing?(): void
  /** Aborts everything in flight and releases the worker. The source is dead afterwards. */
  dispose(): void
}

export function abortError(): DOMException {
  return new DOMException("The operation was aborted.", "AbortError")
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === "AbortError"
    : error instanceof Error && error.name === "AbortError"
}
