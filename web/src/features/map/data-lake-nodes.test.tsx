import type { ComponentProps, ReactNode } from "react"
import { act, fireEvent, render, screen, within } from "@/test/render"
import type { TopologyNode } from "@/types"
import { AthenaWorkgroupNode } from "./athena-workgroup-node"
import { DataCatalogNode } from "./data-catalog-node"
import {
  DataLakeOverlayContext,
  DataLakePeekContext,
  type DataLakeNodeData,
} from "./data-lake-context"
import { EMPTY_OVERLAY, rowKey, RUN_DWELL, type DataLakeOverlay } from "./data-lake-overlay"

const navigateMock = vi.fn()
const openPeekMock = vi.fn()

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
  Link: ({ children, className }: { children: ReactNode; className?: string }) => (
    <a className={className}>{children}</a>
  ),
  linkOptions: (o: unknown) => o,
}))

vi.mock("@xyflow/react", () => ({
  Handle: () => <div data-testid="handle" />,
  Position: { Left: "left", Right: "right" },
}))

vi.mock("@/hooks/use-endpoint", () => ({
  useEndpoint: () => ({ baseUrl: "http://localhost:4566", region: "us-east-1" }),
}))

vi.mock("@/services/endpoint-store", () => ({
  endpointStore: {
    set: vi.fn(),
    get: () => ({ baseUrl: "http://localhost:4566", region: "us-east-1" }),
    getKeys: () => ["endpoint", "http://localhost:4566", "us-east-1"],
  },
}))

vi.mock("./run-query-popover", () => ({
  RunQueryPopover: () => <button type="button">Run query</button>,
}))

type NodeComponentProps = ComponentProps<typeof DataCatalogNode>

function renderNode(
  Node: typeof DataCatalogNode,
  node: TopologyNode,
  overlay: DataLakeOverlay = EMPTY_OVERLAY,
) {
  const data: DataLakeNodeData = { topologyNode: node }
  return render(
    <DataLakePeekContext value={openPeekMock}>
      <DataLakeOverlayContext value={overlay}>
        <Node {...({ id: node.id, data, selected: false } as unknown as NodeComponentProps)} />
      </DataLakeOverlayContext>
    </DataLakePeekContext>,
  )
}

const database: TopologyNode = {
  id: "us-east-1::glue::sales",
  service: "glue",
  label: "sales",
  region: "us-east-1",
  glueResourceType: "database",
  tables: [
    { name: "events", format: "ICEBERG", partitions: 0 },
    { name: "orders", format: "PARQUET", partitions: 12, location: "s3://raw/orders/" },
  ],
}

const bucket: TopologyNode = {
  id: "us-east-1::s3tables::lake",
  service: "s3tables",
  label: "lake",
  region: "us-east-1",
  tables: [
    { name: "orders", namespace: "sales", id: "t-1", snapshots: 3 },
    { name: "clicks", namespace: "web", id: "t-2", snapshots: 0 },
  ],
}

