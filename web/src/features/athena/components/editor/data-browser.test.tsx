import { describe, expect, it, vi } from "vitest"
import { sampleReport, serveSamples } from "@/features/samples/testing/serve-samples"
import { createTestQueryClient, renderWithRouter, screen, waitFor } from "@/test/render"
import { athenaKeys } from "../../data"
import { DEFAULT_CATALOG } from "../../query-tabs"
import { DataBrowser } from "./data-browser"

function renderBrowser(catalog: string, databases = [{ Name: "default" }]) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(athenaKeys.dataCatalogs(), [{ CatalogName: catalog }])
  queryClient.setQueryData(athenaKeys.databases(catalog), databases)
  queryClient.setQueryData(athenaKeys.tables(catalog, "default"), [])
  const onContextChange = vi.fn()
  const result = renderWithRouter(
    () => (
      <DataBrowser
        catalog={catalog}
        database="default"
        databasesError={null}
        onContextChange={onContextChange}
        onInsert={vi.fn()}
        onRunInNewTab={vi.fn()}
      />
    ),
    { queryClient },
  )
  return { ...result, onContextChange }
}

describe("DataBrowser > a database with no tables", () => {
  it("offers the sample dataset, and switches to it once loaded", async () => {
    serveSamples({ report: sampleReport() })
    const { user, onContextChange } = renderBrowser(DEFAULT_CATALOG)

    expect(await screen.findByRole("link", { name: "Create a table from S3 data" })).toBeVisible()
    await user.click(await screen.findByRole("button", { name: "Load sample dataset" }))

    await waitFor(() =>
      expect(onContextChange).toHaveBeenCalledWith({
        catalog: DEFAULT_CATALOG,
        database: "sample_analytics",
      }),
    )
  })

  it("reads a catalog with no databases as empty, not as an error", async () => {
    serveSamples({ report: sampleReport() })
    renderBrowser(DEFAULT_CATALOG, [])

    expect(await screen.findByText("No databases")).toBeVisible()
    expect(await screen.findByRole("button", { name: "Load sample dataset" })).toBeVisible()
  })

  it("does not offer it in another catalog, where it would not land", async () => {
    serveSamples({ report: sampleReport() })
    renderBrowser("s3tablescatalog/lake")

    expect(await screen.findByText("No tables")).toBeVisible()
    expect(screen.queryByRole("button", { name: "Load sample dataset" })).not.toBeInTheDocument()
  })
})
