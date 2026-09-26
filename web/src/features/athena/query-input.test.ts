import { startQueryCall, startQueryInput } from "./query-input"
import { createQueryTab } from "./query-tabs"

const tab = createQueryTab([], {
  sql: "SELECT * FROM orders WHERE id = ? AND region = ?",
  database: "sales",
  workGroup: "adhoc",
  parameters: ["42"],
})

describe("startQueryInput", () => {
  it("sends the tab's SQL, workgroup and query context", () => {
    expect(startQueryInput(tab)).toMatchObject({
      QueryString: tab.sql,
      WorkGroup: "adhoc",
      QueryExecutionContext: { Catalog: "AwsDataCatalog", Database: "sales" },
    })
  })

  it("sends one parameter per placeholder, blank where none was given", () => {
    expect(startQueryInput(tab).ExecutionParameters).toEqual(["42", ""])
  })

  it("runs the selection instead when one is given, with its own placeholders", () => {
    const input = startQueryInput(tab, "SELECT 1")
    expect(input.QueryString).toBe("SELECT 1")
    expect(input.ExecutionParameters).toBeUndefined()
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
