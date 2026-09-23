import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { openParquetSource } from "./parquet-source"
import { bytesObject, fakeFetch, type ByteSource } from "./testing/fake-object"
import { inProcessDataWorker } from "./worker-port"

const fixture = (name: string) =>
  bytesObject(new Uint8Array(readFileSync(resolve(__dirname, "__fixtures__", name))))

function openParquet(object: ByteSource) {
  const fake = fakeFetch(object)
  const opening = openParquetSource({
    url: "/obj",
    size: object.size,
    port: inProcessDataWorker(fake.fetch),
  })
  return { fake, opening }
}

describe("openParquetSource", () => {
  it("knows the exact row count and the schema from the footer alone", async () => {
    const source = await openParquet(fixture("orders.parquet")).opening
    expect(source.rowCount).toEqual({ value: 5, exact: true })
    expect(source.info.fields[0]).toEqual({ name: "order_id", type: "INT64", nullable: false })
  })

  it("reads only the requested columns of the requested rows", async () => {
    // Given: an open file
    const source = await openParquet(fixture("orders.parquet")).opening
    // When: rows 3–4 of the customer column are read
    const block = await source.getRows(3, 5, [1], new AbortController().signal)
    // Then: that column arrives, and no other
    expect(block.columns[1]).toEqual(["Alan Turing", "Edsger Dijkstra"])
    expect(block.columns[0]).toBeUndefined()
  })

  it("hands a column over before the whole block resolves", async () => {
    const source = await openParquet(fixture("orders.parquet")).opening
    const partial = vi.fn()
    await source.getRows(0, 3, [1, 3], new AbortController().signal, partial)
    expect(partial.mock.calls.map(([block]) => block.columns.findIndex(Boolean)).sort()).toEqual([
      1, 3,
    ])
  })

  it("decodes ZSTD", async () => {
    const source = await openParquet(fixture("orders.zstd.parquet")).opening
    const block = await source.getRows(0, 2, [1], new AbortController().signal)
    expect(block.columns[1]).toEqual(["Ada Lovelace", "Grace Hopper"])
  })

  it("rejects a file that is not Parquet", async () => {
    await expect(openParquet(bytesObject("id,name\n1,Ada\n")).opening).rejects.toThrow(/PAR1/)
  })
})
