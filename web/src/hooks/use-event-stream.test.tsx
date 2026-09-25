/**
 * Regression tests for the app-wide event stream subscription.
 *
 * The subscription runs in the app shell, with no component observing its
 * query while the user is anywhere other than the Events page. That makes
 * TanStack Query's garbage collector the thing standing between "capturing
 * in the background" and "capturing only once you look" — hence the
 * gcTime tests below, which use a QueryClient with library defaults rather
 * than the test factory's.
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, renderHook, act, screen } from "@testing-library/react"
import { DISCONNECTED, type WorkerMessage } from "@/workers/event-stream.protocol"
import type { StreamEvent } from "@/types"
import { athenaKeys } from "@/features/athena/data"
import { glueKeys } from "@/features/glue/data"
import { s3tablesKeys } from "@/features/s3tables/data"
import { topologyKey } from "@/features/map/use-topology"

const listeners: ((msg: WorkerMessage) => void)[] = []

vi.mock("@/workers/event-stream.client", () => ({
  subscribe: (_url: string, listener: (msg: WorkerMessage) => void) => {
    listeners.push(listener)
    return () => {
      const i = listeners.indexOf(listener)
      if (i >= 0) listeners.splice(i, 1)
    }
  },
  clear: vi.fn(),
}))

const { useEventStream, useEventStreamSubscription, eventStreamQueryOptions } =
  await import("./use-event-stream")

const FIVE_MINUTES = 5 * 60 * 1000

function ev(seconds: number, type = "s3:BucketCreated", source = "s3"): StreamEvent {
  return {
    type,
    time: new Date(Date.UTC(2026, 0, 1, 0, 0, seconds)).toISOString(),
    source,
    payload: { seq: seconds },
  }
}

/** The worker's opening message: its cached events, on a live connection. */
function init(events: StreamEvent[]): WorkerMessage {
  return {
    type: "init",
    events,
    state: { ...DISCONNECTED, connected: true, lastConnectedAt: Date.now() },
  }
}

/** Deliver a worker message to every active subscriber, inside act(). */
function emit(msg: WorkerMessage): void {
  act(() => {
    for (const l of [...listeners]) l(msg)
  })
}

function makeClient(): QueryClient {
  // Deliberately *not* createTestQueryClient(): the defaults are the subject.
  return new QueryClient()
}

function wrapperFor(client: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

function bufferedEvents(client: QueryClient): StreamEvent[] {
  return client.getQueryData<StreamEvent[]>(eventStreamQueryOptions().queryKey) ?? []
}

describe("useEventStreamSubscription", () => {
  beforeEach(() => {
    listeners.length = 0
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("buffers events that arrive while nothing is observing the stream", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1), ev(2)] })

    expect(bufferedEvents(client)).toHaveLength(2)
  })

  it("keeps buffered events past the default garbage-collection window", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1)] })

    act(() => {
      vi.advanceTimersByTime(FIVE_MINUTES + 1_000)
    })

    expect(bufferedEvents(client)).toHaveLength(1)
  })

  it("shows events captured before the Events page was ever opened", () => {
    const client = makeClient()

    function Shell() {
      useEventStreamSubscription()
      return null
    }
    function Console() {
      const { events } = useEventStream()
      return <div data-testid="count">{events.length}</div>
    }

    const view = render(<Shell />, { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1), ev(2), ev(3)] })

    // The user leaves the UI open on some other page long enough for an
    // unobserved query to be collected, then opens the Events page.
    act(() => {
      vi.advanceTimersByTime(FIVE_MINUTES + 1_000)
    })
    view.rerender(
      <>
        <Shell />
        <Console />
      </>,
    )

    expect(screen.getByTestId("count")).toHaveTextContent("3")
  })

  it("replays the worker's history snapshot on init", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([ev(1), ev(2)]))

    expect(bufferedEvents(client)).toHaveLength(2)
  })

  it("orders a batch by event time and drops nothing", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([ev(2)]))
    emit({ type: "events", events: [ev(1), ev(3)] })

    expect(bufferedEvents(client).map((e) => (e.payload as { seq: number }).seq)).toEqual([1, 2, 3])
  })

  it("evicts request telemetry before state changes when the buffer is full", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    const noise = Array.from({ length: 10_000 }, (_, i) => ev(i, "request:Received", "request"))
    emit(init([ev(0)]))
    emit({ type: "events", events: noise })

    const buffered = bufferedEvents(client)
    expect(buffered).toHaveLength(10_000)
    expect(buffered.some((e) => e.type === "s3:BucketCreated")).toBe(true)
  })

  it("clears the buffer when the worker reports a clear", () => {
    const client = makeClient()
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([ev(1)]))
    emit({ type: "cleared" })

    expect(bufferedEvents(client)).toHaveLength(0)
  })

  it("invalidates a query key at most once per interval during a sustained burst", () => {
    const client = makeClient()
    const invalidate = vi.spyOn(client, "invalidateQueries")
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1)] })
    const leading = invalidate.mock.calls.length
    expect(leading).toBeGreaterThan(0)

    // A Lambda-style burst: the same event type in every 50 ms flush window.
    for (let i = 2; i <= 10; i++) {
      act(() => {
        vi.advanceTimersByTime(50)
      })
      emit({ type: "events", events: [ev(i)] })
    }
    expect(invalidate.mock.calls.length).toBe(leading)

    // The trailing call still lands, so the final state is never missed.
    act(() => {
      vi.advanceTimersByTime(2_000)
    })
    expect(invalidate.mock.calls.length).toBe(leading * 2)
  })

  it("keeps sparse events fresh — a quiet key invalidates immediately", () => {
    const client = makeClient()
    const invalidate = vi.spyOn(client, "invalidateQueries")
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1)] })
    const leading = invalidate.mock.calls.length

    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    const afterQuiet = invalidate.mock.calls.length

    emit({ type: "events", events: [ev(2)] })
    expect(invalidate.mock.calls.length).toBe(afterQuiet + leading)
  })
})

describe("useEventStreamSubscription > data-lake events", () => {
  beforeEach(() => {
    listeners.length = 0
  })

  // #2085: what each event the Athena, Glue and S3 Tables services publish
  // makes stale.
  it.each([
    ["athena:QueryStateChanged", "athena", [athenaKeys.executions()]],
    ["glue:TableChanged", "glue", [glueKeys.tables(), glueKeys.partitions()]],
    ["glue:PartitionsChanged", "glue", [glueKeys.partitions()]],
    ["s3tables:TableCreated", "s3tables", [s3tablesKeys.tables(), topologyKey]],
    ["s3tables:TableDeleted", "s3tables", [s3tablesKeys.tables(), topologyKey]],
    ["s3tables:TableRenamed", "s3tables", [s3tablesKeys.tables(), topologyKey]],
    ["s3tables:TableCommitted", "s3tables", [s3tablesKeys.tables()]],
  ])("invalidates what %s makes stale", (type, source, keys) => {
    const client = makeClient()
    const invalidate = vi.spyOn(client, "invalidateQueries")
    renderHook(() => useEventStreamSubscription(), { wrapper: wrapperFor(client) })

    emit(init([]))
    emit({ type: "events", events: [ev(1, type, source)] })

    expect(invalidate.mock.calls.map(([filters]) => filters?.queryKey)).toEqual(keys)
  })
})
