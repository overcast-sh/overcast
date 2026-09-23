import {
  HybridScroll,
  logicalFor,
  logicalRange,
  nativeFor,
  probeHeightCap,
  ROW_HEIGHT,
  rowWindow,
  SAFE_HEIGHT_CAP,
  spacerHeight,
  type ScrollGeometry,
} from "./scroll-model"

const VIEWPORT = 560
const NOTCH = 100

const geometryFor = (rows: number, cap = SAFE_HEIGHT_CAP): ScrollGeometry => ({
  contentHeight: rows * ROW_HEIGHT,
  viewport: VIEWPORT,
  cap,
})

/**
 * Plays the browser: moves the native position by `delta` within the spacer,
 * reports it, and applies any correction the model asks for.
 */
function browser(geometry: ScrollGeometry, model = new HybridScroll(geometry)) {
  const max = spacerHeight(geometry) - geometry.viewport
  let scrollTop = 0
  return {
    model,
    get scrollTop() {
      return scrollTop
    },
    scrollBy(delta: number) {
      scrollTop = Math.min(Math.max(scrollTop + delta, 0), max)
      scrollTop = model.scrolled(scrollTop) ?? scrollTop
    },
    dragThumbTo(fraction: number) {
      scrollTop = model.scrolled(fraction * max) ?? fraction * max
    },
    goTo(logical: number) {
      scrollTop = model.scrollTo(logical)
    },
    max,
  }
}

const firstRow = (model: HybridScroll) => rowWindow(model.logical, ROW_HEIGHT).first

describe("HybridScroll", () => {
  it.each([1_000, 5_000_000])(
    "moves one wheel notch the same number of rows at %i rows",
    (rows) => {
      // Given: a grid part-way down
      const view = browser(geometryFor(rows))
      view.goTo(10 * ROW_HEIGHT)
      // When: the wheel turns one notch
      view.scrollBy(NOTCH)
      // Then: the rows moved by exactly one notch of pixels
      expect(view.model.logical).toBe(10 * ROW_HEIGHT + NOTCH)
    },
  )

  it("lands near row 2.5 million when the thumb is dragged half-way down 5 million", () => {
    const view = browser(geometryFor(5_000_000))
    view.dragThumbTo(0.5)
    expect(Math.abs(firstRow(view.model) - 2_500_000)).toBeLessThan(1_000)
  })

  it("maps a jump to a quarter of the track to a quarter of the rows, as a track click makes", () => {
    const view = browser(geometryFor(5_000_000))
    view.scrollBy(view.max / 4)
    expect(Math.abs(firstRow(view.model) - 1_250_000)).toBeLessThan(1_000)
  })

  it("moves 1:1 for a fling frame just under the jump size", () => {
    const view = browser(geometryFor(5_000_000))
    view.goTo(1_000_000)
    view.scrollBy(1_900)
    expect(view.model.logical).toBe(1_001_900)
  })

  it.each([
    ["down", NOTCH],
    ["up", -NOTCH],
  ])("never walks the native position to an end by scrolling %s in small steps", (_, delta) => {
    // Given: 5 million rows, in the middle of the file
    const geometry = geometryFor(5_000_000)
    const view = browser(geometry)
    view.goTo(logicalRange(geometry) / 2)
    // When: a long run of small scrolls, with no pause for the scroll to settle
    let touchedEnd = false
    for (let i = 0; i < 50_000; i++) {
      view.scrollBy(delta)
      if (view.scrollTop <= 0 || view.scrollTop >= view.max) touchedEnd = true
    }
    // Then: the native position never reached an end, and the rows moved 1:1 all along
    expect(touchedEnd).toBe(false)
    expect(view.model.logical).toBeCloseTo(logicalRange(geometry) / 2 + 50_000 * delta, 3)
  })

  it.each([
    ["top", 0, -1],
    ["bottom", 1, 1],
  ])("reaches the %s row exactly by scrolling to it", (_, end, direction) => {
    const geometry = geometryFor(5_000_000)
    const view = browser(geometry)
    view.goTo(end * logicalRange(geometry) - direction * 20 * ROW_HEIGHT)
    for (let i = 0; i < 20; i++) view.scrollBy(direction * NOTCH)
    expect(view.model.logical).toBe(end * logicalRange(geometry))
  })

  it("re-centres the thumb once scrolling settles after fine scrolling", () => {
    // Given: fine scrolling that moved the native position 1:1, away from its proportional place
    const geometry = geometryFor(5_000_000)
    const view = browser(geometry)
    view.goTo(logicalRange(geometry) / 2)
    for (let i = 0; i < 100; i++) view.scrollBy(NOTCH)
    // When: the scroll settles
    const recentred = view.model.settle()
    // Then: the thumb goes where the rows are
    expect(recentred).toBe(Math.round(nativeFor(view.model.logical, geometry)))
  })

  it("needs no re-centring below the cap, where native and logical are the same", () => {
    const view = browser(geometryFor(1_000))
    view.scrollBy(NOTCH * 7)
    expect(view.model.settle()).toBeUndefined()
    expect(view.model.logical).toBe(view.scrollTop)
  })

  it("keeps its logical position when the row count grows while indexing", () => {
    const model = new HybridScroll(geometryFor(1_000_000))
    model.scrollTo(12_345)
    model.setGeometry(geometryFor(2_000_000))
    expect(model.logical).toBe(12_345)
  })
})

describe("nativeFor and logicalFor", () => {
  const geometry = geometryFor(5_000_000)

  it.each([0, 1_000, 5e6, 7e7, logicalRange(geometry) - 1_000, logicalRange(geometry)])(
    "are inverses at logical %i",
    (logical) => {
      expect(logicalFor(nativeFor(logical, geometry), geometry)).toBeCloseTo(logical, 3)
    },
  )

  it("keeps the spacer under the cap", () => {
    expect(spacerHeight(geometry)).toBe(SAFE_HEIGHT_CAP)
    expect(nativeFor(logicalRange(geometry), geometry)).toBe(SAFE_HEIGHT_CAP - VIEWPORT)
  })
})

describe("probeHeightCap", () => {
  /** A document whose probe element lays out at `height`. */
  const documentMeasuring = (height: number) => {
    const doc = document.implementation.createHTMLDocument()
    const create = doc.createElement.bind(doc)
    doc.createElement = (tag: string) => {
      const element = create(tag)
      element.getBoundingClientRect = () => ({ height }) as DOMRect
      return element
    }
    return doc
  }

  it.each([
    ["an engine that lays out 17.9 million pixels", 17_895_697, SAFE_HEIGHT_CAP],
    ["an engine capped lower than the safe height", 6_000_000, 6_000_000],
    ["no layout at all", 0, SAFE_HEIGHT_CAP],
  ])("answers for %s", (_, measured, expected) => {
    expect(probeHeightCap(documentMeasuring(measured))).toBe(expected)
  })

  it("leaves nothing behind in the document", () => {
    const doc = documentMeasuring(1)
    probeHeightCap(doc)
    expect(doc.body.children).toHaveLength(0)
  })
})

describe("rowWindow", () => {
  it("finds the first visible row and how far it is scrolled past", () => {
    expect(rowWindow(3 * ROW_HEIGHT + 5, ROW_HEIGHT)).toEqual({ first: 3, offset: 5 })
  })
})
