import { useState } from "react"
import { LayoutGrid, List, Table2 } from "lucide-react"
import { render, screen } from "@/test/render"
import { SegmentedControl, type SegmentedOption } from "./segmented-control"

type View = "table" | "raw" | "schema"

const VIEWS: readonly SegmentedOption<View>[] = [
  { value: "table", label: "Table", icon: Table2 },
  { value: "raw", label: "Raw" },
  { value: "schema", label: "Schema", hint: "The columns and their types." },
]

function Harness({ onChange }: { onChange?: (view: View) => void }) {
  const [view, setView] = useState<View>("table")
  return (
    <SegmentedControl
      label="Object view"
      value={view}
      options={VIEWS}
      onChange={(next) => {
        setView(next)
        onChange?.(next)
      }}
    />
  )
}

describe("SegmentedControl > semantics", () => {
  it("is a radio group named by its label", () => {
    render(<Harness />)
    expect(screen.getByRole("radiogroup", { name: "Object view" })).toBeInTheDocument()
  })

  it("checks the segment for the current value and no other", () => {
    render(<Harness />)
    expect(screen.getByRole("radio", { checked: true })).toHaveAccessibleName("Table")
  })

  it("puts only the checked segment in the tab order", () => {
    render(<Harness />)
    expect(screen.getAllByRole("radio").map((r) => r.tabIndex)).toEqual([0, -1, -1])
  })

  it("keeps the first segment tabbable when the value matches no option", () => {
    render(
      <SegmentedControl
        label="Object view"
        value={"missing" as View}
        options={VIEWS}
        onChange={() => {}}
      />,
    )
    expect(screen.getByRole("radio", { name: "Table" })).toHaveAttribute("tabindex", "0")
  })

  it("names an icon-only segment after its label", () => {
    render(
      <SegmentedControl
        label="Layout"
        value="grid"
        options={[
          { value: "grid", label: "Grid view", icon: LayoutGrid },
          { value: "list", label: "List view", icon: List },
        ]}
        onChange={() => {}}
        iconOnly
      />,
    )
    expect(screen.getByRole("radio", { name: "List view" })).not.toHaveTextContent("List view")
  })
})

describe("SegmentedControl > pointer", () => {
  it("selects a segment when it is clicked", async () => {
    const { user } = render(<Harness />)
    await user.click(screen.getByRole("radio", { name: "Raw" }))
    expect(screen.getByRole("radio", { name: "Raw" })).toBeChecked()
  })

  it("does not report a change when the checked segment is clicked again", async () => {
    const onChange = vi.fn()
    const { user } = render(<Harness onChange={onChange} />)
    await user.click(screen.getByRole("radio", { name: "Table" }))
    expect(onChange).not.toHaveBeenCalled()
  })
})

describe("SegmentedControl > keyboard", () => {
  it.each([
    ["{ArrowRight}", "Raw"],
    ["{ArrowDown}", "Raw"],
    ["{ArrowLeft}", "Schema"],
    ["{ArrowUp}", "Schema"],
    ["{End}", "Schema"],
  ])("moves focus and selection with %s from the first segment", async (key, expected) => {
    const { user } = render(<Harness />)
    screen.getByRole("radio", { name: "Table" }).focus()
    await user.keyboard(key)
    const target = screen.getByRole("radio", { name: expected })
    expect([target === document.activeElement, target.getAttribute("aria-checked")]).toEqual([
      true,
      "true",
    ])
  })

  it("reaches the checked segment with Tab", async () => {
    const { user } = render(<Harness />)
    await user.tab()
    expect(screen.getByRole("radio", { name: "Table" })).toHaveFocus()
  })

  it.each([["{Enter}"], [" "]])("selects the focused segment with %s", async (key) => {
    const { user } = render(<Harness />)
    // Focus without selecting: the roving tab index leaves it out of the Tab order.
    screen.getByRole("radio", { name: "Raw" }).focus()
    await user.keyboard(key)
    expect(screen.getByRole("radio", { name: "Raw" })).toBeChecked()
  })
})
