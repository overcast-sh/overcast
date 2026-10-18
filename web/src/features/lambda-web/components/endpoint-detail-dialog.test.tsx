import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import type { WebEndpoint } from "@/services/api/lambda-web"
import { endpointTrafficQueryOptions } from "../use-endpoint-traffic"
import { EndpointDetailDialog } from "./endpoint-detail-dialog"

const endpoint: WebEndpoint = {
  endpointArn: "arn:aws:lambda:us-east-1:000000000000:web-function/shop/endpoint/prod",
  endpointName: "prod",
  endpointType: "HomeRegion",
  domainName: "abcd1234abcd1234.b7t.lambda-web.us-east-1.localhost",
  authType: "ApplicationManaged",
  autoDeploymentMode: "Disabled",
  revisionWeights: [{ revisionId: "rev-ab12c", weight: 100 }],
  regions: ["us-east-1"],
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  updatedAt: new Date("2026-10-01T10:00:00Z"),
}

function renderDialog(e: WebEndpoint = endpoint, environments: number | undefined = 2) {
  const queryClient = createTestQueryClient()
  const minute = Math.floor(Date.now() / 60_000) * 60_000
  const series = (metric: Parameters<typeof endpointTrafficQueryOptions>[2], sum: number) =>
    queryClient.setQueryData(endpointTrafficQueryOptions("shop", "prod", metric).queryKey, {
      datapoints: sum === 0 ? [] : [{ Timestamp: new Date(minute - 60_000), Sum: sum }],
    })
  series("ExecutedRequests", 40)
  series("HTTPResponseStatusClass4XX", 3)
  series("HTTPResponseStatusClass5XX", 0)
  return renderWithRouter(
    () => (
      <EndpointDetailDialog
        functionName="shop"
        endpoint={e}
        region="us-east-1"
        environments={environments}
        onClose={() => {}}
      />
    ),
    { queryClient },
  )
}

describe("EndpointDetailDialog", () => {
  it("offers the invoke URL to open and copy, beside the domain name", async () => {
    renderDialog()
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("2 running or starting")).toBeInTheDocument()
    expect(
      within(dialog).getByRole("link", {
        name: /\/_overcast\/lambdaweb\/invoke\/abcd1234abcd1234\.b7t\/us-east-1\//,
      }),
    ).toBeInTheDocument()
    expect(within(dialog).getByRole("button", { name: "Copy invoke URL" })).toBeInTheDocument()
    expect(within(dialog).getByRole("button", { name: "Copy domain name" })).toBeInTheDocument()
    expect(within(dialog).getByText(/does not take subdomains/)).toBeInTheDocument()
  })

  it("shows the last hour's traffic", async () => {
    renderDialog()
    const traffic = await screen.findByRole("region", { name: "Traffic" })
    expect(within(traffic).getByText("40")).toBeInTheDocument()
    expect(within(traffic).getByText("3")).toBeInTheDocument()
    expect(within(traffic).getByText("none")).toBeInTheDocument()
  })

  it("says an endpoint with no environments is scaled to zero", async () => {
    renderDialog(endpoint, 0)
    expect(await screen.findByText("0 · scaled to zero")).toBeInTheDocument()
  })

  it("explains an IAM auth endpoint and shows how to call it with curl --aws-sigv4", async () => {
    renderDialog({ ...endpoint, authType: "IamAuth" })
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText(/must be signed with SigV4/)).toBeInTheDocument()
    expect(within(dialog).getByText(/aws:amz:us-east-1:lambda/)).toBeInTheDocument()
    expect(within(dialog).queryByRole("link")).not.toBeInTheDocument()
    expect(
      within(dialog).getByText("IAM auth endpoints cannot be tested from the console"),
    ).toBeInTheDocument()
    expect(within(dialog).queryByRole("button", { name: "Send request" })).not.toBeInTheDocument()
  })

  it("puts a failed update's reason where it cannot be missed", async () => {
    renderDialog({ ...endpoint, updateStatus: "Failed", updateStatusReason: "no such revision" })
    expect(await screen.findByRole("alert")).toHaveTextContent("no such revision")
  })
})
