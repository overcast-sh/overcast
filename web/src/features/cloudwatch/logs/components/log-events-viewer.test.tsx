import { StrictMode } from "react"
import { QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { fireEvent, render } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { createTestQueryClient, renderWithRouter, screen, userEvent, waitFor } from "@/test/render"
import { ToastContextProvider } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import { logsFilterInfiniteQueryOptions } from "@/features/cloudwatch/logs/data"
import { logEventSignature } from "@/features/cloudwatch/logs/tail"
import { formatLogTime } from "@/lib/log-format"
import type { FilteredLogEvent } from "@/types/logs"
import { LogEventsViewer } from "./log-events-viewer"

// Shared spies so a test can assert on scrolls the viewer requested; the mock
// below hands the same instances to every render.
const virt = vi.hoisted(() => ({
  scrollToIndex: vi.fn(),
  scrollToOffset: vi.fn(),
}))

// jsdom gives every element a zero height, so the real virtualizer renders no
// rows at all and nothing about the buffer would be observable.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 34,
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 34,
        end: index * 34 + 34,
      })),
    measureElement: vi.fn(),
    measure: vi.fn(),
    scrollToIndex: virt.scrollToIndex,
    scrollToOffset: virt.scrollToOffset,
    getOffsetForIndex: (index: number) => [index * 34, "start"] as const,
    scrollOffset: 0,
  }),
}))

beforeEach(() => {
  virt.scrollToIndex.mockClear()
  virt.scrollToOffset.mockClear()
  // View preferences persist to localStorage; a toggle flipped by one test
  // must never leak into the next test's defaults.
  localStorage.clear()
})

// A live-tail session, plus whatever the next FilterLogEvents should return.
// The viewer shows a fetched page and the session's pushes together, and those
// two overlap once an event is on disk.
const live = vi.hoisted(() => {
  class Session {
    private queue: unknown[] = []
    private waiter: ((v: IteratorResult<unknown>) => void) | null = null
    closed = false

    push(event: { timestamp: number; message: string; logStreamName: string }) {
      const frame = {
        sessionUpdate: { sessionResults: [{ ...event, ingestionTime: event.timestamp }] },
      }
      if (this.waiter) {
        const resolve = this.waiter
        this.waiter = null
        resolve({ value: frame, done: false })
      } else {
        this.queue.push(frame)
      }
    }

    [Symbol.asyncIterator]() {
      return {
        next: (): Promise<IteratorResult<unknown>> => {
          if (this.queue.length) return Promise.resolve({ value: this.queue.shift(), done: false })
          if (this.closed) return Promise.resolve({ value: undefined, done: true })
          return new Promise((resolve) => {
            this.waiter = resolve
          })
        },
        return: (): Promise<IteratorResult<unknown>> => {
          this.closed = true
          if (this.waiter) {
            const resolve = this.waiter
            this.waiter = null
            resolve({ value: undefined, done: true })
          }
          return Promise.resolve({ value: undefined, done: true })
        },
      }
    }
  }

  return {
    Session,
    sessions: [] as InstanceType<typeof Session>[],
    /**
     * What FilterLogEvents draws on. Time-bounded requests get the subset
     * inside [startTime, endTime] — both ends inclusive, which is
     * FilterLogEvents' contract (GetLogEvents' endTime is the exclusive one).
     */
    stored: [] as { timestamp: number; message: string; logStreamName: string }[],
    /**
     * Extra pages, consumed one per FilterLogEvents call after the first: a
     * response hands out a nextToken whenever pages remain.
     */
    pages: [] as { timestamp: number; message: string; logStreamName: string }[][],
    /** What DescribeLogStreams returns — the backward floor's metadata. */
    streams: [] as { logStreamName: string; firstEventTimestamp?: number }[],
    /**
     * When set, end-bounded FilterLogEvents calls wait on it before replying
     * — backward chunks and the tail's catch-up read; never the initial page,
     * whose window is open-ended.
     */
    gate: null as Promise<void> | null,
    /** When set, every FilterLogEvents call rejects with it. */
    filterError: null as Error | null,
    filterCalls: 0,
    /** Every FilterLogEvents input, for asserting on window boundaries. */
    filterInputs: [] as { nextToken?: string; startTime?: number; endTime?: number }[],
    reset() {
      this.sessions = []
      this.stored = []
      this.pages = []
      this.streams = []
      this.gate = null
      this.filterError = null
      this.filterCalls = 0
      this.filterInputs = []
    },
  }
})

vi.mock("@/services/aws-clients", () => ({
  awsClients: {
    logs: () => ({
      send: (command: {
        constructor: { name: string }
        input?: { nextToken?: string; startTime?: number; endTime?: number }
      }) => {
        if (command.constructor.name === "FilterLogEventsCommand") {
          live.filterCalls++
          const input = command.input ?? {}
          live.filterInputs.push({ ...input })
          if (live.filterError) return Promise.reject(live.filterError)
          const respond = () => {
            const events = input.nextToken
              ? (live.pages.shift() ?? [])
              : live.stored.filter(
                  (e) =>
                    (input.startTime == null || e.timestamp >= input.startTime) &&
                    (input.endTime == null || e.timestamp <= input.endTime),
                )
            return {
              events: events.map((e) => ({ ...e })),
              searchedLogStreams: [],
              ...(live.pages.length > 0 ? { nextToken: `page-${live.filterCalls}` } : {}),
            }
          }
          if (live.gate && input.endTime != null) return live.gate.then(respond)
          return Promise.resolve(respond())
        }
        if (command.constructor.name === "DescribeLogStreamsCommand") {
          return Promise.resolve({ logStreams: live.streams.map((s) => ({ ...s })) })
        }
        const session = new live.Session()
        live.sessions.push(session)
        return Promise.resolve({ responseStream: session })
      },
    }),
  },
}))

const GROUP = "/aws/lambda/checkout"

const EVENTS: FilteredLogEvent[] = [
  { timestamp: 1_000, ingestionTime: 1_000, logStreamName: "s1", message: "first message" },
  { timestamp: 2_000, ingestionTime: 2_000, logStreamName: "s1", message: "second message" },
]

function Providers({ children }: { children: React.ReactNode }) {
  return (
    <ToastContextProvider>
      <TooltipProvider>{children}</TooltipProvider>
    </ToastContextProvider>
  )
}

