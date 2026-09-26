/**
 * use-event-animations — maps live SSE events onto edge/node animation state.
 *
 * Subscribes to the emulator's event stream and translates each event into:
 *   - a set of edge IDs that should glow + carry a particle
 *   - a per-node event count (shown as a badge)
 *
 * Animation state decays after GLOW_TTL ms so the UI returns to idle.
 *
 * Node IDs are region-qualified (`us-east-1::sqs::my-queue`). SSE events
 * don't carry region info, so sourceNode helpers return a service suffix
 * (e.g. `sqs::my-queue`) which is matched against actual topology nodes.
 */

import { useEffect, useRef, useReducer, useCallback, useMemo, useState } from "react"
import { useEventStream } from "@/hooks/use-event-stream"
import { EventType } from "@/services/event-types"
import type { StreamEvent, TopologyEdge, TopologyNode } from "@/types"
import {
  applyLakeEvent,
  EMPTY_OVERLAY,
  expireOverlay,
  runEdgeEffects,
  type DataLakeOverlay,
} from "./data-lake-overlay"

const GLOW_TTL = 1200 // ms — how long an edge glows after an event

/**
 * Burst counters on edges drain by 1 every BURST_DRAIN_INTERVAL ms until
 * they reach zero so rapid-fire events accumulate as a visible number and
 * then slowly disappear rather than vanishing in <1 s.
 */
const BURST_DRAIN_INTERVAL = 2_000 // ms per decrement

export interface AnimationState {
  /** Set of edge IDs currently glowing */
  glowingEdges: Set<string>
  /** node-id → total event count since page load (badge) */
  nodeCounts: Record<string, number>
  /** node-id → write-event count since page load (triggers inbound flash) */
  nodeWriteCounts: Record<string, number>
  /**
   * edge-id → current burst count.
   * Counts increment on every event and drain by 1 every BURST_DRAIN_INTERVAL ms.
   * Used to render a "× N" badge on edges so bursts of fast events are visible
   * to the developer even after the glow fades.
   */
  edgeBurstCounts: Record<string, number>
  /**
   * node-id → current write-burst count (S3 PutObject, DynamoDB PutItem/UpdateItem).
   * Drains by 1 every BURST_DRAIN_INTERVAL ms — lets the developer see write
   * volumes on storage nodes even when writes happen faster than the eye can follow.
   */
  nodeWriteBurstCounts: Record<string, number>
  /** Athena, Glue and S3 Tables row overlays: see data-lake-overlay.ts. */
  dataLake: DataLakeOverlay
}

// ─── Event descriptor registry ─────────────────────────────────────────────

type Payload = Record<string, unknown>

/** Extracts a string field from a payload object, returning undefined if absent. */
function getString(p: Payload | undefined, key: string): string | undefined {
  return p?.[key] as string | undefined
}

/**
 * Describes how a single SSE event type maps to animation behaviour.
 * All four formerly-separate functions (edgesForEvent, sourceNodeForEvent,
 * isWriteEvent, isEdgeDependentEvent) are co-located per event type.
 *
 * `sourceNode` returns a service-qualified suffix (e.g. `sqs::my-queue`)
 * because SSE events don't include region. The caller resolves this to a
 * full region-qualified node ID via the topology node list.
 */
interface EventDescriptor {
  /** Extract the service-qualified node suffix from the event payload (e.g. `s3::my-bucket`). */
  sourceNode?: (p: Payload) => string | null
  /** Find matching edge IDs for this event. */
  matchEdges?: (p: Payload, edges: TopologyEdge[]) => string[]
  /** Whether this event represents data being written into a resource. */
  isWrite?: boolean
  /** Whether edge matching depends on topology edges being loaded (enables retry). */
  edgeDependent?: boolean
}

// ── Shared descriptor helpers ───────────────────────────────────────────────

/** Build a sourceNode extractor: reads `field` from payload, prefixes with `service::`. */
function fieldSourceNode(service: string, field: string) {
  return (p: Payload): string | null => {
    const v = getString(p, field)
    return v ? `${service}::${v}` : null
  }
}

