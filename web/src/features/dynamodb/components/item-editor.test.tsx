import { render, screen } from "@/test/render"
import { ItemEditorDialog } from "./item-editor"

vi.mock("@monaco-editor/react", async () => {
  const { FakeMonacoEditor } = await import("@/test/monaco")
  return { default: FakeMonacoEditor }
})

function renderEditor() {
  return render(
    <ItemEditorDialog
      open
      onOpenChange={() => {}}
      requiredKeys={[{ name: "pk", type: "S" }]}
      onSubmit={() => {}}
    />,
  )
}

describe("ItemEditorDialog > input mode", () => {
  it("opens on the form", () => {
    renderEditor()
    expect(screen.getByRole("radio", { name: "Form" })).toBeChecked()
  })

  it("offers the JSON format once JSON is chosen", async () => {
    const { user } = renderEditor()
    await user.click(screen.getByRole("radio", { name: "JSON" }))
    expect(screen.getByRole("radiogroup", { name: "JSON format" })).toBeInTheDocument()
  })

  it("returns to the form when Form is chosen again", async () => {
    const { user } = renderEditor()
    await user.click(screen.getByRole("radio", { name: "JSON" }))
    await user.click(screen.getByRole("radio", { name: "Form" }))
    expect(screen.queryByRole("radiogroup", { name: "JSON format" })).not.toBeInTheDocument()
  })
})
