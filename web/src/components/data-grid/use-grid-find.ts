import { useMemo, useState } from "react"
import type { BlockLoader } from "./block-loader"
import type { Cell } from "./grid-navigation"
import { findInBlocks } from "./loaded-rows"
import type { LaidOutColumn } from "./use-grid-columns"

export interface GridFind {
  query: string
  setQuery: (query: string) => void
  matches: Cell[]
  /** Which match the cursor is on. */
  index: number
  /** Moves to the next (`1`) or previous (`-1`) match, wrapping round. */
  step: (delta: 1 | -1) => void
  /** `row:col` of every match, for highlighting. */
  keys: ReadonlySet<string>
}

/**
 * Find over the loaded rows. Re-run as blocks land (`version`), so a match
 * scrolled into view appears without searching again.
 */
export function useGridFind(
  loader: BlockLoader,
  columns: readonly LaidOutColumn[],
  version: number,
  goTo: (cell: Cell) => void,
): GridFind {
  const [query, setQueryState] = useState("")
  const [index, setIndex] = useState(0)
  const matches = useMemo(
    () => findInBlocks(loader.loadedBlocks(), columns, query),
    // `version` is the loader's change signal: the blocks it holds moved.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [loader, columns, query, version],
  )
  const keys = useMemo(() => new Set(matches.map(({ row, col }) => `${row}:${col}`)), [matches])

  const setQuery = (next: string) => {
    setQueryState(next)
    setIndex(0)
  }
  const step = (delta: 1 | -1) => {
    if (matches.length === 0) return
    const next = (index + delta + matches.length) % matches.length
    setIndex(next)
    goTo(matches[next])
  }
  return {
    query,
    setQuery,
    matches,
    index: Math.min(index, Math.max(matches.length - 1, 0)),
    step,
    keys,
  }
}
