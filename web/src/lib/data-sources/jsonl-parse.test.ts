import { parseJsonl, recordColumns, uniformColumns } from "./jsonl-parse"

describe("parseJsonl", () => {
  it("reads one record per line and skips blank lines", () => {
    expect(parseJsonl('{"a":1}\n\n{"a":2}\n')).toEqual({ ok: true, records: [{ a: 1 }, { a: 2 }] })
  })

  it("accepts CRLF line ends", () => {
    expect(parseJsonl('{"a":1}\r\n{"a":2}\r\n')).toEqual({
      ok: true,
      records: [{ a: 1 }, { a: 2 }],
    })
  })

  it("drops a byte-order mark before the first record", () => {
    expect(parseJsonl('\uFEFF{"a":1}\n')).toEqual({ ok: true, records: [{ a: 1 }] })
  })

  it("stops at the record limit", () => {
    expect(parseJsonl("1\n2\n3\n", { maxRecords: 2 })).toEqual({ ok: true, records: [1, 2] })
  })

  it("names the line that is not JSON", () => {
    expect(parseJsonl('{"a":1}\n{oops}\n')).toEqual({
      ok: false,
      reason: "Line 2 is not valid JSON.",
    })
  })

  it("forgives the last line of a truncated window, which the cut went through", () => {
    expect(parseJsonl('{"a":1}\n{"a":', { truncated: true })).toEqual({
      ok: true,
      records: [{ a: 1 }],
    })
  })

  it("does not forgive a broken last line in a whole file", () => {
    expect(parseJsonl('{"a":1}\n{"a":').ok).toBe(false)
  })
})

describe("recordColumns", () => {
  it("gives each key a column, with undefined where a record omits it", () => {
    expect(recordColumns([{ a: 1, b: null }, { a: 2 }], ["a", "b"])).toEqual([
      [1, 2],
      [null, undefined],
    ])
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

  it.each<[string, unknown[]]>([
    [
      "arrays",
      [
        [1, 2],
        [3, 4],
      ],
    ],
    ["numbers", [1, 2]],
    ["a null among objects", [{ a: 1 }, null]],
    ["objects with no fields", [{}, {}]],
  ])("refuses records that are %s", (_, records) => {
    expect(uniformColumns(records)).toBeNull()
  })
})