const s3SourceNode = fieldSourceNode("s3", "Bucket")
const dynamodbTableSource = fieldSourceNode("dynamodb", "table")
const snsTopicSource = fieldSourceNode("sns", "topicName")
const logsGroupSource = fieldSourceNode("logs", "logGroupName")

function esmFilterNode(p: Payload): string | null {
  const esmId = getString(p, "esmId")
  return esmId ? `esm-filter::${esmId}` : null
}

function esmFilterEdges(p: Payload, edges: TopologyEdge[], direction: "in" | "out"): string[] {
  const esmId = getString(p, "esmId")
  if (!esmId) return []
  const prefix = direction === "in" ? "esm-filter-in::" : "esm-filter-out::"
  return edges.filter((e) => e.id === `${prefix}${esmId}`).map((e) => e.id)
}

function directEsmEdges(p: Payload, edges: TopologyEdge[]): string[] {
  const source = getString(p, "eventSource")
  const fn = getString(p, "functionName")
  const sourceType = getString(p, "sourceType")
  if (!source || !fn || !sourceType) return []
  const srcSuffix = sourceType === "sqs" ? `::sqs::${source}` : `::dynamodb::${source}`
  const tgtSuffix = `::lambda::${fn}`
  return edges
    .filter((e) => e.type === "esm" && e.source.endsWith(srcSuffix) && e.target.endsWith(tgtSuffix))
    .map((e) => e.id)
}

function s3NotificationEdges(p: Payload, edges: TopologyEdge[]): string[] {
  const bucket = getString(p, "Bucket")
  if (!bucket) return []
  const suffix = `::s3::${bucket}`
  return edges
    .filter((e) => e.source.endsWith(suffix) && e.type === "notification")
    .map((e) => e.id)
}

const EVENT_REGISTRY: Record<string, EventDescriptor | undefined> = {
  [EventType.s3.ObjectCreated]: {
    sourceNode: s3SourceNode,
    matchEdges: s3NotificationEdges,
    isWrite: true,
    edgeDependent: true,
  },
  [EventType.s3.ObjectRemoved]: {
    sourceNode: s3SourceNode,
    matchEdges: s3NotificationEdges,
    edgeDependent: true,
  },
  [EventType.dynamodb.Insert]: { sourceNode: dynamodbTableSource, isWrite: true },
  [EventType.dynamodb.Modify]: { sourceNode: dynamodbTableSource, isWrite: true },
  [EventType.dynamodb.Remove]: { sourceNode: dynamodbTableSource },
  [EventType.lambda.ESMRecordFiltered]: {
    sourceNode: esmFilterNode,
    matchEdges: (p, edges) => esmFilterEdges(p, edges, "in"),
    edgeDependent: true,
  },
  [EventType.lambda.ESMRecordMatched]: {
    sourceNode: esmFilterNode,
    matchEdges: (p, edges) => esmFilterEdges(p, edges, "in"),
    edgeDependent: true,
  },
  [EventType.lambda.ESMInvoked]: {
    sourceNode: (p) => {
      const esmId = getString(p, "esmId")
      return esmId ? `esm-filter::${esmId}` : null
    },
    matchEdges: (p, edges) => {
      const filtered = esmFilterEdges(p, edges, "out")
      return filtered.length > 0 ? filtered : directEsmEdges(p, edges)
    },
    edgeDependent: true,
  },
  [EventType.sns.Notification]: {
    sourceNode: snsTopicSource,
    matchEdges: (p, edges) => {
      const topic = getString(p, "topicName")
      const queue = getString(p, "queueName")
      if (!topic) return []
      const srcSuffix = `::sns::${topic}`
      const tgtSuffix = queue ? `::sqs::${queue}` : null
      return edges
        .filter(
          (e) =>
            e.source.endsWith(srcSuffix) &&
            (tgtSuffix ? e.target.endsWith(tgtSuffix) : true) &&
            e.type === "subscription",
        )
        .map((e) => e.id)
    },
    edgeDependent: true,
  },
  [EventType.sns.Published]: { sourceNode: snsTopicSource, isWrite: true },
  [EventType.sqs.MessageSent]: {
    sourceNode: (p) => {
      const q = getString(p, "queueName")
      return q ? `sqs::${q}` : null
    },
    isWrite: true,
  },
  [EventType.sqs.MessageDLQ]: {
    sourceNode: (p) => {
      const q = getString(p, "sourceQueue")
      return q ? `sqs::${q}` : null
    },
    matchEdges: (p, edges) => {
      const src = getString(p, "sourceQueue")
      const dlq = getString(p, "dlqQueue")
      if (!src || !dlq) return []
      const srcSuffix = `::sqs::${src}`
      const tgtSuffix = `::sqs::${dlq}`
      return edges
        .filter(
          (e) => e.source.endsWith(srcSuffix) && e.target.endsWith(tgtSuffix) && e.type === "dlq",
        )
        .map((e) => e.id)
    },
    edgeDependent: true,
  },
  [EventType.pipes.Delivered]: {
    sourceNode: fieldSourceNode("dynamodb", "sourceTable"),
    matchEdges: (p, edges) => {
      const pipeName = getString(p, "pipeName")
      if (!pipeName) return []
      const suffix = `::${pipeName}`
      return edges.filter((e) => e.type === "pipe" && e.id.endsWith(suffix)).map((e) => e.id)
    },
    edgeDependent: true,
  },
  [EventType.logs.LogGroupCreated]: { sourceNode: logsGroupSource },
  [EventType.logs.LogGroupDeleted]: { sourceNode: logsGroupSource },
  [EventType.logs.LogStreamCreated]: { sourceNode: logsGroupSource },
  [EventType.logs.LogStreamDeleted]: { sourceNode: logsGroupSource },
  [EventType.ecr.RepositoryCreated]: { sourceNode: fieldSourceNode("ecr", "name") },
  [EventType.ecr.RepositoryDeleted]: { sourceNode: fieldSourceNode("ecr", "name") },
  [EventType.ecr.ImagePushed]: { sourceNode: fieldSourceNode("ecr", "name"), isWrite: true },
}

