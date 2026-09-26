import { describe, expect, it } from "vitest"
import type { TopologyEdge } from "@/types"
import {
  applyLakeEvent,
  EMPTY_OVERLAY,
  expireOverlay,
  nodeSuffix,
  RECORD_BURST_TTL,
  ROW_GHOST_TTL,
  rowKey,
  RUN_DWELL,
  RUN_SETTLE,
  runDisplay,
  runEdgeEffects,
  type DataLakeOverlay,
} from "./data-lake-overlay"

const T0 = 1_000_000

function apply(o: DataLakeOverlay, type: string, payload: object, now = T0): DataLakeOverlay {
  return applyLakeEvent(o, { type, payload }, now)
}

describe("applyLakeEvent", () => {
  it("flashes a Glue table that was created or updated, and ghosts one that was deleted", () => {
    // Given: a table created, then updated, then another deleted
    let o = apply(EMPTY_OVERLAY, "glue:TableChanged", {
      database: "sales",
      table: "orders",
      change: "created",
    })
    o = apply(o, "glue:TableChanged", { database: "sales", table: "orders", change: "updated" })
    o = apply(o, "glue:TableChanged", { database: "sales", table: "old", change: "deleted" })

    // Then: the written row flashed twice, and the dropped one is a ghost on its node
    expect(o.rows[rowKey("glue::sales", "orders")]?.flashes).toBe(2)
    expect(o.ghosts["glue::sales"]).toEqual([{ name: "old", deletedAt: T0 }])
  })

  it("ticks a Glue table's partition count when its partitions change", () => {
    const o = apply(EMPTY_OVERLAY, "glue:PartitionsChanged", { database: "sales", table: "orders" })
    expect(o.rows[rowKey("glue::sales", "orders")]).toMatchObject({ ticks: 1, flashes: 0 })
  })

  it("flashes an S3 Tables commit and bursts the records it appended, restarting once the burst ends", () => {
    // Given: two commits in quick succession, then one after the burst has ended
    const commit = { bucket: "lake", namespace: "sales", name: "orders", addedRecords: 100 }
    let o = apply(EMPTY_OVERLAY, "s3tables:TableCommitted", commit)
    o = apply(o, "s3tables:TableCommitted", { ...commit, addedRecords: 50 }, T0 + 500)
    const key = rowKey("s3tables::lake", "sales.orders")
    expect(o.rows[key]).toMatchObject({ flashes: 2, records: 150 })

    o = apply(o, "s3tables:TableCommitted", commit, T0 + 500 + RECORD_BURST_TTL + 1)
    expect(o.rows[key]).toMatchObject({ flashes: 3, records: 100 })
  })

  it("ghosts a dropped S3 Tables table under its namespace", () => {
    const o = apply(EMPTY_OVERLAY, "s3tables:TableDeleted", {
      bucket: "lake",
      namespace: "sales",
      name: "orders",
    })
    expect(o.ghosts["s3tables::lake"]).toEqual([
      { name: "orders", namespace: "sales", deletedAt: T0 },
    ])
  })

  it("records each state a query reaches once, in order", () => {
    let o = EMPTY_OVERLAY
    for (const state of ["QUEUED", "RUNNING", "RUNNING", "SUCCEEDED"]) {
      o = apply(o, "athena:QueryStateChanged", { queryExecutionId: "q-1", state })
    }
    expect(o.runs["q-1"]?.map((s) => s.state)).toEqual(["QUEUED", "RUNNING", "SUCCEEDED"])
  })

  it("leaves the overlay alone for an event it has nothing to say about", () => {
    expect(apply(EMPTY_OVERLAY, "sqs:MessageSent", { queueName: "q" })).toBe(EMPTY_OVERLAY)
  })
})

describe("expireOverlay", () => {
  it("drops ghosts and bursts that have run their course, and nothing else", () => {
    // Given: a ghost and a burst
    let o = apply(EMPTY_OVERLAY, "s3tables:TableDeleted", {
      bucket: "lake",
      namespace: "n",
      name: "t",
    })
    o = apply(o, "s3tables:TableCommitted", {
      bucket: "lake",
      namespace: "n",
      name: "u",
      addedRecords: 5,
    })

    // Then: before either ends nothing changes — the same object comes back
    expect(expireOverlay(o, T0 + 1)).toBe(o)

    // And: once both have ended, both are gone but the row keeps its flash count
    const later = expireOverlay(o, T0 + Math.max(ROW_GHOST_TTL, RECORD_BURST_TTL))
    expect(later.ghosts).toEqual({})
    expect(later.rows[rowKey("s3tables::lake", "n.u")]).toMatchObject({ flashes: 1, records: 0 })
  })
})

