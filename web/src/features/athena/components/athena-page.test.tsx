import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router"
import type { QueryExecution } from "@aws-sdk/client-athena"
import { act, createTestQueryClient, render, screen, waitFor, within } from "@/test/render"
import { latestFakeEditor, resetFakeEditors } from "@/test/monaco"
import { routeTree } from "@/routeTree.gen"
import type * as ApiModule from "@/services/api"
import type { AthenaEngineStatus } from "@/types"
import { athenaEditorLink } from "../links"
import { QUERY_TABS_STORAGE_KEY } from "../query-tabs"

vi.mock("@monaco-editor/react", async () => {
  const { FakeMonacoEditor } = await import("@/test/monaco")
  return { default: FakeMonacoEditor, loader: { __getMonacoInstance: () => null } }
})
vi.mock("@/components/application-ownership-banner", () => ({
  ApplicationOwnershipBanner: () => null,
}))
vi.mock("@/components/layout/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => children,
}))

const api = vi.hoisted(() => ({
  engine: { engine: "trino", state: "ready", runningQueries: 0 } as AthenaEngineStatus,
  executions: [] as QueryExecution[],
  start: vi.fn((_input: unknown) => Promise.resolve("q-new")),
  getExecution: vi.fn((id: string) =>
    id === "gone"
      ? Promise.reject(new Error(`QueryExecution ${id} was not found`))
      : Promise.resolve({
          QueryExecutionId: id,
          Query: "SELECT 1",
          Status: { State: "RUNNING", SubmissionDateTime: new Date() },
        }),
  ),
}))

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiModule>()
  return {
    ...actual,
    athena: {
      ...actual.athena,
      getEngineStatus: () => Promise.resolve(api.engine),
      listWorkGroups: () =>
        Promise.resolve([
          { Name: "primary", State: "ENABLED" },
          { Name: "analytics", State: "ENABLED" },
        ]),
      getWorkGroup: (name: string) => Promise.resolve({ Name: name, Configuration: {} }),
      listDataCatalogs: () => Promise.resolve([{ CatalogName: "AwsDataCatalog", Type: "GLUE" }]),
      listDatabases: () => Promise.resolve([{ Name: "default" }, { Name: "sales" }]),
      listTableMetadata: () =>
        Promise.resolve([
          {
            Name: "orders",
            Columns: [{ Name: "id", Type: "bigint" }],
            PartitionKeys: [{ Name: "dt", Type: "string" }],
            Parameters: { table_type: "ICEBERG" },
          },
        ]),
      listAllQueryExecutions: () => Promise.resolve(api.executions),
      listAllNamedQueries: () => Promise.resolve([]),
      startQueryExecution: api.start,
      getQueryExecution: api.getExecution,
    },
  }
})

function renderAthena(url: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [url] }),
  })
  const result = render(<RouterProvider router={router} />, {
    queryClient: createTestQueryClient(),
  })
  return { ...result, router }
}

beforeEach(() => {
  localStorage.clear()
  resetFakeEditors()
  api.start.mockClear()
  api.getExecution.mockClear()
  api.engine = { engine: "trino", state: "ready", runningQueries: 0 }
  api.executions = []
})

describe("Athena > editor deep link", () => {
  const link = athenaEditorLink({ database: "sales", sql: "SELECT * FROM orders LIMIT 10" })
  const url = `/athena?tab=editor&database=sales&sql=${encodeURIComponent(link.search.sql)}`

  it("opens the SQL in a new query tab with its database", async () => {
    renderAthena(url)
    const tab = await screen.findByRole("tab", { name: "Query 2", selected: true })
    expect(tab).toBeInTheDocument()
    expect(screen.getByRole("textbox", { name: "Editor" })).toHaveValue(link.search.sql)
    expect(screen.getByRole("combobox", { name: "Database" })).toHaveValue("sales")
  })

  it("does not run the query", async () => {
    renderAthena(url)
    await screen.findByRole("tab", { name: "Query 2" })
    expect(api.start).not.toHaveBeenCalled()
  })

  it("takes the SQL out of the URL, so a reload does not open it again", async () => {
    const { router } = renderAthena(url)
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: "editor" }))
  })

  it("runs the new tab in the workgroup the link names", async () => {
    renderAthena(`${url}&workgroup=analytics`)
    await screen.findByRole("tab", { name: "Query 2" })
    expect(await screen.findByRole("combobox", { name: "workgroup" })).toHaveValue("analytics")
  })

  it("opens a past execution's result when the link names it", async () => {
    renderAthena(`${url}&execution=q-past`)
    await waitFor(() => expect(api.getExecution).toHaveBeenCalledWith("q-past"))
  })
})

