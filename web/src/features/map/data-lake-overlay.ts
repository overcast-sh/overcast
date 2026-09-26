/**
 * data-lake-overlay — the live overlay on Athena, Glue and S3 Tables nodes,
 * as one pure model: what an event does to the rows it names, and what the
 * rows then show.
 *
 * One visual-state model for every data-lake row (CONTRIBUTING § Topology map
 * methodology):
 *
 *   - a *write flash* when a table is written — created, committed to, or
 *     updated in the catalog — with a burst of the records a commit appended;
 *   - a *tick* on a Glue table's partition count when its partitions change;
 *   - a *ghost* row, struck through and fading, for a table that was dropped;
 *   - a query's states, each on screen for at least `RUN_DWELL` — a dwell only
 *     for a transition too fast to see; a query that runs for longer shows
 *     each state for as long as it really lasted.
 *
 * Timing is display time (when the event reached the page), never the
 * event's own timestamp: this is observability, not a replay.
 */

import { isFinished } from "@/features/athena/execution-state"
import { EventType } from "@/services/event-types"
import type { TopologyEdge } from "@/types"

/** How long a dropped table stays on its node as a ghost (ms). */
export const ROW_GHOST_TTL = 8_000
/** How long the appended-records burst stays after the last commit (ms). */
export const RECORD_BURST_TTL = 4_000
/** The shortest time a query state is on screen (ms). */
export const RUN_DWELL = 1_000
/** How long a finished query shows its outcome before it settles to a ghost (ms). */
export const RUN_SETTLE = 4_000
/** How long a query's observed states are kept after it finished (ms). */
const RUN_MEMORY = 60_000
/** How long a query that never reported finishing is kept (ms). */
const RUN_STALE = 10 * 60_000

// ─── Keys ────────────────────────────────────────────────────────────────

/**
 * A node's key without its region — `glue::sales`, `s3tables::lake` —
 * since events carry no region. Resolved against the nodes on the map the
 * same way the other overlays are.
 */
export type NodeSuffix = string

/** A row's key: its node's suffix and its name there. */
export type RowKey = string

export function rowKey(node: NodeSuffix, row: string): RowKey {
  return `${node}#${row}`
}

/** The name a table goes by on its node: `namespace.table` in S3 Tables, `table` in Glue. */
export function tableRowName(table: { name: string; namespace?: string }): string {
  return table.namespace ? `${table.namespace}.${table.name}` : table.name
}

/** A node's suffix from its full ID, `us-east-1::glue::sales` → `glue::sales`. */
export function nodeSuffix(nodeId: string): NodeSuffix {
  return nodeId.slice(nodeId.indexOf("::") + 2)
}

// ─── State ───────────────────────────────────────────────────────────────

export interface RowOverlay {
  /** Write flashes so far: a new value restarts the flash. */
  flashes: number
  /** Partition-count ticks so far: a new value restarts the tick. */
  ticks: number
  /** Records appended by the commits of the current burst. */
  records: number
  /** When the burst ends. */
  recordsUntil: number
}

/** A dropped table, shown as a ghost on its node until `ROW_GHOST_TTL` has passed. */
export interface GhostRow {
  name: string
  namespace?: string
  deletedAt: number
}

/** One state a query reached, and when the page learned of it. */
export interface RunStep {
  state: string
  at: number
}

export interface DataLakeOverlay {
  rows: Record<RowKey, RowOverlay>
  ghosts: Record<NodeSuffix, GhostRow[]>
  /** Execution id → the states it reached while the map was open. */
  runs: Record<string, RunStep[]>
}

export const EMPTY_OVERLAY: DataLakeOverlay = { rows: {}, ghosts: {}, runs: {} }

// ─── Events ──────────────────────────────────────────────────────────────

interface LakeEvent {
  type: string
  payload?: unknown
}

type Payload = Record<string, unknown>

function text(p: Payload, key: string): string {
  const v = p[key]
  return typeof v === "string" ? v : ""
}

