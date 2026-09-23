import { RangeScheduler } from "./range-scheduler"
import { TextFile } from "./text-file"
import { fakeFetch, syntheticCsv } from "./testing/fake-object"
import type { IndexProgress, OpenText } from "./worker-protocol"

const csv = syntheticCsv(50_000)

function openFile(
  overrides: Partial<OpenText> = {},
  wrap: (fetchImpl: typeof fetch) => typeof fetch = (fetchImpl) => fetchImpl,
) {
  const fake = fakeFetch(csv, { chunk: 16 * 1024 })
  const fetchImpl = wrap(fake.fetch)
  const progress: IndexProgress[] = []
  const file = new TextFile({
    open: {
      type: "open-text",
      id: 1,
      url: "/obj",
      size: csv.size,
      kind: "csv",
      every: 1000,
      byteLimit: Infinity,
      mode: "background",
      untilRows: 0,
      ...overrides,
    },
    scheduler: new RangeScheduler("/obj", fetchImpl),
    fetchImpl,
    onProgress: (report) => progress.push(report),
  })
  const settled = (state: IndexProgress["state"]) =>
    new Promise<IndexProgress>((resolve) => {
      const check = () => {
        const last = progress.at(-1)
        if (last?.state === state) resolve(last)
        else setTimeout(check, 5)
      }
      check()
    })
  return { file, fake, progress, settled }
}

describe("TextFile", () => {
  it("reads the head and indexes the whole file in the background", async () => {
    const { file, settled } = openFile()
    const head = await file.open(new AbortController().signal)
    expect(head.columns.map((column) => column.name)).toEqual(["id", "name", "amount"])
    expect((await settled("done")).rows).toBe(50_000)
  })

  it("on demand, reads on to the furthest target asked for while a run was reading", async () => {
    // Given: Save-Data, the index resting at its first target
    const { file, settled } = openFile({ mode: "on-demand", untilRows: 2_000 })
    await file.open(new AbortController().signal)
    await settled("on-demand")
    // When: a nearer and then a further target are asked for in the same turn
    file.resume(4_000)
    file.resume(30_000)
    // Then: the index does not rest until it covers the further one
    let last = await settled("on-demand")
    while (last.rows < 30_000) {
      await new Promise((resolve) => setTimeout(resolve, 20))
      last = await settled("on-demand")
    }
    expect(last.rows).toBeGreaterThanOrEqual(30_000)
  })

  it("stops its index stream when the head cannot be read", async () => {
    // Given: a server that refuses ranged reads but streams the whole object
    const { file, fake } = openFile(
      {},
      (fetchImpl) => (input, init) =>
        new Headers(init?.headers).has("Range")
          ? Promise.resolve(new Response(null, { status: 403 }))
          : fetchImpl(input, init),
    )
    // When: it is opened
    await expect(file.open(new AbortController().signal)).rejects.toThrow("HTTP 403")
    await new Promise((resolve) => setTimeout(resolve, 20))
    // Then: the stream it started alongside is not left downloading the file
    expect(fake.streamedBytes()).toBeLessThan(csv.size)
  })
})
