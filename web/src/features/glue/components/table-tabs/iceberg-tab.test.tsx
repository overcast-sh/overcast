import type { Table } from "@aws-sdk/client-glue"
import { icebergMetadataFileQueryOptions, type MetadataFile } from "@/components/iceberg/data"
import { parseIcebergMetadata } from "@/components/iceberg/metadata"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { icebergTable } from "../../__fixtures__/tables"
import { IcebergTab } from "./iceberg-tab"

const LOCATION = "s3://lake/events/metadata/00001-abc.metadata.json"
const SNAPSHOT = "3051729675574597004"

const text = `{
  "format-version": 2, "table-uuid": "u", "location": "s3://lake/events",
  "current-snapshot-id": ${SNAPSHOT},
  "snapshots": [
    { "snapshot-id": ${SNAPSHOT}, "timestamp-ms": 1000,
      "summary": { "operation": "append", "added-records": "5", "total-records": "5" } }
  ],
  "metadata-log": [
    { "timestamp-ms": 500, "metadata-file": "s3://lake/events/metadata/00000-a.metadata.json" }
  ]
}`

const file: MetadataFile = {
  location: LOCATION,
  text,
  truncated: false,
  metadata: parseIcebergMetadata(text),
}

function renderTab(table: Table = icebergTable) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(icebergMetadataFileQueryOptions(LOCATION).queryKey, file)
  return renderWithRouter(
    () => <IcebergTab database="sales" table={table} selection={{}} onSelectionChange={() => {}} />,
    { queryClient },
  )
}

describe("IcebergTab", () => {
  it("lists the snapshots of the file metadata_location points at", async () => {
    renderTab()
    const row = await screen.findByRole("row", { name: new RegExp(SNAPSHOT) })
    expect(within(row).getByText("current")).toBeInTheDocument()
  })

  it("queries a snapshot through AwsDataCatalog in the table's own database", async () => {
    renderTab()
    const link = await screen.findByRole("link", { name: "Query as of this snapshot" })
    const search = new URL(link.getAttribute("href") ?? "", "https://x").searchParams
    expect([search.get("catalog"), search.get("database"), search.get("sql")]).toEqual([
      "AwsDataCatalog",
      "sales",
      `SELECT * FROM "AwsDataCatalog"."sales"."events" FOR VERSION AS OF ${SNAPSHOT} LIMIT 100`,
    ])
  })

  it("offers every metadata version the log keeps", async () => {
    renderTab()
    const picker = await screen.findByRole("combobox", { name: "Version" })
    expect(within(picker).getAllByRole("option")).toHaveLength(2)
  })

  it("says so when the table names no metadata_location", async () => {
    renderTab({ ...icebergTable, Parameters: { table_type: "ICEBERG" } })
    expect(await screen.findByText("No metadata yet")).toBeInTheDocument()
  })
})
