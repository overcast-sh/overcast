import { act, createTestQueryClient, fireEvent, screen, waitFor, within } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedAt,
  renderWithDebugSession,
  scriptParsed,
  settle,
} from "@/test/debug-session"
import { debugTarget } from "@/test/debug-target"
import { latestFakeEditor, resetFakeEditors } from "@/test/monaco"
import { debuggerTargetQueryOptions } from "@/features/debugger/data"
import { DebugCodeBrowser } from "./debug-code-browser"

vi.mock("@monaco-editor/react", async () => {
  const { FakeMonacoEditor } = await import("@/test/monaco")
  return { default: FakeMonacoEditor }
})

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

const DEPLOYED = {
  "index.js": "exports.handler = async () => 1\n",
  "lib/util.js": "module.exports = {}\n",
  "dist/index.js":
    '"use strict";\nexports.handler = async () => {\n    const x = 1;\n    return x;\n};\n',
}

function browser(files = DEPLOYED, { consoleDebug }: { consoleDebug?: boolean } = {}) {
  const { session, bridge } = fakeDebugSession(files)
  const queryClient = createTestQueryClient()
  if (consoleDebug !== undefined) {
    queryClient.setQueryData(
      debuggerTargetQueryOptions("lambda", "my-fn").queryKey,
      debugTarget({
        consoleDebug,
        bridgePath: consoleDebug ? "/_overcast/debugger/targets/lambda/my-fn/ws" : "",
      }),
    )
  }
  const view = renderWithDebugSession(
    <DebugCodeBrowser
      files={Object.entries(files).map(([name, content]) => ({ name, size: content.length }))}
      initialFile="index.js"
      initialValue={files["index.js"]}
      loadFile={(path) =>
        Promise.resolve({ content: files[path as keyof typeof files], language: "javascript" })
      }
      target={consoleDebug === undefined ? undefined : { service: "lambda", resource: "my-fn" }}
    />,
    session,
    { queryClient },
  )
  return { session, bridge, ...view }
}

beforeEach(() => resetFakeEditors())

describe("DebugCodeBrowser > idle", () => {
  it("is a plain CodeBrowser until a session opens: no toolbar, no gutter", () => {
    browser()
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument()
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-glyph-margin", "false")
  })

  it("stays plain on a resource the server offers no console session for", () => {
    browser(DEPLOYED, { consoleDebug: false })
    expect(screen.queryByRole("button", { name: "Debug in console" })).not.toBeInTheDocument()
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-glyph-margin", "false")
  })

  it("offers to start a session, takes breakpoints in the gutter, and paints them hollow until one binds them", async () => {
    const { session, bridge, user } = browser(DEPLOYED, { consoleDebug: true })
    expect(screen.getByRole("note")).toHaveTextContent(/set a breakpoint, then start a session/)
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-glyph-margin", "true")

    act(() => latestFakeEditor().clickGutter(3))
    expect(session.getState().breakpoints).toEqual([
      expect.objectContaining({ path: "index.js", line: 3, resolved: false }),
    ])
    expect(screen.getByRole("note")).toHaveTextContent("1 breakpoint set — it binds when a session starts")
    await waitFor(() =>
      expect(latestFakeEditor().decorations.map((d) => d.options)).toEqual([
        expect.objectContaining({
          glyphMarginClassName: "oc-gutter-glyph oc-gutter-glyph-pending",
          glyphMarginHoverMessage: { value: "Breakpoint — binds when a session starts" },
        }),
      ]),
    )

    await user.click(screen.getByRole("button", { name: "Debug in console" }))
    expect(bridge.latest().url).toBe(
      `ws://${window.location.host}/api/debugger/targets/lambda/my-fn/ws`,
    )
    await act(() => attachFakeSession(session, bridge))
    expect(screen.getByRole("toolbar", { name: "Debug controls" })).toBeInTheDocument()
    expect(bridge.latest().lastRequest("Debugger.setBreakpointByUrl").params).toMatchObject({
      lineNumber: 2,
    })
  })
})

