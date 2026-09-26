import { memorySource } from "@/lib/data-sources/memory-source"
import { formatResult, readAllRows, type ResultRows } from "./result-export"

const result: ResultRows = {
  columns: [
    { name: "id", numeric: true },
    { name: "note", numeric: false },
    { name: "tags", numeric: false },
  ],
  rows: [
    [1, 'say "hi", then | go', ["a", "b"]],
    [9007199254740993n, null, []],
  ],
}

describe("formatResult", () => {
  it("writes CSV with RFC 4180 quoting and NULL spelled out", () => {
    expect(formatResult(result, "csv")).toBe(
      [
        "id,note,tags",
        '1,"say ""hi"", then | go","[""a"",""b""]"',
        "9007199254740993,NULL,[]",
      ].join("\n"),
    )
  })

  it("writes TSV", () => {
    expect(formatResult(result, "tsv").split("\n")[0]).toBe("id\tnote\ttags")
  })

  it("writes JSON with real nulls, lists and big integers as text", () => {
    expect(JSON.parse(formatResult(result, "json"))).toEqual([
      { id: 1, note: 'say "hi", then | go', tags: ["a", "b"] },
      { id: "9007199254740993", note: null, tags: [] },
    ])
  })

  it("writes a Markdown table with numbers right-aligned and pipes escaped", () => {
    const lines = formatResult(result, "markdown").split("\n")
    expect(lines[1]).toBe("| ---: | --- | --- |")
    expect(lines[2]).toContain("then \\| go")
  })
})

describe("readAllRows", () => {
  it("reads every row of a source", async () => {
    const source = memorySource(result.columns, result.rows as unknown[][])
    expect((await readAllRows(source, new AbortController().signal)).rows).toEqual(result.rows)
  })
})
