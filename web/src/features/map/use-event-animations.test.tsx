import { act, renderHook } from "@/test/render"
import type { StreamEvent, TopologyEdge, TopologyNode } from "@/types"
import { rowKey } from "./data-lake-overlay"
import { useEventAnimations } from "./use-event-animations"

const stream = vi.hoisted(() => ({ events: [] as StreamEvent[] }))

vi.mock("@/hooks/use-event-stream", () => ({
  useEventStream: () => ({ events: stream.events }),
}))

const nodes: TopologyNode[] = []
const edges: TopologyEdge[] = [
  {
    id: "reads",
    source: "us-east-1::athena::wg",
    target: "us-east-1::glue::sales",
    type: "queries",
  },
  {
    id: "results",
    source: "us-east-1::athena::wg",
    target: "us-east-1::s3::out",
    type: "query-results",
  },
]

function event(type: string, source: string, payload: object): StreamEvent {
  return { type, source, time: new Date().toISOString(), payload }
}

function queryState(state: string): StreamEvent {
  return event("athena:QueryStateChanged", "athena", {
    queryExecutionId: "q-1",
    workGroup: "wg",
    state,
    database: "sales",
  })
}

describe("useEventAnimations > data-lake overlays", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    stream.events = []
  })
  afterEach(() => vi.useRealTimers())

  function push(...events: StreamEvent[]) {
    stream.events = [...stream.events, ...events]
  }

  it("glows the queries edge while a query runs, then pulses its results edge", () => {
    const { result, rerender } = renderHook(() => useEventAnimations(nodes, edges))

    // When: the query is queued and starts running, in one batch
    act(() => push(queryState("QUEUED"), queryState("RUNNING")))
    rerender()

    // Then: both states are recorded, and the edge to what it reads glows
    expect(result.current.dataLake.runs["q-1"]?.map((s) => s.state)).toEqual(["QUEUED", "RUNNING"])
    expect(result.current.glowingEdges.has("reads")).toBe(true)

    // When: it succeeds a moment later
    act(() => push(queryState("SUCCEEDED")))
    rerender()

    // Then: a pulse runs along the results edge, and the read edge — held too
    // briefly to have been seen — keeps glowing for the glow's length
    expect(result.current.glowingEdges.has("results")).toBe(true)
    expect(result.current.glowingEdges.has("reads")).toBe(true)
    act(() => {
      vi.advanceTimersByTime(1_300)
    })
    expect(result.current.glowingEdges.size).toBe(0)
  })

  it("lights a query's edges once the refetch that draws them lands", () => {
    // Given: a map that has no edges yet for the workgroup
    let current: TopologyEdge[] = []
    const { result, rerender } = renderHook(() => useEventAnimations(nodes, current))

    // When: its first query runs, and only then does the topology draw its edge
    act(() => push(queryState("RUNNING")))
    rerender()
    expect(result.current.glowingEdges.size).toBe(0)
    current = edges
    rerender()

    // Then: the edge to what it reads glows while it runs
    expect(result.current.glowingEdges.has("reads")).toBe(true)
  })

  it("flashes a committed table's row and does not replay history it loaded with", () => {
    // Given: a commit already in the buffer when the map opens
    push(
      event("s3tables:TableCommitted", "s3tables", {
        bucket: "lake",
        namespace: "sales",
        name: "old",
      }),
    )
    const { result, rerender } = renderHook(() => useEventAnimations(nodes, edges))
    act(() => {
      vi.advanceTimersByTime(20_000)
    })

    // When: a new commit arrives
    act(() =>
      push(
        event("s3tables:TableCommitted", "s3tables", {
          bucket: "lake",
          namespace: "sales",
          name: "orders",
          addedRecords: 40,
        }),
      ),
    )
    rerender()

    // Then: only the new one flashes, with its records
    const rows = result.current.dataLake.rows
    expect(rows[rowKey("s3tables::lake", "sales.orders")]).toMatchObject({
      flashes: 1,
      records: 40,
    })
    expect(rows[rowKey("s3tables::lake", "sales.old")]).toBeUndefined()
  })
})
