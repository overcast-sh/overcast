import { BLOCK_ROWS } from "./base-source"
import { isAbortError, NotTabularError } from "./row-source"
import { openTextSource, type TextSourceOptions } from "./text-source"
import { bytesObject, fakeFetch, syntheticCsv, type ByteSource } from "./testing/fake-object"
import { indexSettled, nextTurn, readRows } from "./testing/source-helpers"
import { inProcessDataWorker } from "./worker-port"

function openText(
  object: ByteSource,
  extra: Partial<TextSourceOptions> = {},
  fetchOptions: Parameters<typeof fakeFetch>[1] = {},
) {
  const fake = fakeFetch(object, { chunk: 16 * 1024, ...fetchOptions })
  const opening = openTextSource({
    url: "/obj",
    size: object.size,
    kind: "csv",
    port: inProcessDataWorker(fake.fetch),
    ...extra,
  })
  return { fake, opening }
}

describe("openTextSource > CSV with quoted values (Athena's result CSV)", () => {
  it.each([
    ["LF", "\n"],
    ["CRLF", "\r\n"],
  ])("reads a NULL line as a row, at a block boundary too (%s)", async (_, eol) => {
    // Given: a one-column result whose every 500th value is a NULL — a blank
    // line — so NULLs open the second and third blocks
    const values = Array.from({ length: 2_500 }, (_, i) => (i % 500 === 0 ? "" : `"${i}"`))
    const { opening } = openText(bytesObject(['"n"', ...values, ""].join(eol)), {
      quotedValues: true,
    })
    // When: it is opened and indexed
    const source = await opening
    await indexSettled(source)
    // Then: every line is a row, and the NULLs are where they were written
    expect(source.rowCount).toEqual({ value: 2_500, exact: true })
    expect((await readRows(source, 998, BLOCK_ROWS)).columns[0]).toEqual(["998", "999"])
    expect((await readRows(source, BLOCK_ROWS, BLOCK_ROWS + 1)).columns[0]).toEqual([null])
    expect((await readRows(source, BLOCK_ROWS * 2, BLOCK_ROWS * 2 + 2)).columns[0]).toEqual([
      null,
      "2001",
    ])
    source.dispose()
  })
})