function renderViewer(events: FilteredLogEvent[] = EVENTS) {
  live.reset()
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(logsFilterInfiniteQueryOptions(GROUP, {}).queryKey, {
    pages: [{ events, searchedLogStreams: [], nextToken: undefined }],
    pageParams: [undefined],
  })

  return renderWithRouter(
    () => (
      <Providers>
        <LogEventsViewer groupName={GROUP} />
      </Providers>
    ),
    { queryClient },
  )
}

/** The router mounts the route asynchronously, so the toolbar is a `findBy`. */
async function clearButton() {
  return screen.findByRole("button", { name: /^clear$/i })
}

describe("LogEventsViewer > clearing the buffer", () => {
  it("hides the events on screen when Clear is pressed", async () => {
    const { user } = renderViewer()
    expect(await screen.findByText("first message")).toBeInTheDocument()

    await user.click(await clearButton())

    expect(screen.queryByText("first message")).not.toBeInTheDocument()
    expect(screen.queryByText("second message")).not.toBeInTheDocument()
  })

  it("explains that the buffer was cleared rather than claiming there are no logs", async () => {
    const { user } = renderViewer()

    await user.click(await clearButton())

    expect(screen.getByText("Buffer cleared")).toBeInTheDocument()
    expect(screen.queryByText("No log events")).not.toBeInTheDocument()
  })

  it("offers to bring back the events it hid", async () => {
    const { user } = renderViewer()

    await user.click(await clearButton())

    expect(screen.getByRole("button", { name: /show 2 earlier/i })).toBeInTheDocument()
  })

  it("restores the hidden events when the offer is taken", async () => {
    const { user } = renderViewer()
    await user.click(await clearButton())

    await user.click(screen.getByRole("button", { name: /show 2 earlier/i }))

    expect(screen.getByText("first message")).toBeInTheDocument()
    expect(screen.getByText("second message")).toBeInTheDocument()
  })

  it("disables Clear when there is nothing on screen to clear", async () => {
    const { user } = renderViewer()
    await user.click(await clearButton())

    expect(await clearButton()).toBeDisabled()
  })
})

/*
 * FilterLogEvents caps a page at 10,000 events. The viewer used to read one
 * page and present it as the whole result set; now it pages via nextToken and
 * says when the count on screen is not everything.
 */
describe("LogEventsViewer > pagination", () => {
  it("fetches the following page when the newest edge is on screen", async () => {
    // The mocked virtualizer renders every row, so the newest edge counts as
    // visible and the auto-load drains the remaining pages.
    const { user } = renderViewer([])
    await clearButton() // wait for the toolbar to mount
    live.stored = [{ timestamp: 1_000, message: "page one", logStreamName: "s1" }]
    live.pages = [[{ timestamp: 2_000, message: "page two", logStreamName: "s1" }]]

    await user.click(refreshButton())

    expect(await screen.findByText(/page one/)).toBeInTheDocument()
    expect(await screen.findByText(/page two/)).toBeInTheDocument()
    await waitFor(() => expect(live.filterCalls).toBe(2))
  })

  it("says so at the end of the list once everything is loaded", async () => {
    renderViewer(EVENTS)

    // Everything the server has is on screen and no token remains — the list
    // must end with an explicit marker, not just trail off into empty space
    // that reads the same as "still loading".
    expect(await screen.findByText("End of logs")).toBeInTheDocument()
  })

  it("swaps the end marker for a live-tail notice while tailing", async () => {
    const { user } = renderViewer(EVENTS)
    await screen.findByText("End of logs")

    await user.click(await tailButton())

    expect(await screen.findByText(/watching for new events/i)).toBeInTheDocument()
    expect(screen.queryByText("End of logs")).not.toBeInTheDocument()
  })

  it("shows the live dot on the Tail button while a session is open", async () => {
    const { user } = renderViewer(EVENTS)
    expect(screen.queryByTestId("live-tail-indicator")).not.toBeInTheDocument()

    await user.click(await tailButton())

    const dot = await screen.findByTestId("live-tail-indicator")
    expect(dot).toHaveAttribute("data-status", "live")
  })

  it("chains through every page and settles on the full, unqualified count", async () => {
    const { user } = renderViewer([])
    await clearButton() // wait for the toolbar to mount
    live.stored = [{ timestamp: 1_000, message: "page one", logStreamName: "s1" }]
    live.pages = [
      [{ timestamp: 2_000, message: "page two", logStreamName: "s1" }],
      [{ timestamp: 3_000, message: "page three", logStreamName: "s1" }],
    ]

    await user.click(refreshButton())

    // Three pages, three fetches; once no token remains the count drops its
    // "+" qualifier and reads as the whole result set — because now it is.
    expect(await screen.findByText(/page three/)).toBeInTheDocument()
    await waitFor(() => expect(live.filterCalls).toBe(3))
    expect(screen.getByText(/^3 events$/)).toBeInTheDocument()
  })
})

/*
 * A ResourceNotFoundException is an answer, not an absence: the group (or
 * stream) does not exist in the region the console points at. These used to
 * render the ordinary "No log events" empty state, so opening a stream URL
 * with the region selector on the wrong region read as "your app never
 * logged" instead of "look at the region selector".
 */
describe("LogEventsViewer > load failures", () => {
  function awsError(name: string, message: string): Error {
    const error = new Error(message)
    error.name = name
    return error
  }

  function renderFailing(streamName?: string) {
    return renderWithRouter(
      () => (
        <Providers>
          <LogEventsViewer groupName={GROUP} streamName={streamName} />
        </Providers>
      ),
      { queryClient: createTestQueryClient() },
    )
  }

  it("shows a not-found state naming the group and region, never the empty-stream state", async () => {
    live.reset()
    live.filterError = awsError(
      "ResourceNotFoundException",
      `The specified log group does not exist: ${GROUP}`,
    )

    renderFailing("s1")

    expect(await screen.findByText("Log group not found")).toBeInTheDocument()
    expect(
      screen.getByText(/No log group named .*checkout.* exists in us-east-1/),
    ).toBeInTheDocument()
    expect(screen.queryByText("No log events")).not.toBeInTheDocument()
  })

  it("names the stream instead when the group exists and the stream is what is missing", async () => {
    live.reset()
    live.filterError = awsError(
      "ResourceNotFoundException",
      "The specified log stream does not exist: s1",
    )

    renderFailing("s1")

    expect(await screen.findByText("Log stream not found")).toBeInTheDocument()
    expect(screen.getByText(/no stream named .*s1.* in us-east-1/i)).toBeInTheDocument()
    expect(screen.queryByText("No log events")).not.toBeInTheDocument()
  })

  it("surfaces any other query failure rather than reading as an empty stream", async () => {
    live.reset()
    live.filterError = new Error("connection reset by peer")

    renderFailing()

    expect(await screen.findByText("Failed to load log events")).toBeInTheDocument()
    expect(screen.getByText("connection reset by peer")).toBeInTheDocument()
    expect(screen.queryByText("No log events")).not.toBeInTheDocument()
  })
})

