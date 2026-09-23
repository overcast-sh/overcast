import { useLayoutEffect, useState, type RefObject } from "react"

export interface ElementSize {
  width: number
  height: number
}

/**
 * An element's content-box size (`clientWidth` × `clientHeight`: inside its
 * scrollbars), kept current by a `ResizeObserver`. Zero until the element is
 * laid out; measured before first paint, so a laid-out element never renders
 * a frame at zero.
 */
export function useElementSize(ref: RefObject<HTMLElement | null>): ElementSize {
  const [size, setSize] = useState<ElementSize>({ width: 0, height: 0 })
  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const measure = () =>
      setSize((previous) => {
        const { clientWidth: width, clientHeight: height } = element
        return previous.width === width && previous.height === height ? previous : { width, height }
      })
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
    // The element is created by the render that mounts the hook and never replaced.
  }, [ref])
  return size
}
