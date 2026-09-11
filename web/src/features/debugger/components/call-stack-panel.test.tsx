import { act, screen, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedWith,
  renderWithDebugSession,
  scriptParsed,
  settle,
} from "@/test/debug-session"
import { CallStackPanel } from "./call-stack-panel"

const ORIGINAL = "export const handler = async () => {\n  const x = 1\n  return x\n}\n"
const INLINE_MAP = `data:application/json;base64,${btoa(
  JSON.stringify({
    version: 3,
    file: "index.js",
    sources: ["../src/index.ts"],
    sourcesContent: [ORIGINAL],
    mappings: ";AAAA;IACE;IACA;AACF",
  }),
)}`

function panel() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(<CallStackPanel />, session)
  return { session, bridge, ...view }
}

describe("CallStackPanel", () => {
  it("lists frames at their mapped locations, folds internal ones, and selects on click", async () => {
    const { session, bridge, user } = panel()
    expect(screen.getByText("Not paused.")).toBeInTheDocument()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => scriptParsed(socket, "dist/index.js", INLINE_MAP))
    await settle()
    await settle()

    act(() =>
      pausedWith(socket, [
        { path: "dist/index.js", line0: 2, column0: 4, functionName: "helper" },
        { path: "dist/index.js", line0: 1, column0: 4, functionName: "handler" },
        {
          path: "internal/process/task_queues",
          functionName: "processTicksAndRejections",
          internal: true,
        },
        { path: "internal/modules/run_main", functionName: "runMain", internal: true },
      ]),
    )
    const list = screen.getByRole("listbox", { name: "Call stack" })
    const options = within(list).getAllByRole("option")
    expect(options).toHaveLength(2)
    expect(options[0]).toHaveAttribute("aria-selected", "true")
    expect(options[0]).toHaveTextContent("helper")
    expect(options[0]).toHaveTextContent("src/index.ts:2:2")
    // The generated location rides on the tooltip.
    expect(options[0]).toHaveAttribute("title", "Compiled: dist/index.js:3:4")
    expect(options[1]).toHaveTextContent("handler")

    const fold = within(list).getByRole("button", { name: "2 internal frames" })
    expect(fold).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByText("runMain")).not.toBeInTheDocument()
    await user.click(fold)
    expect(within(list).getAllByRole("option")).toHaveLength(4)
    expect(screen.getByText("runMain")).toBeInTheDocument()

    await user.click(options[1])
    expect(session.getState().pause?.selectedFrame).toBe(1)
    expect(options[1]).toHaveAttribute("aria-selected", "true")
    expect(options[0]).toHaveAttribute("aria-selected", "false")
  })

  it("selects with the keyboard and flags a frame no map resolves", async () => {
    const { session, bridge, user } = panel()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => scriptParsed(socket, "dist/index.js", INLINE_MAP))
    act(() => scriptParsed(socket, "lib/plain.js"))
    await settle()
    await settle()
    act(() =>
      pausedWith(socket, [
        { path: "dist/index.js", line0: 1, column0: 4, functionName: "handler" },
        { path: "lib/plain.js", line0: 4, functionName: "raw" },
      ]),
    )
    const options = screen.getAllByRole("option")
    expect(options[1]).toHaveTextContent("lib/plain.js:5:0")
    expect(options[1]).toHaveTextContent("(no source map)")
    expect(options[0]).not.toHaveTextContent("(no source map)")

    options[1].focus()
    await user.keyboard("{Enter}")
    expect(session.getState().pause?.selectedFrame).toBe(1)
  })
})