/*
 * A search result deep-links to the stream view anchored on one event: the
 * view scrolls to it and keeps it visibly marked, the way the AWS console
 * highlights the focused message when a search hit opens its stream.
 */
describe("LogEventsViewer > anchored arrival", () => {
  function renderAnchored(anchor: { timestamp: number; signature?: string }) {
    live.reset()
    const queryClient = createTestQueryClient()
    queryClient.setQueryData(
      logsFilterInfiniteQueryOptions(GROUP, {
        logStreamNames: ["s1"],
        startTime: anchor.timestamp - 15 * 60 * 1000,
      }).queryKey,
      {
        pages: [{ events: EVENTS, searchedLogStreams: [], nextToken: undefined }],
        pageParams: [undefined],
      },
    )
    return renderWithRouter(
      () => (
        <Providers>
          <LogEventsViewer groupName={GROUP} streamName="s1" anchor={anchor} />
        </Providers>
      ),
      { queryClient },
    )
  }

  it("marks the anchored event's row", async () => {
    renderAnchored({ timestamp: 2_000, signature: logEventSignature(EVENTS[1]) })

    await screen.findByText("second message")
    const anchored = document.querySelector("[data-anchored]")
    expect(anchored).not.toBeNull()
    expect(anchored?.textContent).toContain("second message")
  })

  it("matches by timestamp alone when no signature is given", async () => {
    renderAnchored({ timestamp: 1_000 })

    await screen.findByText("first message")
    expect(document.querySelector("[data-anchored]")?.textContent).toContain("first message")
  })
})

/*
 * FilterLogEvents pages forward only, so "load older" is time-window
 * expansion: the viewer extends its window's start backward in chunks, each
 * chunk fetched as its own fully-paged query and prepended. The anchored
 * arrival is the flow with a bounded window start (the 15-minute lead-in), so
 * it is where expansion is observable — and the mocked virtualizer renders
 * every row, which makes the oldest edge permanently "in view" and lets the
 * expansion drain without simulated scrolling.
 */

/** A realistic anchor instant, and the lead-in window it opens. */
const T = 1_700_000_000_000
const LEAD_MS = 15 * 60 * 1000
const WINDOW_START = T - LEAD_MS

/**
 * Render anchored WITHOUT seeding the query cache, so the base window and the
 * backward chunks all go through the mocked client. Callers configure `live`
 * (and call `live.reset()`) first.
 */
function renderAnchoredUnseeded(anchor: { timestamp: number; signature?: string }) {
  return renderWithRouter(
    () => (
      <Providers>
        <LogEventsViewer groupName={GROUP} streamName="s1" anchor={anchor} />
      </Providers>
    ),
    { queryClient: createTestQueryClient() },
  )
}

