/**
 * Gives jsdom a layout: every element reports this size, inside and out.
 * jsdom lays nothing out, so without it the grid measures a zero viewport
 * and the column virtualizer a zero-wide scroller. Restored after each test
 * by the suite's `restoreMocks`.
 *
 * jsdom has no `Element.scrollTo` either; the grid scrolls through it, so it
 * gets the obvious one — set the offsets, fire nothing, as jsdom does for a
 * `scrollTop` assignment.
 */
export function stubLayout({
  width = 960,
  height = 480,
}: { width?: number; height?: number } = {}): void {
  if (!("scrollTo" in Element.prototype)) {
    Object.defineProperty(Element.prototype, "scrollTo", {
      configurable: true,
      value(this: Element, options?: ScrollToOptions) {
        if (options?.top !== undefined) this.scrollTop = options.top
        if (options?.left !== undefined) this.scrollLeft = options.left
      },
    })
  }
  const sizes = {
    clientWidth: width,
    offsetWidth: width,
    clientHeight: height,
    offsetHeight: height,
  }
  for (const [property, value] of Object.entries(sizes)) {
    vi.spyOn(HTMLElement.prototype, property as keyof typeof sizes, "get").mockReturnValue(value)
  }
}
