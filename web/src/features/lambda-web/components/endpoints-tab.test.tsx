import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { createTestQueryClient, renderWithRouter, screen, waitFor, within } from "@/test/render"
import type { WebEndpoint, WebRevision } from "@/services/api/lambda-web"
import { webEndpointsQueryOptions, webRevisionsQueryOptions } from "../data"
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
  domainName: "abc123.lambda-url.us-east-1.on.aws",
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

function renderTab(endpoints: WebEndpoint[] = [prod]) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, endpoints)
  queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, revisions)
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
    expect(within(row).getByText("Pinned")).toBeInTheDocument()
    expect(within(row).getByText("rev-ab12c 100%")).toBeInTheDocument()
    expect(within(row).getByText("500 req/s")).toBeInTheDocument()
  })

  it("shows the domain as text with a copy button, and says it is not served", async () => {
    renderTab()
    const domain = await screen.findByText("abc123.lambda-url.us-east-1.on.aws")
    expect(domain.closest("a")).toBeNull()
    expect(screen.getByRole("button", { name: "Copy domain name" })).toBeInTheDocument()
    expect(screen.getByText("Endpoint domains are not served yet")).toBeInTheDocument()
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
