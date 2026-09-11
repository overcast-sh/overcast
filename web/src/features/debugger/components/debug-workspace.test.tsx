import { act, screen, within } from "@/test/render"
import { attachFakeSession, fakeDebugSession, renderWithDebugSession } from "@/test/debug-session"
import { DebugWorkspace } from "./debug-workspace"

vi.mock("./debug-logs", () => ({
  DebugLogs: () => <div data-testid="logs-drawer" />,
}))

function workspace() {
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(
    <DebugWorkspace logGroup="/aws/lambda/my-fn">
      <div data-testid="code-pane">code</div>
    </DebugWorkspace>,
    session,
  )
  return { session, bridge, ...view }
}

describe("DebugWorkspace", () => {
  it("is the code pane alone until a session opens, then adds the sidebar and drawer, and takes them away on stop", async () => {
    const { session, bridge, user } = workspace()
    expect(screen.getByTestId("code-pane")).toBeInTheDocument()
    expect(screen.queryByRole("complementary", { name: "Debug panels" })).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Debug drawer" })).not.toBeInTheDocument()

    await act(() => attachFakeSession(session, bridge))
    const sidebar = screen.getByRole("complementary", { name: "Debug panels" })
    // Wide layout: four collapsible sections, all open.
    for (const name of ["Locals", "Watch", "Call stack", "Breakpoints"]) {
      expect(within(sidebar).getByRole("button", { name })).toHaveAttribute("aria-expanded", "true")
    }
    await user.click(within(sidebar).getByRole("button", { name: "Watch" }))
    expect(within(sidebar).getByRole("button", { name: "Watch" })).toHaveAttribute(
      "aria-expanded",
      "false",
    )
    expect(within(sidebar).queryByLabelText("Add watch expression")).not.toBeInTheDocument()

    const drawer = screen.getByRole("region", { name: "Debug drawer" })
    expect(within(drawer).getByRole("tab", { name: "Debug console" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(within(drawer).getByRole("log")).toBeInTheDocument()
    await user.click(within(drawer).getByRole("tab", { name: "Logs" }))
    expect(within(drawer).getByTestId("logs-drawer")).toBeInTheDocument()

    act(() => session.stop())
    expect(screen.queryByRole("complementary")).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "Debug drawer" })).not.toBeInTheDocument()
    expect(screen.getByTestId("code-pane")).toBeInTheDocument()
  })

  it("becomes a tab strip above the drawer below the narrow breakpoint", async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = (query: string) => ({ ...matchMedia(query), matches: true })
    try {
      const { session, bridge, user } = workspace()
      await act(() => attachFakeSession(session, bridge))
      expect(screen.getByTestId("debug-workspace")).toHaveAttribute("data-narrow", "true")
      const strip = screen.getByRole("tablist", { name: "Debug panels" })
      expect(
        within(strip)
          .getAllByRole("tab")
          .map((t) => t.textContent),
      ).toEqual(["Locals", "Watch", "Call stack", "Breakpoints"])
      expect(screen.queryByLabelText("Add watch expression")).not.toBeInTheDocument()
      await user.click(within(strip).getByRole("tab", { name: "Watch" }))
      expect(screen.getByLabelText("Add watch expression")).toBeInTheDocument()
    } finally {
      window.matchMedia = matchMedia
    }
  })

  it("counts console entries that arrive while Logs is showing", async () => {
    const { session, bridge, user } = workspace()
    const socket = await act(() => attachFakeSession(session, bridge))
    const drawer = screen.getByRole("region", { name: "Debug drawer" })
    await user.click(within(drawer).getByRole("tab", { name: "Logs" }))
    act(() =>
      socket.event("Runtime.consoleAPICalled", {
        type: "log",
        args: [{ type: "string", value: "hi" }],
        executionContextId: 1,
        timestamp: 1,
      }),
    )
    expect(within(drawer).getByLabelText("1 new entries")).toBeInTheDocument()
    await user.click(within(drawer).getByRole("tab", { name: /Debug console/ }))
    expect(within(drawer).queryByLabelText(/new entries/)).not.toBeInTheDocument()
  })
})
