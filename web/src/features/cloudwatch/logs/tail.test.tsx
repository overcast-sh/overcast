/**
 * Live-tail subscription teardown.
 *
 * `tailLogEvents` is driven by `useEffect`s whose cleanup aborts an
 * `AbortController`. Those cleanups run constantly — on every filter change,
 * every navigation, and (twice, on mount) under StrictMode — so an abort that
 * does not actually hang up leaves a StartLiveTail session running on the
 * emulator with nothing reading it.
 */

import { StrictMode } from "react"
import { QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { createTestQueryClient } from "@/test/render"
import { ToastContextProvider } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import type { LogStreamTarget } from "@/features/map/log-stream-peek"

// ─── StartLiveTail double ──────────────────────────────────────────────────

const live = vi.hoisted(() => {
  /**
   * A push-based stand-in for the SDK's event stream. `next()` parks until a
   * frame is pushed, which is what makes an un-torn-down session observable:
   * a real session sits idle between log events too.
   */
  class Session {
    private queue: unknown[] = []
    private waiter: ((v: IteratorResult<unknown>) => void) | null = null
    private rejecter: ((err: Error) => void) | null = null
    private error: Error | null = null
    /** Set when the consumer hangs up — the thing an abort is supposed to do. */
    closed = false

    push(message: string, streamName = "s") {
      this.deliver({
        sessionUpdate: {
          sessionResults: [
            {
              timestamp: 1_700_000_000_000,
              ingestionTime: 1_700_000_000_001,
              logStreamName: streamName,
              message,
            },
          ],
        },
      })
    }

    /** An empty sessionUpdate — the emulator's once-a-second heartbeat. */
    heartbeat() {
      this.deliver({ sessionUpdate: { sessionResults: [] } })
    }

    /** Kill the transport mid-stream, the way an emulator restart does. */
    fail(err: Error) {
      this.error = err
      if (this.waiter) {
        const reject = this.rejecter
        this.waiter = null
        this.rejecter = null
        reject?.(err)
      }
    }

    private deliver(frame: unknown) {
      if (this.waiter) {
        const resolve = this.waiter
        this.waiter = null
        this.rejecter = null
        resolve({ value: frame, done: false })
      } else {
        this.queue.push(frame)
      }
    }

    [Symbol.asyncIterator]() {
      return {
        next: (): Promise<IteratorResult<unknown>> => {
          if (this.error) return Promise.reject(this.error)
          if (this.queue.length) return Promise.resolve({ value: this.queue.shift(), done: false })
          if (this.closed) return Promise.resolve({ value: undefined, done: true })
          return new Promise((resolve, reject) => {
            this.waiter = resolve
            this.rejecter = reject
          })
        },
        return: (): Promise<IteratorResult<unknown>> => {
          this.closed = true
          if (this.waiter) {
            const resolve = this.waiter
            this.waiter = null
            this.rejecter = null
            resolve({ value: undefined, done: true })
          }
          return Promise.resolve({ value: undefined, done: true })
        },
      }
    }
  }

  const emptyPage = () => ({
    events: [] as Array<{ timestamp: number; message: string }>,
    nextBackwardToken: undefined as string | undefined,
    nextForwardToken: undefined as string | undefined,
  })

  return {
    Session,
    sessions: [] as InstanceType<typeof Session>[],
    /** Delay before `send` resolves, so a test can abort mid-request. */
    sendDelayMs: 0,
    /** Whether `send` rejects on an aborted signal, as the real SDK does. */
    rejectOnAbort: false,
    /** What GetLogEvents returns, keyed by the nextToken it was sent. */
    getLogEvents: (_token?: string) => emptyPage(),
    reset() {
      this.sessions = []
      this.sendDelayMs = 0
      this.rejectOnAbort = false
      this.getLogEvents = () => emptyPage()
    },
  }
})

vi.mock("@/services/aws-clients", () => ({
  awsClients: {
    logs: () => ({
      send: async (
        command: { constructor: { name: string }; input?: { nextToken?: string } },
        opts?: { abortSignal?: AbortSignal },
      ) => {
        if (command.constructor.name === "GetLogEventsCommand") {
          return live.getLogEvents(command.input?.nextToken)
        }
        if (live.sendDelayMs > 0) {
          await new Promise((r) => setTimeout(r, live.sendDelayMs))
        }
        if (live.rejectOnAbort && opts?.abortSignal?.aborted) {
          throw Object.assign(new Error("Request aborted"), { name: "AbortError" })
        }
        const session = new live.Session()
        live.sessions.push(session)
        return { responseStream: session }
      },
    }),
  },
}))

// jsdom gives every element a zero height, so the real virtualizer renders no
// rows at all and a live event would never be observable in the DOM.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 18,
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 18,
        end: index * 18 + 18,
      })),
    measureElement: vi.fn(),
    scrollToIndex: vi.fn(),
    scrollToOffset: vi.fn(),
    scrollOffset: 0,
  }),
}))

