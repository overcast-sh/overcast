import { runIndex, type IndexRunOptions } from "./index-run"
import { RangeScheduler } from "./range-scheduler"
import { RecordIndexer } from "./record-indexer"
import { fakeFetch, syntheticCsv } from "./testing/fake-object"

const csv = syntheticCsv(10_000)

function options(overrides: Partial<IndexRunOptions> = {}) {
  const fake = fakeFetch(csv, { chunk: 8 * 1024 })
  const scheduler = new RangeScheduler("/obj", fake.fetch)
  const indexer = new RecordIndexer({ every: 1000, delimiter: 0x2c, header: true })
  const run: IndexRunOptions = {
    fetchImpl: fake.fetch,
    scheduler,
    indexer: Promise.resolve(indexer),
    from: 0,
    size: csv.size,
    byteLimit: Infinity,
    signal: new AbortController().signal,
    onProgress: () => {},
    ...overrides,
  }
  return { run, fake, indexer }
}

describe("runIndex", () => {
  it("indexes the whole object and finishes the last record", async () => {
    const { run, indexer } = options()
    await expect(runIndex(run)).resolves.toBe("done")
    expect(indexer.rows).toBe(10_000)
    expect(indexer.offsets).toHaveLength(10)
  })

  it("pauses once it has read its byte budget", async () => {
    const { run, indexer } = options({ byteLimit: 64 * 1024 })
    await expect(runIndex(run)).resolves.toBe("paused-limit")
    expect(indexer.bytes).toBeLessThan(csv.size)
  })

  it("resumes with a ranged read from where the last run stopped", async () => {
    // Given: a run paused at its byte limit
    const { run, indexer, fake } = options({ byteLimit: 64 * 1024 })
    await runIndex(run)
    const stoppedAt = indexer.bytes
    // When: a second run starts from there
    await expect(runIndex({ ...run, from: stoppedAt, byteLimit: Infinity })).resolves.toBe("done")
    // Then: it asked for the rest of the object only, and the index is whole
    expect(fake.requests.at(-1)).toEqual({ range: [stoppedAt, csv.size] })
    expect(indexer.rows).toBe(10_000)
  })

  it("stops on demand once the index covers the rows asked for", async () => {
    const { run, indexer } = options({ untilRows: 2_000 })
    await expect(runIndex(run)).resolves.toBe("on-demand")
    expect(indexer.rows).toBeGreaterThanOrEqual(2_000)
    expect(indexer.rows).toBeLessThan(10_000)
  })

  it("holds one of the scheduler's request slots only while it runs", async () => {
    // Given: a scheduler allowing a single request in flight
    const { run, fake } = options()
    const scheduler = new RangeScheduler("/obj", fake.fetch, { maxInFlight: 1 })
    // When: a run completes
    await runIndex({ ...run, scheduler })
    // Then: the slot is free again for a ranged read
    await expect(scheduler.read(0, 10)).resolves.toHaveLength(10)
  })
})
