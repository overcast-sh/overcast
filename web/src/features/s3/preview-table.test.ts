import {
  PREVIEW_CELL_CHARS,
  describeRowCount,
  formatPreviewCell,
  isNumericColumn,
  roundEstimate,
  type PreviewTableModel,
} from "./preview-table"

const model = (over: Partial<PreviewTableModel> & { shown: number }): PreviewTableModel => ({
  columns: [],
  rows: Array.from({ length: over.shown }, () => []),
  totalIsEstimate: false,
  truncatedByBytes: false,
  hiddenColumns: 0,
  ...over,
})

describe("describeRowCount", () => {
  it("says only the count when the whole object is on screen", () => {
    expect(describeRowCount(model({ shown: 12, totalRows: 12 }))).toBe("12 rows")
    expect(describeRowCount(model({ shown: 1, totalRows: 1 }))).toBe("1 row")
  })

  it("says how many of how many for a longer object", () => {
    expect(describeRowCount(model({ shown: 200, totalRows: 1204 }))).toBe("first 200 rows of 1,204")
  })

  it("marks an estimate as one", () => {
    expect(describeRowCount(model({ shown: 200, totalRows: 48000, totalIsEstimate: true }))).toBe(
      "first 200 rows of ~48,000",
    )
  })

  it("does not invent a total it does not have", () => {
    expect(describeRowCount(model({ shown: 200, totalIsEstimate: true }))).toBe("first 200 rows")
  })
})

describe("roundEstimate", () => {
  it("keeps two significant figures", () => {
    expect(roundEstimate(48_217)).toBe(48_000)
    expect(roundEstimate(1_234_567)).toBe(1_200_000)
    expect(roundEstimate(87)).toBe(87)
  })
})

describe("isNumericColumn", () => {
  it("accepts integers, decimals, signs and exponents, ignoring blanks", () => {
    expect(isNumericColumn(["1", "-2.5", "+3", "1e6", "", null, undefined, ".5"])).toBe(true)
  })

  it("rejects identifiers with leading zeros and any text", () => {
    expect(isNumericColumn(["007"])).toBe(false)
    expect(isNumericColumn(["1", "n/a"])).toBe(false)
  })

  it("is false for a column with nothing in it", () => {
    expect(isNumericColumn(["", null])).toBe(false)
  })
})

describe("formatPreviewCell", () => {
  it("draws the three kinds of nothing differently", () => {
    expect(formatPreviewCell(null)).toEqual({ kind: "null", text: "NULL" })
    expect(formatPreviewCell(undefined)).toEqual({ kind: "absent", text: "" })
    expect(formatPreviewCell("")).toEqual({ kind: "empty", text: '""' })
    // The string "NULL" is a value, and must not read as the token.
    expect(formatPreviewCell("NULL").kind).toBe("value")
  })

  it("formats typed values", () => {
    expect(formatPreviewCell(9007199254740993n).text).toBe("9007199254740993")
    expect(formatPreviewCell(false).text).toBe("false")
    expect(formatPreviewCell(new Date("2026-09-01T08:15:00Z")).text).toBe(
      "2026-09-01T08:15:00.000Z",
    )
    expect(
      formatPreviewCell(new Date("2026-09-03T00:00:00Z"), {
        name: "d",
        numeric: false,
        dateOnly: true,
      }).text,
    ).toBe("2026-09-03")
  })

  it("renders a decimal at its scale, not as the double it arrived as", () => {
    expect(formatPreviewCell(19.990000000000002, { name: "a", numeric: true, scale: 2 }).text).toBe(
      "19.99",
    )
    expect(formatPreviewCell(250, { name: "a", numeric: true, scale: 2 }).text).toBe("250.00")
  })

  it("shows lists and structs compactly, bigints and bytes included", () => {
    expect(formatPreviewCell(["gift", "express"]).text).toBe('["gift","express"]')
    expect(formatPreviewCell({ city: "London", zip: null }).text).toBe(
      '{"city":"London","zip":null}',
    )
    expect(formatPreviewCell({ id: 3051729675574597004n }).text).toBe(
      '{"id":"3051729675574597004"}',
    )
    expect(formatPreviewCell(new Uint8Array([1, 2, 255])).text).toBe("3 B · 0102ff")
  })

  it("clips a huge value and keeps the start of it reachable", () => {
    const cell = formatPreviewCell("x".repeat(PREVIEW_CELL_CHARS * 3))
    expect(cell.text).toHaveLength(PREVIEW_CELL_CHARS + 1)
    expect(cell.title?.length).toBeGreaterThan(PREVIEW_CELL_CHARS)
  })
})
