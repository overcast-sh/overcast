import { MAX_FIELD_CHARS, columnNames, parseDelimited, sniffDelimiter } from "./delimited-parse"

const parse = (text: string, over: { delimiter?: string; truncated?: boolean } = {}) =>
  parseDelimited(text, {
    delimiter: over.delimiter ?? ",",
    maxRecords: 1000,
    truncated: over.truncated ?? false,
  })

describe("parseDelimited", () => {
  it("splits plain records", () => {
    expect(parse("a,b,c\n1,2,3\n").records).toEqual([
      ["a", "b", "c"],
      ["1", "2", "3"],
    ])
  })

  it("reads a quoted delimiter, an escaped quote and an embedded newline as data", () => {
    const { records } = parse('name,note\n"Smith, J","said ""hi""\nthen left"\n')
    expect(records[1]).toEqual(["Smith, J", 'said "hi"\nthen left'])
  })

  it("accepts CRLF, LF and a lone CR as record ends", () => {
    expect(parse("a,b\r\n1,2\r3,4\n5,6").records).toEqual([
      ["a", "b"],
      ["1", "2"],
      ["3", "4"],
      ["5", "6"],
    ])
  })

  it("keeps a CRLF inside quotes as part of the field", () => {
    expect(parse('a\n"x\r\ny"\n').records[1]).toEqual(["x\r\ny"])
  })

  it("drops a UTF-8 byte-order mark instead of gluing it to the first header", () => {
    expect(parse("\uFEFFid,name\n1,x\n").records[0]).toEqual(["id", "name"])
  })

  it("reads the last record of a file with no trailing newline", () => {
    expect(parse("a,b\n1,2").records).toEqual([
      ["a", "b"],
      ["1", "2"],
    ])
  })

  it("keeps empty fields, including trailing ones", () => {
    expect(parse("a,,c,\n").records[0]).toEqual(["a", "", "c", ""])
  })

  it("skips blank lines but keeps a record that is one quoted empty string", () => {
    expect(parse('a\n\n\n""\nb\n').records).toEqual([["a"], [""], ["b"]])
  })

  it("keeps a quote in the middle of an unquoted field as a character", () => {
    expect(parse('size\n5" floppy\n').records[1]).toEqual(['5" floppy'])
  })

  it("is lenient about text after a closing quote", () => {
    expect(parse('"a"b,c\n').records[0]).toEqual(["ab", "c"])
  })

  it("calls an unclosed quote in a complete file malformed rather than guessing", () => {
    const result = parse('a,b\n"open,2\n3,4\n')
    expect(result.malformed).toMatch(/never closed/)
  })

  it("drops the record a truncated window cut through, even mid-quote", () => {
    const result = parse('a,b\n1,2\n"cut, in the mid', { truncated: true })
    expect(result.malformed).toBeUndefined()
    expect(result.records).toEqual([
      ["a", "b"],
      ["1", "2"],
    ])
    expect(result.recordCount).toBe(2)
  })

  it("drops the unterminated last line of a truncated window", () => {
    expect(parse("a,b\n1,2\n3,", { truncated: true }).records).toHaveLength(2)
  })

  it("keeps records only up to the limit but counts them all", () => {
    const text = "h\n" + Array.from({ length: 50 }, (_, i) => `${i}\n`).join("")
    const result = parseDelimited(text, { delimiter: ",", maxRecords: 10, truncated: false })
    expect(result.records).toHaveLength(10)
    expect(result.recordCount).toBe(51)
    expect(result.consumedChars).toBe(text.length)
  })

  it("clips a huge field instead of holding all of it", () => {
    const huge = "x".repeat(MAX_FIELD_CHARS + 500)
    const result = parse(`a,b\n${huge},1\n`)
    expect(result.records[1][0]).toHaveLength(MAX_FIELD_CHARS)
    expect(result.records[1][1]).toBe("1")
    expect(result.clippedFields).toBe(1)
  })

  it("splits on tabs for TSV", () => {
    expect(parse("a\tb\n1,5\t2\n", { delimiter: "\t" }).records[1]).toEqual(["1,5", "2"])
  })
})

describe("sniffDelimiter", () => {
  it("keeps the extension's comma for an ordinary CSV", () => {
    expect(sniffDelimiter("a,b,c\n1,2,3\n", ",")).toBe(",")
  })

  it("finds the semicolon in a European export named .csv", () => {
    expect(sniffDelimiter("name;price\nTea;1,50\nCake;2,75\n", ",")).toBe(";")
  })

  it("finds tabs in a .txt that is really TSV", () => {
    expect(sniffDelimiter("a\tb\tc\n1\t2\t3\n")).toBe("\t")
  })

  it("is not fooled by delimiters inside quotes", () => {
    expect(sniffDelimiter('"x;y;z",b\n"p;q;r",d\n', ",")).toBe(",")
  })

  it("keeps the preferred delimiter for a one-column file", () => {
    expect(sniffDelimiter("value\n1\n2\n", "\t")).toBe("\t")
  })
})

describe("columnNames", () => {
  it("names blank headers, suffixes repeats and names extra fields", () => {
    expect(columnNames(["a", "", "a"], 4)).toEqual(["a", "column_2", "a_2", "column_4"])
  })
})
