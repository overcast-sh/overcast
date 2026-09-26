import type { NamespaceSummary, TableSummary } from "@aws-sdk/client-s3tables"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { bucketTablesQueryOptions, namespacesQueryOptions } from "../data"
import { BucketTablesTab } from "./bucket-tables-tab"

const ARN = "arn:aws:s3tables:us-east-1:000000000000:bucket/analytics"

const namespace = (name: string): NamespaceSummary => ({
  namespace: [name],
  createdAt: new Date(0),
  createdBy: "000000000000",
  ownerAccountId: "000000000000",
})

const table = (ns: string, name: string): TableSummary => ({
  namespace: [ns],
  name,
  type: "customer",
  tableARN: `${ARN}/table/${name}-id`,
  createdAt: new Date(0),
  modifiedAt: new Date(0),
})

function renderTab(namespaces: NamespaceSummary[], tables: TableSummary[], filter = "") {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(namespacesQueryOptions(ARN).queryKey, namespaces)
  queryClient.setQueryData(bucketTablesQueryOptions(ARN).queryKey, tables)
  return renderWithRouter(
    () => (
      <BucketTablesTab
        bucketName="analytics"
        tableBucketARN={ARN}
        filter={filter}
        onFilterChange={() => {}}
      />
    ),
    { queryClient },
  )
}

describe("BucketTablesTab", () => {
  it("lists each table under its namespace", async () => {
    renderTab([namespace("sales"), namespace("web")], [table("sales", "orders")])
    const sales = await screen.findByRole("generic", { name: "Namespace sales" })
    expect(within(sales).getByText("orders")).toBeInTheDocument()
  })

  it("shows an empty namespace, ready for its first table", async () => {
    renderTab([namespace("web")], [])
    expect(await screen.findByText("No tables in this namespace")).toBeInTheDocument()
  })

  it("offers to create a namespace when the bucket has none", async () => {
    renderTab([], [])
    expect(await screen.findByText("No namespaces yet")).toBeInTheDocument()
  })

  it("tells a filter that matches nothing apart from an empty bucket", async () => {
    renderTab([namespace("sales")], [table("sales", "orders")], "zzz")
    expect(await screen.findByText("No matching tables or namespaces")).toBeInTheDocument()
  })

  it("does not offer Create table before there is a namespace to put it in", async () => {
    renderTab([], [])
    expect(await screen.findByRole("button", { name: "Create table" })).toBeDisabled()
  })
})
