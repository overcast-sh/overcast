import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { parquetMetadata, type AsyncBuffer } from "hyparquet"
import { formatCell } from "@/components/data-grid/cell-format"
import { HttpReadError } from "./http-read"
import { openParquet, readParquetRows, schedulerAsyncBuffer } from "./parquet-reader"
import { RangeScheduler } from "./range-scheduler"

// orders.parquet: 5 rows in two row groups (3 + 2), one column per type
// family; orders.zstd.parquet: the same rows as ZSTD. Regenerate both with
// `node scripts/generate-parquet-fixture.mjs`.
// Copied into this realm's ArrayBuffer: hyparquet checks `instanceof
// ArrayBuffer`, and under jsdom a Node Buffer's backing store is not one.
function load(name: string): ArrayBuffer {
  const file = readFileSync(resolve(__dirname, "__fixtures__", name))
  const copy = new ArrayBuffer(file.byteLength)
  new Uint8Array(copy).set(file)
  return copy
}
const bytes = load("orders.parquet")
const meta = parquetMetadata(bytes)

/** An in-memory AsyncBuffer that records every range it is asked for. */
function recordingBuffer(source: ArrayBuffer = bytes) {
  const reads: [number, number][] = []
  const file: AsyncBuffer = {
    byteLength: source.byteLength,
    slice(start, end = source.byteLength) {
      reads.push([start, end])
      return source.slice(start, end)
    },
  }
  return { file, reads }
}

/** The byte span of one column chunk. */
function chunkSpan(group: number, column: number): [number, number] {
  const m = meta.row_groups[group].columns[column].meta_data
  if (!m) throw new Error("no column metadata")
  const start = Number(m.dictionary_page_offset ?? m.data_page_offset)
  return [start, start + Number(m.total_compressed_size)]
}

const overlaps = ([a, b]: [number, number], [c, d]: [number, number]) => a < d && c < b

describe("openParquet", () => {
  it("reads the schema, row count and codecs from the footer", async () => {
    const { head } = await openParquet(recordingBuffer().file)
    expect(head.fields).toEqual([
      { name: "order_id", type: "INT64", nullable: false },
      { name: "customer", type: "STRING", nullable: true },
      { name: "amount", type: "DECIMAL(10,2)", nullable: true },
      { name: "quantity", type: "INT32", nullable: false },
      { name: "discount", type: "DOUBLE", nullable: true },
      { name: "paid", type: "BOOLEAN", nullable: true },
      { name: "ordered_at", type: "TIMESTAMP(MICROS, UTC)", nullable: true },
      { name: "ship_date", type: "DATE", nullable: true },
      { name: "tags", type: "LIST<STRING>", nullable: true },
      { name: "address", type: "STRUCT<city, zip>", nullable: true },
    ])
    expect(head.numRows).toBe(5)
    expect(head.rowGroups).toBe(2)
    expect(head.codecs).toEqual(["SNAPPY"])
    expect(head.rowsError).toBeUndefined()
    expect(head.columns.filter((c) => c.numeric).map((c) => c.name)).toEqual([
      "order_id",
      "amount",
      "quantity",
      "discount",
    ])
  })

  it("reads only the footer to open: a suffix, then the exact metadata length", async () => {
    const { file, reads } = recordingBuffer()
    await openParquet(file, { footerFetchBytes: 8 })
    expect(reads).toEqual([
      [bytes.byteLength - 8, bytes.byteLength],
      [bytes.byteLength - 8 - meta.metadata_length, bytes.byteLength - 8],
    ])
  })

  it("rejects a file that is not Parquet", async () => {
    const notParquet = new TextEncoder().encode("id,name\n1,Ada\n")
    const copy = new ArrayBuffer(notParquet.byteLength)
    new Uint8Array(copy).set(notParquet)
    await expect(openParquet(recordingBuffer(copy).file)).rejects.toThrow(/PAR1/)
  })
})