const { tailLogEvents, dropTailedDuplicates } = await import("@/features/cloudwatch/logs/tail")
const { LogStreamPeek } = await import("@/features/map/log-stream-peek")

// The peek panel pins itself to the newest line on every append; jsdom has no
// layout and so no `scrollTo`.
Element.prototype.scrollTo = () => {}

/**
 * The peek links to the full stream viewer, so it needs a router in the tree —
 * a memory router with the peek as its one route, built fresh per render —
 * and its rows carry a copy button, which needs the toast and tooltip
 * providers the app shell supplies.
 */
function peekRouter(target: LogStreamTarget) {
  const rootRoute = createRootRoute()
  return createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: "/",
        component: () => (
          <ToastContextProvider>
            <TooltipProvider>
              <LogStreamPeek target={target} onClose={() => {}} />
            </TooltipProvider>
          </ToastContextProvider>
        ),
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
}

/** Let every already-scheduled microtask and zero-delay timer run. */
async function settle(times = 4) {
  for (let i = 0; i < times; i++) await new Promise((r) => setTimeout(r, 0))
}

const openSessions = () => live.sessions.filter((s) => !s.closed).length

// ─── Tests ─────────────────────────────────────────────────────────────────

describe("tailLogEvents", () => {
  it("hangs up the session as soon as the subscription is aborted", async () => {
    live.reset()
    const controller = new AbortController()
    const generator = tailLogEvents({ groupIdentifier: "g", signal: controller.signal })

    const pending = generator.next()
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    controller.abort()
    await settle()

    // No further log event is ever going to arrive on this session — the
    // component that wanted it is gone — so nothing may be waiting on one.
    expect(openSessions()).toBe(0)
    await expect(pending).resolves.toEqual({ value: undefined, done: true })
  })

  it("treats an abort during StartLiveTail as a cancellation, not a failure", async () => {
    live.reset()
    live.sendDelayMs = 5
    live.rejectOnAbort = true

    const controller = new AbortController()
    const generator = tailLogEvents({ groupIdentifier: "g", signal: controller.signal })

    const pending = generator.next()
    controller.abort()

    // The abort races the request that opens the session. Losing that race is
    // the ordinary teardown path, so it must not surface as a rejection — an
    // effect body cannot catch it, and it lands as an unhandled rejection.
    await expect(pending).resolves.toEqual({ value: undefined, done: true })
  })

  it("stops yielding once aborted", async () => {
    live.reset()
    const controller = new AbortController()
    const generator = tailLogEvents({ groupIdentifier: "g", signal: controller.signal })

    const pending = generator.next()
    await waitFor(() => expect(live.sessions).toHaveLength(1))
    live.sessions[0].push("before")
    await expect(pending).resolves.toMatchObject({ value: { message: "before" } })

    controller.abort()
    live.sessions[0].push("after")
    await expect(generator.next()).resolves.toEqual({ value: undefined, done: true })
  })

  it("reports activity once per received frame, including empty heartbeats", async () => {
    live.reset()
    const controller = new AbortController()
    const onActivity = vi.fn()
    const generator = tailLogEvents({ groupIdentifier: "g", signal: controller.signal, onActivity })

    const pending = generator.next()
    await waitFor(() => expect(live.sessions).toHaveLength(1))

    // A heartbeat yields no events — the generator stays parked — but it is
    // proof of life, and the caller's staleness watchdog must hear about it.
    live.sessions[0].heartbeat()
    await waitFor(() => expect(onActivity).toHaveBeenCalledTimes(1))

    live.sessions[0].push("real event")
    await expect(pending).resolves.toMatchObject({ value: { message: "real event" } })
    expect(onActivity).toHaveBeenCalledTimes(2)

    controller.abort()
    await settle()
  })
})

describe("dropTailedDuplicates at the forward-page boundary", () => {
  it("cancels overlap by count, so a line genuinely logged twice still shows twice", () => {
    // The shape forward paging produces: a fetched page that covers events the
    // dead tail session already delivered.
    const stored = [
      { logStreamName: "s", timestamp: 1, message: "dup" },
      { logStreamName: "s", timestamp: 1, message: "dup" },
      { logStreamName: "s", timestamp: 2, message: "solo" },
    ]
    const tailed = [
      { logStreamName: "s", timestamp: 1, message: "dup", ingestionTime: 1 },
      { logStreamName: "s", timestamp: 1, message: "dup", ingestionTime: 1 },
      { logStreamName: "s", timestamp: 1, message: "dup", ingestionTime: 1 },
      { logStreamName: "s", timestamp: 3, message: "tail only", ingestionTime: 3 },
    ]

    // Two stored copies cancel two tailed copies; the third tailed copy is a
    // line the function really did log a third time, and survives.
    expect(dropTailedDuplicates(stored, tailed).map((e) => e.message)).toEqual(["dup", "tail only"])
  })
})

