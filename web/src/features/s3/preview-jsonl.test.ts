import { jsonlTable, parseJsonl, uniformColumns } from "./preview-jsonl"

describe("parseJsonl", () => {
  it("reads one record per line and skips blank lines", () => {
    const result = parseJsonl('{"a":1}\n\n{"a":2}\n', 10, false)
    expect(result).toMatchObject({ ok: true, records: [{ a: 1 }, { a: 2 }], recordCount: 2 })
  })

  it("accepts CRLF line ends", () => {
    expect(parseJsonl('{"a":1}\r\n{"a":2}\r\n', 10, false)).toMatchObject({ recordCount: 2 })
  })

  it("names the line that is not JSON", () => {
    expect(parseJsonl('{"a":1}\n{oops}\n', 10, false)).toEqual({
      ok: false,
      reason: "Line 2 is not valid JSON.",
    })
  })

  it("forgives the last line of a truncated window, which the cut went through", () => {
    expect(parseJsonl('{"a":1}\n{"a":', 10, true)).toMatchObject({ ok: true, recordCount: 1 })
  })

  it("does not forgive a broken last line in a whole file", () => {
    expect(parseJsonl('{"a":1}\n{"a":', 10, false).ok).toBe(false)
  })
})

describe("uniformColumns", () => {
  it("accepts records with the same keys, in first-seen order", () => {
    expect(
      uniformColumns([
        { id: 1, name: "a" },
        { name: "b", id: 2 },
      ]),
    ).toEqual(["id", "name"])
  })

  it("accepts optional fields a minority of records omit", () => {
    expect(
      uniformColumns([
        { id: 1, name: "a", email: "x" },
        { id: 2, name: "b" },
      ]),
    ).toEqual(["id", "name", "email"])
  })

  it("refuses a stream of different event shapes", () => {
    expect(
      uniformColumns([
        { type: "click", x: 1, y: 2 },
        { type: "purchase", sku: "A", amount: 3, currency: "GBP" },
      ]),
    ).toBeNull()
  })

  it("refuses records that are not objects", () => {
    expect(
      uniformColumns([
        [1, 2],
        [3, 4],
      ]),
    ).toBeNull()
    expect(uniformColumns([1, 2])).toBeNull()
    expect(uniformColumns([{ a: 1 }, null])).toBeNull()
  })

  it("refuses records with no fields at all", () => {
    expect(uniformColumns([{}, {}])).toBeNull()
  })
})

describe("jsonlTable", () => {
  it("tells an explicit null from an omitted key", () => {
    const result = jsonlTable('{"id":1,"a":null,"b":2}\n{"id":2,"a":3}\n', { truncated: false })
    if (!result.ok) throw new Error(result.reason)
    expect(result.table.rows).toEqual([
      [1, null, 2],
      [2, 3, undefined],
    ])
  })

  it("right-aligns JSON numbers but not numeric-looking strings", () => {
    const result = jsonlTable('{"n":1,"s":"2"}\n{"n":2.5,"s":"3"}\n', { truncated: false })
    if (!result.ok) throw new Error(result.reason)
    expect(result.table.columns.map((c) => c.numeric)).toEqual([true, false])
  })

  it("keeps nested values for the cell to render compactly", () => {
    const result = jsonlTable('{"id":1,"tags":["a","b"],"geo":{"lat":1}}\n', { truncated: false })
    if (!result.ok) throw new Error(result.reason)
    expect(result.table.rows[0]).toEqual([1, ["a", "b"], { lat: 1 }])
  })

  it("estimates the total for a truncated window", () => {
    const line = '{"id":1}\n'
    const text = line.repeat(100)
    const result = jsonlTable(text, { truncated: true, objectBytes: text.length * 4 })
    if (!result.ok) throw new Error(result.reason)
    expect(result.table.totalIsEstimate).toBe(true)
    expect(result.table.totalRows).toBe(400)
  })

  it("explains why mixed records stay raw", () => {
    const result = jsonlTable('{"a":1,"b":2,"c":3}\n{"x":1,"y":2,"z":3}\n', { truncated: false })
    expect(result).toEqual({ ok: false, reason: expect.stringMatching(/do not share/) })
  })
})
