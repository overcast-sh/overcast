import { describe, expect, it, vi } from "vitest"
import { createTestQueryClient, renderWithRouter, screen } from "@/test/render"
import { csvTable } from "../../__fixtures__/tables"
import { glueKeys } from "../../data"
import { VersionsTab } from "./versions-tab"

// Monaco loads from a CDN at mount, which never completes under jsdom.
vi.mock("@monaco-editor/react", () => ({
  DiffEditor: ({ original, modified }: { original: string; modified: string }) => (
    <div>
      <pre data-testid="original">{original}</pre>
      <pre data-testid="modified">{modified}</pre>
    </div>
  ),
}))

const versions = ["1", "2", "3"].map((id) => ({
  VersionId: id,
  Table: { ...csvTable, VersionId: id, Description: `as of version ${id}` },
}))

function renderTab(search: { from?: string; to?: string } = {}) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(glueKeys.tableVersions("sales", "orders"), versions)
  const onCompare = vi.fn()
  const result = renderWithRouter(
    () => <VersionsTab table={csvTable} {...search} onCompare={onCompare} />,
    { queryClient },
  )
  return { ...result, onCompare }
}

describe("VersionsTab", () => {
  it("compares the latest version with the one before it by default", async () => {
    renderTab()

    expect(await screen.findByTestId("original")).toHaveTextContent("as of version 2")
    expect(screen.getByTestId("modified")).toHaveTextContent("as of version 3")
  })

  it("compares the two versions the URL names", async () => {
    renderTab({ from: "1", to: "3" })

    expect(await screen.findByTestId("original")).toHaveTextContent("as of version 1")
  })

  it("compares a clicked version with its predecessor", async () => {
    const { user, onCompare } = renderTab()

    await user.click(await screen.findByRole("row", { name: /^2/ }))

    expect(onCompare).toHaveBeenCalledWith("1", "2")
  })

  it("leaves out what the service stamps on each version, so only real changes show", async () => {
    renderTab()

    expect(await screen.findByTestId("modified")).not.toHaveTextContent("VersionId")
  })
})
