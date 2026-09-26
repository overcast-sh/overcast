import {
  openAthenaResultSource,
  PAGED_RESULT_ROWS,
  type AthenaResult,
} from "./athena-result-source"
import type { DeclaredColumns } from "./declared-columns"
import { bytesObject, fakeFetch, syntheticCsv, type ByteSource } from "./testing/fake-object"
import { readRows } from "./testing/source-helpers"
import { inProcessDataWorker } from "./worker-port"

/** The synthetic CSV's columns, as `ColumnInfo` declares them. */
const COLUMNS = [
  { name: "id", type: "bigint", numeric: true },
  { name: "name", type: "varchar", numeric: false },
  { name: "amount", type: "double", numeric: true },
]

/** A stand-in for the Athena mapping: numbers for numeric columns, NULL for `null`. */
const SCHEMA: DeclaredColumns = {
  columns: COLUMNS,
  value: (text, index) => (text === null ? null : COLUMNS[index].numeric ? Number(text) : text),
}

/** A result of `rows` rows: two GetQueryResults pages of it, and its CSV. */
function athenaResult(
  rows: number,
  rowCount: number | undefined,
  csv: ByteSource = syntheticCsv(rows),
): AthenaResult & {
  pages: (string | undefined)[]
  fake: ReturnType<typeof fakeFetch>
} {
  const fake = fakeFetch(csv)
  const pages: (string | undefined)[] = []
  const half = Math.ceil(rows / 2)
  return {
    rowCount,
    schema: SCHEMA,
    pages,
    fake,
    output: { url: "/results/query.csv", size: csv.size },
    readPage(token) {
      pages.push(token)
      const from = token ? half : 0
      const to = token ? rows : half
      return Promise.resolve({
        columns: COLUMNS,
        rows: Array.from({ length: to - from }, (_, i) => [from + i, `row ${from + i}`, 1]),
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
  ])("reads a result %s from its CSV, by range, typed as ColumnInfo says", async (_, rowCount) => {
    // Given: a result too large to page, or with no Statistics
    const result = athenaResult(PAGED_RESULT_ROWS + 1, rowCount)
    // When: it is opened
    const source = await openAthenaResultSource(result, {
      spawnWorker: () => inProcessDataWorker(result.fake.fetch),
    })
    // Then: no page was read; the CSV at OutputLocation serves the rows, typed
    expect(result.pages).toEqual([])
    expect(source.columns).toEqual(COLUMNS)
    expect((await readRows(source, 7, 8)).columns).toEqual([[7], ["hhhhhhhh"], [10002.59]])
    source.dispose()
  })

  it("reads an unquoted empty field in Athena's CSV as a NULL, and a quoted one as empty", async () => {
    // Given: the CSV Athena writes, every value quoted, a NULL left empty
    const csv = bytesObject('"id","name","amount"\n"1","",\n,,"2.5"\n')
    const result = athenaResult(2, undefined, csv)
    // When: it is opened
    const source = await openAthenaResultSource(result, {
      spawnWorker: () => inProcessDataWorker(result.fake.fetch),
    })
    // Then: NULLs and empty strings stay apart, as on the paged path
    expect((await readRows(source, 0, 2)).columns).toEqual([
      [1, null],
      ["", null],
      [null, 2.5],
    ])
    source.dispose()
  })

  it("keeps the columns a CSV infers when it is not as wide as ColumnInfo says", async () => {
    // Given: a CSV of one column, where ColumnInfo declares three
    const result = athenaResult(1, undefined, bytesObject('"n"\n"1"\n'))
    // When: it is opened
    const source = await openAthenaResultSource(result, {
      spawnWorker: () => inProcessDataWorker(result.fake.fetch),
    })
    // Then: the file is read as it is, untyped
    expect(source.columns.map((column) => [column.name, column.type])).toEqual([["n", undefined]])
    expect((await readRows(source, 0, 1)).columns).toEqual([["1"]])
    source.dispose()
  })
})
