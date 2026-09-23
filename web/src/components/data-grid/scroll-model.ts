/**
 * Scrolling millions of rows past the browser's cap on how tall an element
 * can be — *hybrid scrolling* (decision on #2121).
 *
 * A virtual list's spacer wants `rowCount × rowHeight` pixels. Engines stop
 * laying an element out somewhere between about 17 and 34 million pixels
 * (it varies by engine and version), so at 28 px a row a naive spacer breaks
 * past a few hundred thousand rows. The spacer is therefore capped, and the
 * grid keeps its own **logical position** — pixels into the unscaled rows —
 * which is what it renders from.
 *
 * Scaling every scroll delta by `content / cap` would make one wheel notch
 * jump dozens of rows at five million. Instead:
 *
 * - **small deltas** (wheel, trackpad, touch, keyboard) move the logical
 *   position 1:1, so a notch moves the same rows at a thousand rows as at
 *   five million, and momentum, touch inertia and overlay scrollbars stay
 *   the browser's own;
 * - **large jumps** (dragging the thumb, clicking the track) map
 *   proportionally: half-way down the track is half-way through the rows;
 * - the native `scrollTop` is **re-centred quietly** — when the scroll comes
 *   to rest, so the thumb shows where the rows are, and at once if fine
 *   scrolling has walked it to within reach of either end, where the next
 *   notch would have nowhere to go.
 *
 * The map between the two keeps the first and last `edge` pixels 1:1, so the
 * top and bottom rows are reached exactly by scrolling, and is linear in
 * between. Pure, so every boundary is testable without layout.
 */

/** Height of one row, in px. Fixed: a long value truncates and opens in the inspector. */
export const ROW_HEIGHT = 28

/** The tallest spacer used whatever the engine allows — and the answer where it cannot be measured. */
export const SAFE_HEIGHT_CAP = 10_000_000

/** How many viewports at either end of the track scroll 1:1. */
const EDGE_VIEWPORTS = 4

export interface ScrollGeometry {
  /** The rows' unscaled height: `rowCount × rowHeight`. */
  contentHeight: number
  /** Height of the rows' viewport. */
  viewport: number
  /** The tallest spacer this browser lays out. */
  cap: number
}

/**
 * The tallest element this engine lays out, measured once: a probe asks for
 * a billion pixels and reads back what it got. Capped at `SAFE_HEIGHT_CAP`,
 * which is also the answer when there is no layout to ask (a test runner).
 */
export function probeHeightCap(doc: Document = document): number {
  const probe = doc.createElement("div")
  probe.style.cssText = "position:absolute;visibility:hidden;width:1px;height:1000000000px"
  doc.body.appendChild(probe)
  const measured = probe.getBoundingClientRect().height
  probe.remove()
  return measured > 0 ? Math.min(measured, SAFE_HEIGHT_CAP) : SAFE_HEIGHT_CAP
}

/** The spacer's height: the rows, up to the cap. */
export function spacerHeight({ contentHeight, cap }: ScrollGeometry): number {
  return Math.min(contentHeight, cap)
}

/** The furthest the logical position goes: the last screenful at the bottom. */
export function logicalRange({ contentHeight, viewport }: ScrollGeometry): number {
  return Math.max(contentHeight - viewport, 0)
}

function nativeRange(geometry: ScrollGeometry): number {
  return Math.max(spacerHeight(geometry) - geometry.viewport, 0)
}

function isScaled(geometry: ScrollGeometry): boolean {
  return geometry.contentHeight > geometry.cap
}

function edge(geometry: ScrollGeometry): number {
  return Math.min(nativeRange(geometry) / 4, EDGE_VIEWPORTS * geometry.viewport)
}

/** A delta bigger than this in one frame is a thumb drag or a track click, not a scroll. */
function jumpThreshold({ viewport }: ScrollGeometry): number {
  return Math.max(2 * viewport, 2000)
}

const clamp = (value: number, max: number) => Math.min(Math.max(value, 0), max)

/** The `scrollTop` that shows a logical position: 1:1 at the ends, proportional between. */
export function nativeFor(logical: number, geometry: ScrollGeometry): number {
  const L = logicalRange(geometry)
  const R = nativeRange(geometry)
  const at = clamp(logical, L)
  if (!isScaled(geometry)) return at
  const e = edge(geometry)
  if (at <= e) return at
  if (at >= L - e) return R - (L - at)
  return e + ((at - e) * (R - 2 * e)) / (L - 2 * e)
}

/** The logical position a `scrollTop` stands for: the inverse of `nativeFor`. */
export function logicalFor(native: number, geometry: ScrollGeometry): number {
  const L = logicalRange(geometry)
  const R = nativeRange(geometry)
  const at = clamp(native, R)
  if (!isScaled(geometry)) return at
  const e = edge(geometry)
  if (at <= e) return at
  if (at >= R - e) return L - (R - at)
  return e + ((at - e) * (L - 2 * e)) / (R - 2 * e)
}

/**
 * One grid's scroll position: the logical position it renders, and the
 * native one it last saw or set.
 */
export class HybridScroll {
  /** Pixels into the unscaled rows. */
  logical = 0
  private native = 0
  private geometry: ScrollGeometry

  constructor(geometry: ScrollGeometry) {
    this.geometry = geometry
  }

  /** A resize, or a row count that grew while indexing: the logical position holds. */
  setGeometry(geometry: ScrollGeometry): void {
    this.geometry = geometry
    this.logical = clamp(this.logical, logicalRange(geometry))
  }

  /**
   * The scroller moved to `scrollTop`. Returns a `scrollTop` to correct to
   * when the native position has walked to within reach of an end the rows
   * are nowhere near.
   *
   * Which kind of move it was is read from its size alone: a thumb drag moves
   * thousands of pixels for every pixel the pointer does, far past anything a
   * wheel, a trackpad or a key produces in one frame. (Watching the pointer
   * for scrollbar drags would be surer, but engines disagree about whether a
   * scrollbar press reaches the page at all, and a missed release would leave
   * every later wheel notch proportional.)
   */
  scrolled(scrollTop: number): number | undefined {
    const g = this.geometry
    const delta = scrollTop - this.native
    this.native = scrollTop
    if (!isScaled(g)) {
      this.logical = clamp(scrollTop, logicalRange(g))
      return undefined
    }
    this.logical =
      Math.abs(delta) > jumpThreshold(g)
        ? logicalFor(scrollTop, g)
        : clamp(this.logical + delta, logicalRange(g))
    const nearEnd = scrollTop < edge(g) / 2 || scrollTop > nativeRange(g) - edge(g) / 2
    return nearEnd ? this.recentre() : undefined
  }

  /** The `scrollTop` that shows `logical` — Go to row, the cursor, a deep link. */
  scrollTo(logical: number): number {
    this.logical = clamp(logical, logicalRange(this.geometry))
    this.native = Math.round(nativeFor(this.logical, this.geometry))
    return this.native
  }

  /** The scroll came to rest: the `scrollTop` that makes the thumb honest again, if it drifted. */
  settle(): number | undefined {
    return isScaled(this.geometry) ? this.recentre() : undefined
  }

  private recentre(): number | undefined {
    const target = Math.round(nativeFor(this.logical, this.geometry))
    if (Math.abs(target - this.native) <= 1) return undefined
    this.native = target
    return target
  }
}

/** The first row intersecting the viewport, and how far its top sits above it. */
export function rowWindow(logical: number, rowHeight: number): { first: number; offset: number } {
  const first = Math.floor(logical / rowHeight)
  return { first, offset: logical - first * rowHeight }
}