describe("LogEventsViewer > backward time-window expansion", () => {
  it("loads events older than the anchored lead-in — the anchored flow inherits expansion", async () => {
    live.reset()
    live.stored = [
      { timestamp: WINDOW_START - 120_000, message: "older context line", logStreamName: "s1" },
      { timestamp: WINDOW_START + 1_000, message: "lead-in line", logStreamName: "s1" },
      { timestamp: T, message: "the anchor line", logStreamName: "s1" },
    ]
    live.streams = [{ logStreamName: "s1", firstEventTimestamp: WINDOW_START - 120_000 }]

    renderAnchoredUnseeded({ timestamp: T })

    await screen.findByText("the anchor line")
    // The event before the lead-in arrives without any change to the filter.
    expect(await screen.findByText("older context line")).toBeInTheDocument()
    // The chunk was a plain time window ending right where the loaded window
    // began — endTime is inclusive, so the boundary is windowStart − 1.
    const chunk = live.filterInputs.find((i) => i.endTime != null)
    expect(chunk).toBeDefined()
    expect(chunk!.endTime).toBe(WINDOW_START - 1)
    // History exhausted — the oldest edge says so.
    expect(await screen.findByText("Start of logs")).toBeInTheDocument()
  })

  it("fetches each window exactly once — adjacent inclusive chunks, no refetch of loaded pages", async () => {
    live.reset()
    live.stored = [
      {
        timestamp: WINDOW_START - 900_001,
        message: "second chunk boundary line",
        logStreamName: "s1",
      },
      { timestamp: WINDOW_START - 900_000, message: "first chunk start line", logStreamName: "s1" },
      {
        timestamp: WINDOW_START - 1,
        message: "boundary line before the window",
        logStreamName: "s1",
      },
      { timestamp: WINDOW_START, message: "boundary line inside the window", logStreamName: "s1" },
      { timestamp: T, message: "the anchor line", logStreamName: "s1" },
    ]
    // A floor an hour back: the first two chunks stay bounded, the third
    // closes out unbounded.
    live.streams = [{ logStreamName: "s1", firstEventTimestamp: WINDOW_START - 3_600_000 }]

    renderAnchoredUnseeded({ timestamp: T })

    await screen.findByText("the anchor line")
    expect(await screen.findByText("second chunk boundary line")).toBeInTheDocument()
    await screen.findByText("Start of logs")

    // Base window + three chunks, and not a request more: a prepend never
    // refetches pages that are already loaded.
    await waitFor(() => expect(live.filterCalls).toBe(4))
    await new Promise((r) => setTimeout(r, 30))
    expect(live.filterCalls).toBe(4)

    // Windows are adjacent under inclusive-endTime semantics: [a, b], [b+1, c]
    // — nothing skipped, nothing double-covered — and the chunk sizes double.
    const chunks = live.filterInputs.filter((i) => i.endTime != null)
    expect(chunks).toHaveLength(3)
    expect(chunks[0].endTime).toBe(WINDOW_START - 1)
    expect(chunks[0].startTime).toBe(WINDOW_START - 900_000)
    expect(chunks[1].endTime).toBe(WINDOW_START - 900_001)
    expect(chunks[1].startTime).toBe(WINDOW_START - 900_000 - 1_800_000)
    expect(chunks[2].endTime).toBe(WINDOW_START - 900_000 - 1_800_000 - 1)
    expect(chunks[2].startTime).toBeUndefined()

    // Every boundary event rendered exactly once.
    for (const e of live.stored) {
      expect(rowsShowing(e.message)).toBe(1)
    }
  })

  it("closes out with one unbounded window after an empty chunk when no floor is known", async () => {
    live.reset()
    live.stored = [{ timestamp: T, message: "the anchor line", logStreamName: "s1" }]
    // The stream reports no firstEventTimestamp — the floor is a hint that can
    // be absent, and its absence must not turn into open-ended probing.
    live.streams = [{ logStreamName: "s1" }]

    renderAnchoredUnseeded({ timestamp: T })

    await screen.findByText("the anchor line")
    expect(await screen.findByText("Start of logs")).toBeInTheDocument()

    // Base window, one empty probe, one unbounded close-out — then silence.
    await waitFor(() => expect(live.filterCalls).toBe(3))
    await new Promise((r) => setTimeout(r, 30))
    expect(live.filterCalls).toBe(3)
    const chunks = live.filterInputs.filter((i) => i.endTime != null)
    expect(chunks[1].startTime).toBeUndefined()
  })

  it("says it is loading older events while a backward fetch is in flight", async () => {
    live.reset()
    let release!: () => void
    live.gate = new Promise<void>((r) => {
      release = r
    })
    live.stored = [
      { timestamp: WINDOW_START - 60_000, message: "older line", logStreamName: "s1" },
      { timestamp: T, message: "the anchor line", logStreamName: "s1" },
    ]
    live.streams = [{ logStreamName: "s1", firstEventTimestamp: WINDOW_START - 60_000 }]

    renderAnchoredUnseeded({ timestamp: T })

    await screen.findByText("the anchor line")
    expect(await screen.findByText("Loading older events…")).toBeInTheDocument()

    release()
    live.gate = null
    expect(await screen.findByText("older line")).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText("Loading older events…")).not.toBeInTheDocument())
  })

  it("treats an explicit start time as a hard floor and says the range start is the reason", async () => {
    const { user } = renderViewer(EVENTS)
    await clearButton()
    live.stored = [{ timestamp: Date.now() - 60_000, message: "recent line", logStreamName: "s1" }]

    // Pick "Last 5 minutes" — an explicit, user-chosen startTime.
    await user.click(screen.getByRole("button", { name: /all time/i }))
    await user.click(screen.getByTitle("Last 5 minutes"))

    expect(await screen.findByText("recent line")).toBeInTheDocument()
    expect(await screen.findByText(/start of range/i)).toBeInTheDocument()
    expect(screen.queryByText("Start of logs")).not.toBeInTheDocument()
    // Never a fetch past the floor: no backward window was requested.
    expect(live.filterInputs.filter((i) => i.endTime != null)).toHaveLength(0)
  })

  it("marks the oldest edge as the start of logs when the window was never bounded", async () => {
    renderViewer(EVENTS)

    // An unbounded window's first page already begins at the oldest event, so
    // the marker is symmetric with "End of logs" from the outset.
    expect(await screen.findByText("Start of logs")).toBeInTheDocument()
    expect(await screen.findByText("End of logs")).toBeInTheDocument()
  })
})

/*
 * Jump-to-timestamp reuses the anchor machinery: the toolbar control navigates
 * to the same route with `anchorTs`, and anchor matching falls back to the
 * first event at-or-after the timestamp when nothing sits exactly on it. The
 * persistent mark stays reserved for exact matches.
 */
describe("LogEventsViewer > jump to timestamp", () => {
  function renderAtStreamRoute() {
    const queryClient = createTestQueryClient()
    const rootRoute = createRootRoute()
    const streamRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: "/cloudwatch/logs/stream",
      validateSearch: (search: Record<string, unknown>) => ({
        groupName: typeof search.groupName === "string" ? search.groupName : undefined,
        streamName: typeof search.streamName === "string" ? search.streamName : undefined,
        ...(typeof search.anchorTs === "number" ? { anchorTs: search.anchorTs } : {}),
        ...(typeof search.anchorSig === "string" ? { anchorSig: search.anchorSig } : {}),
      }),
      component: function StreamPage() {
        const { groupName, streamName, anchorTs, anchorSig } = streamRoute.useSearch()
        if (!groupName || !streamName) return null
        return (
          <Providers>
            <LogEventsViewer
              groupName={groupName}
              streamName={streamName}
              anchor={anchorTs != null ? { timestamp: anchorTs, signature: anchorSig } : undefined}
            />
          </Providers>
        )
      },
    })
    const router = createRouter({
      routeTree: rootRoute.addChildren([streamRoute]),
      history: createMemoryHistory({
        initialEntries: [
          `/cloudwatch/logs/stream?groupName=${encodeURIComponent(GROUP)}&streamName=s1`,
        ],
      }),
    })
    render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )
    return { user: userEvent.setup(), router }
  }

  it("navigates to the same route anchored on the chosen time", async () => {
    live.reset()
    const target = new Date("2026-01-05T10:30").getTime()
    live.stored = [
      { timestamp: target - 30_000, message: "line before the jump", logStreamName: "s1" },
      { timestamp: target, message: "jump target line", logStreamName: "s1" },
    ]
    live.streams = [{ logStreamName: "s1", firstEventTimestamp: target - 30_000 }]
    const { user, router } = renderAtStreamRoute()
    await screen.findByText("line before the jump")

    await user.click(screen.getByRole("button", { name: /jump to timestamp/i }))
    fireEvent.change(await screen.findByLabelText(/date and time/i), {
      target: { value: "2026-01-05T10:30" },
    })
    await user.click(screen.getByRole("button", { name: /^jump$/i }))

    await waitFor(() =>
      expect((router.state.location.search as { anchorTs?: number }).anchorTs).toBe(target),
    )
    // The anchor machinery takes over: the exact-timestamp event gets the mark.
    await waitFor(() =>
      expect(document.querySelector("[data-anchored]")?.textContent).toContain("jump target line"),
    )
  })

  it("centres on the first event at-or-after the time, without marking a nearest match", async () => {
    live.reset()
    live.stored = [
      { timestamp: T - 5_000, message: "before the jump point", logStreamName: "s1" },
      { timestamp: T + 5_000, message: "after the jump point", logStreamName: "s1" },
    ]
    live.streams = [{ logStreamName: "s1", firstEventTimestamp: T - 5_000 }]

    renderAnchoredUnseeded({ timestamp: T })

    await screen.findByText("after the jump point")
    await waitFor(() => expect(virt.scrollToIndex).toHaveBeenCalled())
    // Centred on the first event at-or-after the timestamp…
    expect(virt.scrollToIndex).toHaveBeenCalledWith(1, { align: "center" })
    // …but the persistent highlight is for exact matches only.
    expect(document.querySelector("[data-anchored]")).toBeNull()
  })
})

