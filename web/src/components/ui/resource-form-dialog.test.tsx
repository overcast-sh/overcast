import { render, screen } from "@/test/render"
import { ResourceFormDialog, type ResourceFormDialogProps } from "./resource-form-dialog"

function renderDialog(overrides: Partial<ResourceFormDialogProps> = {}) {
  const onSubmit = vi.fn()
  const onOpenChange = vi.fn()
  const result = render(
    <ResourceFormDialog
      open
      onOpenChange={onOpenChange}
      title="Create workgroup"
      action="create"
      submitLabel="Create workgroup"
      busyLabel="Creating"
      onSubmit={onSubmit}
      {...overrides}
    >
      <input aria-label="Name" />
    </ResourceFormDialog>,
  )
  return { ...result, onSubmit, onOpenChange }
}

describe("ResourceFormDialog", () => {
  it("submits on Enter in a field, as its key hint promises", async () => {
    const { user, onSubmit } = renderDialog()
    expect(screen.getByText("⏎ to create · esc to cancel")).toBeInTheDocument()
    await user.type(screen.getByRole("textbox", { name: "Name" }), "adhoc{Enter}")
    expect(onSubmit).toHaveBeenCalledOnce()
  })

  it("closes on Escape", async () => {
    const { user, onOpenChange } = renderDialog()
    await user.keyboard("{Escape}")
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("does not submit while the fields cannot be submitted", async () => {
    const { user, onSubmit } = renderDialog({ canSubmit: false })
    await user.type(screen.getByRole("textbox", { name: "Name" }), "{Enter}")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("does not submit twice while the first submit is pending", async () => {
    const { user, onSubmit } = renderDialog({ pending: true })
    await user.type(screen.getByRole("textbox", { name: "Name" }), "{Enter}")
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it("shows the busy label while pending", () => {
    renderDialog({ pending: true })
    expect(screen.getByRole("button", { name: /Creating/ })).toHaveAttribute("aria-busy", "true")
  })

  it("shows a failed submit's error", () => {
    renderDialog({ error: new Error("WorkGroup adhoc already exists") })
    expect(screen.getByRole("alert")).toHaveTextContent("WorkGroup adhoc already exists")
  })
})
