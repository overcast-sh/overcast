import { describe, expect, it, vi } from "vitest"
import type * as ApiModule from "@/services/api"
import { glue } from "@/services/api"
import { renderWithRouter, screen } from "@/test/render"
import { csvTable } from "../../__fixtures__/tables"
import { PartitionsTab } from "./partitions-tab"

vi.mock("@/services/api", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiModule>()),
  glue: { listPartitions: vi.fn() },
  athena: { getEngineStatus: vi.fn(() => Promise.resolve({ state: "ready" })) },
}))

const PARSE_ERROR = 'Unsupported or invalid partition expression "dt >": expected a literal'

function renderTab(expression: string, onExpressionChange = vi.fn()) {
  return {
    ...renderWithRouter(() => (
      <PartitionsTab
        table={csvTable}
        expression={expression}
        onExpressionChange={onExpressionChange}
      />
    )),
    onExpressionChange,
  }
}

describe("PartitionsTab > filtering", () => {
  it("sends the expression to GetPartitions exactly as typed", async () => {
    vi.mocked(glue.listPartitions).mockResolvedValue([])
    renderTab("dt >= '2026-09-01'")

    await screen.findByText("No matching partitions")

    expect(glue.listPartitions).toHaveBeenCalledWith("sales", "orders", "dt >= '2026-09-01'")
  })

  it("shows the service's parse error under the box, word for word", async () => {
    vi.mocked(glue.listPartitions).mockRejectedValue(new Error(PARSE_ERROR))
    renderTab("dt >")

    expect(await screen.findByRole("alert")).toHaveTextContent(PARSE_ERROR)
  })

  it("applies an expression on Enter", async () => {
    vi.mocked(glue.listPartitions).mockResolvedValue([])
    const { user, onExpressionChange } = renderTab("")

    await user.type(
      await screen.findByRole("textbox", { name: "Partition expression" }),
      "dt = '1'{Enter}",
    )

    expect(onExpressionChange).toHaveBeenCalledWith("dt = '1'")
  })
})

describe("PartitionsTab > listing", () => {
  it("shows each partition as its key=value path", async () => {
    vi.mocked(glue.listPartitions).mockResolvedValue([
      {
        Values: ["2026-09-01"],
        StorageDescriptor: { Location: "s3://lake/orders/dt=2026-09-01/" },
      },
    ])
    renderTab("")

    expect(await screen.findByText("dt=2026-09-01")).toBeInTheDocument()
  })

  it("says an unpartitioned table has nothing to list", async () => {
    renderWithRouter(() => (
      <PartitionsTab
        table={{ ...csvTable, PartitionKeys: [] }}
        expression=""
        onExpressionChange={vi.fn()}
      />
    ))

    expect(await screen.findByText("Not partitioned")).toBeInTheDocument()
  })
})