describe("DataCatalogNode", () => {
  it("lists a database's tables with their format and partition count", () => {
    renderNode(DataCatalogNode, database)
    expect(screen.getByText("Database")).toBeInTheDocument()
    expect(screen.getByText("2 tables")).toBeInTheDocument()
    const orders = screen.getByRole("button", { name: "Preview first rows: orders" })
    expect(within(orders).getByText("PARQUET")).toBeInTheDocument()
    expect(within(orders).getByText("12 partitions")).toBeInTheDocument()
    // An unpartitioned table spends no room saying so.
    const events = screen.getByRole("button", { name: "Preview first rows: events" })
    expect(within(events).queryByText(/partition/)).not.toBeInTheDocument()
  })

  it("opens the preview peek for the row clicked, without navigating", () => {
    renderNode(DataCatalogNode, database)
    fireEvent.click(screen.getByRole("button", { name: "Preview first rows: orders" }))
    expect(openPeekMock).toHaveBeenCalledWith({
      kind: "preview",
      nodeId: "us-east-1::glue::sales",
      row: "orders",
    })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it("groups a table bucket's tables by namespace, with snapshot counts and the warehouse label", () => {
    renderNode(DataCatalogNode, bucket)
    expect(screen.getByText("warehouse")).toBeInTheDocument()
    expect(screen.getByText("sales")).toBeInTheDocument()
    expect(screen.getByText("web")).toBeInTheDocument()
    const orders = screen.getByRole("button", { name: "Latest commit: sales.orders" })
    expect(within(orders).getByText("3 snapshots")).toBeInTheDocument()
    fireEvent.click(orders)
    expect(openPeekMock).toHaveBeenCalledWith({
      kind: "commit",
      nodeId: "us-east-1::s3tables::lake",
      row: "sales.orders",
    })
  })

  it("shows a write flash and the records a commit appended on the committed row", () => {
    const overlay: DataLakeOverlay = {
      ...EMPTY_OVERLAY,
      rows: {
        [rowKey("s3tables::lake", "sales.orders")]: {
          flashes: 1,
          ticks: 0,
          records: 250,
          recordsUntil: Number.MAX_SAFE_INTEGER,
        },
      },
    }
    renderNode(DataCatalogNode, bucket, overlay)
    const orders = screen.getByRole("button", { name: "Latest commit: sales.orders" })
    expect(within(orders).getByTitle("250 records appended")).toHaveTextContent("+250")
    expect(orders.querySelector("[style*='overcastSweep']")).not.toBeNull()
  })

  it("keeps a dropped table as a struck-through ghost row that cannot be opened", () => {
    const overlay: DataLakeOverlay = {
      ...EMPTY_OVERLAY,
      ghosts: { "glue::sales": [{ name: "legacy", deletedAt: 1 }] },
    }
    renderNode(DataCatalogNode, database, overlay)
    expect(screen.getByText("legacy")).toHaveClass("line-through")
    expect(screen.getByText("dropped")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /legacy/ })).not.toBeInTheDocument()
  })

  it("draws s3tablescatalog as a federated catalog with no rows", () => {
    renderNode(DataCatalogNode, {
      id: "us-east-1::glue-catalog::s3tablescatalog",
      service: "glue",
      label: "s3tablescatalog",
      region: "us-east-1",
      glueResourceType: "catalog",
    })
    expect(screen.getByText("Federated catalog · S3 Tables")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /Preview/ })).not.toBeInTheDocument()
  })
})

describe("AthenaWorkgroupNode", () => {
  const workgroup = (over: Partial<TopologyNode> = {}): TopologyNode => ({
    id: "us-east-1::athena::analytics",
    service: "athena",
    label: "analytics",
    region: "us-east-1",
    recentQueries: [
      { id: "q-3", state: "RUNNING", query: "SELECT * FROM orders", submittedAt: 1_000 },
      {
        id: "q-2",
        state: "SUCCEEDED",
        query: "SELECT 1",
        submittedAt: 500,
        completedAt: 840,
      },
    ],
    ...over,
  })

  it("lists its latest queries: a running one pulses, a finished one shows its duration and settles", () => {
    renderNode(AthenaWorkgroupNode, workgroup())
    const running = screen.getByTitle("running · SELECT * FROM orders")
    expect(within(running).getByLabelText("running")).toHaveClass("animate-pulse")
    const done = screen.getByTitle("succeeded · SELECT 1")
    expect(within(done).getByText("340 ms")).toBeInTheDocument()
    expect(done).toHaveClass("opacity-50")
  })

  it("shows the engine chip only while the engine cannot run a query", () => {
    const { unmount } = renderNode(AthenaWorkgroupNode, workgroup({ engineState: "off" }))
    expect(screen.getByText("engine off")).toBeInTheDocument()
    unmount()
    renderNode(AthenaWorkgroupNode, workgroup())
    expect(screen.queryByText(/engine/)).not.toBeInTheDocument()
  })

  it("flashes a query that has just failed, and keeps a fast query's running state on screen", () => {
    // Given: q-3 was seen to fail an instant after it started, and the topology already says FAILED
    const now = Date.now()
    const overlay: DataLakeOverlay = {
      ...EMPTY_OVERLAY,
      runs: {
        "q-3": [
          { state: "RUNNING", at: now },
          { state: "FAILED", at: now + 1 },
        ],
      },
    }
    vi.useFakeTimers({ now })
    renderNode(
      AthenaWorkgroupNode,
      workgroup({
        recentQueries: [
          { id: "q-3", state: "FAILED", query: "SELECT x", submittedAt: 1, completedAt: 2 },
        ],
      }),
      overlay,
    )

    // Then: it still reads as running for the dwell
    expect(screen.getByTitle("running · SELECT x")).toBeInTheDocument()

    // And: once the dwell is over, the failure flashes
    act(() => {
      vi.advanceTimersByTime(RUN_DWELL)
    })
    const failed = screen.getByTitle("failed · SELECT x")
    expect(failed.getAttribute("style")).toContain("overcastFailFlash")
    vi.useRealTimers()
  })
})
