import { describe, expect, it, vi } from "vitest"
import type * as ApiModule from "@/services/api"
import { sampleReport, serveSamples } from "@/features/samples/testing/serve-samples"
import { createTestQueryClient, renderWithRouter, screen, waitFor } from "@/test/render"
import type { CreateTableWizardState } from "../create-table-param"
import { glueKeys } from "../data"
import { GluePage } from "./glue-page"

vi.mock("@/services/api", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  glue: {
    listDatabases: vi.fn(() => Promise.resolve([])),
    listAllTables: vi.fn(() => Promise.resolve([])),
  },
}))

const closedWizard: CreateTableWizardState = { open: false, onOpen: vi.fn(), onClose: vi.fn() }

function renderPage({
  databases,
  filter = "",
  wizard = closedWizard,
}: {
  databases: { Name: string; Description?: string }[]
  filter?: string
  wizard?: CreateTableWizardState
}) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(glueKeys.databases(), databases)
  queryClient.setQueryData(glueKeys.allTables(), [
    { Name: "orders", DatabaseName: "sales" },
    { Name: "items", DatabaseName: "sales" },
  ])
  const onFilterChange = vi.fn()
  const result = renderWithRouter(
    () => (
      <GluePage
        filter={filter}
        onFilterChange={onFilterChange}
        onSortChange={vi.fn()}
        wizard={wizard}
      />
    ),
    { queryClient },
  )
  return { ...result, onFilterChange }
}

describe("GluePage > an empty catalog", () => {
  it("offers to create a table from S3 data", async () => {
    const onOpen = vi.fn()
    const { user } = renderPage({ databases: [], wizard: { ...closedWizard, onOpen } })

    await user.click(await screen.findByRole("button", { name: "Create a table from S3 data" }))

    expect(onOpen).toHaveBeenCalled()
  })
})

describe("GluePage > a catalog with databases", () => {
  it("lists each database with its table count", async () => {
    renderPage({ databases: [{ Name: "sales" }] })

    const row = await screen.findByRole("row", { name: /sales/ })
    expect(row).toHaveTextContent("2")
  })

  it("says nothing matches a filter that finds nothing, and offers to clear it", async () => {
    const { user, onFilterChange } = renderPage({ databases: [{ Name: "sales" }], filter: "zzz" })

    await user.click(await screen.findByRole("button", { name: "Clear filter" }))

    expect(onFilterChange).toHaveBeenCalledWith("")
  })
})

describe("GluePage > loading the sample dataset", () => {
  it("offers it beside the wizard, and opens its database once loaded", async () => {
    serveSamples({ report: sampleReport() })
    const { user, router } = renderPage({ databases: [] })

    await user.click(await screen.findByRole("button", { name: "Load sample dataset" }))

    await waitFor(() => expect(router.state.location.pathname).toBe("/glue/sample_analytics"))
  })
})
