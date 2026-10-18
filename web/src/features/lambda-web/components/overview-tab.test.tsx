import { createTestQueryClient, renderWithRouter, screen } from "@/test/render"
import type { WebEndpoint, WebFunction, WebRevision } from "@/services/api/lambda-web"
import {
  webAccountSettingsQueryOptions,
  webEndpointsQueryOptions,
  webRevisionsQueryOptions,
  webTagsQueryOptions,
} from "../data"
import { OverviewTab } from "./overview-tab"

const ARN = "arn:aws:lambda:us-east-1:000000000000:web-function/shop"
const shop: WebFunction = {
  functionName: "shop",
  functionArn: ARN,
  state: "Active",
  stateReason: "",
  createdAt: new Date("2026-10-01T10:00:00Z"),
  updatedAt: new Date("2026-10-01T10:00:00Z"),
}

describe("OverviewTab", () => {
  it("links the function's application and system logs in CloudWatch Logs", async () => {
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, [] as WebRevision[])
    queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, [] as WebEndpoint[])
    queryClient.setQueryData(webAccountSettingsQueryOptions().queryKey, {
      accountQuotas: undefined,
      accountUsage: undefined,
    })
    queryClient.setQueryData(webTagsQueryOptions(ARN).queryKey, {})
    renderWithRouter(() => <OverviewTab fn={shop} />, { queryClient })

    const application = await screen.findByRole("link", { name: "/aws/lambda/web/shop" })
    expect(application).toHaveAttribute(
      "href",
      expect.stringContaining("/cloudwatch/logs/group?groupName=%2Faws%2Flambda%2Fweb%2Fshop"),
    )
    expect(screen.getByRole("link", { name: "/aws/lambda/web/shop.local" })).toBeInTheDocument()
  })
})