/*
 * Filter highlighting is driven by a matcher the viewer compiles once per
 * filter and hands to every row, rather than by each row re-parsing the pattern
 * on every scroll frame. That plumbing is invisible from the outside — which is
 * exactly why it is worth a test that looks at the rendered marks.
 */
describe("LogEventsViewer > filter highlighting", () => {
  async function search(term: string, events: FilteredLogEvent[]) {
    const { user } = renderViewer([])
    live.stored = events.map((e) => ({
      timestamp: e.timestamp ?? 0,
      message: e.message ?? "",
      logStreamName: e.logStreamName ?? "s1",
    }))

    await user.type(await screen.findByPlaceholderText(/^Filter/), term)
    await user.click(screen.getByRole("button", { name: /^search$/i }))
    return user
  }

  it("marks the matching term inside a row", async () => {
    await search("timeout", levelled(["connection timeout after 30s"]))

    const marks = await screen.findAllByText("timeout", {
      selector: "mark",
    })
    expect(marks).toHaveLength(1)
  })

  it("marks every row that matches, not just the first", async () => {
    await search(
      "timeout",
      levelled(["connection timeout after 30s", "timeout talking to upstream"]),
    )

    // A shared global regex whose `lastIndex` carried between rows would light
    // up every other row instead of all of them.
    await waitFor(async () =>
      expect(await screen.findAllByText("timeout", { selector: "mark" })).toHaveLength(2),
    )
  })

  it("leaves a non-matching row unmarked", async () => {
    await search("timeout", levelled(["all is well"]))

    expect(await screen.findByText(/all is well/)).toBeInTheDocument()
    expect(document.querySelectorAll("mark")).toHaveLength(0)
  })
})

// ─── Live tail ─────────────────────────────────────────────────────────────

const TAILED = {
  timestamp: 1_700_000_000_000,
  message: "the one and only line",
  logStreamName: "stream-a",
}

/** Renders with an empty page, so only the live session can add rows. */
function renderTailViewer() {
  return renderViewer([])
}

/**
 * The same tree with StrictMode where `main.tsx` puts it — at the root, which
 * is the only place React honours it.
 */
function renderTailViewerUnderStrictMode() {
  live.reset()
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(logsFilterInfiniteQueryOptions(GROUP, {}).queryKey, {
    pages: [{ events: [], searchedLogStreams: [], nextToken: undefined }],
    pageParams: [undefined],
  })
  const rootRoute = createRootRoute()
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: "/",
        component: () => (
          <Providers>
            <LogEventsViewer groupName={GROUP} />
          </Providers>
        ),
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })

  render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
  return { user: userEvent.setup() }
}

const tailButton = () => screen.findByRole("button", { name: /^tail$/i })

/** The toolbar's refresh control carries an icon and no label. */
function refreshButton() {
  const button = document.querySelector("svg.lucide-refresh-cw")?.closest("button")
  if (!button) throw new Error("refresh button not found")
  return button
}

const rowsShowing = (message: string) => screen.queryAllByText(message).length

describe("LogEventsViewer > live tail", () => {
  it("opens one session when tail is switched on", async () => {
    const { user } = renderTailViewer()

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 20))

    expect(live.sessions).toHaveLength(1)
  })

  it("opens one session under StrictMode too", async () => {
    const { user } = renderTailViewerUnderStrictMode()

    // StrictMode's extra mount/unmount/remount happens while tail is off, and
    // the effect subscribes to nothing in that state.
    const tail = await tailButton()
    expect(live.sessions).toHaveLength(0)

    // Switching tail on is a dependency change on an already-mounted effect,
    // which React runs once — in development as in production.
    await user.click(tail)
    await waitFor(() => expect(live.sessions).toHaveLength(1))
    await new Promise((r) => setTimeout(r, 20))

    expect(live.sessions).toHaveLength(1)
  })

  it("shows a live event once when a refetch has already picked it up", async () => {
    const { user } = renderTailViewer()

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    // The emulator now holds the event, so the next FilterLogEvents returns it.
    // A refetch is not something the user asks for — react-query fires one on
    // window focus, which is exactly what happens on the way back from a
    // terminal after `aws logs put-log-events`.
    live.stored = [TAILED]
    await user.click(refreshButton())
    await waitFor(() => expect(rowsShowing(TAILED.message)).toBe(1))

    // …and the session that was already open pushes the same event.
    live.sessions[0].push(TAILED)
    await new Promise((r) => setTimeout(r, 20))

    expect(rowsShowing(TAILED.message)).toBe(1)
  })

  it("keeps a live event that the refetch did not return", async () => {
    const { user } = renderTailViewer()

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    live.sessions[0].push(TAILED)
    await waitFor(() => expect(rowsShowing(TAILED.message)).toBe(1))

    // A refetch whose snapshot predates the event must not drop it — this is
    // the other half of the overlap, and clearing the buffer on every refetch
    // would lose the line entirely.
    await user.click(refreshButton())
    await waitFor(() => expect(live.filterCalls).toBeGreaterThan(0))
    await new Promise((r) => setTimeout(r, 20))

    expect(rowsShowing(TAILED.message)).toBe(1)
  })

  it("keeps a line the function really did log twice", async () => {
    const { user } = renderTailViewer()

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    live.sessions[0].push(TAILED)
    live.sessions[0].push(TAILED)
    await waitFor(() => expect(rowsShowing(TAILED.message)).toBe(2))

    // The stored page holds both copies too, so both are accounted for and
    // neither is dropped.
    live.stored = [TAILED, TAILED]
    await user.click(refreshButton())
    await waitFor(() => expect(live.filterCalls).toBeGreaterThan(0))
    await new Promise((r) => setTimeout(r, 20))

    expect(rowsShowing(TAILED.message)).toBe(2)
  })

  it("hides a tailed event when Clear hides the page copy of it", async () => {
    const { user } = renderTailViewer()

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    live.sessions[0].push(TAILED)
    await waitFor(() => expect(rowsShowing(TAILED.message)).toBe(1))

    // Once the event is on disk both sources carry it, and reconciling leaves
    // one row. Clear has to take that row away whichever source it came from.
    live.stored = [TAILED]
    await user.click(refreshButton())
    await waitFor(() => expect(live.filterCalls).toBeGreaterThan(0))
    await user.click(await clearButton())

    expect(rowsShowing(TAILED.message)).toBe(0)
  })
})

