import { BlockCache } from "@/components/data-grid/block-cache"
import { formatCount } from "@/lib/format"
import { BLOCK_ROWS } from "./base-source"
import { DEFAULT_CACHE_BYTES } from "./device-profile"
import { openParquetSource } from "./parquet-source"
import type { RowSource } from "./row-source"
import { openTextSource } from "./text-source"
import { bytesObject, fakeFetch, syntheticCsv } from "./testing/fake-object"
import { indexSettled } from "./testing/source-helpers"
import { inProcessDataWorker } from "./worker-port"

/**
 * The budgets in *Large data in the browser* (`docs/plans/data-lake-console.md`),
 * against files generated here and never committed: a five-million-row CSV
 * (140 MB, generated a range at a time, never held) and a 200-column Parquet
 * file. The numbers are printed so the PR can quote them.
 *
 * The first-rows budget is 300 ms. The workers run in-process here, on the
 * same thread as the test, so these timings include the work a real worker
 * would take off the main thread — they are an upper bound on it.
 */

const report: string[] = []
afterAll(() => {
  console.info(`\nData grid budgets\n${report.map((line) => `  ${line}`).join("\n")}\n`)
})

describe("budgets: a five-million-row CSV", () => {
  const ROWS = 5_000_000
  const csv = syntheticCsv(ROWS)
  const fake = fakeFetch(csv, { chunk: 256 * 1024 })
  let source: RowSource

  afterAll(() => source.dispose())

  it("shows its first rows in under 300 ms, from two requests", async () => {
    const began = performance.now()
    source = await openTextSource({
      url: "/big.csv",
      size: csv.size,
      kind: "csv",
      port: inProcessDataWorker(fake.fetch),
    })
    const first = await source.getRows(0, 40, [], new AbortController().signal)
    const elapsed = performance.now() - began
    report.push(
      `CSV ${formatCount(ROWS)} rows (${(csv.size / 1e6).toFixed(0)} MB): first rows in ${elapsed.toFixed(0)} ms`,
    )
    expect(first.columns[0]?.[39]).toBe("000000039")
    expect(elapsed).toBeLessThan(300)
    // The first 64 KB by range, and the indexer's stream — nothing else.
    expect(fake.requests).toHaveLength(2)
    expect(fake.requests.filter((r) => r.range)).toEqual([{ range: [0, 64 * 1024] }])
  })

  it("indexes the whole file into a few thousand offsets", async () => {
    const began = performance.now()
    await indexSettled(source)
    const elapsed = performance.now() - began
    report.push(
      `CSV: indexed ${formatCount(ROWS)} rows in ${(elapsed / 1000).toFixed(1)} s (in-process)`,
    )
    expect(source.rowCount).toEqual({ value: ROWS, exact: true })
  }, 120_000)

  it("reads any block anywhere with one ranged request", async () => {
    const before = fake.requests.length
    const positions = [4_000_000, 17_000, 2_500_000, ROWS - BLOCK_ROWS]
    let worst = 0
    for (const row of positions) {
      const began = performance.now()
      const block = await source.getRows(row, row + BLOCK_ROWS, [], new AbortController().signal)
      worst = Math.max(worst, performance.now() - began)
      expect(block.columns[0]?.[0]).toBe(String(row).padStart(9, "0"))
    }
    const requests = fake.requests.length - before
    report.push(
      `CSV: ${positions.length} jumps anywhere in the file, ${requests} requests, slowest block ${worst.toFixed(0)} ms`,
    )
    expect(requests).toBe(positions.length)
  })

  it("stays within the block cache's budget however far it is scrolled", async () => {
    const budget = DEFAULT_CACHE_BYTES
    const cache = new BlockCache(budget)
    const heapBefore = process.memoryUsage().heapUsed
    // Top to bottom in 250 stops: every block the scroll would have pinned.
    for (let stop = 0; stop < 250; stop++) {
      const block = Math.floor((stop / 250) * (ROWS / BLOCK_ROWS))
      cache.pin([block])
      cache.set(
        block,
        await source.getRows(
          block * BLOCK_ROWS,
          (block + 1) * BLOCK_ROWS,
          [],
          new AbortController().signal,
        ),
      )
    }
    const heapAfter = process.memoryUsage().heapUsed
    report.push(
      `CSV: after 250 scroll stops the cache holds ${(cache.bytes / 1e6).toFixed(1)} MB of a ${(budget / 1e6).toFixed(0)} MB budget; heap grew ${((heapAfter - heapBefore) / 1e6).toFixed(1)} MB`,
    )
    expect(cache.bytes).toBeLessThanOrEqual(budget)
    expect(heapAfter - heapBefore).toBeLessThan(budget * 2)
  }, 60_000)
})

