import { render, screen, within } from "@/test/render"
import { parseIcebergMetadata } from "./metadata"
import { IcebergSnapshotList } from "./snapshot-list"

const metadata = parseIcebergMetadata(`{
  "format-version": 2, "table-uuid": "u", "location": "s3://w",
  "current-snapshot-id": 3051729675574597004,
  "snapshots": [
    { "snapshot-id": 1868154234528417337, "timestamp-ms": 1000,
      "summary": { "operation": "append", "added-records": "1200", "total-records": "1200" } },
    { "snapshot-id": 3051729675574597004, "parent-snapshot-id": 1868154234528417337, "timestamp-ms": 2000,
      "summary": { "operation": "delete", "deleted-records": "120", "total-records": "1080" } }
  ]
}`)

function renderList(openSnapshotId?: string) {
  if (!metadata) throw new Error("fixture does not parse")
  return render(<IcebergSnapshotList metadata={metadata} openSnapshotId={openSnapshotId} />)
}

const rowOf = (id: string) => screen.getByRole("row", { name: new RegExp(id) })

describe("IcebergSnapshotList", () => {
  it("marks the current snapshot", () => {
    renderList()
    expect(within(rowOf("3051729675574597004")).getByText("current")).toBeInTheDocument()
  })

  it("says what each commit did", () => {
    renderList()
    expect(within(rowOf("3051729675574597004")).getByText("−120 records")).toBeInTheDocument()
  })

  it("shows the totals either side of a commit in its diff with previous", async () => {
    const { user } = renderList()
    await user.click(
      within(rowOf("3051729675574597004")).getByRole("button", { name: "Show diff with previous" }),
    )
    expect(await screen.findByText("Since snapshot 1868154234528417337")).toBeInTheDocument()
  })

  it("opens the diff a deep link names", async () => {
    renderList("3051729675574597004")
    expect(await screen.findByText("Since snapshot 1868154234528417337")).toBeInTheDocument()
  })
})