describe("DebugCodeBrowser > breakpoints", () => {
  it("toggles a breakpoint on a gutter click and paints it", async () => {
    const { session, bridge } = browser()
    await act(() => attachFakeSession(session, bridge))
    expect(screen.getByRole("toolbar", { name: "Debug controls" })).toBeInTheDocument()

    act(() => latestFakeEditor().clickGutter(3))
    expect(session.getState().breakpoints).toEqual([
      expect.objectContaining({ path: "index.js", line: 3, enabled: true }),
    ])
    // Hollow, and saying why, until the inspector places it on a statement.
    await waitFor(() =>
      expect(latestFakeEditor().decorations.map((d) => d.options)).toEqual([
        expect.objectContaining({
          glyphMarginClassName: "oc-gutter-glyph oc-gutter-glyph-pending",
          glyphMarginHoverMessage: {
            value: "Breakpoint — not bound yet: it binds when the container loads index.js",
          },
        }),
      ]),
    )
    const bind = bridge.latest().lastRequest("Debugger.setBreakpointByUrl")
    expect(bind.params).toMatchObject({
      lineNumber: 2,
      urlRegex: "file:///var/task/index\\.js$",
    })
    act(() =>
      bridge.latest().respond(bind.id, {
        breakpointId: "bp:1",
        locations: [{ scriptId: "s-index.js", lineNumber: 2, columnNumber: 0 }],
      }),
    )
    await waitFor(() =>
      expect(latestFakeEditor().decorations.map((d) => d.options.glyphMarginClassName)).toEqual([
        "oc-gutter-glyph",
      ]),
    )

    act(() => latestFakeEditor().clickGutter(3))
    expect(session.getState().breakpoints).toEqual([])
  })

  it("opens the condition editor on a right click and saves a conditional breakpoint", async () => {
    const { session, bridge, user } = browser()
    await act(() => attachFakeSession(session, bridge))

    act(() => latestFakeEditor().clickGutter(5, { right: true }))
    const form = screen.getByRole("form", { name: "Breakpoint at index.js:5" })
    await user.type(within(form).getByRole("textbox"), "event.count > 3{Enter}")

    expect(session.getState().breakpoints).toEqual([
      expect.objectContaining({ path: "index.js", line: 5, condition: "event.count > 3" }),
    ])
    expect(screen.queryByRole("form")).not.toBeInTheDocument()

    // Reopening on the same line edits it; Remove takes it away.
    act(() => latestFakeEditor().clickGutter(5, { right: true }))
    expect(
      within(screen.getByRole("form", { name: "Breakpoint at index.js:5" })).getByRole("textbox"),
    ).toHaveValue("event.count > 3")
    await user.click(screen.getByRole("button", { name: "Remove" }))
    expect(session.getState().breakpoints).toEqual([])
  })

  it("closes the condition editor on Escape without changing anything", async () => {
    const { session, bridge, user } = browser()
    await act(() => attachFakeSession(session, bridge))
    act(() => latestFakeEditor().clickGutter(2, { right: true }))
    await user.keyboard("{Escape}")
    expect(screen.queryByRole("form")).not.toBeInTheDocument()
    expect(session.getState().breakpoints).toEqual([])
  })
})

describe("DebugCodeBrowser > toolbar and keys", () => {
  it("leaves the stepping keys to the workspace: the pane alone binds none", async () => {
    // The keys are bound once, by DebugWorkspace, over the pane and the
    // panels together (debug-workspace.test.tsx); a second binding here
    // would step twice per press.
    const { session, bridge } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      scriptParsed(socket, "index.js")
      pausedAt(socket, "index.js", 0)
    })
    expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled()
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Editor" }), { key: "F10" })
    expect(socket.requests("Debugger.stepOver")).toHaveLength(0)
  })

  it("drives the session from the buttons, and Stop ends it", async () => {
    const { session, bridge, user } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => pausedAt(socket, "index.js", 0))

    await user.click(screen.getByRole("button", { name: "Step over" }))
    await user.click(screen.getByRole("button", { name: "Step into" }))
    await user.click(screen.getByRole("button", { name: "Step out" }))
    await user.click(screen.getByRole("button", { name: "Continue" }))
    expect(socket.sent.slice(-4).map((f) => f.method)).toEqual([
      "Debugger.stepOver",
      "Debugger.stepInto",
      "Debugger.stepOut",
      "Debugger.resume",
    ])

    const exceptions = screen.getByRole("button", { name: "Pause on uncaught exceptions" })
    expect(exceptions).toHaveAttribute("aria-pressed", "false")
    await user.click(exceptions)
    expect(socket.lastRequest("Debugger.setPauseOnExceptions").params).toEqual({
      state: "uncaught",
    })
    expect(exceptions).toHaveAttribute("aria-pressed", "true")

    expect(screen.getByRole("button", { name: "Restart container" })).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Stop debugging" }))
    expect(session.getState().status).toBe("idle")
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument()
  })
})

describe("DebugCodeBrowser > pause", () => {
  it("reveals the paused line in the open file with the current-line marker", async () => {
    const { session, bridge } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      scriptParsed(socket, "index.js")
      pausedAt(socket, "index.js", 6, 2)
    })
    const editor = latestFakeEditor()
    await waitFor(() => expect(editor.revealLineInCenter).toHaveBeenCalledWith(7))
    expect(editor.setPosition).toHaveBeenCalledWith({ lineNumber: 7, column: 3 })
    expect(editor.decorations).toEqual([
      expect.objectContaining({
        range: expect.objectContaining({ startLineNumber: 7 }),
        options: expect.objectContaining({ className: "oc-line-current" }),
      }),
    ])
    expect(screen.getByRole("toolbar")).toHaveTextContent("handler · index.js:7")

    act(() => socket.event("Debugger.resumed", {}))
    await waitFor(() => expect(latestFakeEditor().decorations).toEqual([]))
  })

  it("opens another file when the pause is elsewhere", async () => {
    const { session, bridge } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      scriptParsed(socket, "lib/util.js")
      pausedAt(socket, "lib/util.js", 0)
    })
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "lib/util.js"),
    )
    await waitFor(() => expect(latestFakeEditor().revealLineInCenter).toHaveBeenCalledWith(1))
  })
})

