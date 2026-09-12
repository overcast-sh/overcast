import { act, screen, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  renderWithDebugSession,
  scriptParsed,
  settle,
} from "@/test/debug-session"
import { BreakpointsPanel } from "./breakpoints-panel"

function panel() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(<BreakpointsPanel />, session)
  return { session, bridge, ...view }
}

describe("BreakpointsPanel", () => {
  it("lists breakpoints with their condition, disables, edits and removes them", async () => {
    const { session, bridge, user } = panel()
    expect(screen.getByText(/No breakpoints/)).toBeInTheDocument()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      session.addBreakpoint("index.js", 3)
      session.addBreakpoint("lib/util.js", 8, "n > 2")
    })
    const list = screen.getByRole("list", { name: "Breakpoints" })
    const rows = within(list).getAllByRole("listitem")
    expect(rows[0]).toHaveTextContent("index.js:3")
    expect(rows[1]).toHaveTextContent("lib/util.js:8")
    expect(rows[1]).toHaveTextContent("when n > 2")

    // Not yet bound: the title says so; a bind reply turns it on.
    expect(within(rows[0]).getByTitle(/Not yet bound/)).toBeInTheDocument()
    act(() => {
      socket.respondAll({ breakpointId: "bp:1", locations: [] })
    })
    await screen.findAllByTitle("Set in the runtime")

    // Disable: the runtime's copy goes, the row is struck through.
    await user.click(screen.getByRole("checkbox", { name: "Enable breakpoint at index.js:3" }))
    expect(session.getState().breakpoints[0].enabled).toBe(false)
    expect(socket.requests("Debugger.removeBreakpoint")).toHaveLength(1)
    expect(within(rows[0]).getByTitle("Disabled")).toHaveClass("line-through")
    await user.click(screen.getByRole("checkbox", { name: "Enable breakpoint at index.js:3" }))
    expect(session.getState().breakpoints[0].enabled).toBe(true)

    // Edit the condition through the shared editor.
    await user.click(
      screen.getByRole("button", { name: "Edit condition of breakpoint at lib/util.js:8" }),
    )
    const form = screen.getByRole("form", { name: "Breakpoint at lib/util.js:8" })
    const input = within(form).getByRole("textbox")
    expect(input).toHaveValue("n > 2")
    await user.clear(input)
    await user.type(input, "n > 5{Enter}")
    expect(session.getState().breakpoints[1].condition).toBe("n > 5")
    expect(screen.queryByRole("form")).not.toBeInTheDocument()
    expect(rows[1]).toHaveTextContent("when n > 5")

    await user.click(screen.getByRole("button", { name: "Remove breakpoint at index.js:3" }))
    expect(session.getState().breakpoints.map((bp) => bp.path)).toEqual(["lib/util.js"])
  })

  it("sets the pause-on-exceptions mode with all three choices", async () => {
    const { session, bridge, user } = panel()
    const socket = await act(() => attachFakeSession(session, bridge))
    const select = screen.getByRole("combobox", { name: "Pause on exceptions" })
    expect(select).toHaveValue("none")
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Never", "Uncaught", "Caught and uncaught"])

    await user.selectOptions(select, "all")
    expect(session.getState().pauseOnExceptions).toBe("all")
    expect(socket.lastRequest("Debugger.setPauseOnExceptions").params).toEqual({ state: "all" })
    await user.selectOptions(select, "uncaught")
    expect(socket.lastRequest("Debugger.setPauseOnExceptions").params).toEqual({
      state: "uncaught",
    })
    await user.selectOptions(select, "none")
    expect(socket.lastRequest("Debugger.setPauseOnExceptions").params).toEqual({ state: "none" })
    expect(select).toHaveValue("none")
  })
})

describe("BreakpointsPanel > source maps off", () => {
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

  it("lists a breakpoint in an original file as inactive, saying why", async () => {
    const { session, bridge } = panel()
    const socket = await act(() => attachFakeSession(session, bridge))
    await act(async () => {
      scriptParsed(socket, "dist/index.js", INLINE_MAP)
      await settle()
    })
    act(() => {
      session.addBreakpoint("src/index.ts", 2)
      session.addBreakpoint("dist/index.js", 2)
    })
    const rows = () =>
      within(screen.getByRole("list", { name: "Breakpoints" })).getAllByRole("listitem")
    expect(rows()[0]).not.toHaveTextContent("source maps off")

    await act(async () => {
      session.setSourceMaps(false)
      await settle()
    })
    expect(rows()[0]).toHaveTextContent("src/index.ts:2")
    expect(rows()[0]).toHaveTextContent("source maps off")
    expect(within(rows()[0]).getByTitle(/Inactive — source maps are off/)).toBeInTheDocument()
    // The compiled file's breakpoint is unaffected.
    expect(rows()[1]).not.toHaveTextContent("source maps off")
    expect(
      within(rows()[1]).getByTitle(/Not yet bound|Set in the runtime/),
    ).toBeInTheDocument()
  })
})
