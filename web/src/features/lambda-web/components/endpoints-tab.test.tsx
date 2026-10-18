import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import {
  act,
  createTestQueryClient,
  renderWithRouter,
  screen,
  waitFor,
  within,
} from "@/test/render"
import { useFakeTimers } from "@/test/fake-timers"
import type { TopologyResponse } from "@/types"
import type { WebEndpoint, WebRevision } from "@/services/api/lambda-web"
import { webEndpointsQueryOptions, webRevisionsQueryOptions } from "../data"
import { TRANSITION_POLL_MS } from "../models"
import { endpointTrafficQueryOptions } from "../use-endpoint-traffic"
import { EndpointsTab } from "./endpoints-tab"

const FN_ARN = "arn:aws:lambda:us-east-1:000000000000:web-function/shop"

const revisions: WebRevision[] = ["rev-ab12c", "rev-de34f"].map((revisionId) => ({
  revisionArn: `${FN_ARN}/revision/${revisionId}`,
  revisionId,
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
}))

const prod: WebEndpoint = {
  endpointArn: `${FN_ARN}/endpoint/prod`,
  endpointName: "prod",
  description: "customers",
  endpointType: "HomeRegion",
  domainName: "abcd1234abcd1234.b7t.lambda-web.us-east-1.localhost",
  authType: "ApplicationManaged",
  autoDeploymentMode: "Disabled",
  revisionWeights: [{ revisionId: "rev-ab12c", weight: 100 }],
  regions: ["us-east-1"],
  throttleConfig: { rateLimit: 500 },
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  updatedAt: new Date("2026-10-01T10:00:00Z"),
}

/** The map's node for `shop`, with `environments` running behind `prod`. */
function topologyWith(environments: number): TopologyResponse {
  return {
    regions: ["us-east-1"],
    edges: [],
    nodes: [
      {
        id: "us-east-1::lambdaweb::shop",
        service: "lambdaweb",
        label: "shop",
        region: "us-east-1",
        webEndpoints: [
          {
            name: "prod",
            type: "HomeRegion",
            authType: "ApplicationManaged",
            autoDeployment: "Disabled",
            domainName: "",
            serves: [],
            environments,
          },
        ],
      },
    ],
  }
}

function renderTab(endpoints: WebEndpoint[] = [prod]) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, endpoints)
  queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, revisions)
  // The Traffic column reads CloudWatch; an hour of silence.
  for (const e of endpoints) {
    queryClient.setQueryData(
      endpointTrafficQueryOptions("shop", e.endpointName ?? "", "ExecutedRequests").queryKey,
      { datapoints: [] },
    )
  }
  return renderWithRouter(() => <EndpointsTab functionName="shop" homeRegion="us-east-1" />, {
    queryClient,
  })
}

/** Answers the next request to `method` `path` with an empty endpoint, keeping its body. */
function capture(method: "put" | "patch", path: string) {
  const seen: { body?: Record<string, unknown> } = {}
  server.use(
    http[method](path, async ({ request }) => {
      seen.body = (await request.json()) as Record<string, unknown>
      return HttpResponse.json({ endpointName: "prod" })
    }),
  )
  return seen
}

