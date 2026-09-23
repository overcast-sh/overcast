import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react"
import {
  HybridScroll,
  logicalRange,
  probeHeightCap,
  ROW_HEIGHT,
  spacerHeight,
  type ScrollGeometry,
} from "./scroll-model"

/** Idle time after the last scroll frame before the grid counts as settled. */
export const SETTLE_MS = 120

export interface HybridScrollState {
  /** Logical scroll position: pixels into the unscaled rows. */
  top: number
  /** How far across the columns are scrolled — native, there is no cap across. */
  left: number
  /** Height to give the spacer: the rows, up to the browser's cap. */
  spacerHeight: number
  /** Which way the rows last moved — where to prefetch. */
  direction: 1 | -1
  /**
   * Moving more than a screenful a frame: a thumb drag or a fling. Rows are
   * not worth fetching until it slows or settles.
   */
  fast: boolean
  /** Brings a logical position into view at once: Go to row, the cursor, a deep link. */
  scrollToTop: (top: number) => void
  /** The scroller's `onScroll`. */
  onScroll: () => void
}

interface Position {
  top: number
  left: number
  direction: 1 | -1
  fast: boolean
}

/**
 * Binds a `HybridScroll` to a scroller element: reads its scroll events a
 * frame at a time — both axes, so the grid re-renders once a frame whichever
 * way it moved — applies the corrections the model asks for, re-centres the
 * thumb when the scroll settles, and tells the grid how fast it is moving.
 */
export function useHybridScroll(
  scroller: RefObject<HTMLElement | null>,
  { rowCount, viewport }: { rowCount: number; viewport: number },
): HybridScrollState {
  const [cap] = useState(() => probeHeightCap())
  const geometry = useMemo<ScrollGeometry>(
    () => ({ contentHeight: rowCount * ROW_HEIGHT, viewport, cap }),
    [rowCount, viewport, cap],
  )
  const [model] = useState(() => new HybridScroll(geometry))
  const [position, setPosition] = useState<Position>({
    top: 0,
    left: 0,
    direction: 1,
    fast: false,
  })
  const frame = useRef(0)
  const settleTimer = useRef<number | undefined>(undefined)

  const moveNative = useCallback(
    (scrollTop: number | undefined) => {
      if (scrollTop !== undefined) scroller.current?.scrollTo({ top: scrollTop })
    },
    [scroller],
  )

  // A resize, or rows arriving from the indexer: the logical position holds,
  // and a resting thumb moves to match. A thumb mid-scroll is left alone —
  // setting scrollTop would cut short the browser's own smooth scrolling.
  useLayoutEffect(() => {
    model.setGeometry(geometry)
    if (settleTimer.current === undefined) moveNative(model.settle())
  }, [model, geometry, moveNative])

  useEffect(
    () => () => {
      cancelAnimationFrame(frame.current)
      window.clearTimeout(settleTimer.current)
    },
    [],
  )

  const settle = useCallback(() => {
    settleTimer.current = undefined
    moveNative(model.settle())
    setPosition((previous) => (previous.fast ? { ...previous, fast: false } : previous))
  }, [model, moveNative])

  const onScroll = useCallback(() => {
    cancelAnimationFrame(frame.current)
    frame.current = requestAnimationFrame(() => {
      const element = scroller.current
      if (!element) return
      const before = model.logical
      moveNative(model.scrolled(element.scrollTop))
      const moved = model.logical - before
      setPosition((previous) => ({
        top: model.logical,
        left: element.scrollLeft,
        direction: moved === 0 ? previous.direction : moved > 0 ? 1 : -1,
        fast: Math.abs(moved) > viewport,
      }))
      window.clearTimeout(settleTimer.current)
      settleTimer.current = window.setTimeout(settle, SETTLE_MS)
    })
  }, [model, moveNative, scroller, settle, viewport])

  const scrollToTop = useCallback(
    (top: number) => {
      moveNative(model.scrollTo(top))
      setPosition((previous) => ({
        ...previous,
        top: model.logical,
        direction: model.logical >= previous.top ? 1 : -1,
        fast: false,
      }))
    },
    [model, moveNative],
  )

  return {
    // Clamped here rather than in state: a taller viewport or a shorter file
    // moves the last screenful up without a render of its own.
    top: Math.min(position.top, logicalRange(geometry)),
    left: position.left,
    spacerHeight: spacerHeight(geometry),
    direction: position.direction,
    fast: position.fast,
    scrollToTop,
    onScroll,
  }
}
