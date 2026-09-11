import { act, screen } from "@/test/render"
import { debugTarget } from "@/test/debug-target"
import { fakeDebugSession, renderWithDebugSession, settle } from "@/test/debug-session"
import { createTestQueryClient } from "@/test/render"
import { debuggerTargetQueryOptions } from "@/features/debugger/data"
import type { DebuggerTarget } from "@/types"
import { DebugSessionControls } from "./debug-session-controls"

/** The descriptor with the phase 2 fields the backend adds (§ 3.2), until `api.gen.ts` carries them. */
function consoleTarget(overrides: Partial<DebuggerTarget> = {}): DebuggerTarget {
  return {
    ...debugTarget(overrides),
    consoleDebug: true,
    bridgePath: "/_overcast/debugger/targets/lambda/my-fn/ws",
  } as DebuggerTarget
}

function controls(target: DebuggerTarget) {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(debuggerTargetQueryOptions("lambda", "my-fn").queryKey, target)
  const { session, bridge } = fakeDebugSession()
  const view = renderWithDebugSession(
    <DebugSessionControls service="lambda" resource="my-fn" />,
    session,
    {
      queryClient,
    },
  )
  return { session, bridge, ...view }
}

describe("DebugSessionControls", () => {
  it("renders nothing for a target the server does not offer a console session for", () => {
    controls(debugTarget())
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })

  it("starts a session on the bridge path through the BFF, shows its state, and stops it", async () => {
    const { session, bridge, user } = controls(consoleTarget())
    expect(screen.getByRole("status")).toHaveTextContent("No console session.")

    await user.click(screen.getByRole("button", { name: "Debug in console" }))
    expect(bridge.latest().url).toBe(
      `ws://${window.location.host}/api/debugger/targets/lambda/my-fn/ws`,
    )
    expect(screen.getByRole("status")).toHaveTextContent("Connecting to the debugger…")

    await act(async () => {
      bridge.latest().open()
      await settle()
    })
    expect(screen.getByRole("status")).toHaveTextContent(/Attached — set breakpoints/)
    expect(screen.getByTitle("Debugger: attached")).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Stop debugging" }))
    expect(session.getState().status).toBe("idle")
    expect(screen.getByRole("button", { name: "Debug in console" })).toBeInTheDocument()
  })

  it("says it is waiting for a container when the bridge has none", async () => {
    const { bridge, user } = controls(consoleTarget())
    await user.click(screen.getByRole("button", { name: "Debug in console" }))
    act(() => bridge.latest().serverClose(1011, "no container"))
    expect(screen.getByRole("status")).toHaveTextContent(/Waiting for a container — invoke once/)
  })

  it("disables the start button while the target is off", () => {
    controls(consoleTarget({ enabled: false, state: "inert" }))
    expect(screen.getByRole("button", { name: "Debug in console" })).toBeDisabled()
  })
})
