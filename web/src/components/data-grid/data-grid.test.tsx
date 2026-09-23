import { act, fireEvent, render, screen, userEvent, waitFor, within } from "@/test/render"
import { DataGrid, ROW_HEIGHT } from "./data-grid"
import { MAX_SCROLL_PX } from "./scroll-map"
import type { GridColumn, IndexingStatus, RowBlock, RowSource } from "./row-source"
import { abortError } from "./row-source"
import { SETTLE_MS } from "./use-row-blocks"

const clipboard = vi.hoisted(() => ({ text: "" }))
vi.mock("@/lib/clipboard", () => ({
  writeClipboardText: (text: string) => {
    clipboard.text = text
    return Promise.resolve()
  },
}))

/**
 * A source of `rows` generated rows (`r<row>c<col>`), recording every read.
 * `delay` holds reads open, so a test can watch what is aborted.
 */
function fakeSource(
  rows: number,
  { columns = 4, delay = 0, projects = false, indexing }: {
    columns?: number
    delay?: number
    projects?: boolean
    indexing?: IndexingStatus
  } = {},
) {
  const reads: { start: number; end: number; cols: readonly number[]; aborted: boolean }[] = []
  const listeners = new Set<() => void>()
  const source: RowSource & { reads: typeof reads; setIndexing(s: IndexingStatus): void } = {
    columns: Array.from({ length: columns }, (_, c): GridColumn => ({ name: `col_${c}`, numeric: false })),
    rowCount: { value: rows, exact: true },
    blockSize: 1000,
    projects,
    indexing,
    reads,
    getRows(start, end, cols, signal): Promise<RowBlock> {
      const read = { start, end, cols, aborted: false }
      reads.push(read)
      return new Promise((resolve, reject) => {
        const answer = () => {
          const which = projects ? cols : Array.from({ length: columns }, (_, c) => c)
          const out: (string[] | undefined)[] = []
          for (const c of which) out[c] = Array.from({ length: end - start }, (_, i) => `r${start + i}c${c}`)
          resolve({ start, count: end - start, columns: out })
        }
        signal.addEventListener("abort", () => {
          read.aborted = true
          reject(abortError())
        })
        if (delay > 0) setTimeout(answer, delay)
        else queueMicrotask(answer)
      })
    },
    subscribe(listener) {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    setIndexing(s) {
      this.indexing = s
      for (const l of listeners) l()
    },
    continueIndexing: vi.fn(),
    dispose() {},
  }
  return source
}

function scroller() {
  return screen.getByRole("grid")
}

/** Scrolls the grid as the browser would, and lets the frame run. */
function scrollTo(top: number, left = 0) {
  const el = scroller()
  el.scrollTop = top
  el.scrollLeft = left
  fireEvent.scroll(el)
  act(() => {
    vi.advanceTimersByTime(20)
  })
}

describe("DataGrid", () => {
  beforeEach(() => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "requestAnimationFrame", "cancelAnimationFrame"],
    })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it("renders the first screen of rows with a sticky header and row numbers", async () => {
    render(<DataGrid source={fakeSource(5_000_000)} label="Rows" />)
    const grid = scroller()
    expect(grid).toHaveAttribute("aria-rowcount", "5000001")
    expect(within(grid).getByRole("columnheader", { name: "col_0" })).toBeInTheDocument()
    await act(async () => {})
    expect(within(grid).getByText("r0c0")).toBeInTheDocument()
    expect(within(grid).getByText("1")).toBeInTheDocument()
    // Only a screenful is in the DOM, however many rows there are.
    expect(within(grid).getAllByRole("row").length).toBeLessThan(30)
  })

  it("shows static skeletons for rows not loaded yet", () => {
    render(<DataGrid source={fakeSource(100, { delay: 1000 })} label="Rows" />)
    const busy = scroller().querySelectorAll("[aria-busy=true]")
    expect(busy.length).toBeGreaterThan(0)
  })

  it("keeps the spacer under the browser's height cap for millions of rows", () => {
    render(<DataGrid source={fakeSource(5_000_000)} label="Rows" />)
    const spacer = scroller().firstElementChild as HTMLElement
    expect(parseFloat(spacer.style.height)).toBeLessThanOrEqual(MAX_SCROLL_PX + 100)
  })

  it("reaches the last row by scrolling to the bottom", async () => {
    render(<DataGrid source={fakeSource(5_000_000)} label="Rows" />)
    scrollTo(MAX_SCROLL_PX)
    act(() => {
      vi.advanceTimersByTime(SETTLE_MS + 10)
    })
    await act(async () => {})
    expect(within(scroller()).getByText("r4999999c0")).toBeInTheDocument()
  })

  it("fetches nothing while flung, then only the final viewport once settled", async () => {
    const source = fakeSource(5_000_000)
    render(<DataGrid source={source} label="Rows" />)
    await act(async () => {})
    const initial = source.reads.length
    // A drag of the scrollbar to row 4,000,000: many events, far apart.
    const target = (4_000_000 / 5_000_000) * MAX_SCROLL_PX
    for (let step = 1; step <= 10; step++) scrollTo((target * step) / 10)
    expect(source.reads.length).toBe(initial)
    act(() => {
      vi.advanceTimersByTime(SETTLE_MS + 10)
    })
    await act(async () => {})
    const after = source.reads.slice(initial)
    // The final viewport's block, and one ahead in the direction of travel.
    expect(after.length).toBeLessThanOrEqual(2)
    const rows = after.map((r) => r.start)
    expect(rows[0]).toBeGreaterThanOrEqual(3_999_000)
    expect(rows[0]).toBeLessThanOrEqual(4_000_000)
  })

  it("aborts reads for blocks the viewport has left", async () => {
    const source = fakeSource(1_000_000, { delay: 5_000 })
    render(<DataGrid source={source} label="Rows" />)
    const first = source.reads[0]
    scrollTo(MAX_SCROLL_PX / 2)
    act(() => {
      vi.advanceTimersByTime(SETTLE_MS + 10)
    })
    expect(first.aborted).toBe(true)
  })

  it("asks a projecting source only for the visible columns", async () => {
    const source = fakeSource(10, { columns: 200, projects: true })
    render(<DataGrid source={source} label="Rows" />)
    await act(async () => {})
    const cols = source.reads[0].cols
    expect(cols.length).toBeGreaterThan(0)
    expect(cols.length).toBeLessThan(30)
    expect(Math.max(...cols)).toBeLessThan(30)
  })

  it("jumps to a row with Go to row, and ⌘G focuses it", async () => {
    vi.useRealTimers()
    const user = userEvent.setup()
    render(<DataGrid source={fakeSource(5_000_000)} label="Rows" />)
    scroller().focus()
    await user.keyboard("{Control>}g{/Control}")
    const input = screen.getByLabelText("Go to row")
    expect(input).toHaveFocus()
    await user.type(input, "4,000,000{Enter}")
    expect(await within(scroller()).findByText("r3999999c0")).toBeInTheDocument()
    expect(scroller()).toHaveFocus()
  })

  it("says how many rows there are when Go to row overshoots", async () => {
    vi.useRealTimers()
    const user = userEvent.setup()
    render(<DataGrid source={fakeSource(50)} label="Rows" />)
    await user.type(screen.getByLabelText("Go to row"), "999{Enter}")
    expect(screen.getByText("The file has 50 rows")).toBeInTheDocument()
  })

  it("moves a cell cursor with the keyboard and copies the selection as TSV", async () => {
    vi.useRealTimers()
    const user = userEvent.setup()
    render(<DataGrid source={fakeSource(100)} label="Rows" />)
    await screen.findByText("r0c0")
    await user.click(screen.getByText("r0c0"))
    await user.keyboard("{Shift>}{ArrowDown}{ArrowRight}{/Shift}")
    expect(scroller()).toHaveAttribute("aria-activedescendant")
    await user.keyboard("{Control>}c{/Control}")
    await waitFor(() => expect(clipboard.text).toBe("r0c0\tr0c1\nr1c0\tr1c1"))
    expect(screen.getByText("2 × 2 selected")).toBeInTheDocument()
  })

  it("opens the cell inspector on Enter, and Escape closes only it", async () => {
    vi.useRealTimers()
    const user = userEvent.setup()
    render(<DataGrid source={fakeSource(10)} label="Rows" />)
    await user.click(await screen.findByText("r2c1"))
    await user.keyboard("{Enter}")
    const inspector = await screen.findByRole("dialog", { name: /col_1/ })
    expect(inspector).toHaveTextContent("r2c1")
    await user.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(scroller()).toBeInTheDocument()
  })

  it("finds text in the loaded rows, and says it searched only those", async () => {
    vi.useRealTimers()
    const user = userEvent.setup()
    render(<DataGrid source={fakeSource(100)} label="Rows" />)
    await screen.findByText("r0c0")
    await user.type(screen.getByPlaceholderText("Find in loaded rows"), "r7c2")
    expect(await screen.findByText(/1 of 1 in loaded rows/)).toBeInTheDocument()
  })

  it("shows indexing progress, and offers Continue at the byte limit", async () => {
    vi.useRealTimers()
    const source = fakeSource(1_200_000, {
      indexing: { state: "running", rows: 1_200_000, bytes: 50e6, totalBytes: 900e6 },
    })
    const { rerender } = render(<DataGrid source={source} label="Rows" />)
    expect(screen.getByText(/indexing · 1,200,000 rows so far/)).toBeInTheDocument()
    act(() => source.setIndexing({ state: "paused-limit", rows: 9e6, bytes: 512 * 1024 * 1024, totalBytes: 900e6 }))
    rerender(<DataGrid source={source} label="Rows" />)
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }))
    expect(source.continueIndexing).toHaveBeenCalled()
  })

  it("does the same, small amount of DOM work for every scroll step", async () => {
    // jsdom's timings are not a browser's (the 50 ms long-task budget is
    // measured in Chrome — see the PR), but the work a scroll causes is
    // deterministic and is what the budget rests on: a steady DOM size, and
    // only the rows that scrolled into view added to it.
    const source = fakeSource(5_000_000, { columns: 40 })
    render(<DataGrid source={source} label="Rows" />)
    await act(async () => {})
    const el = scroller()
    const size = el.querySelectorAll("*").length
    const visibleCols =
      el.querySelector("[role=row][aria-rowindex='2']")?.querySelectorAll("[role=gridcell]")
        .length ?? 0
    expect(visibleCols).toBeGreaterThan(5)
    let added = 0
    const observer = new MutationObserver((records) => {
      for (const r of records) added += r.addedNodes.length
    })
    observer.observe(el, { childList: true, subtree: true })
    let worstAdded = 0
    for (let step = 1; step <= 20; step++) {
      added = 0
      act(() => {
        el.scrollTop = step * ROW_HEIGHT * 7
        fireEvent.scroll(el)
        vi.advanceTimersByTime(16)
      })
      await act(async () => {})
      worstAdded = Math.max(worstAdded, added)
    }
    observer.disconnect()
    // Seven new rows a step, each a row element, its number and its cells —
    // never a re-render of the whole screen.
    expect(worstAdded).toBeLessThanOrEqual(8 * (visibleCols * 2 + 2))
    expect(Math.abs(el.querySelectorAll("*").length - size)).toBeLessThan(size * 0.2)
  }, 30_000)
})
