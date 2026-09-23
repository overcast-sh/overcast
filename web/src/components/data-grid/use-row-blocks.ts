import { useEffect, useRef, useState } from "react"
import { BlockCache } from "./block-cache"
import { isAbortError, type RowBlock, type RowSource } from "./row-source"

/**
 * Keeps the blocks under the viewport loaded, and nothing much else.
 *
 * Given the visible row range and the visible columns, it asks the source for
 * every block that is missing (or missing a visible column, for a projecting
 * source), then **one block ahead in the direction of scroll** — enough that a
 * steady scroll finds the next block already there.
 *
 * **Nothing is fetched while the grid is `paused`** — while the user drags
 * the scrollbar or flings (see `useScrollSettle`). The rows flying past show
 * static skeletons, and only the blocks where the scroll comes to rest are
 * read. Without that, a drag from row 0 to row 4,000,000 would queue a range
 * request for every block it crossed.
 *
 * A read whose block has left that set before it lands is aborted, so fast
 * scrolling costs the requests for where the user stopped, not for everywhere
 * they passed. A source that can hand back a block a column at a time
 * (`onPartial`) has each column painted as it arrives.
 *
 * The cache pins the visible blocks and evicts the rest least-recently-used
 * once it is over budget. One hook instance serves one source: `DataGrid`
 * remounts on a new source, so a cache can never hold another file's rows.
 */
export interface RowBlocks {
  cache: BlockCache
  /** Bumps whenever a block (or part of one) lands, so the grid re-renders. */
  version: number
  /** The last read failure that was not an abort. */
  error: Error | null
}

export function useRowBlocks(
  source: RowSource,
  firstRow: number,
  lastRow: number,
  columns: readonly number[],
  direction: 1 | -1,
  { budget, paused = false }: { budget?: number; paused?: boolean } = {},
): RowBlocks {
  const [cache] = useState(() => new BlockCache(budget))
  const [version, setVersion] = useState(0)
  const [error, setError] = useState<Error | null>(null)
  const inflight = useRef(new Map<number, { controller: AbortController; cols: Set<number> }>())

  const rowCount = source.rowCount.value
  const size = source.blockSize
  const firstBlock = Math.floor(Math.max(firstRow, 0) / size)
  const lastBlock = Math.floor(Math.max(Math.min(lastRow, rowCount - 1), 0) / size)
  const colsKey = columns.join(",")

  useEffect(() => {
    if (rowCount === 0) return
    const blockCount = Math.ceil(rowCount / size)
    const visible: number[] = []
    for (let b = firstBlock; b <= lastBlock && b < blockCount; b++) visible.push(b)
    const wanted = new Set(visible)
    const ahead = direction > 0 ? lastBlock + 1 : firstBlock - 1
    if (!paused && ahead >= 0 && ahead < blockCount) wanted.add(ahead)
    cache.pin(visible)
    // Touch what is on screen, so least-recently-used means least recently seen.
    for (const b of visible) cache.get(b)

    // Reads for blocks the viewport has left behind are abandoned — also
    // while paused, which is exactly when the viewport leaves them fastest.
    for (const [index, entry] of inflight.current) {
      if (!wanted.has(index)) {
        entry.controller.abort()
        inflight.current.delete(index)
      }
    }
    if (paused) return

    const wantedCols = source.projects ? columns : []
    // Visible first, then the block ahead: the order the reads are queued in.
    const order = [...visible, ...[...wanted].filter((b) => !visible.includes(b))]
    for (const index of order) {
      const start = index * size
      const end = Math.min(start + size, rowCount)
      const cached = cache.peek(index)
      const missing = wantedCols.filter((c) => !cached?.columns[c])
      // A block counted short while the file was still being indexed is
      // read again once the index says it holds more rows.
      const complete = !!cached && cached.count >= end - start
      if (complete && missing.length === 0) continue
      const running = inflight.current.get(index)
      if (running && (!source.projects || missing.every((c) => running.cols.has(c)))) continue
      running?.controller.abort()

      const cols = source.projects ? (complete ? missing : [...wantedCols]) : []
      const controller = new AbortController()
      inflight.current.set(index, { controller, cols: new Set(cols) })
      const current = () => inflight.current.get(index)?.controller === controller
      source
        .getRows(start, end, cols, controller.signal, (part: RowBlock) => {
          if (!current()) return
          cache.set(index, part)
          setVersion((v) => v + 1)
        })
        .then(
          (block: RowBlock) => {
            if (!current()) return
            inflight.current.delete(index)
            cache.set(index, block)
            setError(null)
            setVersion((v) => v + 1)
          },
          (reason: unknown) => {
            if (current()) inflight.current.delete(index)
            if (isAbortError(reason)) return
            setError(reason instanceof Error ? reason : new Error(String(reason)))
          },
        )
    }
    // `columns` is compared through its key: a new array with the same
    // columns is not a reason to re-read anything.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, cache, firstBlock, lastBlock, colsKey, direction, rowCount, size, paused])

  useEffect(() => {
    const map = inflight.current
    return () => {
      for (const { controller } of map.values()) controller.abort()
      map.clear()
    }
  }, [])

  return { cache, version, error }
}

/** Idle time after the last scroll event before the grid counts as settled. */
export const SETTLE_MS = 120

/**
 * Whether scrolling is fast enough that fetching would be wasted: the user is
 * dragging the scrollbar or has flung the list. Fast means moving more than a
 * screenful of rows in one scroll event; reading-speed scrolling (a wheel
 * notch, an arrow key) never pauses. The flag clears once the scroll has been
 * idle for `SETTLE_MS`.
 *
 * A jump the grid makes itself — Go to row, Home, End, a deep link — passes
 * `immediate`, and fetches at once: there is nothing in between to skip.
 */
export function useScrollSettle(): {
  paused: boolean
  onScrollRows: (firstRow: number, screenRows: number, immediate?: boolean) => void
} {
  const [paused, setPaused] = useState(false)
  const lastRow = useRef(0)
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => () => window.clearTimeout(timer.current), [])

  const onScrollRows = (firstRow: number, screenRows: number, immediate = false) => {
    const jump = Math.abs(firstRow - lastRow.current)
    lastRow.current = firstRow
    window.clearTimeout(timer.current)
    if (immediate || jump <= screenRows) {
      setPaused(false)
      return
    }
    setPaused(true)
    timer.current = window.setTimeout(() => setPaused(false), SETTLE_MS)
  }

  return { paused, onScrollRows }
}