describe("LogStreamPeek live tail", () => {
  const target = {
    title: "fn",
    subtitle: "abc123",
    logGroup: "/aws/lambda/fn",
    logStream: "2026/08/10/[$LATEST]abc123",
  }

  it("leaves exactly one session running when StrictMode double-invokes the effect", async () => {
    live.reset()
    render(
      <StrictMode>
        <QueryClientProvider client={createTestQueryClient()}>
          <RouterProvider router={peekRouter(target)} />
        </QueryClientProvider>
      </StrictMode>,
    )

    await waitFor(() => expect(live.sessions.length).toBeGreaterThan(0))
    await settle()

    // StrictMode deliberately mounts, tears down, and remounts. The discarded
    // first subscription must be hung up by its own cleanup rather than left
    // reading the emulator alongside the surviving one.
    expect(openSessions()).toBe(1)
  })

  it("renders a live event once", async () => {
    live.reset()
    render(
      <StrictMode>
        <QueryClientProvider client={createTestQueryClient()}>
          <RouterProvider router={peekRouter(target)} />
        </QueryClientProvider>
      </StrictMode>,
    )

    await waitFor(() => expect(live.sessions.length).toBeGreaterThan(0))
    await settle()
    for (const session of live.sessions) session.push("hello from the function")
    await settle()

    expect(await screen.findAllByText("hello from the function")).toHaveLength(1)
  })

  it("pages forward to events written after the tail dies, without doubling the overlap", async () => {
    live.reset()
    const T = 1_700_000_000_000
    const tokens: Array<string | undefined> = []
    live.getLogEvents = (nextToken) => {
      tokens.push(nextToken)
      // First page: the newest stored events at open time.
      if (nextToken === undefined)
        return {
          events: [{ timestamp: T - 1_000, message: "before the tail opened" }],
          nextBackwardToken: "b1",
          nextForwardToken: "f1",
        }
      // Backward: nothing older (same backward token = end).
      if (nextToken === "b1") return { events: [], nextBackwardToken: "b1", nextForwardToken: "f1" }
      // Forward from f1: one event the dead tail already delivered, one that
      // only exists on disk because it was written after the death.
      if (nextToken === "f1")
        return {
          events: [
            { timestamp: T, message: "caught by the tail" },
            { timestamp: T + 1_000, message: "written after the death" },
          ],
          nextBackwardToken: "b1",
          nextForwardToken: "f2",
        }
      // Forward from f2: the same token back — AWS's canonical end signal.
      return { events: [], nextBackwardToken: "b1", nextForwardToken: "f2" }
    }

    // jsdom has no IntersectionObserver at all; the top (backward) sentinel
    // arms itself once a backward token exists, so it needs a stand-in. Inert
    // is enough — the forward walk under test is effect-driven, not observed.
    class InertObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal("IntersectionObserver", InertObserver)
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    try {
      render(
        <StrictMode>
          <QueryClientProvider client={createTestQueryClient()}>
            <RouterProvider router={peekRouter(target)} />
          </QueryClientProvider>
        </StrictMode>,
      )

      await waitFor(() => expect(live.sessions.length).toBeGreaterThan(0))
      await settle()
      expect(await screen.findAllByText("before the tail opened")).toHaveLength(1)

      // The tail delivers one event, then the transport dies mid-stream.
      for (const session of live.sessions) session.push("caught by the tail", target.logStream)
      await settle()
      expect(await screen.findAllByText("caught by the tail")).toHaveLength(1)
      for (const session of live.sessions) session.fail(new TypeError("network error"))

      // Forward paging walks f1 → f2 and stops when f2 comes back unchanged.
      expect(await screen.findAllByText("written after the death")).toHaveLength(1)
      expect(tokens).toContain("f1")
      // The probe of f2 (the end-of-stream round trip) may still be in flight
      // when the f1 events render — wait for it rather than racing it.
      await waitFor(() => expect(tokens).toContain("f2"))

      // The fetched page's overlap with the dead tail's buffer reconciles by
      // count — the event the tail caught is not doubled.
      expect(screen.getAllByText("caught by the tail")).toHaveLength(1)
    } finally {
      warn.mockRestore()
      vi.unstubAllGlobals()
    }
  })
})
