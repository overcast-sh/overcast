import { memorySource } from "@/lib/data-sources/memory-source"
import { useFakeTimers } from "@/test/fake-timers"
import { act, fireEvent, render, screen, waitFor, within } from "@/test/render"
import { DataGrid } from "./data-grid"
import { nativeFor, ROW_HEIGHT, SAFE_HEIGHT_CAP } from "./scroll-model"
import { FakeSource } from "./testing/fake-source"
import { stubLayout } from "./testing/layout"
import { SETTLE_MS } from "./use-hybrid-scroll"

const clipboard = vi.hoisted(() => ({ text: "" }))
vi.mock("@/lib/clipboard", () => ({
  writeClipboardText: (text: string) => {
    clipboard.text = text
    return Promise.resolve()
  },
}))

/** 960 × 480 with a one-line header: 448 px of rows, 16 rows a page. */
const VIEWPORT = 480 - 32
const PAGE_ROWS = Math.floor(VIEWPORT / ROW_HEIGHT)
const NOTCH = 100

const grid = () => screen.getByRole("grid")

/** The row numbers on screen, as numbers. */
const rowNumbers = () =>
  within(grid())
    .getAllByRole("rowheader")
    .map((header) => Number(header.textContent.replace(/,/g, "")))

/** Scrolls the grid as the browser would, and lets the frame run. */
function scrollGrid(top: number, left?: number) {
  const element = grid()
  act(() => {
    element.scrollTop = top
    if (left !== undefined) element.scrollLeft = left
    fireEvent.scroll(element)
    vi.advanceTimersByTime(16)
  })
}

/** Lets the scroll come to rest. */
function settle() {
  act(() => {
    vi.advanceTimersByTime(SETTLE_MS + 10)
  })
}

/** Lets reads answered on the microtask queue land. */
const landed = () => act(async () => {})

