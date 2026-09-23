import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { isAbortError, type RowSource } from "@/components/data-grid/row-source"
import {
  BLOCK_ROWS,
  NotTabularError,
  memorySource,
  openParquetSource,
  openTextSource,
} from "./sources"
import { bytesObject, fakeFetch, syntheticCsv, type ByteSource } from "./testing/fake-object"
import { inProcessDataWorker } from "./worker-port"

/** Resolves once the source's index says `done` (or anything but running). */
function settled(source: RowSource, states = ["done", "error", "paused-limit", "on-demand"]) {
  return new Promise<void>((resolve) => {
    const check = () => {
      if (states.includes(source.indexing?.state ?? "")) {
        stop()
        resolve()
      }
    }
    const stop = source.subscribe(check)
    check()
  })
}

function openCsv(object: ByteSource, extra: Partial<Parameters<typeof openTextSource>[0]> = {}) {
  const fake = fakeFetch(object, { chunk: 16 * 1024 })
  const port = inProcessDataWorker(fake.fetch)
  const opening = openTextSource({ url: "/obj", size: object.size, kind: "csv", port, ...extra })
  return { fake, port, opening }
}

const rows = (source: RowSource, start: number, end: number) =>
  source.getRows(start, end, [], new AbortController().signal)

describe("openTextSource", () => {
  it("opens on the header and the first block, then indexes the rest", async () => {
    const csv = syntheticCsv(5_500)
    const { opening } = openCsv(csv)
    const source = await opening
    expect(source.columns.map((c) => c.name)).toEqual(["id", "name", "amount"])
    expect(source.columns.map((c) => c.numeric)).toEqual([false, false, true])
    await settled(source)
    expect(source.rowCount).toEqual({ value: 5_500, exact: true })
    expect(source.indexing?.state).toBe("done")
  })

  it("reads any block by one ranged GET between two index points", async () => {
    const csv = syntheticCsv(5_500)
    const { opening, fake } = openCsv(csv)
    const source = await opening
    await settled(source)
    const before = fake.requests.length
    const block = await rows(source, 4 * BLOCK_ROWS, 5 * BLOCK_ROWS)
    expect(fake.requests.slice(before)).toHaveLength(1)
    expect(fake.requests.at(-1)?.range).toBeDefined()
    expect(block.count).toBe(BLOCK_ROWS)
    expect(block.columns[0]?.[0]).toBe("000004000")
    expect(block.columns[0]?.[BLOCK_ROWS - 1]).toBe("000004999")
    // The last, short block.
    const last = await rows(source, 5 * BLOCK_ROWS, 5_500)
    expect(last.count).toBe(500)
    expect(last.columns[0]?.[499]).toBe("000005499")
  })

  it("serves the first block without a second request", async () => {
    const { opening, fake } = openCsv(syntheticCsv(3_000))
    const source = await opening
    const before = fake.requests.length
    const first = await rows(source, 0, BLOCK_ROWS)
    expect(first.columns[1]?.[0]).toBe("aaaaaaaa")
    expect(fake.requests.length).toBe(before)
  })

  it("keeps quoted newlines inside a row, across block boundaries", async () => {
    let text = "id,note\n"
    for (let i = 0; i < 2_500; i++) text += `${i},"line one\nline two, with ""quotes"""\n`
    const { opening } = openCsv(bytesObject(text))
    const source = await opening
    await settled(source)
    expect(source.rowCount.value).toBe(2_500)
    const block = await rows(source, 2_000, 2_500)
    expect(block.columns[0]?.[0]).toBe("2000")
    expect(block.columns[1]?.[0]).toBe('line one\nline two, with "quotes"')
  })

  it("stops at the byte limit and continues on request", async () => {
    const csv = syntheticCsv(20_000)
    const { opening } = openCsv(csv, { byteLimit: 200 * 1024 })
    const source = await opening
    await settled(source)
    expect(source.indexing?.state).toBe("paused-limit")
    const partial = source.rowCount.value
    expect(partial).toBeGreaterThan(BLOCK_ROWS)
    expect(partial).toBeLessThan(20_000)
    expect(source.rowCount.exact).toBe(false)
    // Continue until done, one run at a time.
    while (source.indexing?.state === "paused-limit") {
      source.continueIndexing?.()
      await new Promise((r) => setTimeout(r, 0))
      await settled(source)
    }
    expect(source.rowCount).toEqual({ value: 20_000, exact: true })
    const tail = await rows(source, 19_000, 20_000)
    expect(tail.columns[0]?.[999]).toBe("000019999")
  })

  it("under Save-Data indexes only as far as the reader scrolls", async () => {
    const csv = syntheticCsv(50_000)
    const { opening, fake } = openCsv(csv, { saveData: true })
    const source = await opening
    await settled(source)
    expect(source.indexing?.state).toBe("on-demand")
    expect(source.rowCount.value).toBeLessThan(5 * BLOCK_ROWS)
    expect(fake.streamedBytes()).toBeLessThan(csv.size / 4)
    // Reading near the end of what is indexed asks for more.
    const counted = source.rowCount.value
    await rows(source, Math.floor(counted / BLOCK_ROWS) * BLOCK_ROWS - BLOCK_ROWS, Math.floor(counted / BLOCK_ROWS) * BLOCK_ROWS)
    await new Promise((r) => setTimeout(r, 0))
    await settled(source, ["on-demand", "done"])
    expect(source.rowCount.value).toBeGreaterThan(counted)
  })

  it("rejects a read with AbortError when its signal aborts", async () => {
    const { opening } = openCsv(syntheticCsv(5_000))
    const source = await opening
    await settled(source)
    const controller = new AbortController()
    const read = source.getRows(3_000, 4_000, [], controller.signal)
    controller.abort()
    await expect(read).rejects.toSatisfy(isAbortError)
  })

  it("calls a file with an unclosed quote in its first block not a table", async () => {
    const { opening } = openCsv(bytesObject('a,b\n"never closed,1\n2,3\n'))
    await expect(opening).rejects.toBeInstanceOf(NotTabularError)
  })

  it("reads TSV on its tabs and names a sniffed delimiter", async () => {
    const fake = fakeFetch(bytesObject("a;b\n1,5;2\n"))
    const source = await openTextSource({
      url: "/obj",
      size: 10,
      kind: "csv",
      port: inProcessDataWorker(fake.fetch),
    })
    expect(source.delimiter).toBe(";")
    expect((await rows(source, 0, 1)).columns[0]?.[0]).toBe("1,5")
  })

  it("stops everything on dispose", async () => {
    const csv = syntheticCsv(5_000_000)
    const { opening, fake } = openCsv(csv)
    const source = await opening
    source.dispose()
    const streamed = fake.streamedBytes()
    await new Promise((r) => setTimeout(r, 20))
    // At most the chunk already in flight arrives after dispose.
    expect(fake.streamedBytes() - streamed).toBeLessThanOrEqual(16 * 1024)
    expect(streamed).toBeLessThan(csv.size)
  })
})

