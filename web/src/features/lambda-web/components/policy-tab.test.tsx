import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { createTestQueryClient, renderWithRouter, screen, waitFor } from "@/test/render"
import type { WebPolicy } from "@/services/api/lambda-web"
import { webPolicyQueryOptions } from "../data"
import { PolicyTab } from "./policy-tab"

const ARN = "arn:aws:lambda:us-east-1:123456789012:web-function/shop"
const POLICY_PATH = `*/2025-03-07/resource-policy/${encodeURIComponent(ARN)}`

function renderTab(policy: WebPolicy | null) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(webPolicyQueryOptions(ARN).queryKey, policy)
  return renderWithRouter(() => <PolicyTab functionArn={ARN} />, { queryClient })
}

const stored: WebPolicy = {
  policy: JSON.stringify({ Version: "2012-10-17", Statement: [] }),
  revisionId: "pol-1234",
}

describe("PolicyTab", () => {
  it("says the policy is validated but not enforced", async () => {
    renderTab(null)
    expect(await screen.findByText("Stored, validated, not enforced")).toBeInTheDocument()
  })

  it("starts a new policy from a template in the function's account", async () => {
    const { user } = renderTab(null)
    await user.click(await screen.findByRole("button", { name: "Add a policy" }))
    const text = screen.getByRole<HTMLTextAreaElement>("textbox").value
    expect(text).toContain("arn:aws:iam::123456789012:role/caller")
    expect(text).toContain("lambda:InvokeWebFunctionEndpoint")
  })

  it("shows the policy's revision id with a copy button", async () => {
    renderTab(stored)
    expect(await screen.findByText(/policy revision pol-1234/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Copy policy revision ID" })).toBeInTheDocument()
  })

  it("keeps Save off until the stored policy is edited", async () => {
    renderTab(stored)
    expect(await screen.findByRole("button", { name: "Save policy" })).toBeDisabled()
  })

  it("shows the service's refusal of a public policy, and keeps what was typed", async () => {
    server.use(
      http.put(POLICY_PATH, () =>
        HttpResponse.json(
          {
            message: "The policy is public: it grants access to an unspecified set of principals.",
          },
          { status: 400, headers: { "x-amzn-errortype": "ValidationException" } },
        ),
      ),
    )
    const { user } = renderTab(null)
    await user.click(await screen.findByRole("button", { name: "Add a policy" }))
    await user.click(screen.getByRole("button", { name: "Save policy" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("The policy is public")
    expect(screen.getByRole<HTMLTextAreaElement>("textbox").value).toContain("lambda:Invoke")
  })

  it("sends the revision id it loaded the policy at, so a concurrent change is refused", async () => {
    let body: Record<string, unknown> | undefined
    server.use(
      http.put(POLICY_PATH, async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ policy: "{}", revisionId: "pol-5678" })
      }),
    )
    const { user } = renderTab(stored)
    const editor = await screen.findByRole("textbox")
    await user.clear(editor)
    await user.type(editor, "{{}")
    await user.click(screen.getByRole("button", { name: "Save policy" }))
    await waitFor(() => expect(body).toBeDefined())
    expect(body).toEqual({ Policy: "{}", RevisionId: "pol-1234" })
  })
})
