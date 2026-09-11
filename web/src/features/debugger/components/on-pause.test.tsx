import { useCallback, useState } from "react"
import { act, screen } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedAt,
  renderWithDebugSession,
  scriptParsed,
} from "@/test/debug-session"
import { OnPause } from "./on-pause"

/** The route's shape: a tab state, and a pause anywhere switches it to Code. */
function TabsHarness() {
  const [tab, setTab] = useState("test")
  const showCode = useCallback(() => setTab("code"), [])
  return (
    <>
      <OnPause onPause={showCode} />
      <output>{tab}</output>
    </>
  )
}

describe("OnPause", () => {
  it("switches from the Test tab to the Code tab when execution pauses", async () => {
    const { session, bridge } = fakeDebugSession()
    renderWithDebugSession(<TabsHarness />, session)
    expect(screen.getByRole("status")).toHaveTextContent("test")

    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => {
      scriptParsed(socket, "index.js")
      pausedAt(socket, "index.js", 3)
    })
    expect(screen.getByRole("status")).toHaveTextContent("code")
  })

  it("stops listening once unmounted", async () => {
    const { session, bridge } = fakeDebugSession()
    const onPause = vi.fn()
    const { unmount } = renderWithDebugSession(<OnPause onPause={onPause} />, session)
    const socket = await act(() => attachFakeSession(session, bridge))
    unmount()
    act(() => pausedAt(socket, "index.js", 0))
    expect(onPause).not.toHaveBeenCalled()
  })
})