/*
 * A live session pushes only what is written after it opens, and the page on
 * screen is as old as the visit. Ticking Tail therefore reads the stretch in
 * between — from the newest event on screen up to now — once per opened
 * session, and shows the result beside the page and the live buffer.
 */
describe("LogEventsViewer > live tail catch-up", () => {
  /** The page on screen, in the shape the mock's store holds. */
  const PAGE = [
    { timestamp: 1_000, logStreamName: "s1", message: "first message" },
    { timestamp: 2_000, logStreamName: "s1", message: "second message" },
  ]
  /** Written after the page was fetched, before Tail was ticked. */
  const IN_THE_GAP = {
    timestamp: 3_000,
    logStreamName: "s1",
    message: "logged while nobody was watching",
  }

  /**
   * The catch-up read's window: the FilterLogEvents calls made after Tail was
   * ticked, reduced to their time bounds (the mock records the whole input).
   */
  const catchUpWindows = (from: number) =>
    live.filterInputs
      .slice(from)
      .map(({ startTime, endTime, nextToken }) => ({ startTime, endTime, nextToken }))

  it("reads the events written between the page fetch and the session opening", async () => {
    const { user } = renderViewer(EVENTS)
    // What is on disk by the time Tail is ticked: the page, plus one more.
    live.stored = [...PAGE, IN_THE_GAP]
    const before = live.filterInputs.length

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    expect(await screen.findByText(IN_THE_GAP.message)).toBeInTheDocument()
    // The read starts at the newest event on screen — inclusive, so that
    // event comes back too and is reconciled rather than doubled.
    expect(catchUpWindows(before)).toEqual([
      { startTime: 2_000, endTime: expect.any(Number), nextToken: undefined },
    ])
    expect(rowsShowing("second message")).toBe(1)
    expect(rowsShowing("first message")).toBe(1)
  })

  it("pages the read to the end when the gap exceeds one page", async () => {
    const { user } = renderViewer(EVENTS)
    live.stored = [...PAGE, IN_THE_GAP]
    live.pages = [[{ timestamp: 4_000, logStreamName: "s1", message: "second page" }]]

    await user.click(await tailButton())

    expect(await screen.findByText("second page")).toBeInTheDocument()
    expect(rowsShowing(IN_THE_GAP.message)).toBe(1)
  })

  it("reconciles an event the session also pushes", async () => {
    const { user } = renderViewer(EVENTS)
    live.stored = [...PAGE, IN_THE_GAP]

    await user.click(await tailButton())
    await waitFor(() => expect(live.sessions).toHaveLength(1))
    await screen.findByText(IN_THE_GAP.message)

    // Written just as the session opened: the read returned it and the
    // session pushes it. One row.
    live.sessions[0].push(IN_THE_GAP)
    await new Promise((r) => setTimeout(r, 20))

    expect(rowsShowing(IN_THE_GAP.message)).toBe(1)
  })

  it("catches up again from where the view got to when Tail is reopened", async () => {
    const { user } = renderViewer(EVENTS)
    live.stored = [...PAGE, IN_THE_GAP]

    await user.click(await tailButton())
    await screen.findByText(IN_THE_GAP.message)

    // Tail off; more gets written; Tail on again.
    await user.click(await tailButton())
    const LATER = { timestamp: 5_000, logStreamName: "s1", message: "later" }
    live.stored = [...PAGE, IN_THE_GAP, LATER]
    const before = live.filterInputs.length
    await user.click(await tailButton())

    expect(await screen.findByText("later")).toBeInTheDocument()
    expect(catchUpWindows(before)).toEqual([
      { startTime: 3_000, endTime: expect.any(Number), nextToken: undefined },
    ])
    expect(rowsShowing(IN_THE_GAP.message)).toBe(1)
  })

  it("starts from the cleared-through cut when the screen was cleared", async () => {
    const { user } = renderViewer(EVENTS)
    live.stored = [...PAGE, IN_THE_GAP]

    await user.click(await clearButton())
    const before = live.filterInputs.length
    await user.click(await tailButton())

    // Everything at or before the cut stays hidden; the gap event shows.
    expect(await screen.findByText(IN_THE_GAP.message)).toBeInTheDocument()
    expect(rowsShowing("second message")).toBe(0)
    expect(catchUpWindows(before)).toEqual([
      { startTime: 2_000, endTime: expect.any(Number), nextToken: undefined },
    ])
  })

  it("says the read is in progress at the newest edge, then that it is watching", async () => {
    const { user } = renderViewer(EVENTS)
    // Hold the read open so the marker is observable: the mock gates only
    // end-bounded calls, and the catch-up read is one.
    let release!: () => void
    live.gate = new Promise<void>((resolve) => {
      release = resolve
    })

    await user.click(await tailButton())

    expect(await screen.findByText(/catching up on events since the last fetch/i)).toBeVisible()
    release()
    expect(await screen.findByText(/watching for new events/i)).toBeVisible()
  })
})

