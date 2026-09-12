/**
 * `ResizableSplit` — two panes with a handle between them that the reader
 * drags, or moves with the arrow keys once it has focus. One pane is the
 * *sized* one (a sidebar, a drawer) and the other takes what is left, so the
 * primitive works inside a row or column whose own size is not fixed.
 *
 * The handle is a WAI-ARIA `separator` with `aria-valuenow` in pixels, so a
 * screen reader hears the size as it changes; Home and End go to the
 * smallest and the largest, a double-click puts the default back. The size
 * is either controlled (`size` + `onSizeChange`) or kept by the split, and
 * remembered in `localStorage` under `storageKey` when one is given.
 *
 * No library: the repo had no resizable-panel primitive and the drag is a
 * dozen lines of pointer events.
 */
import {
  useCallback,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from "react"
import { cn } from "@/lib/utils"

export interface ResizableSplitProps {
  /**
   * `horizontal`: the panes sit side by side and the handle moves left and
   * right. `vertical`: they stack and the handle moves up and down.
   */
  direction: "horizontal" | "vertical"
  /** Which pane the size belongs to; the other flexes. Default `second`. */
  sized?: "first" | "second"
  /** The sized pane's size, in pixels, before the reader has moved it. */
  defaultSize: number
  minSize: number
  maxSize: number
  /** Controlled size; the drag then reports through `onSizeChange`. */
  size?: number
  /** Every size change, controlled or not. */
  onSizeChange?: (size: number) => void
  /** Remember the size in `localStorage` under this key. Uncontrolled only. */
  storageKey?: string
  /** The handle's accessible name — "Resize the debug panels". */
  label: string
  /** Pixels per arrow key. Shift multiplies by four. Default 16. */
  step?: number
  first: ReactNode
  second: ReactNode
  className?: string
  firstClassName?: string
  secondClassName?: string
}

function readStored(key: string | undefined): number | null {
  if (!key) return null
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return null
    const n = Number(JSON.parse(raw))
    return Number.isFinite(n) ? n : null
  } catch {
    return null
  }
}

/**
 * Keep the pointer's moves on the handle once a drag starts, so a fast drag
 * that leaves it still resizes. jsdom has no pointer capture, and a browser
 * refuses one for a pointer that is already gone; neither is worth an error.
 */
function capturePointer(handle: HTMLElement, pointerId: number, on: boolean): void {
  try {
    if (on) handle.setPointerCapture(pointerId)
    else handle.releasePointerCapture(pointerId)
  } catch {
    // No capture: the drag still follows moves over the handle.
  }
}

function writeStored(key: string | undefined, size: number | null): void {
  if (!key) return
  try {
    if (size === null) localStorage.removeItem(key)
    else localStorage.setItem(key, JSON.stringify(size))
  } catch {
    // A private window forgets; the split still resizes.
  }
}

export function ResizableSplit({
  direction,
  sized = "second",
  defaultSize,
  minSize,
  maxSize,
  size,
  onSizeChange,
  storageKey,
  label,
  step = 16,
  first,
  second,
  className,
  firstClassName,
  secondClassName,
}: ResizableSplitProps) {
  const clamp = useCallback(
    (n: number) => Math.round(Math.min(maxSize, Math.max(minSize, n))),
    [minSize, maxSize],
  )
  const [internal, setInternal] = useState(() => clamp(readStored(storageKey) ?? defaultSize))
  const controlled = size !== undefined
  const current = clamp(controlled ? size : internal)

  const update = useCallback(
    (next: number, { reset = false } = {}) => {
      const clamped = clamp(next)
      if (!controlled) {
        setInternal(clamped)
        writeStored(storageKey, reset ? null : clamped)
      }
      if (clamped !== current) onSizeChange?.(clamped)
    },
    [clamp, controlled, storageKey, onSizeChange, current],
  )

  // ── Drag ──
  const horizontal = direction === "horizontal"
  const drag = useRef<{ pointerId: number; start: number; size: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    capturePointer(e.currentTarget, e.pointerId, true)
    drag.current = { pointerId: e.pointerId, start: horizontal ? e.clientX : e.clientY, size: current }
    setDragging(true)
  }
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d || d.pointerId !== e.pointerId) return
    const delta = (horizontal ? e.clientX : e.clientY) - d.start
    // The handle moving right or down grows the first pane and shrinks the second.
    update(sized === "first" ? d.size + delta : d.size - delta)
  }
  const endDrag = (e: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.pointerId !== e.pointerId) return
    drag.current = null
    setDragging(false)
    capturePointer(e.currentTarget, e.pointerId, false)
  }

  // ── Keys ──
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const grow = horizontal ? "ArrowRight" : "ArrowDown"
    const shrink = horizontal ? "ArrowLeft" : "ArrowUp"
    const amount = e.shiftKey ? step * 4 : step
    let next: number
    if (e.key === grow || e.key === shrink) {
      const towardsSecond = e.key === grow
      next = current + (towardsSecond === (sized === "first") ? amount : -amount)
    } else if (e.key === "Home") next = minSize
    else if (e.key === "End") next = maxSize
    else return
    e.preventDefault()
    update(next)
  }

  const sizedStyle: CSSProperties = { flex: `0 0 ${current}px` }
  const flexStyle: CSSProperties = { flex: "1 1 auto" }
  const paneClass = "min-h-0 min-w-0"

  return (
    <div
      className={cn("flex", !horizontal && "flex-col", dragging && "select-none", className)}
      data-resizing={dragging || undefined}
    >
      <div
        className={cn(paneClass, firstClassName)}
        style={sized === "first" ? sizedStyle : flexStyle}
      >
        {first}
      </div>
      <div
        role="separator"
        tabIndex={0}
        aria-label={label}
        aria-orientation={horizontal ? "vertical" : "horizontal"}
        aria-valuenow={current}
        aria-valuemin={minSize}
        aria-valuemax={maxSize}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onKeyDown={onKeyDown}
        onDoubleClick={() => update(defaultSize, { reset: true })}
        title={`${label} — drag, or use the arrow keys; double-click to reset`}
        className={cn(
          "group/split flex shrink-0 touch-none items-center justify-center focus-visible:outline-none",
          horizontal ? "w-3 cursor-col-resize" : "h-3 cursor-row-resize",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "rounded-full bg-border transition-colors group-hover/split:bg-accent group-focus-visible/split:bg-accent",
            horizontal ? "h-8 w-0.5" : "h-0.5 w-8",
            dragging && "bg-accent",
          )}
        />
      </div>
      <div
        className={cn(paneClass, secondClassName)}
        style={sized === "second" ? sizedStyle : flexStyle}
      >
        {second}
      </div>
    </div>
  )
}