function updateRow(
  o: DataLakeOverlay,
  key: RowKey,
  change: (row: RowOverlay) => Partial<RowOverlay>,
): DataLakeOverlay {
  const row = o.rows[key] ?? { flashes: 0, ticks: 0, records: 0, recordsUntil: 0 }
  return { ...o, rows: { ...o.rows, [key]: { ...row, ...change(row) } } }
}

function flash(o: DataLakeOverlay, key: RowKey): DataLakeOverlay {
  return updateRow(o, key, (r) => ({ flashes: r.flashes + 1 }))
}

function ghost(o: DataLakeOverlay, node: NodeSuffix, row: GhostRow): DataLakeOverlay {
  const kept = (o.ghosts[node] ?? []).filter((g) => tableRowName(g) !== tableRowName(row))
  return { ...o, ghosts: { ...o.ghosts, [node]: [...kept, row] } }
}

function addStep(o: DataLakeOverlay, id: string, state: string, now: number): DataLakeOverlay {
  const steps = o.runs[id] ?? []
  if (steps.some((s) => s.state === state)) return o
  return { ...o, runs: { ...o.runs, [id]: [...steps, { state, at: now }] } }
}

/** Folds one event into the overlay; an event it has nothing to say about returns `o`. */
export function applyLakeEvent(o: DataLakeOverlay, ev: LakeEvent, now: number): DataLakeOverlay {
  const p = (ev.payload ?? {}) as Payload
  switch (ev.type) {
    case EventType.glue.TableChanged: {
      const node = `glue::${text(p, "database")}`
      const table = text(p, "table")
      return text(p, "change") === "deleted"
        ? ghost(o, node, { name: table, deletedAt: now })
        : flash(o, rowKey(node, table))
    }
    case EventType.glue.PartitionsChanged:
      return updateRow(o, rowKey(`glue::${text(p, "database")}`, text(p, "table")), (r) => ({
        ticks: r.ticks + 1,
      }))
    case EventType.s3tables.TableCreated:
    case EventType.s3tables.TableRenamed:
      return flash(o, s3TablesRow(p))
    case EventType.s3tables.TableDeleted:
      return ghost(o, `s3tables::${text(p, "bucket")}`, {
        name: text(p, "name"),
        namespace: text(p, "namespace"),
        deletedAt: now,
      })
    case EventType.s3tables.TableCommitted: {
      const added = typeof p.addedRecords === "number" ? p.addedRecords : 0
      return updateRow(o, s3TablesRow(p), (r) => ({
        flashes: r.flashes + 1,
        records: (r.recordsUntil > now ? r.records : 0) + added,
        recordsUntil: now + RECORD_BURST_TTL,
      }))
    }
    case EventType.athena.QueryStateChanged: {
      const id = text(p, "queryExecutionId")
      return id ? addStep(o, id, text(p, "state"), now) : o
    }
    default:
      return o
  }
}

function s3TablesRow(p: Payload): RowKey {
  return rowKey(
    `s3tables::${text(p, "bucket")}`,
    tableRowName({ name: text(p, "name"), namespace: text(p, "namespace") }),
  )
}

/**
 * Drops what has run its course: ghosts past their TTL, finished bursts, and
 * queries long finished. Returns `o` itself when nothing expired, so a tick
 * with nothing to do re-renders nothing.
 */
export function expireOverlay(o: DataLakeOverlay, now: number): DataLakeOverlay {
  let changed = false
  const ghosts: Record<NodeSuffix, GhostRow[]> = {}
  for (const [node, rows] of Object.entries(o.ghosts)) {
    const kept = rows.filter((g) => now - g.deletedAt < ROW_GHOST_TTL)
    if (kept.length !== rows.length) changed = true
    if (kept.length > 0) ghosts[node] = kept
  }
  const rows: Record<RowKey, RowOverlay> = {}
  for (const [key, row] of Object.entries(o.rows)) {
    if (row.records > 0 && row.recordsUntil <= now) {
      rows[key] = { ...row, records: 0 }
      changed = true
    } else {
      rows[key] = row
    }
  }
  const runs: Record<string, RunStep[]> = {}
  for (const [id, steps] of Object.entries(o.runs)) {
    const last = steps[steps.length - 1]
    if (now - last.at > (isFinished(last.state) ? RUN_MEMORY : RUN_STALE)) changed = true
    else runs[id] = steps
  }
  return changed ? { rows, ghosts, runs } : o
}

