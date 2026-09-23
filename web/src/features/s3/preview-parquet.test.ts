import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { parquetMetadata, type AsyncBuffer } from "hyparquet"
import { formatPreviewCell } from "./preview-table"
import { rangeAsyncBuffer, readParquetPreview } from "./preview-parquet"

// orders.parquet: 5 rows in two row groups (3 + 2), one column per type
// family. Regenerate with `node scripts/generate-parquet-fixture.mjs`.
// Copied into this realm's ArrayBuffer: hyparquet checks `instanceof
// ArrayBuffer`, and under jsdom a Node Buffer's backing store is not one.
function load(name: string): ArrayBuffer {
  const file = readFileSync(resolve(__dirname, "__fixtures__", name))
  const copy = new ArrayBuffer(file.byteLength)
  new Uint8Array(copy).set(file)
  return copy
}
const bytes = load("orders.parquet")

const metadataLength = () => parquetMetadata(bytes).metadata_length

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

describe("readParquetPreview", () => {
  it("reads the schema with each column's type and nullability", async () => {
    const preview = await readParquetPreview(recordingBuffer().file)
    expect(preview.fields).toEqual([
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
    expect(preview.numRows).toBe(5)
    expect(preview.rowGroups).toBe(2)
    expect(preview.codecs).toEqual(["SNAPPY"])
  })

  it("reads rows from the first row group only", async () => {
    // The fixture is smaller than the footer's opening read, which would
    // otherwise fetch it whole. An 8-byte opening read is how a large file
    // looks from the footer: the metadata is a second request, the data a
    // third.
    const { file, reads } = recordingBuffer()
    const preview = await readParquetPreview(file, { footerFetchBytes: 8 })
    expect(preview.table?.rows).toHaveLength(3)
    expect(preview.table?.totalRows).toBe(5)

    // The second row group's column chunks start where the first group's
    // bytes end; nothing past the footer's metadata and the first group may
    // have been fetched from that region.
    const meta = parquetMetadata(bytes)
    const second = meta.row_groups[1].columns.map((c) => Number(c.meta_data?.data_page_offset))
    const secondStart = Math.min(...second)
    const footerStart = bytes.byteLength - 8 - meta.metadata_length
    for (const [start, end] of reads) {
      const touchesSecondGroup = start < footerStart && end > secondStart
      expect(touchesSecondGroup, `read ${start}-${end}`).toBe(false)
    }
    // …and the first group's data really was read, by range, not skipped.
    expect(reads.some(([start]) => start < secondStart)).toBe(true)
  })

  it("hands each type to the table in a form it renders faithfully", async () => {
    const preview = await readParquetPreview(recordingBuffer().file)
    const table = preview.table
    if (!table) throw new Error(preview.rowsError)
    const text = (row: number) =>
      table.rows[row].map((value, i) => formatPreviewCell(value, table.columns[i]).text)
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
    // Row 3 is the one with the nulls: a null string, list and struct all
    // read as NULL, not as an empty cell.
    expect(table.rows[2].map((v, i) => formatPreviewCell(v, table.columns[i]).kind)).toEqual([
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

  it("right-aligns numbers and decimals but not dates or nested values", async () => {
    const preview = await readParquetPreview(recordingBuffer().file)
    expect(preview.table?.columns.filter((c) => c.numeric).map((c) => c.name)).toEqual([
      "order_id",
      "amount",
      "quantity",
      "discount",
    ])
  })

  it("keeps to the row limit it is given", async () => {
    const preview = await readParquetPreview(recordingBuffer().file, { maxRows: 2 })
    expect(preview.table?.rows).toHaveLength(2)
  })

  it("rejects a file that is not Parquet", async () => {
    const notParquet = new TextEncoder().encode("id,name\n1,Ada\n").buffer
    await expect(readParquetPreview(recordingBuffer(notParquet).file)).rejects.toThrow(/PAR1/)
  })

  it("keeps the schema and explains the rows when the first row group is too big", async () => {
    const { file, reads } = recordingBuffer()
    const preview = await readParquetPreview(file, { maxRowGroupBytes: 64, footerFetchBytes: 8 })
    expect(preview.fields).toHaveLength(10)
    expect(preview.table).toBeUndefined()
    expect(preview.rowsError).toMatch(/first row group is .* over the preview's .* limit/)
    // Declined from the footer alone: nothing before the metadata was read.
    const footerStart = bytes.byteLength - 8 - metadataLength()
    expect(reads.every(([start]) => start >= footerStart)).toBe(true)
  })
})

describe("readParquetPreview > codecs", () => {
  it("reads ZSTD rows, the codec Iceberg and S3 Tables write by default", async () => {
    const preview = await readParquetPreview(recordingBuffer(load("orders.zstd.parquet")).file)
    expect(preview.codecs).toEqual(["ZSTD"])
    expect(preview.rowsError).toBeUndefined()
    const table = preview.table
    if (!table) throw new Error("no rows")
    expect(table.rows.map((r) => formatPreviewCell(r[1], table.columns[1]).text)).toEqual([
      "Ada Lovelace",
      "Grace Hopper",
      "NULL",
    ])
    expect(formatPreviewCell(table.rows[0][2], table.columns[2]).text).toBe("19.99")
  })

  it("keeps the schema and names a codec it cannot decode", async () => {
    // Written here rather than committed: Node's zlib has a Brotli encoder,
    // and the preview deliberately ships no Brotli decoder.
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

    const preview = await readParquetPreview(recordingBuffer(brotli).file)
    expect(preview.fields).toEqual([{ name: "id", type: "INT32", nullable: true }])
    expect(preview.table).toBeUndefined()
    expect(preview.rowsError).toMatch(/compressed with BROTLI/)
  })
})

describe("rangeAsyncBuffer", () => {
  it("asks for exactly the slice as an inclusive byte range", async () => {
    const fetchImpl = vi.fn(() =>
      Promise.resolve(new Response(new Uint8Array([1, 2, 3]), { status: 206 })),
    ) as unknown as typeof fetch
    const buffer = rangeAsyncBuffer("/obj", 100, fetchImpl)
    const slice = await buffer.slice(10, 13)
    expect(new Uint8Array(slice)).toEqual(new Uint8Array([1, 2, 3]))
    expect(fetchImpl).toHaveBeenCalledWith("/obj", { headers: { Range: "bytes=10-12" } })
  })

  it("cuts the slice out of a server that ignored the range and sent everything", async () => {
    const whole = Uint8Array.from({ length: 20 }, (_, i) => i)
    const fetchImpl = (() =>
      Promise.resolve(new Response(whole, { status: 200 }))) as unknown as typeof fetch
    const slice = await rangeAsyncBuffer("/obj", 20, fetchImpl).slice(5, 8)
    expect(new Uint8Array(slice)).toEqual(new Uint8Array([5, 6, 7]))
  })

  it("carries the HTTP status on a failed read", async () => {
    const fetchImpl = (() =>
      Promise.resolve(new Response("", { status: 403 }))) as unknown as typeof fetch
    await expect(rangeAsyncBuffer("/obj", 20, fetchImpl).slice(0, 4)).rejects.toMatchObject({
      status: 403,
    })
  })
})
