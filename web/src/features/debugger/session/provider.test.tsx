import { act, createTestQueryClient, render, screen } from "@/test/render"
import { debugTarget } from "@/test/debug-target"
import { fakeBridge } from "@/test/fake-bridge"
import { settle } from "@/test/debug-session"
import type { DebuggerTarget } from "@/types"
import { DebugSessionControls } from "../components/debug-session-controls"
import { debuggerTargetQueryOptions } from "../data"
import { useDebugSession } from "./hooks"
import { DebugSessionProvider } from "./provider"
import type { DebugSession } from "./session"

const RECORD_KEY = "overcast-debug:lambda/my-fn"
const KEY = debuggerTargetQueryOptions("lambda", "my-fn").queryKey

function consoleTarget(overrides: Partial<DebuggerTarget> = {}): DebuggerTarget {
  return debugTarget({
    consoleDebug: true,
    bridgePath: "/_overcast/debugger/targets/lambda/my-fn/ws",
    ...overrides,
  })
}

/** Hands the provider's session out, so the test can drive and inspect it. */
function Probe({ onSession }: { onSession: (session: DebugSession) => void }) {
  onSession(useDebugSession())
  return null
}

/**
 * The provider around a probe and, unless `bare`, the Debug tab's controls —
 * which read the descriptor themselves, as the page's do, so a test about
 * the provider asking nothing leaves them out.
 */
function mount(target: DebuggerTarget | null | undefined, { bare = false } = {}) {
  const queryClient = createTestQueryClient()
  if (target !== undefined) queryClient.setQueryData(KEY, target)
  const bridge = fakeBridge()
  let session: DebugSession | null = null
  const view = render(
    <DebugSessionProvider
      service="lambda"
      resource="my-fn"
      fetchFile={() => Promise.reject(new Error("no files"))}
      sessionOptions={{ dial: bridge.dial, backoffMs: [10] }}
    >
      <Probe onSession={(s) => (session = s)} />
      {!bare && <DebugSessionControls service="lambda" resource="my-fn" />}
    </DebugSessionProvider>,
    { queryClient },
  )
  return { ...view, queryClient, bridge, session: () => session! }
}

const record = () => JSON.parse(localStorage.getItem(RECORD_KEY) ?? "{}") as { sessionOpen?: boolean }

beforeEach(() => localStorage.clear())

describe("DebugSessionProvider > auto-restore", () => {
  it("asks the server nothing for a function no session was left open on", () => {
    const { queryClient, bridge, session } = mount(undefined, { bare: true })
    expect(bridge.sockets).toHaveLength(0)
    expect(session().restorable).toBe(false)
    expect(queryClient.getQueryState(KEY)?.fetchStatus).toBe("idle")
    expect(queryClient.getQueryState(KEY)?.dataUpdatedAt).toBe(0)
  })

  it("starts the session again for one left open, says so, and stops saying so after a while", async () => {
    vi.useFakeTimers()
    try {
      localStorage.setItem(RECORD_KEY, JSON.stringify({ sessionOpen: true }))
      const { bridge } = mount(consoleTarget())
      expect(bridge.latest().url).toBe(
        `ws://${window.location.host}/api/debugger/targets/lambda/my-fn/ws`,
      )
      expect(screen.getByRole("status")).toHaveTextContent(
        "Session restored. Connecting to the debugger…",
      )
      await act(async () => {
        bridge.latest().open()
        await settle()
      })
      expect(screen.getByRole("status")).toHaveTextContent(/^Session restored\. Attached/)
      act(() => {
        vi.advanceTimersByTime(15_000)
      })
      expect(screen.getByRole("status")).toHaveTextContent(/^Attached/)
    } finally {
      vi.useRealTimers()
    }
  })

  it("leaves a function alone when the console is no longer on offer, and asks only once", async () => {
    localStorage.setItem(RECORD_KEY, JSON.stringify({ sessionOpen: true }))
    const { bridge, session, queryClient } = mount(debugTarget({ consoleDebug: false }))
    await act(() => settle())
    expect(bridge.sockets).toHaveLength(0)
    expect(session().restorable).toBe(false)
    // Settled: the idle page stops watching the descriptor.
    expect(queryClient.getQueryCache().find({ queryKey: KEY })?.getObserversCount()).toBeGreaterThan(0)
    expect(queryClient.getQueryState(KEY)?.fetchStatus).toBe("idle")
  })

  it("keeps the flag when the page is left and clears it on Stop", async () => {
    localStorage.setItem(RECORD_KEY, JSON.stringify({ sessionOpen: true }))
    const first = mount(consoleTarget())
    expect(first.bridge.sockets).toHaveLength(1)
    first.unmount()
    expect(record().sessionOpen).toBe(true)

    const second = mount(consoleTarget())
    expect(second.bridge.sockets).toHaveLength(1)
    act(() => second.bridge.latest().open())
    await second.user.click(screen.getByRole("button", { name: "Stop debugging" }))
    expect(record().sessionOpen).toBe(false)
    second.unmount()

    const third = mount(consoleTarget())
    expect(third.bridge.sockets).toHaveLength(0)
  })

  it("records a session the reader starts, so the next visit restores it", async () => {
    const { user, bridge } = mount(consoleTarget())
    expect(bridge.sockets).toHaveLength(0)
    await user.click(screen.getByRole("button", { name: "Debug in console" }))
    expect(record().sessionOpen).toBe(true)
    expect(screen.getByRole("status")).not.toHaveTextContent("Session restored")
  })
})
