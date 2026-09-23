import { useEffect, useLayoutEffect, useRef, useState } from "react"
import type { RowSource } from "@/lib/data-sources/row-source"

export interface RowSourceState<S extends RowSource> {
  source: S | null
  error: Error | null
}

/**
 * Opens a row source for as long as the component is mounted with the same
 * `key`, and disposes of it — terminating its worker — on unmount or when the
 * key changes. An open still in flight when that happens is aborted, and a
 * source that resolves after it is disposed of on arrival, so a quickly
 * closed preview never leaves a worker behind.
 */
export function useRowSource<S extends RowSource>(
  key: string,
  open: (signal: AbortSignal) => Promise<S>,
): RowSourceState<S> {
  const [state, setState] = useState<RowSourceState<S> & { key: string }>({
    key,
    source: null,
    error: null,
  })
  const openRef = useRef(open)
  useLayoutEffect(() => {
    openRef.current = open
  })

  useEffect(() => {
    const controller = new AbortController()
    let opened: S | null = null
    openRef.current(controller.signal).then(
      (source) => {
        if (controller.signal.aborted) {
          source.dispose()
          return
        }
        opened = source
        setState({ key, source, error: null })
      },
      (error: unknown) => {
        if (controller.signal.aborted) return
        setState({
          key,
          source: null,
          error: error instanceof Error ? error : new Error(String(error)),
        })
      },
    )
    return () => {
      controller.abort()
      opened?.dispose()
    }
  }, [key])

  // A new key's first render must not show the previous key's source.
  return state.key === key ? state : { source: null, error: null }
}
