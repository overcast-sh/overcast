import { render, screen } from "@/test/render"
import { BUCKET_NAME_RULE, bucketNameProblem } from "../names"
import { CreateNameDialog } from "./create-name-dialog"

const onSubmit = vi.fn()

function Harness({ open = true }: { open?: boolean }) {
  return (
    <CreateNameDialog
      open={open}
      onOpenChange={() => {}}
      icon={null}
      title="Create table bucket"
      label="Name"
      placeholder="analytics"
      rule={BUCKET_NAME_RULE}
      problem={bucketNameProblem}
      pending={false}
      error={null}
      onSubmit={onSubmit}
    />
  )
}

describe("CreateNameDialog", () => {
  it("submits the typed name on ⏎", async () => {
    const { user } = render(<Harness />)
    await user.type(screen.getByRole("textbox", { name: /Name/ }), "analytics{Enter}")
    expect(onSubmit).toHaveBeenCalledWith("analytics")
  })

  it("says what is wrong with a name the service would refuse", async () => {
    const { user } = render(<Harness />)
    await user.type(screen.getByRole("textbox", { name: /Name/ }), "Bad_Name")
    expect(screen.getByRole("button", { name: "Create" })).toBeDisabled()
  })

  it("opens empty after being closed from outside, as a successful create does", async () => {
    const { user, rerender } = render(<Harness />)
    await user.type(screen.getByRole("textbox", { name: /Name/ }), "analytics")
    rerender(<Harness open={false} />)
    rerender(<Harness />)
    expect(screen.getByRole("textbox", { name: /Name/ })).toHaveValue("")
  })
})
