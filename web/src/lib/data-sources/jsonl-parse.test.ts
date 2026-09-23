import { parseJsonl, uniformColumns } from "./jsonl-parse"

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
