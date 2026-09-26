import { queryRun, startQueryCall, startQueryInput } from "./query-input"
import { createQueryTab } from "./query-tabs"

const sql = "SELECT * FROM orders WHERE id = ? AND region = ?"
const tab = createQueryTab([], {
  sql,
  database: "sales",
  workGroup: "adhoc",
  parameters: ["42"],
})

describe("startQueryInput", () => {
  it("sends the tab's SQL, workgroup and query context", () => {
    expect(startQueryInput(tab)).toMatchObject({
      QueryString: sql,
      WorkGroup: "adhoc",
      QueryExecutionContext: { Catalog: "AwsDataCatalog", Database: "sales" },
    })
  })

  it("sends one parameter per placeholder, blank where none was given", () => {
    expect(startQueryInput(tab).ExecutionParameters).toEqual(["42", ""])
  })
})

describe("queryRun", () => {
  it("sends no parameters for SQL without placeholders", () => {
    expect(queryRun(tab, { selection: "SELECT 1", offset: 0 })).toEqual({ sql: "SELECT 1" })
  })

  it("gives a selection the values of the placeholders it contains", () => {
    const two = createQueryTab([], { sql, parameters: ["42", "'eu'"] })
    const selection = "region = ?"
    expect(queryRun(two, { selection, offset: sql.indexOf(selection) }).parameters).toEqual([
      "'eu'",
    ])
  })

  it("sends as many values as the executed prepared statement has placeholders", () => {
    const execute = createQueryTab([], { sql: "EXECUTE by_id", parameters: ["7"] })
    expect(queryRun(execute, { executeCount: 1 })).toEqual({
      sql: "EXECUTE by_id",
      parameters: ["7"],
    })
  })
})

describe("startQueryCall", () => {
  it("names Athena's StartQueryExecution for the copy snippets", () => {
    expect(startQueryCall(tab)).toMatchObject({
      service: { cli: "athena", sdkClient: "AthenaClient" },
      operation: "StartQueryExecution",
    })
  })
})
