import { stubLayout } from "@/components/data-grid/testing/layout"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { s3ObjectMetaQueryOptions } from "../data"
import { serveObject } from "../testing/serve-object"
import { DataViewerPage } from "./data-viewer-page"

const ORDERS_CSV = "id,name\n1,Ada\n2,Grace\n3,Alan\n"

/** The viewer for `key`, its metadata already fetched. */
function renderViewer(key: string, contentType: string, row?: number) {
  stubLayout()
  const size = serveObject(ORDERS_CSV)
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(s3ObjectMetaQueryOptions("lake", key).queryKey, {
    contentType,
    contentLength: size,
    lastModified: "2026-09-01T00:00:00.000Z",
    etag: '"v1"',
    metadata: {},
    storageClass: "STANDARD",
  })
  return renderWithRouter(() => <DataViewerPage bucket="lake" objectKey={key} row={row} />, {
    queryClient,
  })
}

describe("DataViewerPage", () => {
  it("opens the object's rows in the grid, titled by its name", async () => {
    renderViewer("raw/orders.csv", "text/csv")
    const grid = await screen.findByRole("grid", { name: "Rows of raw/orders.csv" })
    expect(await within(grid).findByText("Grace")).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: "orders.csv" })).toBeInTheDocument()
  })

  it("opens with the cursor on the row the link names", async () => {
    renderViewer("orders.csv", "text/csv", 2)
    const grid = await screen.findByRole("grid")
    await within(grid).findByText("Grace")
    expect(grid.getAttribute("aria-activedescendant")).toMatch(/-1-0$/)
  })

  it("says an object that is not a table is not one, and links back to it", async () => {
    renderViewer("notes.txt", "text/plain")
    expect(await screen.findByText("Not a table")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Back to the object" })).toHaveAttribute(
      "href",
      "/s3/lake/objects/notes.txt",
    )
  })
})