describe("EndpointsTab", () => {
  it("shows each endpoint's type, authentication, mode, what it serves and its limits", async () => {
    renderTab()
    const row = (await screen.findByText("prod")).closest("tr") as HTMLElement
    expect(within(row).getByText("HomeRegion")).toBeInTheDocument()
    expect(within(row).getByText("App managed")).toBeInTheDocument()
    expect(within(row).getByText("rev-ab12c 100%")).toBeInTheDocument()
    expect(within(row).getByText("500 req/s")).toBeInTheDocument()
  })

  it("links to the endpoint through the invoke path, which works with any Host", async () => {
    // The console reaches the emulator on localhost, which takes no subdomains here.
    renderTab()
    const row = (await screen.findByText("prod")).closest("tr") as HTMLElement
    const link = within(row).getByRole("link", {
      name: /_overcast\/lambdaweb\/invoke\/abcd1234abcd1234\.b7t\/us-east-1\//,
    })
    expect(link).toHaveAttribute(
      "href",
      "http://localhost:4566/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1/",
    )
    expect(link).toHaveAttribute("target", "_blank")
    expect(within(row).getByRole("button", { name: "Copy invoke URL" })).toBeInTheDocument()
    expect(screen.queryByText(/not served/i)).not.toBeInTheDocument()
  })

  it("shows an IAM auth endpoint's address as text: a browser cannot sign", async () => {
    renderTab([{ ...prod, authType: "IamAuth" }])
    const row = (await screen.findByText("prod")).closest("tr") as HTMLElement
    expect(within(row).queryByRole("link")).not.toBeInTheDocument()
    expect(within(row).getByText(/_overcast\/lambdaweb\/invoke/)).toHaveAttribute(
      "title",
      expect.stringContaining("SigV4"),
    )
  })

  it("shows how many environments each endpoint has, from the map's topology", async () => {
    server.use(http.get("/api/topology", () => HttpResponse.json(topologyWith(3))))
    renderTab()
    const row = (await screen.findByText("prod")).closest("tr") as HTMLElement
    expect(await within(row).findByText("3")).toBeInTheDocument()
  })

  it("says an endpoint with no environments is scaled to zero", async () => {
    server.use(http.get("/api/topology", () => HttpResponse.json(topologyWith(0))))
    renderTab()
    const row = (await screen.findByText("prod")).closest("tr") as HTMLElement
    expect(await within(row).findByText("scaled to zero")).toBeInTheDocument()
  })

  it("shows a failed endpoint's reason and an update still in progress", async () => {
    renderTab([
      { ...prod, state: "Failed", stateReason: "The endpoint could not be registered: boom" },
      {
        ...prod,
        endpointName: "dev",
        endpointArn: `${FN_ARN}/endpoint/dev`,
        updateStatus: "InProgress",
        updateStatusReason: "Routing traffic",
      },
    ])
    expect(
      await screen.findByText("The endpoint could not be registered: boom"),
    ).toBeInTheDocument()
    const dev = screen.getByText("dev").closest("tr") as HTMLElement
    expect(within(dev).getByText("InProgress")).toBeInTheDocument()
    expect(within(dev).getByText("Routing traffic")).toBeInTheDocument()
  })

  describe("while an endpoint settles", () => {
    useFakeTimers()

    it("polls until it is Active", async () => {
      let reads = 0
      server.use(
        http.post("*/2025-03-07/web-functions/shop/list-endpoints", () => {
          reads++
          return HttpResponse.json({
            endpoints: [{ ...prod, state: "Active", createdAt: "2026-10-01T10:00:00Z" }],
          })
        }),
      )
      renderTab([{ ...prod, state: "Pending" }])
      expect(await screen.findByText("Pending")).toBeInTheDocument()
      await act(() => vi.advanceTimersByTimeAsync(TRANSITION_POLL_MS + 100))
      expect(await screen.findByText("Active")).toBeInTheDocument()
      expect(reads).toBeGreaterThan(0)
    })
  })

  it("opens the endpoint's detail from its row, with a test request form", async () => {
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Test prod" }))
    expect(await screen.findByRole("heading", { name: "Test request" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Send request" })).toBeInTheDocument()
  })

  it("explains what an empty function needs", async () => {
    renderTab([])
    expect(await screen.findByText("No endpoints yet")).toBeInTheDocument()
  })

  it("creates a HomeRegion endpoint that follows the newest revision", async () => {
    const seen = capture("put", "*/2025-03-07/web-functions/shop/endpoints")
    const { user } = renderTab([])
    // The header's button and the empty state's.
    await user.click((await screen.findAllByRole("button", { name: "Create endpoint" }))[0])
    await user.type(await screen.findByLabelText(/Endpoint name/), "dev")
    await user.click(screen.getByRole("button", { name: "Create endpoint", hidden: false }))
    await waitFor(() => expect(seen.body).toBeDefined())
    expect(seen.body).toEqual({
      endpointName: "dev",
      endpointType: "HomeRegion",
      authType: "ApplicationManaged",
      autoDeploymentMode: "LatestRevision",
    })
  })

  it("will not create a name the function already uses", async () => {
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Create endpoint" }))
    await user.type(await screen.findByLabelText(/Endpoint name/), "prod")
    await user.click(screen.getByRole("button", { name: "Create endpoint", hidden: false }))
    expect(
      await screen.findByText("The function already has an endpoint by this name."),
    ).toBeInTheDocument()
  })

  it("splits an edited endpoint's traffic between two revisions", async () => {
    const seen = capture("patch", "*/2025-03-07/web-functions/shop/endpoints/prod")
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Edit prod" }))
    await user.selectOptions(await screen.findByLabelText(/Split traffic with/), "rev-de34f")
    await user.click(screen.getByRole("button", { name: "90 / 10" }))
    await user.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(seen.body).toBeDefined())
    expect(seen.body).toMatchObject({
      authType: "ApplicationManaged",
      autoDeploymentMode: "Disabled",
      revisionWeights: [
        { revisionId: "rev-ab12c", weight: 90 },
        { revisionId: "rev-de34f", weight: 10 },
      ],
    })
    // The rate limit was left as it was: the update does not mention it unless it is cleared.
    expect(seen.body).toHaveProperty("throttleConfig", { rateLimit: 500 })
  })

  it("clears a rate limit with an empty throttleConfig", async () => {
    const seen = capture("patch", "*/2025-03-07/web-functions/shop/endpoints/prod")
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Edit prod" }))
    await user.selectOptions(await screen.findByLabelText("Rate limit (requests/s)"), "")
    await user.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(seen.body).toBeDefined())
    expect(seen.body).toHaveProperty("throttleConfig", {})
    expect(seen.body).not.toHaveProperty("revisionWeights")
  })

  it("hides the weights while the endpoint follows the newest revision", async () => {
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Edit prod" }))
    expect(await screen.findByLabelText(/Serves revision/)).toBeInTheDocument()
    await user.click(screen.getByRole("radio", { name: /Latest revision/ }))
    expect(screen.queryByLabelText(/Serves revision/)).not.toBeInTheDocument()
  })
})
