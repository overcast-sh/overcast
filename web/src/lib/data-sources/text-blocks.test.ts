import { NotTabularError } from "./row-source"
import { delimitedHead, jsonlHead, textBlock } from "./text-blocks"

describe("delimitedHead", () => {
  it("reads the columns, their alignment and the first rows", () => {
    const { head, layout } = delimitedHead("id,name\n1,Ada\n2,Grace\n", ",", {
      rows: 10,
      truncated: false,
    })
    expect(head.columns).toEqual([
      { name: "id", numeric: true },
      { name: "name", numeric: false },
    ])
    expect(head.first).toEqual({
      count: 2,
      columns: [
        ["1", "2"],
        ["Ada", "Grace"],
      ],
    })
    expect(layout).toEqual({ kind: "delimited", delimiter: ",", width: 2, quotedValues: false })
  })

  it("keeps a quoted-values file's NULLs, and lays out its blocks the same way", () => {
    const { head, layout } = delimitedHead('"id","note"\n"1",\n', ",", {
      rows: 10,
      truncated: false,
      quotedValues: true,
    })
    expect(head.first.columns).toEqual([["1"], [null]])
    expect(textBlock('"2",\n', layout, 10).columns).toEqual([["2"], [null]])
  })

  it("keeps only the rows asked for", () => {
    const { head } = delimitedHead("n\n1\n2\n3\n", ",", { rows: 2, truncated: false })
    expect(head.first.count).toBe(2)
  })

  it("names the columns of a row wider than the header", () => {
    const { head } = delimitedHead("a\n1,2\n", ",", { rows: 10, truncated: false })
    expect(head.columns.map((c) => c.name)).toEqual(["a", "column_2"])
  })

  it("chooses the delimiter the text uses over the extension's", () => {
    const { head } = delimitedHead("a;b\n1,5;2\n", ",", { rows: 10, truncated: false })
    expect(head.delimiter).toBe(";")
  })

  it.each([
    ["an unclosed quote", 'a,b\n"open,1\n', false, /never closed/],
    ["an empty file", "", false, /empty/],
    ["a header longer than the window", "a,b,c", true, /longer than 64 KB/],
  ])("refuses %s as not a table", (_, text, truncated, message) => {
    expect(() => delimitedHead(text, ",", { rows: 10, truncated })).toThrow(
      expect.objectContaining({
        name: NotTabularError.name,
        message: expect.stringMatching(message),
      }),
    )
  })
})

describe("jsonlHead", () => {
  it("takes the union of keys as columns, numeric only for JSON numbers", () => {
    const { head, layout } = jsonlHead('{"id":1,"code":"42","n":1}\n{"id":2,"n":2}\n', {
      rows: 10,
      truncated: false,
    })
    expect(head.columns).toEqual([
      { name: "id", numeric: true },
      { name: "code", numeric: false },
      { name: "n", numeric: true },
    ])
    expect(layout).toEqual({ kind: "jsonl", keys: ["id", "code", "n"] })
  })

  it("drops the record a truncated window cut through", () => {
    const { head } = jsonlHead('{"a":1}\n{"a":', { rows: 10, truncated: true })
    expect(head.first.count).toBe(1)
  })

  it.each([
    ["records of different shapes", '{"a":1,"b":2}\n{"x":1,"y":2}\n', /do not share/],
    ["a window with no complete record", '{"a":', /no complete records/],
    ["a line that is not JSON", "{oops}\n", /not valid JSON/],
  ])("refuses %s as not a table", (_, text, message) => {
    expect(() => jsonlHead(text, { rows: 10, truncated: text.endsWith(":") })).toThrow(message)
  })
})

describe("textBlock", () => {
  it("parses a CSV block with the head's delimiter and width", () => {
    const block = textBlock(
      "1;x\n2\n",
      { kind: "delimited", delimiter: ";", width: 2, quotedValues: false },
      10,
    )
    expect(block).toEqual({
      count: 2,
      columns: [
        ["1", "2"],
        ["x", ""],
      ],
    })
  })

  it("parses a JSON Lines block by the head's keys", () => {
    const block = textBlock('{"b":2,"a":1}\n', { kind: "jsonl", keys: ["a", "b", "c"] }, 10)
    expect(block).toEqual({ count: 1, columns: [[1], [2], [undefined]] })
  })
})
