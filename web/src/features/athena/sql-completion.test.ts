import { completionItems, qualifierBefore, type CompletionContext } from "./sql-completion"

const context: CompletionContext = {
  catalogs: ["AwsDataCatalog"],
  databases: ["sales", "default"],
  database: "sales",
  tables: [
    {
      Name: "orders",
      Columns: [
        { Name: "id", Type: "bigint" },
        { Name: "amount", Type: "double" },
      ],
      PartitionKeys: [{ Name: "dt", Type: "string" }],
    },
  ],
}

describe("completionItems", () => {
  it("offers qualified tables, their columns, databases, keywords and Trino functions", () => {
    const labels = (kind: string) =>
      completionItems(context, undefined)
        .filter((i) => i.kind === kind)
        .map((i) => i.insertText)
    expect(labels("table")).toEqual(["sales.orders"])
    expect(labels("column")).toEqual(["id", "amount", "dt"])
    expect(labels("database")).toContain("sales")
    expect(labels("keyword")).toContain("SELECT")
    expect(labels("function")).toContain("date_trunc")
  })

  it("offers only a table's columns after its name and a dot", () => {
    expect(completionItems(context, "orders").map((i) => i.label)).toEqual(["id", "amount", "dt"])
  })

  it("offers the database's tables after the database's name and a dot", () => {
    expect(completionItems(context, "sales").map((i) => i.insertText)).toEqual(["orders"])
  })

  it("describes a column by its type and table", () => {
    expect(completionItems(context, "orders")[0].detail).toBe("bigint · orders")
  })
})

describe("qualifierBefore", () => {
  it.each([
    ["SELECT orders.", "orders"],
    ["SELECT Orders.am", "orders"],
    ['SELECT "Order Items".', "order items"],
    ["SELECT amount", undefined],
  ])("reads %s as %s", (text, qualifier) => {
    expect(qualifierBefore(text)).toBe(qualifier)
  })
})
