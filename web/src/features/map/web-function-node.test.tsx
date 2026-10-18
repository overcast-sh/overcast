import type { ComponentProps } from "react"
import { renderWithRouter, screen, within } from "@/test/render"
import type { TopologyNode } from "@/types"
import { DataLakeOverlayContext, type DataLakeNodeData } from "./data-lake-context"
import { applyLakeEvent, EMPTY_OVERLAY, rowKey, type DataLakeOverlay } from "./data-lake-overlay"
import { WebFunctionNode } from "./web-function-node"

vi.mock("@xyflow/react", () => ({
  Handle: () => <div data-testid="handle" />,
  Position: { Left: "left", Right: "right" },
}))

type NodeProps = ComponentProps<typeof WebFunctionNode>

function renderNode(node: TopologyNode, overlay: DataLakeOverlay = EMPTY_OVERLAY) {
  const data: DataLakeNodeData = { topologyNode: node }
  const props = { id: node.id, data, selected: false } as unknown as NodeProps
  return renderWithRouter(() => (
    <DataLakeOverlayContext value={overlay}>
      <WebFunctionNode {...props} />
    </DataLakeOverlayContext>
  ))
}

const node: TopologyNode = {
  id: "us-east-1::lambdaweb::shop",
  service: "lambdaweb",
  label: "shop",
  region: "us-east-1",
  webRevisions: 3,
  webEndpoints: [
    {
      name: "prod",
      type: "HomeRegion",
      authType: "ApplicationManaged",
      autoDeployment: "Disabled",
      domainName: "abc.lambda-url.us-east-1.on.aws",
      serves: [
        { revisionId: "rev-ab12c", weight: 90 },
        { revisionId: "rev-de34f", weight: 10 },
      ],
      environments: 2,
    },
    {
      name: "edge",
      type: "MultiRegion",
      authType: "IamAuth",
      autoDeployment: "Disabled",
      domainName: "def.lambda-url.on.aws",
      serves: [{ revisionId: "rev-ab12c", weight: 100 }],
      environments: 1,
    },
    {
      name: "dev",
      type: "HomeRegion",
      authType: "ApplicationManaged",
      autoDeployment: "LatestRevision",
      domainName: "ghi.lambda-url.us-east-1.on.aws",
      serves: [],
      environments: 0,
    },
  ],
}

function overlayAfter(type: string, payload: object): DataLakeOverlay {
  return applyLakeEvent(EMPTY_OVERLAY, { type, payload }, 1_000)
}

