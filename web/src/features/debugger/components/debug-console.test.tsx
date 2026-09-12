import { act, screen, waitFor, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedWith,
  renderWithDebugSession,
  settle,
} from "@/test/debug-session"
import { DebugConsole } from "./debug-console"

function console_() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(<DebugConsole />, session)
  return { session, bridge, ...view }
}

describe("DebugConsole", () => {
  it("shows the program's output and exceptions as they arrive, and clears", async () => {
    const { session, bridge, user } = console_()
    const socket = await act(() => attachFakeSession(session, bridge))
    const log = screen.getByRole("log", { name: "Debug console output" })
    expect(log).toHaveTextContent("Nothing yet")

    act(() => {
      socket.event("Runtime.consoleAPICalled", {
        type: "log",
        args: [
          { type: "string", value: "hello" },
          {
            type: "object",
            className: "Object",
            description: "Object",
            objectId: "o1",
            preview: {
              type: "object",
              overflow: false,
              properties: [{ name: "n", type: "number", value: "1" }],
            },
          },
        ],
        executionContextId: 1,
        timestamp: 1_700_000_000_000,
      })
      socket.event("Runtime.exceptionThrown", {
        timestamp: 1_700_000_000_001,
        exceptionDetails: {
          exceptionId: 1,
          text: "Uncaught",
          lineNumber: 0,
          columnNumber: 0,
          exception: { type: "object", description: "TypeError: nope" },
        },
      })
    })
    const lines = within(log).getAllByRole("listitem")
    expect(lines[0]).toHaveTextContent("hello {n: 1}")
    expect(lines[1]).toHaveTextContent("TypeError: nope")
    expect(lines[1]).toHaveClass("text-danger")

    await user.click(screen.getByRole("button", { name: "Clear" }))
    expect(log).toHaveTextContent("Nothing yet")
  })

  it("evaluates in the selected frame while paused, globally otherwise, with ↑/↓ history", async () => {
    const { session, bridge, user } = console_()
    const input = screen.getByRole("textbox", { name: "Debug console input" })
    expect(input).toBeDisabled()
    const socket = await act(() => attachFakeSession(session, bridge))
    expect(input).toBeEnabled()
    expect(screen.getByText("Evaluating globally.")).toBeInTheDocument()

    await user.type(input, "1 + 1{Enter}")
    expect(input).toHaveValue("")
    expect(socket.lastRequest("Runtime.evaluate").params).toMatchObject({
      expression: "1 + 1",
      objectGroup: "console",
    })
    act(() =>
      socket.respond(socket.lastRequest("Runtime.evaluate").id, {
        result: { type: "number", value: 2 },
      }),
    )
    const log = screen.getByRole("log", { name: "Debug console output" })
    await waitFor(() => expect(within(log).getAllByRole("listitem")).toHaveLength(2))
    expect(within(log).getAllByRole("listitem")[0]).toHaveTextContent("1 + 1")
    expect(within(log).getAllByRole("listitem")[1]).toHaveTextContent("2")

    act(() => pausedWith(socket, [{ path: "index.js" }, { path: "index.js" }]))
    act(() => session.selectFrame(1))
    expect(screen.getByText("Evaluating in the selected frame.")).toBeInTheDocument()
    await user.type(input, "this{Enter}")
    expect(socket.lastRequest("Debugger.evaluateOnCallFrame").params).toMatchObject({
      callFrameId: "f1",
      expression: "this",
      objectGroup: "console",
    })
    // An object answer expands through the tree.
    act(() =>
      socket.respond(socket.lastRequest("Debugger.evaluateOnCallFrame").id, {
        result: {
          type: "object",
          className: "Object",
          description: "Object",
          objectId: "this-1",
          preview: { type: "object", overflow: false, properties: [] },
        },
      }),
    )
    const result = await screen.findByRole("treeitem", { name: "{}" })
    expect(result).toHaveAttribute("aria-expanded", "false")

    // A failed evaluation is an error line, and ↑ walks back through what was typed.
    await user.type(input, "nope{Enter}")
    act(() =>
      socket.respond(socket.lastRequest("Debugger.evaluateOnCallFrame").id, {
        result: { type: "undefined" },
        exceptionDetails: {
          exceptionId: 2,
          text: "Uncaught",
          lineNumber: 0,
          columnNumber: 0,
          exception: { type: "object", description: "ReferenceError: nope is not defined" },
        },
      }),
    )
    await settle()
    await waitFor(() => expect(log).toHaveTextContent("ReferenceError: nope is not defined"))

    await user.keyboard("{ArrowUp}")
    expect(input).toHaveValue("nope")
    await user.keyboard("{ArrowUp}")
    expect(input).toHaveValue("this")
    await user.keyboard("{ArrowUp}")
    expect(input).toHaveValue("1 + 1")
    await user.keyboard("{ArrowUp}")
    expect(input).toHaveValue("1 + 1")
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}")
    expect(input).toHaveValue("")
  })
})