describe("readParquetRows", () => {
  it("reads the requested rows of the requested columns, and no other column chunk", async () => {
    const { file, reads } = recordingBuffer()
    const { metadata } = await openParquet(file, { footerFetchBytes: 8 })
    reads.length = 0
    const [customer] = await readParquetRows(file, metadata, 3, 5, ["customer"])
    expect(customer).toEqual(["Alan Turing", "Edsger Dijkstra"])
    // Rows 3–4 live in the second row group: its `customer` chunk is read,
    // and nothing of the first group, nor any other column of the second.
    expect(reads.some((r) => overlaps(r, chunkSpan(1, 1)))).toBe(true)
    for (let c = 0; c < meta.row_groups[0].columns.length; c++) {
      expect(
        reads.some((r) => overlaps(r, chunkSpan(0, c))),
        `group 0, column ${c}`,
      ).toBe(false)
      if (c !== 1) {
        expect(
          reads.some((r) => overlaps(r, chunkSpan(1, c))),
          `group 1, column ${c}`,
        ).toBe(false)
      }
    }
  })

  it("hands each column over the moment it decodes", async () => {
    const { file } = recordingBuffer()
    const { metadata } = await openParquet(file)
    const arrived: number[] = []
    await readParquetRows(file, metadata, 0, 3, ["customer", "quantity"], (i) => arrived.push(i))
    expect(arrived.sort()).toEqual([0, 1])
  })

  it("hands each type over in a form the grid renders faithfully", async () => {
    const { file } = recordingBuffer()
    const { metadata, head } = await openParquet(file)
    const names = head.columns.map((c) => c.name)
    const columns = await readParquetRows(file, metadata, 0, 3, names)
    const text = (row: number) =>
      columns.map((values, i) => formatCell(values[row], head.columns[i]).text)
    expect(text(0)).toEqual([
      "1001",
      "Ada Lovelace",
      "19.99",
      "1",
      "0",
      "true",
      "2026-09-01T08:15:00.000Z",
      "2026-09-03",
      '["gift","express"]',
      '{"city":"London","zip":"N1 9GU"}',
    ])
    // Row 3 has the nulls: a null string, list and struct all read as NULL.
    expect(columns.map((values, i) => formatCell(values[2], head.columns[i]).kind)).toEqual([
      "value",
      "null",
      "value",
      "value",
      "null",
      "value",
      "value",
      "null",
      "null",
      "null",
    ])
  })

  it("reads ZSTD rows, the codec Iceberg and S3 Tables write by default", async () => {
    const { file } = recordingBuffer(load("orders.zstd.parquet"))
    const { metadata, head } = await openParquet(file)
    expect(head.codecs).toEqual(["ZSTD"])
    expect(head.rowsError).toBeUndefined()
    const [customer, amount] = await readParquetRows(file, metadata, 0, 3, ["customer", "amount"])
    expect(customer).toEqual(["Ada Lovelace", "Grace Hopper", null])
    expect(formatCell(amount[0], head.columns[2]).text).toBe("19.99")
  })

  it("keeps the schema and names a codec it cannot decode", async () => {
    // Written here rather than committed: Node's zlib has a Brotli encoder,
    // and the console deliberately ships no Brotli decoder.
    const { ByteWriter, parquetWrite } = await import("hyparquet-writer")
    const { brotliCompressSync } = await import("node:zlib")
    const writer = new ByteWriter()
    await parquetWrite({
      writer,
      codec: "BROTLI",
      compressors: { BROTLI: (input: Uint8Array) => new Uint8Array(brotliCompressSync(input)) },
      columnData: [{ name: "id", data: [1, 2, 3], type: "INT32" }],
    })
    const written = new Uint8Array(writer.getBuffer())
    const brotli = new ArrayBuffer(written.byteLength)
    new Uint8Array(brotli).set(written)
    const { head } = await openParquet(recordingBuffer(brotli).file)
    expect(head.fields).toEqual([{ name: "id", type: "INT32", nullable: true }])
    expect(head.rowsError).toMatch(/compressed with BROTLI/)
  })
})

describe("schedulerAsyncBuffer", () => {
  const over = (fetchImpl: typeof fetch, size: number) =>
    schedulerAsyncBuffer(new RangeScheduler("/obj", fetchImpl), size)

  it("asks for exactly the slice as an inclusive byte range", async () => {
    // Given: a server that answers any range with three bytes
    const fetchImpl = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response(new Uint8Array([1, 2, 3]), { status: 206 })),
    )
    // When: hyparquet slices bytes 10 to 13
    const slice = await over(fetchImpl, 100).slice(10, 13)
    // Then: one request for bytes=10-12, and its bytes as an ArrayBuffer
    expect(new Uint8Array(slice)).toEqual(new Uint8Array([1, 2, 3]))
    expect(new Headers(fetchImpl.mock.calls[0][1]?.headers).get("Range")).toBe("bytes=10-12")
  })

  it("cuts the slice out of a server that ignored the range and sent everything", async () => {
    const whole = Uint8Array.from({ length: 20 }, (_, i) => i)
    const fetchImpl: typeof fetch = () => Promise.resolve(new Response(whole, { status: 200 }))
    const slice = await over(fetchImpl, 20).slice(5, 8)
    expect(new Uint8Array(slice)).toEqual(new Uint8Array([5, 6, 7]))
  })

  it("carries the HTTP status on a failed read", async () => {
    const fetchImpl: typeof fetch = () => Promise.resolve(new Response("", { status: 403 }))
    await expect(over(fetchImpl, 20).slice(0, 4)).rejects.toEqual(new HttpReadError(403))
  })
})
