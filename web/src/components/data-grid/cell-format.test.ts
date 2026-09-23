import {
  CELL_CHARS,
  cellText,
  clipboardText,
  formatCell,
  isNumericColumn,
  prettyJson,
  sampleColumnWidth,
} from "./cell-format"

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

describe("formatCell", () => {
  it("draws the three kinds of nothing differently", () => {
    expect(formatCell(null)).toEqual({ kind: "null", text: "NULL" })
    expect(formatCell(undefined)).toEqual({ kind: "absent", text: "" })
    expect(formatCell("")).toEqual({ kind: "empty", text: "empty" })
    // The strings "NULL" and "empty" are values, and must not read as tokens.
    expect(formatCell("NULL").kind).toBe("value")
    expect(formatCell("empty").kind).toBe("value")
  })

  it("formats typed values", () => {
    expect(formatCell(9007199254740993n).text).toBe("9007199254740993")
    expect(formatCell(false).text).toBe("false")
    expect(formatCell(new Date("2026-09-01T08:15:00Z")).text).toBe(
      "2026-09-01T08:15:00.000Z",
    )
    expect(
      formatCell(new Date("2026-09-03T00:00:00Z"), {
        name: "d",
        numeric: false,
        dateOnly: true,
      }).text,
    ).toBe("2026-09-03")
  })

  it("renders a decimal at its scale, not as the double it arrived as", () => {
    expect(formatCell(19.990000000000002, { name: "a", numeric: true, scale: 2 }).text).toBe(
      "19.99",
    )
    expect(formatCell(250, { name: "a", numeric: true, scale: 2 }).text).toBe("250.00")
  })

  it("shows lists and structs compactly, bigints and bytes included", () => {
    expect(formatCell(["gift", "express"]).text).toBe('["gift","express"]')
    expect(formatCell({ city: "London", zip: null }).text).toBe(
      '{"city":"London","zip":null}',
    )
    expect(formatCell({ id: 3051729675574597004n }).text).toBe(
      '{"id":"3051729675574597004"}',
    )
    expect(formatCell(new Uint8Array([1, 2, 255])).text).toBe("3 B · 0102ff")
  })

  it("clips a huge value for the cell and keeps more of it for the inspector", () => {
    const value = "x".repeat(CELL_CHARS * 3)
    const cell = formatCell(value)
    expect(cell.text).toHaveLength(CELL_CHARS + 1)
    expect(cell.clipped).toBe(true)
    expect(cellText(value).text).toBe(value)
  })
})

describe("clipboardText", () => {
  it("copies values, NULL as NULL, and nothing for an empty or missing value", () => {
    expect(clipboardText("a")).toBe("a")
    expect(clipboardText(null)).toBe("NULL")
    expect(clipboardText("")).toBe("")
    expect(clipboardText(undefined)).toBe("")
  })

  it("keeps a TSV row on one line", () => {
    expect(clipboardText("a\tb\nc")).toBe("a b c")
  })
})

describe("prettyJson", () => {
  it("indents structs and spells out bigints", () => {
    expect(prettyJson({ id: 3051729675574597004n })).toBe('{\n  "id": "3051729675574597004"\n}')
  })
})

describe("sampleColumnWidth", () => {
  it("fits the longest sampled value, within bounds", () => {
    const column = { name: "id", numeric: true }
    expect(sampleColumnWidth(column, ["1", "22"])).toBe(64)
    expect(sampleColumnWidth(column, ["x".repeat(200)])).toBe(320)
    expect(sampleColumnWidth({ name: "a_rather_long_column_name", numeric: false }, [])).toBeGreaterThan(64)
  })
})
