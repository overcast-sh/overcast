import type { TableSummary } from "@aws-sdk/client-s3tables"
import { groupByNamespace } from "./namespace-groups"

const table = (namespace: string, name: string): TableSummary => ({
  namespace: [namespace],
  name,
  tableARN: `arn/${name}`,
  type: "customer",
  createdAt: new Date(0),
  modifiedAt: new Date(0),
})

const tables = [table("sales", "refunds"), table("sales", "orders"), table("web", "clicks")]

describe("groupByNamespace", () => {
  it("puts each table under its namespace, both A→Z, empty namespaces included", () => {
    const groups = groupByNamespace(["web", "sales", "raw"], tables, "")
    expect(groups.map((g) => [g.namespace, g.tables.map((t) => t.name)])).toEqual([
      ["raw", []],
      ["sales", ["orders", "refunds"]],
      ["web", ["clicks"]],
    ])
  })

  it("keeps a namespace whose name matches whole", () => {
    expect(groupByNamespace(["sales", "web"], tables, "sal")[0].tables).toHaveLength(2)
  })

  it("keeps only the matching tables of any other namespace", () => {
    expect(groupByNamespace(["sales", "web"], tables, "ord")).toEqual([
      { namespace: "sales", tables: [tables[1]] },
    ])
  })
})
