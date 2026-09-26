import { engineStatusQueryOptions } from "@/features/athena/data"
import type * as ApiModule from "@/services/api"
import { createTestQueryClient, renderWithRouter, screen } from "@/test/render"
import type { AthenaEngineStatus, TopologyDataTable } from "@/types"
import { LatestCommitPeek } from "./latest-commit-peek"

vi.mock("@/services/api", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  athena: { getEngineStatus: vi.fn() },
}))

const orders: TopologyDataTable = {
  id: "t-1",
  name: "orders",
  namespace: "sales",
  snapshots: 2,
  lastCommit: {
    operation: "append",
    committedAt: Date.UTC(2026, 8, 26, 10),
    snapshotId: "3051729675574597004",
    addedRecords: 5,
  },
}

// The engine's status is seeded, so each test reads the peek as it is once
// the status has loaded.
function renderPeek(engine: Pick<AthenaEngineStatus, "engine" | "state">) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(engineStatusQueryOptions().queryKey, engine as AthenaEngineStatus)
  return renderWithRouter(
    () => <LatestCommitPeek bucket="lake" table={orders} onClose={vi.fn()} />,
    { queryClient },
  )
}

describe("LatestCommitPeek", () => {
  it("queries the table with Athena in its bucket's catalog, as the table page does", async () => {
    // Given: the peek open on sales.orders in the lake table bucket
    renderPeek({ engine: "trino", state: "ready" })

    // When: its Query with Athena link is read
    const link = await screen.findByRole("link", { name: "Query with Athena" })
    const url = new URL(link.getAttribute("href") ?? "", "https://x")
    const search = url.searchParams

    // Then: it opens the editor on the table, in s3tablescatalog/lake
    expect(url.pathname).toBe("/athena")
    expect([
      search.get("tab"),
      search.get("catalog"),
      search.get("database"),
      search.get("sql"),
    ]).toEqual([
      "editor",
      "s3tablescatalog/lake",
      "sales",
      'SELECT * FROM "sales"."orders" LIMIT 10;',
    ])
    // And: with the engine running, nothing warns about it
    expect(screen.queryByText(/query engine is off/)).not.toBeInTheDocument()
  })

  it("says a query succeeds with no rows while the engine is off", async () => {
    // Given: the engine is off by setting
    renderPeek({ engine: "inert", state: "off" })

    // Then: the peek says so, under its commit
    expect(
      await screen.findByText("The query engine is off: queries succeed with no rows"),
    ).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Query with Athena" })).toBeInTheDocument()
  })
})