describe("openTextSource > JSON Lines", () => {
  it("tabulates uniform records, telling NULL from a missing key", async () => {
    let text = ""
    for (let i = 0; i < 1_500; i++) {
      text += JSON.stringify(i % 2 ? { id: i, email: null } : { id: i, email: "a@x", tag: "t" }) + "\n"
    }
    const fake = fakeFetch(bytesObject(text))
    const source = await openTextSource({
      url: "/obj",
      size: text.length,
      kind: "jsonl",
      port: inProcessDataWorker(fake.fetch),
    })
    expect(source.columns.map((c) => c.name)).toEqual(["id", "email", "tag"])
    await settled(source)
    const block = await rows(source, 1_000, 1_500)
    expect(block.columns[0]?.[1]).toBe(1_001)
    expect(block.columns[1]?.[1]).toBeNull()
    expect(block.columns[2]?.[1]).toBeUndefined()
  })

  it("refuses records of different shapes", async () => {
    const text = '{"a":1,"b":2,"c":3}\n{"x":1,"y":2,"z":3}\n'
    const fake = fakeFetch(bytesObject(text))
    await expect(
      openTextSource({ url: "/o", size: text.length, kind: "jsonl", port: inProcessDataWorker(fake.fetch) }),
    ).rejects.toThrow(/do not share/)
  })
})

describe("openParquetSource", () => {
  const load = (name: string) => {
    const file = readFileSync(resolve(__dirname, "__fixtures__", name))
    return new Uint8Array(file)
  }

  it("reads only the requested columns of the requested rows", async () => {
    const object = bytesObject(load("orders.parquet"))
    const fake = fakeFetch(object)
    const source = await openParquetSource({
      url: "/obj",
      size: object.size,
      port: inProcessDataWorker(fake.fetch),
    })
    expect(source.rowCount).toEqual({ value: 5, exact: true })
    expect(source.projects).toBe(true)
    expect(source.info.fields[0]).toEqual({ name: "order_id", type: "INT64", nullable: false })
    const block = await source.getRows(3, 5, [1], new AbortController().signal)
    expect(block.columns[1]).toEqual(["Alan Turing", "Edsger Dijkstra"])
    expect(block.columns[0]).toBeUndefined()
  })

  it("decodes ZSTD", async () => {
    const object = bytesObject(load("orders.zstd.parquet"))
    const source = await openParquetSource({
      url: "/obj",
      size: object.size,
      port: inProcessDataWorker(fakeFetch(object).fetch),
    })
    const block = await source.getRows(0, 2, [1], new AbortController().signal)
    expect(block.columns[1]).toEqual(["Ada Lovelace", "Grace Hopper"])
  })

  it("rejects a file that is not Parquet", async () => {
    const object = bytesObject("id,name\n1,Ada\n")
    await expect(
      openParquetSource({ url: "/o", size: object.size, port: inProcessDataWorker(fakeFetch(object).fetch) }),
    ).rejects.toThrow(/PAR1/)
  })
})

describe("memorySource", () => {
  it("serves rows from memory, exactly", async () => {
    const source = memorySource([{ name: "a", numeric: false }], [["x"], ["y"]])
    expect(source.rowCount).toEqual({ value: 2, exact: true })
    expect((await rows(source, 1, 2)).columns[0]).toEqual(["y"])
  })
})