describe("DataGrid", () => {
  useFakeTimers()
  beforeEach(() => stubLayout())

  describe("rendering", () => {
    it("renders a screenful of rows under a header, however many rows there are", async () => {
      render(<DataGrid source={new FakeSource(5_000_000)} label="Rows" />)
      await landed()
      expect(grid()).toHaveAttribute("aria-rowcount", "5000001")
      expect(within(grid()).getByRole("columnheader", { name: "col_0" })).toBeInTheDocument()
      expect(within(grid()).getByText("r0c0")).toBeInTheDocument()
      expect(rowNumbers()).toEqual(Array.from({ length: PAGE_ROWS }, (_, i) => i + 1))
    })

    it("shows static skeletons for rows not loaded yet", () => {
      render(<DataGrid source={new FakeSource(100, { hold: true })} label="Rows" />)
      expect(within(grid()).getAllByRole("gridcell")[0]).toHaveAttribute("aria-busy", "true")
    })

    it("keeps the spacer under the browser's height cap for millions of rows", () => {
      render(<DataGrid source={new FakeSource(5_000_000)} label="Rows" />)
      const spacer = grid().firstElementChild as HTMLElement
      expect(parseFloat(spacer.style.height)).toBeLessThanOrEqual(SAFE_HEIGHT_CAP + 32)
    })

    it("says so when the source has no rows", () => {
      render(
        <DataGrid
          source={new FakeSource(0)}
          label="Rows"
          emptyMessage="No rows below the header."
        />,
      )
      expect(screen.getByText("No rows below the header.")).toBeInTheDocument()
    })
  })

  describe("tokens", () => {
    const source = memorySource(
      [
        { name: "email", numeric: false },
        { name: "note", numeric: false },
      ],
      [
        [null, ""],
        [undefined, "x"],
      ],
    )

    it.each([
      ["NULL", "NULL"],
      ["an empty string", "empty string"],
      ["a key the record does not have", "not present"],
    ])("names %s for screen readers by its text, not an aria-label", async (_, name) => {
      render(<DataGrid source={source} label="Rows" />)
      await landed()
      expect(within(grid()).getByRole("gridcell", { name })).toBeInTheDocument()
    })
  })

  describe("hybrid scrolling", () => {
    it.each([1_000, 5_000_000])("moves one wheel notch the same rows at %i rows", (rows) => {
      render(<DataGrid source={new FakeSource(rows)} label="Rows" />)
      scrollGrid(NOTCH)
      expect(rowNumbers()[0]).toBe(Math.floor(NOTCH / ROW_HEIGHT) + 1)
    })

    it("lands near row 2.5 million when the thumb is dragged half-way down 5 million", () => {
      render(<DataGrid source={new FakeSource(5_000_000)} label="Rows" />)
      scrollGrid((SAFE_HEIGHT_CAP - VIEWPORT) / 2)
      expect(Math.abs(rowNumbers()[0] - 2_500_000)).toBeLessThan(100)
    })

    it("moves the cursor and the rows one viewport with PageDown", async () => {
      // Given: the cursor on the first row
      const { user } = render(<DataGrid source={new FakeSource(5_000_000)} label="Rows" />)
      await user.click(await within(grid()).findByText("r0c0"))
      // When: PageDown is pressed
      await user.keyboard("{PageDown}")
      // Then: both moved down one viewport of rows
      expect(rowNumbers()[0]).toBe(PAGE_ROWS + 1)
      expect(grid().getAttribute("aria-activedescendant")).toMatch(new RegExp(`-${PAGE_ROWS}-0$`))
    })
  })

  describe("fetching", () => {
    it("reads only the final view's blocks when the thumb is dragged from row 0 to row 4,000,000", async () => {
      // Given: five million rows, the first block loaded
      const source = new FakeSource(5_000_000, { hold: true })
      render(<DataGrid source={source} label="Rows" />)
      source.release()
      await landed()
      const before = source.reads.length
      // When: the thumb is dragged down in ten fast frames, and the scroll settles
      const geometry = {
        contentHeight: 5_000_000 * ROW_HEIGHT,
        viewport: VIEWPORT,
        cap: SAFE_HEIGHT_CAP,
      }
      const target = nativeFor(4_000_000 * ROW_HEIGHT, geometry)
      for (let step = 1; step <= 10; step++) scrollGrid((target * step) / 10)
      expect(source.reads).toHaveLength(before)
      settle()
      // Then: the blocks under the final view were read, and nothing on the way
      const blocks = new Set(rowNumbers().map((row) => Math.floor((row - 1) / 1000) * 1000))
      expect(source.reads.slice(before).map((read) => read.start)).toEqual([...blocks])
      expect(Math.abs(rowNumbers()[0] - 4_000_000)).toBeLessThan(10)
    })

    it("aborts a read whose block has left the view", () => {
      const source = new FakeSource(1_000_000, { hold: true })
      render(<DataGrid source={source} label="Rows" />)
      scrollGrid(SAFE_HEIGHT_CAP / 2)
      expect(source.reads[0].aborted).toBe(true)
    })

    it("asks a projecting source only for the columns in view", async () => {
      const source = new FakeSource(10, { columns: 200, projects: true })
      render(<DataGrid source={source} label="Rows" />)
      await landed()
      expect(Math.max(...source.reads[0].cols)).toBeLessThan(20)
    })
  })

  describe("keyboard", () => {
    it("focuses Go to row on ⌘G, and jumps to the row asked for", async () => {
      const { user } = render(<DataGrid source={new FakeSource(5_000_000)} label="Rows" />)
      grid().focus()
      await user.keyboard("{Control>}g{/Control}")
      expect(screen.getByLabelText("Go to row")).toHaveFocus()
      await user.keyboard("4,000,000{Enter}")
      await landed()
      expect(rowNumbers()[0]).toBe(4_000_000)
      expect(grid()).toHaveFocus()
    })

    it("says how many rows there are when Go to row overshoots", async () => {
      const { user } = render(<DataGrid source={new FakeSource(50)} label="Rows" />)
      await user.type(screen.getByLabelText("Go to row"), "999{Enter}")
      expect(screen.getByText("The file has 50 rows")).toBeInTheDocument()
    })

    it("copies a keyboard selection as TSV", async () => {
      // Given: a cursor on the first cell
      const { user } = render(<DataGrid source={new FakeSource(100)} label="Rows" />)
      await user.click(await within(grid()).findByText("r0c0"))
      // When: the selection is extended a row and a column, and copied
      await user.keyboard("{Shift>}{ArrowDown}{ArrowRight}{/Shift}{Control>}c{/Control}")
      // Then: the clipboard holds a 2 × 2 TSV, and the footer counts it
      await waitFor(() => expect(clipboard.text).toBe("r0c0\tr0c1\nr1c0\tr1c1"))
      expect(screen.getByText("2 rows × 2 columns selected")).toBeInTheDocument()
    })

    it("opens the cell inspector on Enter, and Escape closes only it", async () => {
      const { user } = render(<DataGrid source={new FakeSource(10)} label="Rows" />)
      await user.click(await within(grid()).findByText("r2c1"))
      await user.keyboard("{Enter}")
      expect(await screen.findByRole("dialog", { name: /col_1/ })).toHaveTextContent("r2c1")
      await user.keyboard("{Escape}")
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
      expect(grid()).toHaveFocus()
    })
  })

  describe("toolbar and footer", () => {
    it("finds text in the loaded rows, and says it searched only those", async () => {
      const { user } = render(<DataGrid source={new FakeSource(100)} label="Rows" />)
      await landed()
      await user.type(screen.getByRole("searchbox", { name: "Find in loaded rows" }), "r7c2")
      expect(screen.getByText("1 of 1 in loaded rows")).toBeInTheDocument()
    })

    it("hides a column from the Columns menu", async () => {
      const { user } = render(<DataGrid source={new FakeSource(10)} label="Rows" />)
      await user.click(screen.getByRole("button", { name: /Columns/ }))
      await user.click(screen.getByRole("checkbox", { name: /col_1/ }))
      expect(within(grid()).queryByRole("columnheader", { name: "col_1" })).not.toBeInTheDocument()
    })

    it("resizes a column by dragging its edge", () => {
      render(<DataGrid source={new FakeSource(10)} label="Rows" />)
      const header = within(grid()).getByRole("columnheader", { name: "col_0" })
      const before = parseFloat(header.style.width)
      const handle = header.lastElementChild as HTMLElement
      fireEvent.mouseDown(handle, { clientX: 100 })
      fireEvent.mouseMove(document, { clientX: 160 })
      fireEvent.mouseUp(document, { clientX: 160 })
      expect(
        parseFloat(within(grid()).getByRole("columnheader", { name: "col_0" }).style.width),
      ).toBe(before + 60)
    })

    it("shows indexing progress, and offers Continue at the byte limit", async () => {
      // Given: a source indexing in the background
      const source = new FakeSource(1_200_000, {
        indexing: { state: "running", rows: 1_200_000, bytes: 50e6, totalBytes: 900e6 },
      })
      const { user } = render(<DataGrid source={source} label="Rows" />)
      expect(screen.getByText(/indexing · 1,200,000 rows so far/)).toBeInTheDocument()
      // When: it stops at the byte limit and the reader continues
      act(() =>
        source.setIndexing({
          state: "paused-limit",
          rows: 9e6,
          bytes: 512 * 1024 * 1024,
          totalBytes: 900e6,
        }),
      )
      await user.click(screen.getByRole("button", { name: "Continue" }))
      // Then: the source is asked to read on
      expect(source.continueIndexing).toHaveBeenCalled()
    })

    it("says the file changed under it, and offers to reload", async () => {
      const source = new FakeSource(10)
      const onReload = vi.fn()
      const { user } = render(<DataGrid source={source} label="Rows" onReload={onReload} />)
      act(() => source.overwrite())
      expect(screen.getByRole("alert")).toHaveTextContent(/changed since it was opened/)
      await user.click(screen.getByRole("button", { name: "Reload" }))
      expect(onReload).toHaveBeenCalled()
    })
  })

  it("adds only the rows scrolled into view, a steady amount of DOM for every step", async () => {
    // jsdom's timings are not a browser's (the 50 ms long-task budget is
    // traced in Chrome — see the PR), but the work a scroll causes is
    // deterministic and is what that budget rests on: a steady DOM size, and
    // only the rows that scrolled into view added to it.
    render(<DataGrid source={new FakeSource(5_000_000, { columns: 40 })} label="Rows" />)
    await landed()
    const element = grid()
    const size = element.querySelectorAll("*").length
    const cellsPerRow = within(element)
      .getAllByRole("row")[1]
      .querySelectorAll("[role=gridcell]").length
    let added = 0
    const observer = new MutationObserver((records) => {
      for (const record of records) added += record.addedNodes.length
    })
    observer.observe(element, { childList: true, subtree: true })
    let worst = 0
    for (let step = 1; step <= 20; step++) {
      added = 0
      scrollGrid(step * ROW_HEIGHT * 7)
      await landed()
      worst = Math.max(worst, added)
    }
    observer.disconnect()
    // Seven new rows a step, each a row, its number, its track and its cells.
    expect(worst).toBeLessThanOrEqual(7 * (cellsPerRow * 2 + 4))
    expect(Math.abs(element.querySelectorAll("*").length - size)).toBeLessThan(size * 0.2)
  })
})
