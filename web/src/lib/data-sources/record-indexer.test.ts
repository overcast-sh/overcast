import { parseDelimited } from "./delimited-parse"
import { RecordIndexer } from "./record-indexer"

const encode = (text: string) => new TextEncoder().encode(text)

const COMMA = 0x2c

function index(
  text: string,
  { every = 2, header = true, delimiter = COMMA as number | null, chunk = 0 } = {},
) {
  const indexer = new RecordIndexer({ every, header, delimiter })
  const bytes = encode(text)
  if (chunk > 0) {
    for (let i = 0; i < bytes.length; i += chunk) indexer.push(bytes.subarray(i, i + chunk))
  } else {
    indexer.push(bytes)
  }
  indexer.finish()
  return indexer
}

/** The text between two offsets, as the block reader would fetch it. */
const between = (text: string, from: number, to: number) =>
  new TextDecoder().decode(encode(text).subarray(from, to))

describe("RecordIndexer", () => {
  it("records where every nth data record starts, after the header", () => {
    const text = "h\na\nb\nc\nd\ne\n"
    const indexer = index(text)
    expect(indexer.rows).toBe(5)
    expect(indexer.headerEnd).toBe(2)
    expect(indexer.offsets).toEqual([2, 6, 10])
    expect(between(text, indexer.offsets[1], indexer.offsets[2])).toBe("c\nd\n")
  })

  it("does not split a record at a newline inside quotes", () => {
    const text = 'h\n"line one\nline two",x\nb\nc\n'
    const indexer = index(text)
    expect(indexer.rows).toBe(3)
    expect(between(text, indexer.offsets[0], indexer.offsets[1])).toBe(
      '"line one\nline two",x\nb\n',
    )
  })

  it("treats an escaped quote as data, not as the end of the field", () => {
    const indexer = index('h\n"say ""hi""\nthere"\nb\n')
    expect(indexer.rows).toBe(2)
    expect(indexer.unterminatedQuote).toBe(false)
  })

  it("ends records at CRLF, LF and a lone CR, even across chunk boundaries", () => {
    const text = "h\r\na\r\nb\rc\nd"
    for (const chunk of [0, 1, 2, 3]) {
      expect(index(text, { every: 1, chunk }).rows, `chunk ${chunk}`).toBe(4)
    }
    expect(index(text, { every: 1 }).offsets).toEqual([3, 6, 8, 10])
  })

  it("skips blank lines but counts a record of one quoted empty string", () => {
    expect(index('h\n\n\na\n""\n\nb\n', { every: 1 }).rows).toBe(3)
  })

  it("skips a byte-order mark before the header", () => {
    const indexer = index("\uFEFFid\n1\n", { every: 1 })
    expect(indexer.headerEnd).toBe(6)
    expect(indexer.offsets).toEqual([6])
  })

  it("counts a final record with no line break", () => {
    expect(index("h\na\nb").rows).toBe(2)
  })

  it("reads a quote in the middle of a field as a character, as the parser does", () => {
    expect(index('h\n5" floppy,x\nb\n', { every: 1 }).rows).toBe(2)
  })

  it("reports an unclosed quote", () => {
    expect(index('h\n"never closed\nb\n').unterminatedQuote).toBe(true)
  })

  it("reads JSON Lines without quote tracking, since JSON escapes its quotes", () => {
    const text = '{"a":"x\\"y"}\n{"a":"z"}\n'
    const indexer = index(text, { every: 1, header: false, delimiter: null })
    expect(indexer.rows).toBe(2)
    expect(indexer.offsets).toEqual([0, 13])
  })

  it("resumes from where it paused and continues the same offsets", () => {
    const text = "h\na\nb\nc\nd\ne\nf\n"
    const whole = index(text)
    const first = new RecordIndexer({ every: 2, header: true, delimiter: COMMA })
    const bytes = encode(text)
    first.push(bytes.subarray(0, 7)) // "h\na\nb\nc" — c is incomplete
    const state = first.resumeState()
    expect(state.position).toBe(6)
    const second = new RecordIndexer(first.options, state)
    second.push(bytes.subarray(state.position))
    second.finish()
    expect([...first.offsets, ...second.offsets]).toEqual(whole.offsets)
    expect(second.rows).toBe(whole.rows)
  })

  it("agrees with the block parser about how many records there are", () => {
    // A seeded mix of every construct both have to agree on.
    let seed = 7
    const random = () => (seed = (seed * 16807) % 2147483647) / 2147483647
    const pieces = ['plain', '"a,b"', '"x\ny"', '""', '"q""q"', "", "5\" floppy"]
    const ends = ["\n", "\r\n", "\r", "\n\n"]
    let text = "h1,h2\n"
    for (let i = 0; i < 400; i++) {
      text += `${pieces[Math.floor(random() * pieces.length)]},${pieces[Math.floor(random() * pieces.length)]}`
      text += ends[Math.floor(random() * ends.length)]
    }
    const indexer = index(text, { every: 7, chunk: 5 })
    const parsed = parseDelimited(text, { delimiter: ",", maxRecords: Infinity, truncated: false })
    expect(indexer.rows).toBe(parsed.recordCount - 1)
    // And every block the index cuts parses to exactly its share of records.
    const offsets = [...indexer.offsets, indexer.end]
    for (let k = 0; k + 1 < offsets.length; k++) {
      const block = parseDelimited(between(text, offsets[k], offsets[k + 1]), {
        delimiter: ",",
        maxRecords: Infinity,
        truncated: false,
      })
      expect(block.recordCount, `block ${k}`).toBe(Math.min(7, indexer.rows - k * 7))
    }
  })
})