describe("Athena > running a query", () => {
  it("runs the tab's SQL in its database on ⌘⏎", async () => {
    renderAthena(`/athena?tab=editor&database=sales&sql=SELECT%201`)
    await screen.findByRole("tab", { name: "Query 2" })
    act(() => latestFakeEditor().runAction("athena.run"))
    await waitFor(() => expect(api.start).toHaveBeenCalledOnce())
    expect(api.start.mock.calls[0][0]).toMatchObject({
      QueryString: "SELECT 1",
      WorkGroup: "primary",
      QueryExecutionContext: { Catalog: "AwsDataCatalog", Database: "sales" },
    })
  })

  it("lets esc stop the query only while it runs", async () => {
    renderAthena("/athena")
    const editor = await waitFor(() => latestFakeEditor())
    expect(editor.actions.find((a) => a.id === "athena.stop")?.precondition).toContain(
      "athenaQueryRunning",
    )
    expect(editor.contextKeys.get("athenaQueryRunning")).toBe(false)
    act(() => editor.runAction("athena.run"))
    await waitFor(() => expect(editor.contextKeys.get("athenaQueryRunning")).toBe(true))
  })

  it("shows Stop while the query runs", async () => {
    renderAthena("/athena")
    const editor = await waitFor(() => latestFakeEditor())
    act(() => editor.runAction("athena.run"))
    expect(await screen.findByRole("button", { name: /Stop/ })).toBeInTheDocument()
  })

  it("does not start a second run while the first is starting", async () => {
    renderAthena("/athena")
    const editor = await waitFor(() => latestFakeEditor())
    act(() => editor.runAction("athena.run"))
    act(() => editor.runAction("athena.run"))
    await screen.findByRole("button", { name: /Stop/ })
    expect(api.start).toHaveBeenCalledOnce()
  })

  it("says why a restored tab's last execution cannot be shown, and stops asking", async () => {
    localStorage.setItem(
      QUERY_TABS_STORAGE_KEY,
      JSON.stringify({
        tabs: [{ id: "t1", sql: "SELECT 1", executionId: "gone" }],
        activeId: "t1",
      }),
    )
    renderAthena("/athena")
    expect(await screen.findByRole("alert")).toHaveTextContent("QueryExecution gone was not found")
    expect(api.getExecution).toHaveBeenCalledOnce()
  })
})

describe("Athena > engine", () => {
  it("says the engine is off, above the editor, in inert mode", async () => {
    api.engine = { engine: "inert", state: "off", runningQueries: 0 }
    renderAthena("/athena")
    const note = await screen.findByRole("note")
    expect(note).toHaveTextContent("The query engine is off")
    expect(note).toHaveTextContent("ATHENA_ENGINE=inert")
  })

  it("shows a pull as progress in the chip", async () => {
    api.engine = { engine: "trino", state: "pulling", runningQueries: 1 }
    renderAthena("/athena")
    const state = await screen.findByText("Starting engine")
    expect(state).toHaveAttribute("role", "status")
    expect(state.parentElement).toHaveTextContent("pulling image")
  })
})

describe("Athena > data browser", () => {
  it("offers a table's qualified name to insert at the cursor", async () => {
    renderAthena("/athena?tab=editor&database=sales&sql=SELECT%201")
    expect(await screen.findByRole("button", { name: "Insert sales.orders" })).toBeInTheDocument()
  })

  it("marks an Iceberg table and its partition keys", async () => {
    const { user } = renderAthena("/athena?tab=editor&database=sales&sql=SELECT%201")
    const tables = await screen.findByRole("list", { name: "Tables in sales" })
    expect(within(tables).getByText("ICEBERG")).toBeInTheDocument()
    await user.click(within(tables).getByRole("button", { name: "Expand orders" }))
    expect(within(tables).getByText("◆ partition")).toBeInTheDocument()
  })

  it("previews a table in a new tab, and runs it", async () => {
    const { user } = renderAthena("/athena?tab=editor&database=sales&sql=SELECT%201")
    await user.click(await screen.findByRole("button", { name: "Actions for orders" }))
    await user.click(await screen.findByRole("menuitem", { name: "Preview" }))
    expect(await screen.findByRole("tab", { name: "orders", selected: true })).toBeInTheDocument()
    expect(api.start.mock.calls[0][0]).toMatchObject({
      QueryString: "SELECT * FROM sales.orders LIMIT 10",
    })
  })
})

describe("Athena > history", () => {
  it("lists executions newest first and opens one by ?execution=", async () => {
    api.executions = [
      {
        QueryExecutionId: "q-old",
        Query: "SELECT 'old'",
        WorkGroup: "primary",
        StatementType: "DML",
        Status: { State: "SUCCEEDED", SubmissionDateTime: new Date("2026-09-26T09:00:00Z") },
      },
      {
        QueryExecutionId: "q-new",
        Query: "SELECT nope",
        WorkGroup: "primary",
        StatementType: "DML",
        Status: {
          State: "FAILED",
          SubmissionDateTime: new Date("2026-09-26T10:00:00Z"),
          AthenaError: {
            ErrorCategory: 2,
            ErrorType: 1006,
            ErrorMessage: "COLUMN_NOT_FOUND: line 1:8: Column 'nope' cannot be resolved",
          },
        },
      },
    ]
    renderAthena("/athena?tab=history&execution=q-new")
    const rows = await screen.findAllByRole("row")
    expect(rows[1]).toHaveTextContent("SELECT nope")
    expect(
      await screen.findByText(/Column 'nope' cannot be resolved/, { selector: "p" }),
    ).toBeInTheDocument()
  })

  it("runs an execution again from its row", async () => {
    api.executions = [
      {
        QueryExecutionId: "q-1",
        Query: "SELECT 1",
        WorkGroup: "primary",
        QueryExecutionContext: { Database: "sales" },
        Status: { State: "SUCCEEDED", SubmissionDateTime: new Date() },
      },
    ]
    const { user } = renderAthena("/athena?tab=history")
    await user.click(await screen.findByRole("button", { name: "Run again" }))
    await waitFor(() => expect(api.start).toHaveBeenCalledOnce())
    expect(api.start.mock.calls[0][0]).toMatchObject({
      QueryString: "SELECT 1",
      QueryExecutionContext: { Database: "sales" },
    })
  })

  it("opens an execution in the editor in the workgroup it ran in", async () => {
    api.executions = [
      {
        QueryExecutionId: "q-1",
        Query: "SELECT 1",
        WorkGroup: "analytics",
        Status: { State: "SUCCEEDED", SubmissionDateTime: new Date() },
      },
    ]
    renderAthena("/athena?tab=history")
    const link = await screen.findByRole("link", { name: "Open in editor" })
    expect(new URL(link.getAttribute("href") ?? "", "http://x").searchParams.get("workgroup")).toBe(
      "analytics",
    )
  })
})
