import { useMemo, useState } from "react"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import type { BlockLoader } from "./block-loader"
import type { Cell } from "./grid-navigation"
import { findInBlocks } from "./loaded-rows"
import type { LaidOutColumn } from "./use-grid-columns"

export interface GridFind {
  query: string
  setQuery: (query: string) => void
  matches: Cell[]
  /** The query has changed and its search has not run yet. */
  searching: boolean
  /** Which match the cursor is on. */
  index: number
  /** Moves to the next (`1`) or previous (`-1`) match, wrapping round. */
  step: (delta: 1 | -1) => void
  /** `row:col` of every match, for highlighting. */
  keys: ReadonlySet<string>
}

/** How long typing, or blocks landing, must pause before the loaded rows are searched again. */
export const FIND_DELAY_MS = 150

/**
 * Find over the loaded rows. Re-run as blocks land (`version`), so a match
 * scrolled into view appears without searching again — but only once typing
 * and scrolling pause: a search walks every loaded row on the main thread,
 * and doing that per keystroke, or per block landing mid-scroll, would drop
 * frames.
 */
export function useGridFind(
  loader: BlockLoader,
  columns: readonly LaidOutColumn[],
  version: number,
  goTo: (cell: Cell) => void,
): GridFind {
  const [query, setQueryState] = useState("")
  const [index, setIndex] = useState(0)
  const searched = useDebouncedValue(query.trim(), FIND_DELAY_MS)
  const loaded = useDebouncedValue(version, FIND_DELAY_MS)
  const matches = useMemo(
    () => (searched === "" ? [] : findInBlocks(loader.loadedBlocks(), columns, searched)),
    // `loaded` is the loader's change signal, settled: the blocks it holds moved.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [loader, columns, searched, loaded],
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
    searching: query.trim() !== searched,
    index: Math.min(index, Math.max(matches.length - 1, 0)),
    step,
    keys,
  }
}
