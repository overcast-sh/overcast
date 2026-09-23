import { useEffect, useState } from "react"
import { cacheBudget, deviceMemory } from "@/lib/data-sources/device-profile"
import type { RowSource } from "@/lib/data-sources/row-source"
import { useMediaQuery } from "@/hooks/use-media-query"
import { BlockLoader, type BlockView } from "./block-loader"

export interface RowBlocks {
  loader: BlockLoader
  /** Bumps whenever a block (or a column of one) lands: the grid's cue to re-render. */
  version: number
  /** The last read that failed for a reason other than an abort; cleared when one lands. */
  error: Error | null
}

/**
 * The loader of `source`'s blocks (see `BlockLoader`), with a decoded-row
 * budget for this device unless `budget` says otherwise. One loader per
 * source: the grid remounts on a new source, so a cache never outlives the
 * file it holds, and unmounting aborts every read.
 */
export function useRowBlocks(source: RowSource, budget?: number): RowBlocks {
  const coarsePointer = useMediaQuery("(pointer: coarse)")
  const [version, setVersion] = useState(0)
  const [error, setError] = useState<Error | null>(null)
  const [loader] = useState(() => {
    const bytes = budget ?? cacheBudget({ deviceMemory: deviceMemory(), coarsePointer })
    return new BlockLoader(source, bytes, {
      onChange: () => {
        setError(null)
        setVersion((v) => v + 1)
      },
      onError: setError,
    })
  })
  useEffect(() => () => loader.dispose(), [loader])
  return { loader, version, error }
}

/**
 * Keeps the loader's blocks in step with the view: re-run as the view moves,
 * as blocks land (`version` — so the block ahead is read once the view's own
 * are in) and as the row count grows under it while the file indexes.
 */
export function useBlockView(
  { loader, version }: RowBlocks,
  view: BlockView,
  rowCount: number,
): void {
  const { firstRow, lastRow, direction, fast } = view
  const columns = useStableNumbers(view.columns)
  useEffect(() => {
    loader.update({ firstRow, lastRow, direction, fast, columns })
  }, [loader, firstRow, lastRow, direction, fast, columns, rowCount, version])
}

/**
 * The same array for the same numbers, so a column window recomputed each
 * render only counts as a change when a column scrolls in or out.
 */
function useStableNumbers(values: readonly number[]): readonly number[] {
  const [stable, setStable] = useState(values)
  const same = stable.length === values.length && stable.every((value, i) => value === values[i])
  if (!same) setStable(values)
  return same ? stable : values
}
