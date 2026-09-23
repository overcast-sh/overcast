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

/** Every column of rows `[start, end)`, with a signal nobody aborts. */
export function readRows(source: RowSource, start: number, end: number): Promise<RowBlock> {
  return source.getRows(start, end, [], new AbortController().signal)
}

/** One turn of the event loop: long enough for a posted message to be handled. */
export function nextTurn(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}
