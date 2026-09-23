import {
  openAthenaResultSource,
  PAGED_RESULT_ROWS,
  type AthenaResult,
} from "./athena-result-source"
import { fakeFetch, syntheticCsv } from "./testing/fake-object"
import { readRows } from "./testing/source-helpers"
import { inProcessDataWorker } from "./worker-port"

const COLUMNS = [
  { name: "id", type: "bigint", numeric: true },
  { name: "name", type: "varchar", numeric: false },
]

/** A result of `rows` rows: two GetQueryResults pages of it, and its CSV. */
function athenaResult(
  rows: number,
  rowCount: number | undefined,
): AthenaResult & {
  pages: (string | undefined)[]
  fake: ReturnType<typeof fakeFetch>
} {
  const csv = syntheticCsv(rows)
  const fake = fakeFetch(csv)
  const pages: (string | undefined)[] = []
  const half = Math.ceil(rows / 2)
  return {
    rowCount,
    pages,
    fake,
    output: { url: "/results/query.csv", size: csv.size },
    readPage(token) {
      pages.push(token)
      const from = token ? half : 0
      const to = token ? rows : half
      return Promise.resolve({
        columns: COLUMNS,
        rows: Array.from({ length: to - from }, (_, i) => [from + i, `row ${from + i}`]),
        nextToken: token ? undefined : "page-2",
      })
    },
  }
}

describe("openAthenaResultSource", () => {
  it("pages a small result through GetQueryResults into memory, typed", async () => {
    // Given: a result Statistics says has 10 rows
    const result = athenaResult(10, 10)
    const spawnWorker = vi.fn(() => inProcessDataWorker(result.fake.fetch))
    // When: it is opened
    const source = await openAthenaResultSource(result, { spawnWorker })
    // Then: both pages were read, no worker was spawned, and the types came along
    expect(result.pages).toEqual([undefined, "page-2"])
    expect(spawnWorker).not.toHaveBeenCalled()
    expect(source.columns).toEqual(COLUMNS)
    expect((await readRows(source, 9, 10)).columns[1]).toEqual(["row 9"])
  })

  it.each([
    ["larger than a few pages", PAGED_RESULT_ROWS + 1],
    ["of unknown size", undefined],
  ])("reads a result %s from its CSV, by range", async (_, rowCount) => {
    // Given: a result too large to page, or with no Statistics
    const result = athenaResult(PAGED_RESULT_ROWS + 1, rowCount)
    // When: it is opened
    const source = await openAthenaResultSource(result, {
      spawnWorker: () => inProcessDataWorker(result.fake.fetch),
    })
    // Then: no page was read; the CSV at OutputLocation serves the rows
    expect(result.pages).toEqual([])
    expect((await readRows(source, 0, 1)).columns[0]).toEqual(["000000000"])
    source.dispose()
  })
})
