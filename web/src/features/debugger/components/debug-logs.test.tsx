/**
 * CloudWatch Logs goes through the AWS SDK client, not a BFF route, so MSW
 * cannot see these calls — `@/services/aws-clients` is mocked, as in
 * `log-panel.test.tsx`, and the test asserts on what the drawer built.
 */
import { act, screen, waitFor } from "@/test/render"
import {
  attachFakeSession,
  fakeDebugSession,
  pausedWith,
  renderWithDebugSession,
} from "@/test/debug-session"
import { DebugLogs } from "./debug-logs"

// jsdom gives every element a zero height, so the real virtualizer renders
// no rows; the stand-in is the one `log-viewer.test.tsx` uses.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({
    count,
    estimateSize,
  }: {
    count: number
    estimateSize: (index: number) => number
  }) => {
    let offset = 0
    const items = Array.from({ length: count }, (_, index) => {
      const size = estimateSize(index)
      const item = { index, key: index, start: offset, end: offset + size }
      offset += size
      return item
    })
    return {
      getTotalSize: () => offset,
      getVirtualItems: () => items,
      measureElement: vi.fn(),
      measure: vi.fn(),
      isScrolling: false,
      scrollOffset: 0,
      scrollRect: { height: 800 },
    }
  },
}))

interface FilterCall {
  logGroupName?: string
  startTime?: number
  endTime?: number
  limit?: number
}

const backend = vi.hoisted(() => ({
  calls: [] as FilterCall[],
  events: [] as Array<{ timestamp: number; message: string; logStreamName?: string }>,
}))

vi.mock("@/services/aws-clients", () => ({
  awsClients: {
    logs: () => ({
      send: (command: { constructor: { name: string }; input: FilterCall }) => {
        if (command.constructor.name === "FilterLogEventsCommand") {
          backend.calls.push(command.input)
          return Promise.resolve({ events: backend.events, searchedLogStreams: [] })
        }
        return Promise.resolve({})
      },
    }),
  },
}))

beforeEach(() => {
  backend.calls = []
  backend.events = []
})

describe("DebugLogs", () => {
  it("tails the group over the last 15 minutes and interleaves the session's markers by time", async () => {
    const now = Date.now()
    backend.events = [
      {
        timestamp: now - 5_000,
        message: "START RequestId: abc",
        logStreamName: "2026/09/12/[$LATEST]1",
      },
      {
        timestamp: now + 5_000,
        message: "END RequestId: abc",
        logStreamName: "2026/09/12/[$LATEST]1",
      },
    ]
    const { session, bridge } = fakeDebugSession()
    renderWithDebugSession(<DebugLogs logGroup="/aws/lambda/my-fn" />, session)

    await waitFor(() => expect(backend.calls.length).toBeGreaterThan(0))
    const call = backend.calls[0]
    expect(call.logGroupName).toBe("/aws/lambda/my-fn")
    expect(call.limit).toBe(200)
    expect(call.endTime).toBeUndefined()
    expect(Date.now() - call.startTime!).toBeGreaterThanOrEqual(15 * 60 * 1000)
    expect(now - call.startTime!).toBeLessThan(15 * 60 * 1000 + 5_000)
    expect(screen.getByText(/carries no request id/)).toBeInTheDocument()

    await screen.findByText("START RequestId: abc")
    const socket = await act(() => attachFakeSession(session, bridge))
    act(() => pausedWith(socket, [{ path: "index.js", line0: 6 }]))
    // The viewer's rows are the virtualizer's, indexed in display order.
    const rowText = () =>
      [...document.querySelectorAll("[data-index]")].map((row) => row.textContent)
    await waitFor(() => expect(rowText()).toHaveLength(3))
    // The pause landed between the two events.
    expect(rowText()[0]).toContain("START RequestId: abc")
    expect(rowText()[1]).toContain("── Paused at index.js:7 (other) ──")
    expect(rowText()[2]).toContain("END RequestId: abc")

    act(() => socket.event("Debugger.resumed", {}))
    await waitFor(() => expect(rowText()).toHaveLength(4))
    expect(rowText()[2]).toContain("── Resumed ──")
  })

  it("says so when the function has no log group", () => {
    const { session } = fakeDebugSession()
    renderWithDebugSession(<DebugLogs logGroup={null} />, session)
    expect(screen.getByText(/No log group is configured/)).toBeInTheDocument()
    expect(backend.calls).toEqual([])
  })
})
