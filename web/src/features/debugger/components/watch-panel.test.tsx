import { act, screen, waitFor, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedWith,
  renderWithDebugSession,
  settle,
} from "@/test/debug-session"
import type { FakeBridgeSocket } from "@/test/fake-bridge"
import { WatchPanel } from "./watch-panel"

function panel() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(<WatchPanel />, session)
  return { session, bridge, ...view }
}

/** Answer the latest evaluation with a number. */
function answer(socket: FakeBridgeSocket, method: string, value: number) {
  socket.respond(socket.lastRequest(method).id, { result: { type: "number", value } })
}

describe("WatchPanel", () => {
  it("adds an expression, evaluates it per pause and per frame, and edits it in place", async () => {
    const { session, bridge, user } = panel()
    expect(screen.getByText(/No watch expressions/)).toBeInTheDocument()

    await user.type(screen.getByLabelText("Add watch expression"), "x + 1{Enter}")
    const row = () => screen.getByRole("tree", { name: "Watch x + 1" })
    expect(within(row()).getByRole("treeitem")).toHaveTextContent("x + 1: not available")
    expect(session.getState().watches).toEqual([
      expect.objectContaining({ expression: "x + 1", result: null }),
    ])

    const socket = await act(() => attachFakeSession(session, bridge))
    // Not paused yet: nothing to evaluate against.
    expect(socket.requests("Debugger.evaluateOnCallFrame")).toHaveLength(0)

    act(() => pausedWith(socket, [{ path: "index.js" }, { path: "index.js" }]))
    expect(socket.lastRequest("Debugger.evaluateOnCallFrame").params).toMatchObject({
      callFrameId: "f0",
      expression: "x + 1",
      objectGroup: "watch",
    })
    act(() => answer(socket, "Debugger.evaluateOnCallFrame", 2))
    await waitFor(() => expect(within(row()).getByRole("treeitem")).toHaveTextContent("x + 1: 2"))

    // Selecting another frame re-evaluates there.
    act(() => session.selectFrame(1))
    expect(socket.lastRequest("Debugger.evaluateOnCallFrame").params).toMatchObject({
      callFrameId: "f1",
      expression: "x + 1",
    })
    act(() => answer(socket, "Debugger.evaluateOnCallFrame", 7))
    await waitFor(() => expect(within(row()).getByRole("treeitem")).toHaveTextContent("x + 1: 7"))

    // Editing re-evaluates the new expression; an error shows in the value's place.
    await user.click(screen.getByRole("button", { name: "Edit watch x + 1" }))
    const input = within(screen.getByRole("form", { name: "Edit watch x + 1" })).getByRole(
      "textbox",
    )
    await user.clear(input)
    await user.type(input, "nope.y{Enter}")
    expect(socket.lastRequest("Debugger.evaluateOnCallFrame").params).toMatchObject({
      expression: "nope.y",
    })
    act(() =>
      socket.respond(socket.lastRequest("Debugger.evaluateOnCallFrame").id, {
        result: { type: "undefined" },
        exceptionDetails: {
          exceptionId: 1,
          text: "Uncaught",
          lineNumber: 0,
          columnNumber: 0,
          exception: { type: "object", description: "ReferenceError: nope is not defined" },
        },
      }),
    )
    await waitFor(() =>
      expect(screen.getByRole("tree", { name: "Watch nope.y" })).toHaveTextContent(
        "ReferenceError: nope is not defined",
      ),
    )

    await user.click(screen.getByRole("button", { name: "Remove watch nope.y" }))
    expect(session.getState().watches).toEqual([])
  })

  it("expands an object result through the shared tree and drops the handle on resume", async () => {
    const { session, bridge, user } = panel()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      session.addWatch("event")
    })
    act(() => pausedWith(socket, [{ path: "index.js" }]))
    act(() =>
      socket.respond(socket.lastRequest("Debugger.evaluateOnCallFrame").id, {
        result: {
          type: "object",
          className: "Object",
          description: "Object",
          objectId: "ev-1",
          preview: {
            type: "object",
            overflow: false,
            properties: [{ name: "key", type: "string", value: "value" }],
          },
        },
      }),
    )
    const item = await screen.findByRole("treeitem", { name: 'event: {key: "value"}' })
    expect(item).toHaveAttribute("aria-expanded", "false")
    await user.click(item)
    expect(socket.lastRequest("Runtime.getProperties").params).toMatchObject({ objectId: "ev-1" })

    act(() => socket.event("Debugger.resumed", {}))
    await settle()
    expect(socket.requests("Runtime.releaseObjectGroup").map((r) => r.params)).toEqual([
      { objectGroup: "watch" },
      { objectGroup: "console" },
    ])
    // The text stays; the handle does not.
    expect(screen.getByRole("treeitem", { name: 'event: {key: "value"}' })).not.toHaveAttribute(
      "aria-expanded",
    )
  })
})
