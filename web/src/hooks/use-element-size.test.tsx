import { useRef } from "react"
import { render, screen } from "@/test/render"
import { useElementSize } from "./use-element-size"

function Measured() {
  const ref = useRef<HTMLDivElement>(null)
  const { width, height } = useElementSize(ref)
  return (
    <div ref={ref}>
      {width}×{height}
    </div>
  )
}

describe("useElementSize", () => {
  it("reports the element's size inside its scrollbars from the first paint", () => {
    // Given: a layout where every element is 320 × 200 inside its scrollbars
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(320)
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(200)
    // When: it renders
    render(<Measured />)
    // Then: the size is already measured
    expect(screen.getByText("320×200")).toBeInTheDocument()
  })
})