describe("budgets: a 200-column Parquet file", () => {
  const COLUMNS = 200
  const ROWS = 20_000
  let object: ReturnType<typeof bytesObject>

  beforeAll(async () => {
    const { ByteWriter, parquetWrite } = await import("hyparquet-writer")
    const writer = new ByteWriter()
    await parquetWrite({
      writer,
      rowGroupSize: 5_000,
      columnData: Array.from({ length: COLUMNS }, (_, c) => ({
        name: `c${String(c).padStart(3, "0")}`,
        data: Int32Array.from({ length: ROWS }, (_, r) => r * COLUMNS + c),
        type: "INT32" as const,
      })),
    })
    object = bytesObject(new Uint8Array(writer.getBuffer()))
  }, 60_000)

  it("opens from its footer in under 300 ms and reads only the columns in view", async () => {
    const fake = fakeFetch(object)
    const began = performance.now()
    const source = await openParquetSource({
      url: "/wide.parquet",
      size: object.size,
      port: inProcessDataWorker(fake.fetch),
    })
    const opened = performance.now() - began
    const openRequests = fake.requests.length

    // Eight visible columns of rows 12,000–13,000.
    const visible = [0, 1, 2, 3, 4, 5, 6, 7]
    const readBegan = performance.now()
    const block = await source.getRows(12_000, 13_000, visible, new AbortController().signal)
    const read = performance.now() - readBegan
    const readRequests = fake.requests.length - openRequests
    report.push(
      `Parquet ${formatCount(COLUMNS)} columns × ${formatCount(ROWS)} rows (${(object.size / 1e6).toFixed(1)} MB): opened in ${opened.toFixed(0)} ms with ${openRequests} request(s); 8 columns × 1,000 rows in ${read.toFixed(0)} ms with ${readRequests} request(s), ${(fake.rangedBytes() / 1e3).toFixed(0)} KB read in all`,
    )
    expect(block.columns[3]?.[0]).toBe(12_000 * COLUMNS + 3)
    expect(block.columns[8]).toBeUndefined()
    expect(opened).toBeLessThan(300)
    expect(openRequests).toBeLessThanOrEqual(2)
    // Adjacent column chunks coalesce: one request, not eight.
    expect(readRequests).toBeLessThanOrEqual(2)
    expect(fake.rangedBytes()).toBeLessThan(object.size / 4)
    source.dispose()
  })
})

describe("budgets: a Parquet file with one large row group and no page index", () => {
  // Large enough that each column chunk (9.6 MB) is past what the raw byte
  // cache keeps, so only decoding it once can keep scrolling from refetching it.
  const ROWS = 1_200_000
  let object: ReturnType<typeof bytesObject>

  beforeAll(async () => {
    // What pyarrow and Spark write by default: no offset index, so the
    // smallest read is a whole column chunk.
    const { ByteWriter, parquetWrite } = await import("hyparquet-writer")
    const writer = new ByteWriter()
    await parquetWrite({
      writer,
      rowGroupSize: ROWS,
      // Numbers: jsdom's TextEncoder answers in another realm's Uint8Array,
      // which the writer refuses for strings.
      columnData: ["a", "b"].map((name, c) => ({
        name,
        type: "DOUBLE" as const,
        offsetIndex: false,
        data: Float64Array.from({ length: ROWS }, (_, r) => r + c / 10),
      })),
    })
    object = bytesObject(new Uint8Array(writer.getBuffer()))
  }, 60_000)

  it("decodes each column chunk once, however many blocks are scrolled through", async () => {
    // Given: the file open, and its first block read
    const fake = fakeFetch(object)
    const source = await openParquetSource({
      url: "/big-group.parquet",
      size: object.size,
      port: inProcessDataWorker(fake.fetch),
    })
    const signal = new AbortController().signal
    await source.getRows(0, BLOCK_ROWS, [0, 1], signal)
    const firstBlock = fake.rangedBytes()
    // When: the next nine blocks are scrolled through
    for (let block = 1; block < 10; block++) {
      await source.getRows(block * BLOCK_ROWS, (block + 1) * BLOCK_ROWS, [0, 1], signal)
    }
    const tenBlocks = fake.rangedBytes()
    report.push(
      `Parquet, one ${formatCount(ROWS)}-row group without a page index: first block ${(firstBlock / 1e3).toFixed(0)} KB, ten blocks ${(tenBlocks / 1e3).toFixed(0)} KB`,
    )
    // Then: nothing more was fetched after the chunks the first block needed
    expect(tenBlocks).toBe(firstBlock)
    source.dispose()
  })
})
