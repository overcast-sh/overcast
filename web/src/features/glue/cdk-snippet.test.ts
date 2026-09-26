import { cfnTableSnippet } from "./cdk-snippet"

describe("cfnTableSnippet", () => {
  const snippet = cfnTableSnippet("sales", {
    Name: "orders_2026",
    TableType: "EXTERNAL_TABLE",
    Parameters: { "skip.header.line.count": "1", classification: "csv" },
    StorageDescriptor: {
      Columns: [{ Name: "id", Type: "bigint" }],
      SerdeInfo: { Parameters: { "field.delim": "," } },
    },
  })

  it("names the construct after the table", () => {
    expect(snippet).toContain('new glue.CfnTable(this, "Orders2026Table", {')
  })

  it("camel-cases CloudFormation's property names", () => {
    expect(snippet).toContain("storageDescriptor: {")
    expect(snippet).toContain('tableType: "EXTERNAL_TABLE",')
  })

  it("keeps a parameters map's keys as written, quoted where they must be", () => {
    expect(snippet).toContain('"skip.header.line.count": "1",')
    expect(snippet).toContain('classification: "csv",')
    expect(snippet).toContain('"field.delim": ",",')
  })

  it("puts the table in the named database of the stack's account", () => {
    expect(snippet).toContain('databaseName: "sales",')
    expect(snippet).toContain("catalogId: this.account,")
  })
})
