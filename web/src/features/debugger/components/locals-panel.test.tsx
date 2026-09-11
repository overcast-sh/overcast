import { act, screen, waitFor, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedWith,
  renderWithDebugSession,
  settle,
} from "@/test/debug-session"
import { LocalsPanel } from "./locals-panel"

function panel() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(<LocalsPanel />, session)
  return { session, bridge, ...view }
}

describe("LocalsPanel", () => {
  it("asks for the frame's variables only when a scope is opened, and reads getters as such", async () => {
    const { session, bridge, user } = panel()
    expect(screen.getByText(/Pause to see/)).toBeInTheDocument()
    const socket = await act(() => attachFakeSession(session, bridge))

    act(() =>
      pausedWith(socket, [
        {
          path: "index.js",
          scopes: [
            { type: "local", objectId: "local-1" },
            { type: "closure", name: "handler", objectId: "closure-1" },
            { type: "global", objectId: "global-1" },
          ],
        },
      ]),
    )
    const tree = screen.getByRole("tree", { name: "Locals" })
    expect(within(tree).getByRole("treeitem", { name: "Local" })).toHaveAttribute(
      "aria-expanded",
      "true",
    )
    expect(within(tree).getByRole("treeitem", { name: "Closure (handler)" })).toHaveAttribute(
      "aria-expanded",
      "false",
    )
    expect(within(tree).getByRole("treeitem", { name: "Global" })).toHaveAttribute(
      "aria-expanded",
      "false",
    )
    // Only the Local scope, open by default, has been read.
    expect(socket.requests("Runtime.getProperties").map((r) => r.params)).toEqual([
      { objectId: "local-1", ownProperties: true, generatePreview: true },
    ])

    act(() =>
      socket.respond(socket.lastRequest("Runtime.getProperties").id, {
        result: [
          {
            name: "count",
            value: { type: "number", value: 3 },
            configurable: true,
            enumerable: true,
          },
          {
            name: "name",
            value: { type: "string", value: "hi" },
            configurable: true,
            enumerable: true,
          },
          {
            name: "items",
            value: {
              type: "object",
              subtype: "array",
              className: "Array",
              description: "Array(2)",
              objectId: "arr-1",
              preview: {
                type: "object",
                subtype: "array",
                overflow: false,
                properties: [
                  { name: "0", type: "number", value: "1" },
                  { name: "1", type: "string", value: "x" },
                ],
              },
            },
            configurable: true,
            enumerable: true,
          },
          {
            name: "lazy",
            get: { type: "function", description: "get lazy() {}" },
            configurable: true,
            enumerable: false,
          },
        ],
      }),
    )
    await waitFor(() => expect(screen.getByRole("treeitem", { name: "count: 3" })).toBeVisible())
    expect(screen.getByRole("treeitem", { name: 'name: "hi"' })).toBeInTheDocument()
    expect(screen.getByRole("treeitem", { name: 'items: (2) [1, "x"]' })).toHaveAttribute(
      "aria-expanded",
      "false",
    )
    expect(screen.getByRole("treeitem", { name: "lazy: (getter)" })).not.toHaveAttribute(
      "aria-expanded",
    )
    // The getter was never invoked and nothing else was fetched.
    expect(socket.requests("Runtime.getProperties")).toHaveLength(1)
    expect(socket.requests("Runtime.callFunctionOn")).toHaveLength(0)

    // Opening the array fetches its elements, once.
    await user.click(screen.getByRole("treeitem", { name: 'items: (2) [1, "x"]' }))
    expect(socket.lastRequest("Runtime.getProperties").params).toMatchObject({ objectId: "arr-1" })
    act(() =>
      socket.respond(socket.lastRequest("Runtime.getProperties").id, {
        result: [
          { name: "0", value: { type: "number", value: 1 }, configurable: true, enumerable: true },
        ],
      }),
    )
    await waitFor(() => expect(screen.getByRole("treeitem", { name: "0: 1" })).toBeVisible())
    await user.click(screen.getByRole("treeitem", { name: 'items: (2) [1, "x"]' }))
    await user.click(screen.getByRole("treeitem", { name: 'items: (2) [1, "x"]' }))
    expect(socket.requests("Runtime.getProperties")).toHaveLength(2)
  })

  it("re-scopes to the selected frame and shows a load failure inline", async () => {
    const { session, bridge } = panel()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() =>
      pausedWith(socket, [
        { path: "index.js", scopes: [{ type: "local", objectId: "top" }] },
        { path: "index.js", scopes: [{ type: "local", objectId: "caller" }] },
      ]),
    )
    expect(socket.lastRequest("Runtime.getProperties").params).toMatchObject({ objectId: "top" })

    act(() => session.selectFrame(1))
    await settle()
    expect(socket.lastRequest("Runtime.getProperties").params).toMatchObject({
      objectId: "caller",
    })
    act(() => socket.fail(socket.lastRequest("Runtime.getProperties").id, -32000, "Object gone"))
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Object gone"))
  })
})
