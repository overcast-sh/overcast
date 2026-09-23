import { useCallback, useSyncExternalStore } from "react"
import type { IndexingStatus, RowCount, RowSource } from "@/lib/data-sources/row-source"

export interface SourceStatus {
  rowCount: RowCount
  indexing?: IndexingStatus
  changed: boolean
}

/**
 * A row source's live status — its row count, its indexing progress and
 * whether the object changed under it — re-read whenever the source says so.
 * The snapshot is a string, so an unchanged status is equal and does not
 * re-render.
 */
export function useSourceStatus(source: RowSource): SourceStatus {
  const subscribe = useCallback((listener: () => void) => source.subscribe(listener), [source])
  const snapshot = useCallback(() => {
    const { rowCount, indexing, changed } = source
    return [
      rowCount.value,
      rowCount.exact,
      indexing?.state,
      indexing?.rows,
      indexing?.bytes,
      indexing?.error,
      changed,
    ].join("|")
  }, [source])
  useSyncExternalStore(subscribe, snapshot, snapshot)
  return { rowCount: source.rowCount, indexing: source.indexing, changed: !!source.changed }
}