/** Look up edge IDs that should animate for a given SSE event. */
function edgesForEvent(eventType: string, payload: unknown, edges: TopologyEdge[]): string[] {
  const desc = EVENT_REGISTRY[eventType]
  return desc?.matchEdges?.(payload as Payload, edges) ?? []
}

/**
 * Returns the service-qualified suffix for the source node (e.g. `sqs::queue`).
 * The caller must resolve this to a full node ID via resolveNodeId().
 */
function sourceNodeSuffix(eventType: string, payload: unknown): string | null {
  const desc = EVENT_REGISTRY[eventType]
  return desc?.sourceNode?.(payload as Payload) ?? null
}

/**
 * Resolve a service suffix like `sqs::my-queue` to the full region-qualified
 * node ID (e.g. `us-east-1::sqs::my-queue`) using the current topology nodes.
 * Returns the first match, or null if no node matches.
 */
function resolveNodeId(suffix: string, nodes: TopologyNode[]): string | null {
  const fullSuffix = `::${suffix}`
  for (const n of nodes) {
    if (n.id.endsWith(fullSuffix)) return n.id
  }
  return null
}

/** Events that represent data being written into a resource (not just routed through). */
function isWriteEvent(eventType: string): boolean {
  return EVENT_REGISTRY[eventType]?.isWrite === true
}

/**
 * Events whose edge match depends on topology edges being loaded.
 * If no edge is found on first delivery, the event is queued for retry
 * whenever edges update (e.g. topology refetch completing).
 */
function isEdgeDependentEvent(eventType: string): boolean {
  return EVENT_REGISTRY[eventType]?.edgeDependent === true
}

/** Payload + expiry for a pending edge-match retry. */
interface PendingRetry {
  event: StreamEvent
  deadline: number
}

const RETRY_TTL = 10_000 // ms — give up retrying unmatched edge events after 10s

interface AnimState {
  glowingEdges: Set<string>
  nodeCounts: Record<string, number>
  nodeWriteCounts: Record<string, number>
  edgeBurstCounts: Record<string, number>
  nodeWriteBurstCounts: Record<string, number>
}

