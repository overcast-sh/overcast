import { describe, it, expect, vi } from "vitest"
import { render, screen, within } from "@/test/render"
import { HighlightedName, LISTING_NOUNS, ObjectSearchBar, SortHead } from "./object-controls"
import { DEFAULT_SORT } from "@/features/s3/object-browser"

function searchBar(over: Partial<React.ComponentProps<typeof ObjectSearchBar>> = {}) {
  return (
    <ObjectSearchBar
      value=""
      onChange={vi.fn()}
      scope="folder"
      onScopeChange={vi.fn()}
      matches={0}
      scanned={0}
      isScanning={false}
      capped={false}
      noun={LISTING_NOUNS.objects}
      {...over}
    />
  )
}

describe("ObjectSearchBar", () => {
  it("reports what the user typed on every keystroke", async () => {
    const onChange = vi.fn()
    const { user } = render(searchBar({ onChange }))
    await user.type(screen.getByRole("searchbox", { name: "Search objects" }), "a")
    expect(onChange).toHaveBeenCalledWith("a")
  })

  it("offers no clear button while the search is empty", () => {
    render(searchBar())
    expect(screen.queryByRole("button", { name: "Clear search" })).not.toBeInTheDocument()
  })

  it("clears the search when the clear button is pressed", async () => {
    const onChange = vi.fn()
    const { user } = render(searchBar({ value: "logs", onChange }))
    await user.click(screen.getByRole("button", { name: "Clear search" }))
    expect(onChange).toHaveBeenCalledWith("")
  })

  it("clears the search when Escape is pressed in the box", async () => {
    const onChange = vi.fn()
    const { user } = render(searchBar({ value: "logs", onChange }))
    await user.type(screen.getByRole("searchbox", { name: "Search objects" }), "{Escape}")
    expect(onChange).toHaveBeenCalledWith("")
  })

  it("marks the active scope as checked", () => {
    render(searchBar({ scope: "recursive" }))
    expect(screen.getByRole("radio", { name: "All nested" })).toBeChecked()
  })

  it("switches to a recursive listing when the other scope is chosen", async () => {
    const onScopeChange = vi.fn()
    const { user } = render(searchBar({ onScopeChange }))
    await user.click(screen.getByRole("radio", { name: "All nested" }))
    expect(onScopeChange).toHaveBeenCalledWith("recursive")
  })

  it("shows a plain count when nothing is being filtered", () => {
    render(searchBar({ scanned: 1234, matches: 1234 }))
    expect(screen.getByText("1,234 objects")).toBeInTheDocument()
  })

  it("shows matches against the number scanned once a search is active", () => {
    render(searchBar({ value: "log", scanned: 1234, matches: 7 }))
    expect(screen.getByText(/7 of 1,234 objects/)).toBeInTheDocument()
  })

  it("says it is still scanning while pages are being pulled", () => {
    render(searchBar({ value: "log", isScanning: true }))
    expect(screen.getByText("scanning…")).toBeInTheDocument()
  })

  it("warns that the view is partial when the scan hit its cap", () => {
    render(searchBar({ value: "log", capped: true }))
    expect(screen.getByText("partial")).toBeInTheDocument()
  })

  it("does not call the view partial while the scan is still running", () => {
    render(searchBar({ value: "log", capped: true, isScanning: true }))
    expect(screen.queryByText("partial")).not.toBeInTheDocument()
  })

  it("names the summary after versions when browsing history", () => {
    render(searchBar({ scanned: 3, noun: LISTING_NOUNS.versions }))
    expect(screen.getByText("3 versions")).toBeInTheDocument()
  })
})

describe("SortHead", () => {
  function head(sort = DEFAULT_SORT, onSort = vi.fn()) {
    return render(
      <table>
        <thead>
          <tr>
            <SortHead column="size" label="Size" sort={sort} onSort={onSort} />
          </tr>
        </thead>
      </table>,
    )
  }

  it("requests its own column when clicked", async () => {
    const onSort = vi.fn()
    const { user } = head(DEFAULT_SORT, onSort)
    await user.click(screen.getByRole("button", { name: /Size/ }))
    expect(onSort).toHaveBeenCalledWith("size")
  })

  it("reports no sort order while another column is active", () => {
    head()
    expect(screen.getByRole("columnheader")).toHaveAttribute("aria-sort", "none")
  })

  it("reports its direction to assistive technology when it is the active column", () => {
    head({ column: "size", direction: "desc" })
    expect(screen.getByRole("columnheader")).toHaveAttribute("aria-sort", "descending")
  })
})

describe("HighlightedName", () => {
  it("marks the matched run and leaves the rest plain", () => {
    render(
      <HighlightedName
        slices={[
          { text: "re", match: false },
          { text: "port", match: true },
        ]}
      />,
    )
    expect(screen.getByText("port").tagName).toBe("MARK")
  })

  it("renders the whole name when nothing matched", () => {
    const { container } = render(<HighlightedName slices={[{ text: "a.txt", match: false }]} />)
    expect(within(container).queryByText("a.txt")).toBeInTheDocument()
    expect(container.querySelector("mark")).toBeNull()
  })
})