describe("WebFunctionNode", () => {
  it("names the function and counts its revisions", async () => {
    renderNode(node)
    expect(await screen.findByText("Web function")).toBeInTheDocument()
    expect(screen.getByText("3 revisions")).toBeInTheDocument()
  })

  it("lists each endpoint with its type, authentication and the revisions it serves", async () => {
    renderNode(node)
    const prod = await screen.findByRole("button", {
      name: "Endpoint prod: rev-ab12c 90% · rev-de34f 10%",
    })
    expect(within(prod).getByText("home")).toBeInTheDocument()
    expect(within(prod).getByText("app")).toBeInTheDocument()
    expect(within(prod).getByText("rev-ab12c 90% · rev-de34f 10%")).toBeInTheDocument()

    const edge = screen.getByRole("button", { name: "Endpoint edge: rev-ab12c 100%" })
    expect(within(edge).getByText("multi")).toBeInTheDocument()
    expect(within(edge).getByText("IAM")).toBeInTheDocument()
  })

  it("says so when an endpoint serves nothing, and marks one that follows the newest revision", async () => {
    renderNode(node)
    const dev = await screen.findByRole("button", { name: "Endpoint dev: no revision" })
    expect(within(dev).getByText("latest")).toBeInTheDocument()
    expect(within(dev).getByText("no revision")).toBeInTheDocument()
  })

  it("shows no domain: the endpoints tab has it", async () => {
    renderNode(node)
    await screen.findByText("Web function")
    expect(screen.queryByText(/lambda-url/)).not.toBeInTheDocument()
  })

  it("opens the function's endpoints tab from an endpoint row", async () => {
    const { user, router } = renderNode(node)
    await user.click(await screen.findByRole("button", { name: /^Endpoint prod/ }))
    expect(router.state.location.pathname).toBe("/lambda-web/shop")
    expect(router.state.location.search).toEqual({ tab: "endpoints" })
  })

  it("leaves an endpoint that was just deleted as a struck-through ghost", async () => {
    const overlay = overlayAfter("lambda-web:EndpointDeleted", {
      function: "shop",
      endpoint: "old",
    })
    renderNode(node, overlay)
    const ghost = await screen.findByText("old")
    expect(ghost).toHaveClass("line-through")
    expect(screen.getByText("deleted")).toBeInTheDocument()
  })

  it("cuts a long list at four endpoints with a count of the rest", async () => {
    const many = Array.from({ length: 6 }, (_, i) => ({
      ...node.webEndpoints![0],
      name: `ep-${i}`,
    }))
    renderNode({ ...node, webEndpoints: many })
    await screen.findByText("ep-0")
    expect(screen.getAllByRole("button", { name: /^Endpoint ep-/ })).toHaveLength(4)
    expect(screen.getByText("+2 more endpoints")).toBeInTheDocument()
  })

  it("sweeps the endpoint a create or update names", async () => {
    const overlay = overlayAfter("lambda-web:EndpointUpdated", {
      function: "shop",
      endpoint: "prod",
    })
    expect(overlay.rows[rowKey("lambdaweb::shop", "prod")]?.flashes).toBe(1)
    const { container } = renderNode(node, overlay)
    const prod = await screen.findByRole("button", { name: /^Endpoint prod/ })
    expect(prod.querySelector("[aria-hidden]")).not.toBeNull()
    expect(container.querySelectorAll("[style*='overcastSweep']")).toHaveLength(1)
  })

  describe("the data plane", () => {
    it("says how many environments each endpoint has, and the function in all", async () => {
      renderNode(node)
      const prod = await screen.findByRole("button", { name: /^Endpoint prod/ })
      expect(within(prod).getByText("2 envs")).toBeInTheDocument()
      const edge = screen.getByRole("button", { name: /^Endpoint edge/ })
      expect(within(edge).getByText("1 env")).toBeInTheDocument()
      const dev = screen.getByRole("button", { name: /^Endpoint dev/ })
      expect(within(dev).getByText("0 envs")).toBeInTheDocument()
      // The header sums the same rows.
      expect(screen.getAllByText("3 envs")).toHaveLength(1)
      expect(screen.queryByText("scaled to zero")).not.toBeInTheDocument()
    })

    it("says the function is scaled to zero when no endpoint has an environment", async () => {
      const idle = {
        ...node,
        webEndpoints: node.webEndpoints!.map((e) => ({ ...e, environments: 0 })),
      }
      renderNode(idle)
      expect(await screen.findByText("scaled to zero")).toBeInTheDocument()
    })

    it("does not call a function with no endpoints scaled to zero", async () => {
      renderNode({ ...node, webEndpoints: [] })
      await screen.findByText("Web function")
      expect(screen.queryByText("scaled to zero")).not.toBeInTheDocument()
    })

    it.each([
      [200, "2xx"],
      [301, "3xx"],
      [404, "4xx"],
      [429, "4xx"],
      [502, "5xx"],
    ])("pulses the endpoint a %i response reached, as %s", async (status, statusClass) => {
      const overlay = overlayAfter("lambda-web:RequestCompleted", {
        function: "shop",
        endpoint: "prod",
        status,
        coldStart: false,
        count: 3,
      })
      renderNode(node, overlay)
      const prod = await screen.findByRole("button", { name: /^Endpoint prod/ })
      expect(prod.querySelector(`[data-request-class='${statusClass}']`)).not.toBeNull()
      expect(within(prod).queryByLabelText("Cold start")).not.toBeInTheDocument()
    })

    it("marks a cold start on the endpoint it reached, and only there", async () => {
      const overlay = overlayAfter("lambda-web:RequestCompleted", {
        function: "shop",
        endpoint: "prod",
        status: 200,
        coldStart: true,
      })
      renderNode(node, overlay)
      const prod = await screen.findByRole("button", { name: /^Endpoint prod/ })
      expect(within(prod).getByLabelText("Cold start")).toBeInTheDocument()
      const edge = screen.getByRole("button", { name: /^Endpoint edge/ })
      expect(within(edge).queryByLabelText("Cold start")).not.toBeInTheDocument()
    })

    it("sweeps the endpoint an environment started or stopped for", async () => {
      const started = overlayAfter("lambda-web:EnvironmentStarted", {
        function: "shop",
        endpoint: "prod",
        reason: "initialized",
      })
      const { container, unmount } = renderNode(node, started)
      await screen.findByRole("button", { name: /^Endpoint prod/ })
      expect(container.querySelectorAll("[style*='overcastSweep']")).toHaveLength(1)
      expect(container.querySelector("[style*='--color-success']")).not.toBeNull()
      unmount()

      const stopped = overlayAfter("lambda-web:EnvironmentStopped", {
        function: "shop",
        endpoint: "prod",
        reason: "requested",
      })
      const again = renderNode(node, stopped)
      await screen.findByRole("button", { name: /^Endpoint prod/ })
      expect(again.container.querySelectorAll("[style*='overcastSweep']")).toHaveLength(1)
      expect(again.container.querySelector("[style*='--color-fg-muted']")).not.toBeNull()
    })
  })
})
