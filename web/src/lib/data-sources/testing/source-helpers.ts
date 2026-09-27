import type { IndexingState, RowBlock, RowSource } from "../row-source"

/** Resolves once the source's index reaches one of `states` — by default, anything but running. */
export function indexSettled(
  source: RowSource,
  states: readonly IndexingState[] = ["done", "error", "paused-limit", "on-demand"],
): Promise<void> {
  return new Promise((resolve) => {
    const check = () => {
      if (source.indexing && states.includes(source.indexing.state)) {
        stop()
        resolve()
      }
    }
    const stop = source.subscribe(check)
    check()
  })
}

/**
 * Resolves once the index covers more than `rows` rows, or has stopped for good
 * (done, error or paused at its byte limit) — whichever comes first.
 *
 * On demand (`Save-Data`) the state reads `on-demand` both before and while the
 * index reads on, so `indexSettled` cannot tell a read-on apart from none; the
 * row count is the only thing that moves.
 */
export function indexPast(source: RowSource, rows: number): Promise<void> {
  return new Promise((resolve) => {
    const check = () => {
      const status = source.indexing
      if (!status) return
      if (status.rows > rows || ["done", "error", "paused-limit"].includes(status.state)) {
        stop()
        resolve()
      }
    }
    const stop = source.subscribe(check)
    check()
  })
}

/** Every column of rows `[start, end)`, with a signal nobody aborts. */
export function readRows(source: RowSource, start: number, end: number): Promise<RowBlock> {
  return source.getRows(start, end, [], new AbortController().signal)
}

/** One turn of the event loop: long enough for a posted message to be handled. */
export function nextTurn(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}