type AnimAction =
  | { type: "glow"; edgeIds: string[] }
  | { type: "fade"; edgeIds: string[] }
  | { type: "incNode"; nodeId: string }
  | { type: "incNodeWrite"; nodeId: string }
  | { type: "incEdgeBurst"; edgeIds: string[] }
  | { type: "drainBursts" }
  | { type: "reset" }

function animReducer(state: AnimState, action: AnimAction): AnimState {
  switch (action.type) {
    case "glow": {
      const next = new Set(state.glowingEdges)
      for (const id of action.edgeIds) next.add(id)
      return { ...state, glowingEdges: next }
    }
    case "fade": {
      const next = new Set(state.glowingEdges)
      for (const id of action.edgeIds) next.delete(id)
      return { ...state, glowingEdges: next }
    }
    case "incNode":
      return {
        ...state,
        nodeCounts: {
          ...state.nodeCounts,
          [action.nodeId]: (state.nodeCounts[action.nodeId] ?? 0) + 1,
        },
      }
    case "incNodeWrite":
      return {
        ...state,
        nodeWriteCounts: {
          ...state.nodeWriteCounts,
          [action.nodeId]: (state.nodeWriteCounts[action.nodeId] ?? 0) + 1,
        },
        nodeWriteBurstCounts: {
          ...state.nodeWriteBurstCounts,
          [action.nodeId]: (state.nodeWriteBurstCounts[action.nodeId] ?? 0) + 1,
        },
      }
    case "incEdgeBurst": {
      const next = { ...state.edgeBurstCounts }
      for (const id of action.edgeIds) next[id] = (next[id] ?? 0) + 1
      return { ...state, edgeBurstCounts: next }
    }
    case "drainBursts":
      return {
        ...state,
        edgeBurstCounts: drainCounts(state.edgeBurstCounts),
        nodeWriteBurstCounts: drainCounts(state.nodeWriteBurstCounts),
      }
    case "reset":
      return { ...state, nodeCounts: {}, nodeWriteCounts: {} }
  }
}

export function useEventAnimations(nodes: TopologyNode[], edges: TopologyEdge[]): AnimationState {
  const { events } = useEventStream()
  const [state, dispatch] = useReducer(animReducer, {
    glowingEdges: new Set<string>(),
    nodeCounts: {},
    nodeWriteCounts: {},
    edgeBurstCounts: {},
    nodeWriteBurstCounts: {},
  })

  // Use a ref to hold pending timers so we can clean up on unmount
  const timers = useRef<Set<ReturnType<typeof setTimeout>>>(new Set())

  // seenIds avoids re-processing the same event on re-render
  const seenIds = useRef(new Set<string>())

  // pendingRetries holds edge-dependent events that arrived but found no matching
  // edge (e.g. topology refetch hadn't completed yet). Retried on each edges change.
  const pendingRetries = useRef(new Map<string, PendingRetry>())

  const glow = useCallback((edgeIds: string[]) => {
    if (edgeIds.length === 0) return
    dispatch({ type: "glow", edgeIds })
    const t = setTimeout(() => {
      dispatch({ type: "fade", edgeIds })
      timers.current.delete(t)
    }, GLOW_TTL)
    timers.current.add(t)
  }, [])

  useEffect(() => {
    const pendingTimers = timers.current
    return () => {
      for (const t of pendingTimers) clearTimeout(t)
    }
  }, [])

  useEffect(() => {
    if (events.length === 0) return
    const latest = events[events.length - 1]
    // Deduplicate by time+type+index. The array index breaks ties when two events
    // of the same type arrive in the same millisecond, ensuring neither is dropped.
    const key = `${latest.time}::${latest.type}::${events.length - 1}`
    if (seenIds.current.has(key)) {
      // Already processed from the events array — but still retry pending edge events
      // below in case edges have since updated.
    } else {
      seenIds.current.add(key)

      // Trim seen set to avoid unbounded growth
      if (seenIds.current.size > 5000) {
        const arr = [...seenIds.current]
        seenIds.current = new Set(arr.slice(arr.length - 2500))
      }

      const matchedEdges = edgesForEvent(latest.type, latest.payload, edges)
      glow(matchedEdges)

      // Increment burst counter on every matched edge
      if (matchedEdges.length > 0) {
        dispatch({ type: "incEdgeBurst", edgeIds: matchedEdges })
      }

      // If this is an edge-dependent event and no edge matched yet, queue for retry
      // so the animation fires once the topology refetch completes.
      if (isEdgeDependentEvent(latest.type) && matchedEdges.length === 0) {
        pendingRetries.current.set(key, { event: latest, deadline: Date.now() + RETRY_TTL })
      }

      const suffix = sourceNodeSuffix(latest.type, latest.payload)
      const nodeId = suffix ? resolveNodeId(suffix, nodes) : null
      if (nodeId) {
        dispatch({ type: "incNode", nodeId })
        if (isWriteEvent(latest.type)) {
          dispatch({ type: "incNodeWrite", nodeId })
        }
      }
    }

    // Retry any pending edge events with the current (possibly updated) edges.
    // This handles the race where pipes:Delivered arrived before the topology
    // refetch completed.
    const now = Date.now()
    for (const [k, pending] of pendingRetries.current) {
      if (now > pending.deadline) {
        pendingRetries.current.delete(k)
        continue
      }
      const retryEdges = edgesForEvent(pending.event.type, pending.event.payload, edges)
      if (retryEdges.length > 0) {
        glow(retryEdges)
        dispatch({ type: "incEdgeBurst", edgeIds: retryEdges })
        pendingRetries.current.delete(k)
      }
    }
    // Intentionally depends on the last event only, not the full array
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events.length, edges, glow])

  // Reset counts when node list changes (topology refresh with new resources)
  useEffect(() => {
    dispatch({ type: "reset" })
    seenIds.current.clear()
    pendingRetries.current.clear()
  }, [nodes.length])

  // Drain burst counts (edges + node writes) by 1 every BURST_DRAIN_INTERVAL ms
  useEffect(() => {
    const id = setInterval(() => {
      dispatch({ type: "drainBursts" })
    }, BURST_DRAIN_INTERVAL)
    return () => clearInterval(id)
  }, [])

  const { overlay: dataLake, heldEdges } = useDataLakeEvents(edges, glow)
  const glowingEdges = useMemo(
    () =>
      heldEdges.size === 0 ? state.glowingEdges : new Set([...state.glowingEdges, ...heldEdges]),
    [state.glowingEdges, heldEdges],
  )
  return { ...state, glowingEdges, dataLake }
}