/*
 * The level badge and the row tint are driven by the same `detectLogLevel`
 * result, and they used to disagree about which rows deserved one: a JSON
 * document got both, a plain line got the tint alone unless Format happened to
 * be ticked. These pin that they now agree, because the whole point of the
 * badge is reading a stream that mixes the two.
 */

/** A `console.warn` under the Node runtime's text log format. */
const CONSOLE_WARN =
  "2026-08-10T02:34:39.674Z\t13aa488f-ea9e-4d38-bdfe-74ad3d71e708\tWARN\tCannot push rates for property '934811' as there are no rates available."

/** The same severity, the way Powertools writes it. */
const POWERTOOLS_WARN =
  '{"level":"WARN","message":"Cannot push rates for property \'934811\'","service":"rate-pusher"}'

/** A timed-out invocation's report, as a runtime on `LogFormat: JSON` writes it. */
const TIMED_OUT_REPORT =
  '{"time":"2026-08-10T02:34:42.700Z","type":"platform.report","record":{"requestId":"13aa488f","status":"timeout","metrics":{"durationMs":3003.12}}}'

function levelled(messages: string[]): FilteredLogEvent[] {
  return messages.map((message, i) => ({
    timestamp: 1_000 + i,
    ingestionTime: 1_000 + i,
    logStreamName: "s1",
    message,
  }))
}

/** The badge is the only element whose whole text is the level name. */
const badge = (level: string) => screen.queryByText(level, { exact: true })

describe("LogEventsViewer > level badges", () => {
  it("labels a plain-text line whose level is a column, not a field", async () => {
    renderViewer(levelled([CONSOLE_WARN]))

    expect(await screen.findByText("warn")).toBeInTheDocument()
  })

  it("labels the same severity written as JSON", async () => {
    renderViewer(levelled([POWERTOOLS_WARN]))

    expect(await screen.findByText("warn")).toBeInTheDocument()
  })

  it("labels both shapes in one stream, which is how they arrive", async () => {
    renderViewer(levelled([CONSOLE_WARN, POWERTOOLS_WARN]))

    expect(await screen.findAllByText("warn")).toHaveLength(2)
  })

  it("labels a system log record with the level AWS assigns it", async () => {
    // A run that did not succeed is a warning, per AWS's system log level
    // mapping — the row reads as the REPORT line the record replaced.
    renderViewer(levelled([TIMED_OUT_REPORT]))

    expect(await screen.findByText("warn")).toBeInTheDocument()
    expect(screen.getByText(/REPORT RequestId: 13aa488f/)).toBeInTheDocument()
  })

  it("adds no badge to a line with no level to report", async () => {
    renderViewer(levelled(["Certificate request self-signature ok"]))

    expect(await screen.findByText(/Certificate request/)).toBeInTheDocument()
    expect(badge("warn")).not.toBeInTheDocument()
    expect(badge("info")).not.toBeInTheDocument()
  })

  it("still labels once Format is ticked", async () => {
    const { user } = renderViewer(levelled([CONSOLE_WARN]))
    await screen.findByText("warn")

    await user.click(screen.getByRole("checkbox", { name: /format/i }))

    expect(badge("warn")).toBeInTheDocument()
  })

  it("drops the badge in plaintext mode, which shows the line as it arrived", async () => {
    const { user } = renderViewer(levelled([CONSOLE_WARN]))
    await screen.findByText("warn")

    await user.click(screen.getByRole("button", { name: /^plaintext$/i }))

    expect(badge("warn")).not.toBeInTheDocument()
  })
})

/*
 * The AWS console offers local or UTC timestamp display; we always showed
 * local. The toggle switches the whole viewer's rendering — rows and the
 * time-range chip — without touching the stored timestamps.
 */
describe("LogEventsViewer > UTC timestamp display", () => {
  const utcButton = () => screen.findByRole("button", { name: /^utc$/i })

  it("shows local time by default, as every call site always has", async () => {
    renderViewer()

    await screen.findByText("first message")
    expect(screen.getByText(formatLogTime(1_000))).toBeInTheDocument()
  })

  it("switches the time column to UTC when toggled", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await utcButton())

    // EVENTS[0] sits at epoch+1000ms — 00:00:01.000 UTC wherever this runs.
    expect(screen.getByText("00:00:01.000")).toBeInTheDocument()
    expect(screen.getByText("00:00:02.000")).toBeInTheDocument()
  })

  it("switches back to local when toggled off", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await utcButton())
    await user.click(await utcButton())

    expect(screen.getByText(formatLogTime(1_000))).toBeInTheDocument()
  })
})

/*
 * Inter-row time deltas, devtools-style: an optional "+N ms" beside the time
 * showing the gap to the chronologically previous event. Display-only fiction
 * over real timestamps — the timestamps themselves never change.
 */
describe("LogEventsViewer > inter-row time deltas", () => {
  const deltasToggle = () => screen.findByRole("checkbox", { name: /deltas/i })

  it("shows no deltas by default", async () => {
    renderViewer()

    await screen.findByText("first message")
    expect(screen.queryByText("+1.00 s")).not.toBeInTheDocument()
  })

  it("shows the gap to the previous event when toggled on", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await deltasToggle())

    // 1_000 → 2_000: one second. The oldest event has no predecessor, so
    // exactly one delta renders for two events.
    expect(await screen.findByText("+1.00 s")).toBeInTheDocument()
    expect(screen.getAllByText(/^[+-]/)).toHaveLength(1)
  })

  it("keeps the delta chronological under Newest-first", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await deltasToggle())
    await user.click(screen.getByRole("button", { name: /oldest/i }))

    // The display order flips; the gap between the same two events does not.
    expect(await screen.findByText("+1.00 s")).toBeInTheDocument()
  })
})

/*
 * View preferences persist: Table/Plaintext, Format, Syntax, Wrap, sort
 * direction, UTC, Deltas and Collapse survive a revisit through one versioned
 * localStorage key. The unit tests for the hook cover corrupt state; these
 * pin that the viewer actually consumes it.
 */
