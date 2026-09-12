import { fireEvent, render, screen } from "@/test/render"
import { ResizableSplit, type ResizableSplitProps } from "./resizable-split"

function split(props: Partial<ResizableSplitProps> = {}) {
  return render(
    <ResizableSplit
      direction="horizontal"
      label="Resize the panels"
      defaultSize={320}
      minSize={240}
      maxSize={640}
      first={<div data-testid="first">code</div>}
      second={<div data-testid="second">panels</div>}
      {...props}
    />,
  )
}

const handle = () => screen.getByRole("separator", { name: "Resize the panels" })
const size = () => Number(handle().getAttribute("aria-valuenow"))

beforeEach(() => localStorage.clear())

describe("ResizableSplit", () => {
  it("is a separator naming the sized pane's size, and lays the sized pane out at it", () => {
    split()
    const sep = handle()
    expect(sep).toHaveAttribute("aria-orientation", "vertical")
    expect(sep).toHaveAttribute("aria-valuenow", "320")
    expect(sep).toHaveAttribute("aria-valuemin", "240")
    expect(sep).toHaveAttribute("aria-valuemax", "640")
    expect(sep).toHaveAttribute("tabindex", "0")
    expect(screen.getByTestId("second").parentElement).toHaveStyle({ flex: "0 0 320px" })
    expect(screen.getByTestId("first").parentElement).toHaveStyle({ flex: "1 1 auto" })
  })

  it("follows a pointer drag, within the bounds, and stops following on release", () => {
    split()
    const sep = handle()
    fireEvent.pointerDown(sep, { pointerId: 1, button: 0, clientX: 500 })
    // Left grows the second pane; right shrinks it.
    fireEvent.pointerMove(sep, { pointerId: 1, clientX: 440 })
    expect(size()).toBe(380)
    fireEvent.pointerMove(sep, { pointerId: 1, clientX: 600 })
    expect(size()).toBe(240)
    fireEvent.pointerMove(sep, { pointerId: 1, clientX: 0 })
    expect(size()).toBe(640)
    // Another pointer is not this drag.
    fireEvent.pointerMove(sep, { pointerId: 2, clientX: 300 })
    expect(size()).toBe(640)
    fireEvent.pointerUp(sep, { pointerId: 1, clientX: 0 })
    fireEvent.pointerMove(sep, { pointerId: 1, clientX: 500 })
    expect(size()).toBe(640)
  })

  it("resizes with the arrow keys, by four steps with Shift, and to the bounds with Home and End", () => {
    split({ step: 10 })
    const sep = handle()
    fireEvent.keyDown(sep, { key: "ArrowLeft" })
    expect(size()).toBe(330)
    fireEvent.keyDown(sep, { key: "ArrowRight", shiftKey: true })
    expect(size()).toBe(290)
    fireEvent.keyDown(sep, { key: "Home" })
    expect(size()).toBe(240)
    fireEvent.keyDown(sep, { key: "End" })
    expect(size()).toBe(640)
    // The other axis is not the split's.
    fireEvent.keyDown(sep, { key: "ArrowUp" })
    expect(size()).toBe(640)
  })

  it("moves up and down when vertical, with the separator reading horizontal", () => {
    split({ direction: "vertical", label: "Resize the drawer" })
    const sep = screen.getByRole("separator", { name: "Resize the drawer" })
    expect(sep).toHaveAttribute("aria-orientation", "horizontal")
    fireEvent.keyDown(sep, { key: "ArrowUp" })
    expect(sep).toHaveAttribute("aria-valuenow", "336")
    fireEvent.pointerDown(sep, { pointerId: 1, button: 0, clientY: 700 })
    fireEvent.pointerMove(sep, { pointerId: 1, clientY: 750 })
    expect(sep).toHaveAttribute("aria-valuenow", "286")
  })

  it("sizes the first pane instead when asked, with the directions mirrored", () => {
    split({ sized: "first" })
    const sep = handle()
    expect(screen.getByTestId("first").parentElement).toHaveStyle({ flex: "0 0 320px" })
    fireEvent.keyDown(sep, { key: "ArrowRight" })
    expect(size()).toBe(336)
    fireEvent.pointerDown(sep, { pointerId: 1, button: 0, clientX: 500 })
    fireEvent.pointerMove(sep, { pointerId: 1, clientX: 540 })
    expect(size()).toBe(376)
  })

  it("remembers the size under its storage key and forgets it on a double-click reset", () => {
    const first = split({ storageKey: "test:split" })
    fireEvent.keyDown(handle(), { key: "ArrowLeft" })
    expect(localStorage.getItem("test:split")).toBe("336")
    first.unmount()

    const second = split({ storageKey: "test:split" })
    expect(size()).toBe(336)
    fireEvent.doubleClick(handle())
    expect(size()).toBe(320)
    expect(localStorage.getItem("test:split")).toBeNull()
    second.unmount()

    // A stored size outside the bounds is clamped, and rubbish is ignored.
    localStorage.setItem("test:split", "9000")
    const third = split({ storageKey: "test:split" })
    expect(size()).toBe(640)
    third.unmount()
    localStorage.setItem("test:split", "{not a number")
    split({ storageKey: "test:split" })
    expect(size()).toBe(320)
  })

  it("reports through onSizeChange when controlled, and shows the size it is given", () => {
    const onSizeChange = vi.fn()
    const view = split({ size: 400, onSizeChange })
    expect(size()).toBe(400)
    fireEvent.keyDown(handle(), { key: "ArrowLeft" })
    expect(onSizeChange).toHaveBeenCalledWith(416)
    // Not applied until the owner does.
    expect(size()).toBe(400)
    fireEvent.doubleClick(handle())
    expect(onSizeChange).toHaveBeenLastCalledWith(320)
    view.rerender(
      <ResizableSplit
        direction="horizontal"
        label="Resize the panels"
        defaultSize={320}
        minSize={240}
        maxSize={640}
        size={416}
        onSizeChange={onSizeChange}
        first={<div />}
        second={<div />}
      />,
    )
    expect(size()).toBe(416)
  })
})