// ─── Data-lake overlay ─────────────────────────────────────────────────────

/** How often expired ghosts, bursts and finished queries are swept (ms). */
const LAKE_SWEEP_INTERVAL = 1_000

const LAKE_SOURCES = ["athena", "glue", "s3tables"]

type LakeAction =
  { type: "events"; events: StreamEvent[]; now: number } | { type: "expire"; now: number }

function lakeReducer(o: DataLakeOverlay, action: LakeAction): DataLakeOverlay {
  if (action.type === "expire") return expireOverlay(o, action.now)
  return action.events.reduce((acc, ev) => applyLakeEvent(acc, ev, action.now), o)
}

/**
 * How far behind the newest of a batch of unseen events an event may be and
 * still be live. A live batch spans a moment; the history a reconnect loads
 * spans much longer, and only its last moment is replayed. Measured between
 * the events' own timestamps, so a server clock that differs from the
 * browser's does not matter.
 */
const LAKE_LIVE_SPAN = 10_000

/** The events of `batch` within `LAKE_LIVE_SPAN` of its newest. */
function recent(batch: StreamEvent[]): StreamEvent[] {
  const newest = Math.max(...batch.map((ev) => Date.parse(ev.time)))
  return batch.filter((ev) => newest - Date.parse(ev.time) <= LAKE_LIVE_SPAN)
}

/** A running query's glowing `queries` edges, and since when. */
interface Hold {
  edgeIds: string[]
  since: number
}

/** A hold whose query never reported finishing is let go after this long (ms). */
const MAX_HOLD = 10 * 60_000

const NO_EDGES: ReadonlySet<string> = new Set()