// ─── Query display ───────────────────────────────────────────────────────

export interface RunDisplay {
  /** The state to show now. */
  state: string
  /** A finished query still showing its outcome, before it settles. */
  fresh: boolean
  /** When the display next changes on its own, if it does. */
  nextChangeAt?: number
}

/**
 * What a query row shows now. With the states the page saw it reach, each is
 * shown from when it arrived but no sooner than `RUN_DWELL` after the one
 * before — so a query that goes QUEUED → RUNNING → SUCCEEDED in a
 * millisecond still visibly runs, in order, while a slow one is shown as it
 * really went. Without them (the query finished before the map opened), the
 * topology's state stands, already settled.
 */
export function runDisplay(
  steps: readonly RunStep[] | undefined,
  topologyState: string,
  now: number,
): RunDisplay {
  if (!steps || steps.length === 0) return { state: topologyState, fresh: false }
  let shownAt = steps[0].at
  let current = { state: steps[0].state, since: shownAt }
  for (let i = 1; i < steps.length; i++) {
    shownAt = Math.max(steps[i].at, shownAt + RUN_DWELL)
    if (shownAt > now) return { state: current.state, fresh: false, nextChangeAt: shownAt }
    current = { state: steps[i].state, since: shownAt }
  }
  // The page may have missed the last event: the topology's terminal state wins.
  if (!isFinished(current.state) && isFinished(topologyState)) {
    return { state: topologyState, fresh: false }
  }
  if (!isFinished(current.state)) return { state: current.state, fresh: false }
  const settlesAt = current.since + RUN_SETTLE
  return settlesAt > now
    ? { state: current.state, fresh: true, nextChangeAt: settlesAt }
    : { state: current.state, fresh: false }
}

// ─── Edges ───────────────────────────────────────────────────────────────

export interface RunEdgeEffects {
  /** `queries` edges to hold glowing while the query runs. */
  hold: string[]
  /** `query-results` edges a pulse runs along. */
  pulse: string[]
  /** The query has finished: whatever it held is released. */
  release: boolean
  /** The edge the state glows or pulses is not on the map (yet). */
  unmatched: boolean
}

const NO_EFFECTS: RunEdgeEffects = { hold: [], pulse: [], release: false, unmatched: false }

/**
 * What an Athena state change does to the workgroup's edges: while it runs,
 * its `queries` edge to the database it reads glows; when it succeeds a
 * pulse runs along `query-results` to the bucket its results went to.
 */
export function runEdgeEffects(ev: LakeEvent, edges: readonly TopologyEdge[]): RunEdgeEffects {
  if (ev.type !== EventType.athena.QueryStateChanged) return NO_EFFECTS
  const p = (ev.payload ?? {}) as Payload
  const source = `::athena::${text(p, "workGroup")}`
  const state = text(p, "state")
  const from = (type: string) => edges.filter((e) => e.type === type && e.source.endsWith(source))
  if (state === "RUNNING") {
    const target = readTarget(text(p, "catalog"), text(p, "database"))
    const hold = from("queries")
      .filter((e) => e.target.endsWith(target))
      .map((e) => e.id)
    return { ...NO_EFFECTS, hold, unmatched: hold.length === 0 }
  }
  if (!isFinished(state)) return NO_EFFECTS
  const pulse = state === "SUCCEEDED" ? from("query-results").map((e) => e.id) : []
  return { hold: [], release: true, pulse, unmatched: state === "SUCCEEDED" && pulse.length === 0 }
}

/** The node a query reads, as a suffix: a table bucket for its catalog, else the Glue database. */
function readTarget(catalog: string, database: string): string {
  const [parent, bucket] = catalog.split("/", 2)
  if (bucket && parent.toLowerCase() === "s3tablescatalog") return `::s3tables::${bucket}`
  return `::glue::${database}`
}