describe("DebugCodeBrowser > source maps", () => {
  it("lists original sources in their own group, hides compiled files behind the toggle, and maps the pause", async () => {
    const { session, bridge, user } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    await act(async () => {
      scriptParsed(socket, "dist/index.js", INLINE_MAP)
      await settle()
    })

    const group = screen.getByRole("group", { name: "Original" })
    expect(within(group).getByRole("button", { name: "index.ts" })).toBeInTheDocument()
    // The compiled script is hidden; the unrelated deployed files stay.
    expect(screen.queryByRole("button", { name: "dist" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "util.js" })).toBeInTheDocument()

    await user.click(screen.getByRole("checkbox", { name: "Show compiled" }))
    expect(screen.getByRole("button", { name: "dist" })).toBeInTheDocument()

    // A pause in the compiled file lands on the original line.
    act(() => pausedAt(socket, "dist/index.js", 2, 4))
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "src/index.ts"),
    )
    expect(screen.getByRole("toolbar")).toHaveTextContent("handler · src/index.ts:2")
    expect(screen.queryByText("no source map for this frame")).not.toBeInTheDocument()
    // The map carries the text (the deployment has no src/); it opens read-only with a note.
    expect(await screen.findByRole("note")).toHaveTextContent(/Read-only/)
    expect(screen.getByRole("textbox", { name: "Editor" })).toHaveAttribute("readonly")
    expect(screen.getByRole("textbox", { name: "Editor" })).toHaveValue(ORIGINAL)
  })

  it("flags a frame the map does not resolve and shows the compiled file", async () => {
    const { session, bridge } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    await act(async () => {
      scriptParsed(socket, "dist/index.js", INLINE_MAP)
      await settle()
    })
    // Line 1 of the compiled output has no mapping.
    act(() => pausedAt(socket, "dist/index.js", 0, 0))
    expect(await screen.findByText("no source map for this frame")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "dist/index.js"),
    )
  })

  it("binds a breakpoint set on an original line at the compiled position", async () => {
    const { session, bridge, user } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    await act(async () => {
      scriptParsed(socket, "dist/index.js", INLINE_MAP)
      await settle()
    })
    await user.click(screen.getByRole("button", { name: "index.ts" }))
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "src/index.ts"),
    )
    await act(async () => {
      latestFakeEditor().clickGutter(2)
      await settle()
    })
    expect(socket.lastRequest("Debugger.setBreakpointByUrl").params).toMatchObject({
      lineNumber: 2,
      columnNumber: 4,
      urlRegex: "file:///var/task/dist/index\\.js$",
    })
  })
})

describe("DebugCodeBrowser > source maps switch", () => {
  it("turns the maps off and on: no Original group while off, the group back when on", async () => {
    const { session, bridge, user } = browser()
    const socket = await act(() => attachFakeSession(session, bridge))
    await act(async () => {
      scriptParsed(socket, "dist/index.js", INLINE_MAP)
      await settle()
    })
    expect(screen.getByRole("group", { name: "Original" })).toBeInTheDocument()
    const maps = screen.getByRole("switch", { name: "Source maps" })
    expect(maps).toHaveAttribute("aria-checked", "true")

    await user.click(maps)
    expect(session.getState().sourceMaps).toBe(false)
    await waitFor(() =>
      expect(screen.queryByRole("group", { name: "Original" })).not.toBeInTheDocument(),
    )
    // Every deployed file is listed as such; nothing to show or hide.
    expect(screen.getByRole("button", { name: "dist" })).toBeInTheDocument()
    expect(screen.queryByRole("checkbox", { name: "Show compiled" })).not.toBeInTheDocument()
    // A pause lands on the compiled line.
    act(() => pausedAt(socket, "dist/index.js", 2, 4))
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "dist/index.js"),
    )
    expect(screen.getByRole("toolbar")).toHaveTextContent("handler · dist/index.js:3")
    expect(screen.queryByText("no source map for this frame")).not.toBeInTheDocument()

    // On again while paused: the same pause moves to the original line.
    await user.click(screen.getByRole("switch", { name: "Source maps" }))
    expect(await screen.findByRole("group", { name: "Original" })).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "src/index.ts"),
    )
    expect(screen.getByRole("toolbar")).toHaveTextContent("handler · src/index.ts:2")
  })
})