describe("runDisplay", () => {
  const fast = [
    { state: "QUEUED", at: T0 },
    { state: "RUNNING", at: T0 + 2 },
    { state: "SUCCEEDED", at: T0 + 4 },
  ]

  it("keeps each state of a query too fast to see on screen for the dwell, in order", () => {
    expect(runDisplay(fast, "SUCCEEDED", T0 + 10)).toEqual({
      state: "QUEUED",
      fresh: false,
      nextChangeAt: T0 + RUN_DWELL,
    })
    expect(runDisplay(fast, "SUCCEEDED", T0 + RUN_DWELL).state).toBe("RUNNING")
    expect(runDisplay(fast, "SUCCEEDED", T0 + 2 * RUN_DWELL)).toMatchObject({
      state: "SUCCEEDED",
      fresh: true,
    })
  })

  it("does not slow a query that is already slow enough to see", () => {
    const slow = [
      { state: "RUNNING", at: T0 },
      { state: "SUCCEEDED", at: T0 + 5 * RUN_DWELL },
    ]
    expect(runDisplay(slow, "SUCCEEDED", T0 + 5 * RUN_DWELL).state).toBe("SUCCEEDED")
  })

  it("settles a finished query's outcome after it has been seen", () => {
    const done = runDisplay(fast, "SUCCEEDED", T0 + 2 * RUN_DWELL + RUN_SETTLE)
    expect(done).toEqual({ state: "SUCCEEDED", fresh: false })
  })

  it("shows a query that finished before the map opened as the topology has it, settled", () => {
    expect(runDisplay(undefined, "FAILED", T0)).toEqual({ state: "FAILED", fresh: false })
  })

  it("takes the topology's finished state when the page missed the last event", () => {
    const missed = [{ state: "RUNNING", at: T0 }]
    expect(runDisplay(missed, "CANCELLED", T0 + 10 * RUN_DWELL).state).toBe("CANCELLED")
  })
})

describe("runEdgeEffects", () => {
  const edges: TopologyEdge[] = [
    {
      id: "q-glue",
      source: "us-east-1::athena::wg",
      target: "us-east-1::glue::sales",
      type: "queries",
    },
    {
      id: "q-lake",
      source: "us-east-1::athena::wg",
      target: "us-east-1::s3tables::lake",
      type: "queries",
    },
    {
      id: "results",
      source: "us-east-1::athena::wg",
      target: "us-east-1::s3::out",
      type: "query-results",
    },
    {
      id: "other",
      source: "us-east-1::athena::other",
      target: "us-east-1::s3::out",
      type: "query-results",
    },
  ]
  const ev = (state: string, extra: object = {}) => ({
    type: "athena:QueryStateChanged",
    payload: { queryExecutionId: "q", workGroup: "wg", state, database: "sales", ...extra },
  })

  it("holds the queries edge to the database a running query reads", () => {
    expect(runEdgeEffects(ev("RUNNING"), edges).hold).toEqual(["q-glue"])
    expect(runEdgeEffects(ev("RUNNING", { catalog: "s3tablescatalog/lake" }), edges).hold).toEqual([
      "q-lake",
    ])
  })

  it("pulses the workgroup's query-results edge on success and releases the hold", () => {
    expect(runEdgeEffects(ev("SUCCEEDED"), edges)).toEqual({
      hold: [],
      pulse: ["results"],
      release: true,
      unmatched: false,
    })
    expect(runEdgeEffects(ev("FAILED"), edges)).toEqual({
      hold: [],
      pulse: [],
      release: true,
      unmatched: false,
    })
  })

  it("says when the edge a state needs is not on the map yet", () => {
    expect(runEdgeEffects(ev("RUNNING", { database: "new_db" }), edges)).toMatchObject({
      hold: [],
      unmatched: true,
    })
    const first = {
      type: "athena:QueryStateChanged",
      payload: { workGroup: "new", state: "SUCCEEDED" },
    }
    expect(runEdgeEffects(first, edges).unmatched).toBe(true)
  })

  it("does nothing for a queued query or another event", () => {
    expect(runEdgeEffects(ev("QUEUED"), edges)).toEqual({
      hold: [],
      pulse: [],
      release: false,
      unmatched: false,
    })
    expect(runEdgeEffects({ type: "glue:TableChanged", payload: {} }, edges).release).toBe(false)
  })
})

describe("nodeSuffix", () => {
  it("drops the region from a node ID", () => {
    expect(nodeSuffix("eu-west-1::s3tables::lake")).toBe("s3tables::lake")
  })
})