describe("LogEventsViewer > persisted view preferences", () => {
  it("restores persisted toggles on mount", async () => {
    localStorage.setItem(
      "overcast.logs.viewPrefs.v1",
      JSON.stringify({ formatted: true, wrapLines: false, sortAsc: false }),
    )

    renderViewer()

    expect(await screen.findByRole("checkbox", { name: /format/i })).toBeChecked()
    expect(screen.getByRole("checkbox", { name: /wrap/i })).not.toBeChecked()
    // sortAsc false renders the toggle in its "Newest" state.
    expect(screen.getByRole("button", { name: /^newest$/i })).toBeInTheDocument()
  })

  it("writes a toggle change back to storage", async () => {
    const { user } = renderViewer()

    await user.click(await screen.findByRole("checkbox", { name: /format/i }))

    await waitFor(() => {
      const stored = localStorage.getItem("overcast.logs.viewPrefs.v1")
      expect(stored).not.toBeNull()
      expect(JSON.parse(stored!)).toMatchObject({ formatted: true })
    })
  })
})

/*
 * Collapsed single-line rows — the AWS console's default presentation and the
 * plan doc's §2c perf lever, here as an opt-in toggle (default OFF; flipping
 * the default is a later product decision). Every row renders as one truncated
 * line; clicking a row expands just that row to the full current rendering.
 */
describe("LogEventsViewer > collapsed rows", () => {
  const collapseToggle = () => screen.findByRole("checkbox", { name: /collapse/i })
  const rowOf = (text: string | RegExp) => {
    const el = screen.getByText(text)
    const row = el.closest("[data-index]")
    if (!row) throw new Error("row not found")
    return row
  }

  it("renders rows fully by default — collapse is opt-in", async () => {
    renderViewer()

    await screen.findByText("first message")
    expect(rowOf("first message").querySelector("pre")).not.toBeNull()
  })

  it("renders every row as one truncated line when Collapse is on", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await collapseToggle())

    // The wrapping <pre> is gone; a single-line truncating container remains.
    expect(rowOf("first message").querySelector("pre")).toBeNull()
    expect(rowOf("first message").querySelector(".truncate")).not.toBeNull()
  })

  it("keeps the level badge and tint on a collapsed row", async () => {
    const { user } = renderViewer(levelled([CONSOLE_WARN]))
    await screen.findByText("warn")

    await user.click(await collapseToggle())

    expect(screen.getByText("warn")).toBeInTheDocument()
  })

  it("still shows a platform record's summary as the collapsed text", async () => {
    const { user } = renderViewer(levelled([TIMED_OUT_REPORT]))
    await screen.findByText(/REPORT RequestId/)

    await user.click(await collapseToggle())

    expect(screen.getByText(/REPORT RequestId: 13aa488f/)).toBeInTheDocument()
  })

  it("expands just the clicked row to the full rendering, and collapses it again", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")
    await user.click(await collapseToggle())

    await user.click(screen.getByText("first message"))

    expect(rowOf("first message").querySelector("pre")).not.toBeNull()
    // The neighbour stays collapsed: expansion is per-row.
    expect(rowOf("second message").querySelector("pre")).toBeNull()

    await user.click(screen.getByText("first message"))

    expect(rowOf("first message").querySelector("pre")).toBeNull()
  })

  it("persists the collapse preference", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(await collapseToggle())

    await waitFor(() => {
      expect(JSON.parse(localStorage.getItem("overcast.logs.viewPrefs.v1")!)).toMatchObject({
        collapsed: true,
      })
    })
  })
})

/*
 * Export downloads the currently loaded events — exactly what the list holds,
 * in the displayed sort order. CSV mirrors the console's search-results shape;
 * the builders' escaping and field fidelity are unit-tested in
 * export-log-events.test.ts, so these cover the wiring.
 */
describe("LogEventsViewer > export", () => {
  /** jsdom's Blob has no .text(); FileReader is the lowest common denominator. */
  const blobText = (blob: Blob) =>
    new Promise<string>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.onerror = () => reject(new Error("blob read failed"))
      reader.readAsText(blob)
    })
  const captured: Blob[] = []
  const createObjectURL = vi.fn((blob: Blob) => {
    captured.push(blob)
    return "blob:mock-url"
  })
  const revokeObjectURL = vi.fn()

  beforeEach(() => {
    captured.length = 0
    createObjectURL.mockClear()
    revokeObjectURL.mockClear()
    // jsdom has no object URLs at all, so these are definitions, not spies.
    Object.assign(URL, { createObjectURL, revokeObjectURL })
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
  })

  it("downloads the loaded events as CSV in display order", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(screen.getByRole("button", { name: /export/i }))
    await user.click(await screen.findByRole("button", { name: /csv/i }))

    expect(createObjectURL).toHaveBeenCalledTimes(1)
    const text = await blobText(captured[0])
    expect(text).toBe(
      "timestamp,logStreamName,message\r\n" +
        "1970-01-01T00:00:01.000Z,s1,first message\r\n" +
        "1970-01-01T00:00:02.000Z,s1,second message\r\n",
    )
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:mock-url")
  })

  it("exports in the flipped order when the sort is Newest-first", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(screen.getByRole("button", { name: /oldest/i }))
    await user.click(screen.getByRole("button", { name: /export/i }))
    await user.click(await screen.findByRole("button", { name: /csv/i }))

    const text = await blobText(captured[0])
    const lines = text.trimEnd().split("\r\n")
    expect(lines[1]).toContain("second message")
    expect(lines[2]).toContain("first message")
  })

  it("offers a JSON export carrying the raw fields", async () => {
    const { user } = renderViewer()
    await screen.findByText("first message")

    await user.click(screen.getByRole("button", { name: /export/i }))
    await user.click(await screen.findByRole("button", { name: /json/i }))

    const parsed = JSON.parse(await blobText(captured[0])) as { message?: string }[]
    expect(parsed).toHaveLength(2)
    expect(parsed[0]).toEqual({
      timestamp: 1_000,
      ingestionTime: 1_000,
      logStreamName: "s1",
      message: "first message",
    })
  })

  it("is disabled with nothing loaded to export", async () => {
    renderViewer([])

    await clearButton()
    expect(screen.getByRole("button", { name: /export/i })).toBeDisabled()
  })
})
