import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { createTestQueryClient, renderWithRouter, screen, waitFor, within } from "@/test/render"
import type { WebEndpoint, WebRevision } from "@/services/api/lambda-web"
import { webEndpointsQueryOptions, webRevisionsQueryOptions } from "../data"
import { RevisionsTab } from "./revisions-tab"

const revisions: WebRevision[] = [
  {
    revisionArn: "arn:aws:lambda:us-east-1:000000000000:web-function/shop/revision/rev-ab12c",
    revisionId: "rev-ab12c",
    state: "Active",
    stateReason: "",
    description: "first cut",
    createdAt: new Date("2026-10-01T10:00:00Z"),
  },
  {
    revisionArn: "arn:aws:lambda:us-east-1:000000000000:web-function/shop/revision/rev-de34f",
    revisionId: "rev-de34f",
    state: "Active",
    stateReason: "",
    createdAt: new Date("2026-10-02T10:00:00Z"),
  },
]

const endpoints = [
  {
    endpointName: "prod",
    revisionWeights: [{ revisionId: "rev-ab12c", weight: 100 }],
  },
] as WebEndpoint[]

function renderTab() {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webRevisionsQueryOptions("shop").queryKey, revisions)
  queryClient.setQueryData(webEndpointsQueryOptions("shop").queryKey, endpoints)
  return renderWithRouter(() => <RevisionsTab functionName="shop" />, { queryClient })
}

describe("RevisionsTab", () => {
  it("lists the revisions with the endpoints serving each", async () => {
    renderTab()
    const row = (await screen.findByText("rev-ab12c")).closest("tr") as HTMLElement
    expect(within(row).getByText("prod")).toBeInTheDocument()
    expect(within(row).getByText("first cut")).toBeInTheDocument()
    const idle = screen.getByText("rev-de34f").closest("tr") as HTMLElement
    expect(within(idle).queryByText("prod")).not.toBeInTheDocument()
  })

  it("warns, before asking, that a revision an endpoint serves cannot be deleted", async () => {
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Delete rev-ab12c" }))
    expect(await screen.findByText(/serving traffic on prod/)).toBeInTheDocument()
  })

  it("keeps the dialog open and shows the service's refusal when the delete is a conflict", async () => {
    server.use(
      http.delete("*/2025-03-07/web-functions/shop/revisions/rev-ab12c", () =>
        HttpResponse.json(
          { message: "Revision rev-ab12c is serving traffic on endpoint prod." },
          { status: 409, headers: { "x-amzn-errortype": "ConflictException" } },
        ),
      ),
    )
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Delete rev-ab12c" }))
    await user.click(await screen.findByRole("button", { name: "Delete" }))
    expect(
      await screen.findByText("Revision rev-ab12c is serving traffic on endpoint prod."),
    ).toBeInTheDocument()
    expect(screen.getByText("Delete revision?")).toBeInTheDocument()
  })

  it("deletes a revision nothing serves", async () => {
    let deleted = ""
    server.use(
      http.delete("*/2025-03-07/web-functions/shop/revisions/:id", ({ params }) => {
        deleted = String(params.id)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    const { user } = renderTab()
    await user.click(await screen.findByRole("button", { name: "Delete rev-de34f" }))
    await user.click(await screen.findByRole("button", { name: "Delete" }))
    await waitFor(() => expect(deleted).toBe("rev-de34f"))
  })
})
