import { connectSnippets, unsignedCatalogUri, type ConnectTarget } from "./connect-snippets"

const bucket: ConnectTarget = {
  endpoint: "http://localhost:4566",
  region: "eu-west-1",
  warehouseArn: "arn:aws:s3tables:eu-west-1:000000000000:bucket/lake",
}

function snippet(target: ConnectTarget, client: string): string {
  return connectSnippets(target).find((s) => s.client === client)?.code ?? ""
}

describe("connectSnippets", () => {
  it("offers PyIceberg, Spark, Trino, DuckDB and the AWS CLI", () => {
    expect(connectSnippets(bucket).map((s) => s.label)).toEqual([
      "PyIceberg",
      "Spark",
      "Trino",
      "DuckDB",
      "AWS CLI",
    ])
  })

  it.each(["pyiceberg", "spark", "trino"])(
    "signs %s for s3tables in the target's region",
    (client) => {
      const code = snippet(bucket, client)
      expect(code).toContain("s3tables")
      expect(code).toMatch(/signing-name["=:\s]+"?s3tables/)
    },
  )

  it("points every client at the bucket as its warehouse", () => {
    for (const s of connectSnippets(bucket)) expect(s.code).toContain(bucket.warehouseArn)
  })

  it("attaches DuckDB to the unsigned catalog", () => {
    expect(snippet(bucket, "duckdb")).toContain(unsignedCatalogUri(bucket))
  })

  it("ends each snippet by reading the table on a table's page", () => {
    const table = { ...bucket, table: { namespace: "sales", name: "orders" } }
    expect(snippet(table, "pyiceberg")).toContain('catalog.load_table("sales.orders")')
    expect(snippet(table, "duckdb")).toContain("SELECT * FROM s3tables.sales.orders")
  })

  it("asks the CLI for the table's metadata location on a table's page", () => {
    const table = { ...bucket, table: { namespace: "sales", name: "orders" } }
    expect(snippet(table, "aws-cli")).toMatch(/^aws s3tables get-table-metadata-location/)
  })

  it("mentions host.docker.internal only for a localhost endpoint", () => {
    expect(snippet(bucket, "trino")).toContain("host.docker.internal")
    expect(snippet({ ...bucket, endpoint: "http://overcast:4566" }, "trino")).not.toContain(
      "host.docker.internal",
    )
  })
})
