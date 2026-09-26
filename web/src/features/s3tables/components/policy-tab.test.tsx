import { createTestQueryClient, renderWithRouter, screen } from "@/test/render"
import { policyQueryOptions, type ConfigTarget } from "../data"
import { PolicyTab } from "./policy-tab"

const ARN = "arn:aws:s3tables:us-east-1:123456789012:bucket/analytics"
const target: ConfigTarget = { resource: { kind: "bucket", tableBucketARN: ARN }, arn: ARN }

function renderTab(policy: string | null) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(policyQueryOptions(target).queryKey, policy)
  return renderWithRouter(() => <PolicyTab target={target} />, { queryClient })
}

describe("PolicyTab", () => {
  it("says the policy is stored but not enforced", async () => {
    renderTab(null)
    expect(await screen.findByText("Stored, not enforced")).toBeInTheDocument()
  })

  it("starts a new policy from a template in the resource's account", async () => {
    const { user } = renderTab(null)
    await user.click(await screen.findByRole("button", { name: "Add a policy" }))
    expect(screen.getByRole<HTMLTextAreaElement>("textbox").value).toContain(
      "arn:aws:iam::123456789012:role/reader",
    )
  })

  it("keeps Save off until the stored policy is edited", async () => {
    renderTab('{"Version":"2012-10-17","Statement":[]}')
    expect(await screen.findByRole("button", { name: "Save policy" })).toBeDisabled()
  })
})