/**
 * The data-lake overlay, fed by every Athena, Glue and S3 Tables event rather
 * than only the latest: a fast query's QUEUED, RUNNING and SUCCEEDED can
 * arrive in one batch, and the row has to show all three, in order. Also
 * holds a running query's `queries` edge glowing until it finishes — at
 * least `GLOW_TTL`, so a query faster than that still visibly lights it.
 *
 * A query's first run against a database, or a workgroup's first result, has
 * no edge yet: the topology refetch that draws it lands after the event. Such
 * an event is tried again whenever the edges change, for `RETRY_TTL`.
 */
function useDataLakeEvents(edges: TopologyEdge[], glow: (edgeIds: string[]) => void) {
  const { events } = useEventStream({ source: LAKE_SOURCES })
  const [overlay, dispatch] = useReducer(lakeReducer, EMPTY_OVERLAY)
  const [heldEdges, setHeldEdges] = useState(NO_EDGES)
  const holds = useRef(new Map<string, Hold>())
  /** Queries that have finished, and when, so a late edge match only flashes. */
  const finished = useRef(new Map<string, number>())
  /** Events whose edges were not on the map yet, and until when to retry them. */
  const pending = useRef(new Map<StreamEvent, number>())
  // Null until the first render has seen what the buffer already held.
  const seen = useRef<WeakSet<StreamEvent> | null>(null)
  const edgesRef = useRef(edges)

  const syncHeld = useCallback(() => {
    const ids = [...holds.current.values()].flatMap((h) => h.edgeIds)
    setHeldEdges(ids.length === 0 ? NO_EDGES : new Set(ids))
  }, [])

  /** Applies an event's edge effects; false when the edges it needs are not drawn yet. */
  const applyEdges = useCallback(
    (ev: StreamEvent, now: number): boolean => {
      const effects = runEdgeEffects(ev, edgesRef.current)
      if (effects.pulse.length > 0) glow(effects.pulse)
      const id = (ev.payload as { queryExecutionId?: string } | undefined)?.queryExecutionId
      if (id && effects.hold.length > 0) {
        if (finished.current.has(id)) glow(effects.hold)
        else holds.current.set(id, { edgeIds: effects.hold, since: now })
      } else if (id && effects.release) {
        finished.current.set(id, now)
        const hold = holds.current.get(id)
        // Dilate only a hold too short to have been seen.
        if (hold && now - hold.since < GLOW_TTL) glow(hold.edgeIds)
        holds.current.delete(id)
      }
      return !effects.unmatched
    },
    [glow],
  )

  useEffect(() => {
    // What the buffer held when the map opened already happened.
    if (seen.current === null) {
      seen.current = new WeakSet(events)
      return
    }
    const known = seen.current
    const fresh = recent(events.filter((ev) => !known.has(ev)))
    for (const ev of events) known.add(ev)
    if (fresh.length === 0) return
    const now = Date.now()
    dispatch({ type: "events", events: fresh, now })
    for (const ev of fresh) {
      if (!applyEdges(ev, now)) pending.current.set(ev, now + RETRY_TTL)
    }
    syncHeld()
  }, [events, applyEdges, syncHeld])

  useEffect(() => {
    edgesRef.current = edges
    if (pending.current.size === 0) return
    const now = Date.now()
    for (const [ev, deadline] of pending.current) {
      if (now > deadline || applyEdges(ev, now)) pending.current.delete(ev)
    }
    syncHeld()
  }, [edges, applyEdges, syncHeld])

  useEffect(() => {
    const id = setInterval(() => {
      const now = Date.now()
      dispatch({ type: "expire", now })
      let dropped = false
      for (const [run, hold] of holds.current) {
        if (now - hold.since > MAX_HOLD) dropped = holds.current.delete(run)
      }
      for (const [run, at] of finished.current) {
        if (now - at > MAX_HOLD) finished.current.delete(run)
      }
      if (dropped) syncHeld()
    }, LAKE_SWEEP_INTERVAL)
    return () => clearInterval(id)
  }, [syncHeld])

  return { overlay, heldEdges }
}

/** Decrements every value by 1, dropping entries that reach zero. */
function drainCounts(prev: Record<string, number>): Record<string, number> {
  const entries = Object.entries(prev)
  if (entries.length === 0) return prev
  const next: Record<string, number> = {}
  for (const [k, v] of entries) {
    if (v > 1) next[k] = v - 1
  }
  return next
}