describe("openTextSource > CSV", () => {
  it("opens on the header and the first rows, then indexes the rest", async () => {
    // Given: a CSV of 5,500 rows
    const { opening } = openText(syntheticCsv(5_500))
    // When: it opens
    const source = await opening
    // Then: the columns are known at once, and the count is exact once indexed
    expect(source.columns.map((c) => [c.name, c.numeric])).toEqual([
      ["id", false],
      ["name", false],
      ["amount", true],
    ])
    await indexSettled(source)
    expect(source.rowCount).toEqual({ value: 5_500, exact: true })
  })

  it("reads the head and starts the index stream together, and nothing else", async () => {
    const { opening, fake } = openText(syntheticCsv(5_500))
    await opening
    expect(fake.requests).toHaveLength(2)
    expect(fake.requests).toEqual(expect.arrayContaining([{ range: [0, 64 * 1024] }, {}]))
  })

  it("reads any block by one ranged GET between two index points", async () => {
    // Given: an indexed CSV
    const { opening, fake } = openText(syntheticCsv(5_500))
    const source = await opening
    await indexSettled(source)
    const before = fake.requests.length
    // When: a block in the middle is read
    const block = await readRows(source, 4 * BLOCK_ROWS, 5 * BLOCK_ROWS)
    // Then: one ranged request fetched exactly its rows
    expect(fake.requests.slice(before)).toEqual([{ range: expect.any(Array) }])
    expect(block.count).toBe(BLOCK_ROWS)
    expect(block.columns[0]?.[0]).toBe("000004000")
    expect(block.columns[0]?.[BLOCK_ROWS - 1]).toBe("000004999")
  })

  it("reads the last, short block", async () => {
    const { opening } = openText(syntheticCsv(5_500))
    const source = await opening
    await indexSettled(source)
    const last = await readRows(source, 5 * BLOCK_ROWS, 5_500)
    expect([last.count, last.columns[0]?.[499]]).toEqual([500, "000005499"])
  })

  it("serves the first rows without a second request", async () => {
    const { opening, fake } = openText(syntheticCsv(3_000))
    const source = await opening
    const before = fake.requests.length
    const first = await readRows(source, 0, 100)
    expect(first.columns[1]?.[0]).toBe("aaaaaaaa")
    expect(fake.requests).toHaveLength(before)
  })

  it("keeps quoted newlines inside a row, across block boundaries", async () => {
    // Given: 2,500 rows each holding a quoted line break and escaped quotes
    let text = "id,note\n"
    for (let i = 0; i < 2_500; i++) text += `${i},"line one\nline two, with ""quotes"""\n`
    const { opening } = openText(bytesObject(text))
    const source = await opening
    await indexSettled(source)
    // When: the third block is read
    const block = await readRows(source, 2_000, 2_500)
    // Then: every record is one row, its note whole
    expect(source.rowCount.value).toBe(2_500)
    expect(block.columns[0]?.[0]).toBe("2000")
    expect(block.columns[1]?.[0]).toBe('line one\nline two, with "quotes"')
  })

  it("stops at the byte limit, says so, and continues on request", async () => {
    // Given: a CSV larger than the byte limit
    const { opening } = openText(syntheticCsv(20_000), { byteLimit: 200 * 1024 })
    const source = await opening
    await indexSettled(source)
    expect(source.indexing?.state).toBe("paused-limit")
    expect(source.rowCount.exact).toBe(false)
    // When: the reader continues until the index is done, a run at a time
    while (source.indexing?.state === "paused-limit") {
      source.continueIndexing?.()
      await nextTurn()
      await indexSettled(source)
    }
    // Then: every row is indexed and readable
    expect(source.rowCount).toEqual({ value: 20_000, exact: true })
    expect((await readRows(source, 19_000, 20_000)).columns[0]?.[999]).toBe("000019999")
  })

  it("under Save-Data indexes only as far as the reader scrolls", async () => {
    // Given: Save-Data, and a 50,000-row file
    const csv = syntheticCsv(50_000)
    const { opening, fake } = openText(csv, { saveData: true })
    const source = await opening
    await indexSettled(source)
    // Then: the index stops a few blocks in, having streamed little of the file
    expect(source.indexing?.state).toBe("on-demand")
    expect(source.rowCount.value).toBeLessThan(10 * BLOCK_ROWS)
    expect(fake.streamedBytes()).toBeLessThan(csv.size / 4)
    // When: the reader reaches the end of what is indexed
    const counted = source.rowCount.value
    const lastBlock = Math.floor(counted / BLOCK_ROWS) * BLOCK_ROWS
    await readRows(source, lastBlock - BLOCK_ROWS, lastBlock)
    await nextTurn()
    await indexSettled(source, ["on-demand", "done"])
    // Then: the index reads on
    expect(source.rowCount.value).toBeGreaterThan(counted)
  })

  it("rejects a read with AbortError when its signal aborts", async () => {
    const { opening } = openText(syntheticCsv(5_000))
    const source = await opening
    await indexSettled(source)
    const controller = new AbortController()
    const read = source.getRows(3_000, 4_000, [], controller.signal)
    controller.abort()
    await expect(read).rejects.toSatisfy(isAbortError)
  })

  it("calls a file with an unclosed quote in its first rows not a table", async () => {
    const { opening } = openText(bytesObject('a,b\n"never closed,1\n2,3\n'))
    await expect(opening).rejects.toBeInstanceOf(NotTabularError)
  })

  it("names a delimiter the text chose over the extension", async () => {
    const { opening } = openText(bytesObject("a;b\n1,5;2\n"))
    const source = await opening
    expect(source.delimiter).toBe(";")
    expect((await readRows(source, 0, 1)).columns[0]?.[0]).toBe("1,5")
  })

  it("reports that the object changed when its ETag moves under it", async () => {
    // Given: an open, indexed file
    let etag = '"v1"'
    const { opening } = openText(syntheticCsv(5_000), {}, { etag: () => etag })
    const source = await opening
    await indexSettled(source)
    expect(source.changed).toBe(false)
    // When: the object is overwritten and a block is read
    etag = '"v2"'
    const notified = vi.fn()
    source.subscribe(notified)
    await readRows(source, 3_000, 4_000)
    // Then: the source says so, and tells its subscribers
    expect(source.changed).toBe(true)
    expect(notified).toHaveBeenCalled()
  })

  it("stops streaming the moment it is disposed", async () => {
    // Given: a five-million-row file, indexing
    const csv = syntheticCsv(5_000_000)
    const { opening, fake } = openText(csv)
    const source = await opening
    // When: it is disposed
    source.dispose()
    const streamed = fake.streamedBytes()
    await new Promise((resolve) => setTimeout(resolve, 20))
    // Then: at most the chunk already in flight arrives afterwards
    expect(fake.streamedBytes() - streamed).toBeLessThanOrEqual(16 * 1024)
    expect(streamed).toBeLessThan(csv.size)
  })
})

describe("openTextSource > JSON Lines", () => {
  it("tabulates uniform records, telling NULL from a missing key", async () => {
    // Given: records alternating between two shapes of the same table
    let text = ""
    for (let i = 0; i < 1_500; i++) {
      text +=
        JSON.stringify(i % 2 ? { id: i, email: null } : { id: i, email: "a@x", tag: "t" }) + "\n"
    }
    const { opening } = openText(bytesObject(text), { kind: "jsonl" })
    const source = await opening
    await indexSettled(source)
    // When: the second block is read
    const block = await readRows(source, 1_000, 1_500)
    // Then: the union of keys are the columns; null stays null, a missing key is undefined
    expect(source.columns.map((c) => c.name)).toEqual(["id", "email", "tag"])
    expect([block.columns[0]?.[1], block.columns[1]?.[1], block.columns[2]?.[1]]).toEqual([
      1_001,
      null,
      undefined,
    ])
  })

  it("refuses records of different shapes", async () => {
    const text = '{"a":1,"b":2,"c":3}\n{"x":1,"y":2,"z":3}\n'
    const { opening } = openText(bytesObject(text), { kind: "jsonl" })
    await expect(opening).rejects.toThrow(/do not share/)
  })
})
