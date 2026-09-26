import { render, screen } from "@/test/render"
import type { TopologyNode } from "@/types"
import { DataLakePeeks } from "./data-lake-peeks"

// The panels are their own components; here only which table each gets matters.
vi.mock("./table-preview-peek", () => ({
  TablePreviewPeek: ({ table }: { table: { name: string; format?: string } | null }) =>
    table && <div role="dialog">preview {table.name}</div>,
}))
vi.mock("./latest-commit-peek", () => ({
  LatestCommitPeek: ({ table }: { table: { name: string; snapshots?: number } | null }) =>
    table && (
      <div role="dialog">
        commit {table.name} · {table.snapshots} snapshots
      </div>
    ),
}))

const bucket = (snapshots: number): TopologyNode => ({
  id: "us-east-1::s3tables::lake",
  service: "s3tables",
  label: "lake",
  region: "us-east-1",
  tables: [{ name: "orders", namespace: "sales", id: "t-1", snapshots }],
})

describe("DataLakePeeks", () => {
  const peek = { kind: "commit" as const, nodeId: "us-east-1::s3tables::lake", row: "sales.orders" }

  it("opens the peek its kind names, on the table as the topology has it now", () => {
    // Given: a commit peek open on a table
    const { rerender } = render(
      <DataLakePeeks peek={peek} nodes={new Map([[bucket(1).id, bucket(1)]])} onClose={vi.fn()} />,
    )
    expect(screen.getByRole("dialog")).toHaveTextContent("commit orders · 1 snapshots")

    // When: a commit lands while it is open
    rerender(
      <DataLakePeeks peek={peek} nodes={new Map([[bucket(2).id, bucket(2)]])} onClose={vi.fn()} />,
    )

    // Then: the peek shows it
    expect(screen.getByRole("dialog")).toHaveTextContent("commit orders · 2 snapshots")
  })

  it("closes when its table is dropped", () => {
    const onClose = vi.fn()
    const empty: TopologyNode = { ...bucket(1), tables: [] }
    render(<DataLakePeeks peek={peek} nodes={new Map([[empty.id, empty]])} onClose={onClose} />)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(onClose).toHaveBeenCalled()
  })
})
