/**
 * Turns an execution's raw history into what the console draws: one `StateRun`
 * per time a state was entered, the Map iterations and Parallel branches each
 * run happened inside, and the transitions actually taken between states.
 *
 * The walk follows `previousEventId` rather than list order. On AWS that id is
 * the event's causal parent, so the events of concurrent branches and
 * iterations interleave in the list but each still chains back to its own
 * branch; following the chain keeps them apart. A backend that links every
 * event to the one before it (a sequential interpreter) degenerates to a plain
 * in-order walk, and the same code handles both.
 */
import type { HistoryEvent } from "@aws-sdk/client-sfn"
import { parentContainer, type AslModel } from "./asl"
import { END_ID, START_ID, forkId, joinId } from "./graph-layout"
import { isRecord } from "@/lib/utils"

export { formatDuration } from "@/lib/format"

export type RunStatus = "running" | "succeeded" | "failed" | "caught" | "aborted"

/** One Map iteration or Parallel branch a run is nested inside, outermost first. */
export interface IterationFrame {
  /** The Map state's name. */
  map: string
  index: number
}

export interface StateRun {
  /** Stable key: the id of the event that entered the state. */
  key: string
  name: string
  type: string
  status: RunStatus
  enteredEventId: number
  exitedEventId?: number
  start: number
  end?: number
  input?: string
  output?: string
  error?: string
  cause?: string
  /** How many times the state's work was scheduled — more than one means Retry kicked in. */
  attempts: number
  /** Failures of earlier attempts that a Retry absorbed. */
  retriedErrors: Array<{ error: string; cause: string }>
  iterationPath: IterationFrame[]
  /** The Parallel or Map run this run is nested inside. */
  parentKey?: string
  /** The scope (top level, branch, item processor) the state belongs to, when the definition is known. */
  scopeId: string
  /** Every event attributed to this run, in id order. */
  eventIds: number[]
  /** For a Map run: items it was given, and its iterations by index. */
  itemCount?: number
  /** For a distributed Map run: the map run whose child executions did the work. */
  mapRunArn?: string
  iterations?: Map<number, MapIterationInfo>
  /** Resource the task called, when it is a Task. */
  resource?: string
  /**
   * Each time the Task's work was scheduled, in order — one per Retry attempt.
   * Empty for anything that is not a Task.
   */
  taskAttempts: TaskAttempt[]
  /** Last outcome of the work inside the state, before the run closed. */
  lastOutcome?: "succeeded" | "failed"
}

/**
 * One attempt at a Task's work: from the `…Scheduled` event to the event that
 * settled it. Its window is what ties the attempt to the Lambda invocation it
 * made, and its own error — not the run's, which a later Retry may have
 * cleared — is what that attempt failed with.
 */
export interface TaskAttempt {
  scheduledEventId: number
  scheduledAt: number
  startedAt?: number
  endAt?: number
  /** `aborted`: the run ended around it — StopExecution, a timeout, a failed sibling. */
  status: "running" | "succeeded" | "failed" | "timedOut" | "aborted"
  /** `TaskScheduled`'s `resourceType` ("lambda", "sqs", …); absent for a Lambda function ARN Task. */
  resourceType?: string
  /** `TaskScheduled`'s `resource` ("invoke", …), or the function ARN of a `LambdaFunctionScheduled`. */
  resource?: string
  region?: string
  /** What the attempt sent: an integration's resolved Parameters, or a Lambda function ARN Task's input. */
  parameters?: string
  output?: string
  error?: string
  cause?: string
}

export interface MapIterationInfo {
  index: number
  status: "running" | "succeeded" | "failed" | "aborted"
  start: number
  end?: number
}

export interface Transition {
  from: string
  to: string
  /** Whether the edge taken was a Catch — the source state failed. */
  viaCatch: boolean
  eventId: number
  iterationPath: IterationFrame[]
}

export interface EventContext {
  runKey?: string
  iterationPath: IterationFrame[]
}

export interface ExecutionTrace {
  runs: StateRun[]
  runsByKey: Map<string, StateRun>
  runsByName: Map<string, StateRun[]>
  transitions: Transition[]
  eventContext: Map<number, EventContext>
  eventsById: Map<number, HistoryEvent>
  start?: number
  end?: number
  status?: "RUNNING" | "SUCCEEDED" | "FAILED" | "TIMED_OUT" | "ABORTED"
  input?: string
  output?: string
  error?: string
  cause?: string
  /** The run the execution failed in, when it failed. */
  failedRunKey?: string
  lastEventId: number
}

interface Frame {
  runKey: string
  /** For a Map run: the iteration this causal thread is inside. */
  iteration?: number
}

type Details = {
  mapRunArn?: string
  resourceType?: string
  region?: string
  parameters?: string
  name?: string
  input?: string
  output?: string
  error?: string
  cause?: string
  index?: number
  length?: number
  resource?: string
}

/** Finds the `…EventDetails` object on an event, whichever typed field it arrived in. */
export function eventDetails(event: HistoryEvent): Details | undefined {
  for (const [key, value] of Object.entries(event)) {
    if (key.endsWith("EventDetails") && isRecord(value)) return value
  }
  return undefined
}

function time(event: HistoryEvent): number {
  const t = event.timestamp
  if (t instanceof Date) return t.getTime()
  if (typeof t === "number") return t * 1000
  if (typeof t === "string") return Date.parse(t)
  return 0
}

const isFailure = (type: string) =>
  type.endsWith("Failed") ||
  type.endsWith("TimedOut") ||
  type.endsWith("StartFailed") ||
  type.endsWith("SubmitFailed")

const SCHEDULE_EVENTS = /Scheduled$/

function samePath(a: IterationFrame[], b: IterationFrame[]): boolean {
  return a.length === b.length && a.every((f, i) => f.map === b[i].map && f.index === b[i].index)
}

/** Sorts the events by id; GetExecutionHistory pages may arrive in reverse order. */
export function sortEvents(events: HistoryEvent[]): HistoryEvent[] {
  return [...events].sort((a, b) => Number(a.id ?? 0) - Number(b.id ?? 0))
}

export function buildTrace(events: HistoryEvent[], model?: AslModel): ExecutionTrace {
  const sorted = sortEvents(events)
  const trace: ExecutionTrace = {
    runs: [],
    runsByKey: new Map(),
    runsByName: new Map(),
    transitions: [],
    eventContext: new Map(),
    eventsById: new Map(),
    lastEventId: 0,
  }
  // The frame stack after each event, keyed by event id — the "thread" a
  // later event continues when its previousEventId points here.
  const stacks = new Map<number, Frame[]>()
  // The last run entered in each (scope, iteration) context, so the next run
  // entered in the same context can record the transition between them.
  const lastInContext = new Map<string, string>()
  // For a container run: the last child run per branch/iteration context, so
  // closing the container can record each lane's join.
  const childLast = new Map<string, Map<string, { runKey: string; scope: string }>>()
  let previousStack: Frame[] = []

  const runOf = (frame: Frame | undefined) =>
    frame ? trace.runsByKey.get(frame.runKey) : undefined

  const iterationPathOf = (stack: Frame[]): IterationFrame[] => {
    const path: IterationFrame[] = []
    for (const frame of stack) {
      const run = runOf(frame)
      if (run && frame.iteration !== undefined) path.push({ map: run.name, index: frame.iteration })
    }
    return path
  }

  const contextKey = (scope: string, path: IterationFrame[]) =>
    `${scope}|${path.map((f) => `${f.map}[${f.index}]`).join("/")}`

  const scopeOf = (name: string): string => model?.states.get(name)?.scopeId ?? "root"

  const close = (run: StateRun, at: number, status?: RunStatus) => {
    if (run.status !== "running") return
    run.end = at
    run.status = status ?? (run.lastOutcome === "failed" ? "caught" : "succeeded")
    // An attempt still open when its run closes never got an outcome event:
    // the run was cut short around it.
    const attempt = run.taskAttempts.at(-1)
    if (attempt?.status === "running") {
      attempt.status = "aborted"
      attempt.endAt = at
    }
  }

  /** Pops frames above the given index, closing each run as it goes. */
  const unwindTo = (
    stack: Frame[],
    keep: number,
    at: number,
    innermost?: RunStatus,
    rest?: RunStatus,
  ) => {
    for (let i = stack.length - 1; i >= keep; i--) {
      const run = runOf(stack[i])
      if (run) close(run, at, i === stack.length - 1 ? innermost : rest)
    }
    stack.length = keep
  }

  const findFrame = (stack: Frame[], predicate: (run: StateRun) => boolean) => {
    for (let i = stack.length - 1; i >= 0; i--) {
      const run = runOf(stack[i])
      if (run && predicate(run)) return i
    }
    return -1
  }

  /** Records each lane's final transition into its container's join. */
  const recordJoins = (container: StateRun, eventId: number, only?: (ctx: string) => boolean) => {
    const lanes = childLast.get(container.key)
    if (!lanes) return
    for (const [ctx, last] of lanes) {
      if (only && !only(ctx)) continue
      const run = trace.runsByKey.get(last.runKey)
      if (!run || run.status === "failed" || run.status === "aborted") continue
      trace.transitions.push({
        from: run.name,
        to: joinId(last.scope),
        viaCatch: false,
        eventId,
        iterationPath: run.iterationPath,
      })
      lanes.delete(ctx)
    }
  }

  for (const event of sorted) {
    const id = Number(event.id ?? 0)
    const type = String(event.type ?? "")
    const at = time(event)
    const details = eventDetails(event)
    trace.eventsById.set(id, event)
    trace.lastEventId = Math.max(trace.lastEventId, id)

    const prevId = Number(event.previousEventId ?? 0)
    const stack = [...(stacks.get(prevId) ?? (prevId === 0 ? [] : previousStack))].map((f) => ({
      ...f,
    }))
    let attributed: string | undefined

    if (type === "ExecutionStarted") {
      trace.start = at
      trace.status = "RUNNING"
      trace.input = details?.input
    } else if (type === "ExecutionRedriven") {
      // A redrive resumes a finished execution: it is running again, and the
      // runs that failed stay in the record as they ended.
      trace.status = "RUNNING"
      trace.end = undefined
      trace.error = undefined
      trace.cause = undefined
      trace.output = undefined
      trace.failedRunKey = undefined
      unwindTo(stack, 0, at)
    } else if (/^[A-Za-z]+StateAborted$/.test(type)) {
      // A Task, Wait, Parallel or Map interrupted by StopExecution, a timeout or
      // a failed sibling. It links to the state's own last event, so the state
      // is the innermost open run of that type on this thread.
      const stateType = type.slice(0, -"StateAborted".length)
      const index = findFrame(stack, (run) => run.type === stateType && run.status === "running")
      const run = runOf(stack[index])
      if (run) {
        attributed = run.key
        unwindTo(stack, index, at, "aborted", "aborted")
      }
    } else if (type === "MapRunStarted" || type === "MapRunRedriven") {
      const run = runOf(stack[findFrame(stack, (r) => r.type === "Map")])
      if (run) {
        attributed = run.key
        run.mapRunArn = details?.mapRunArn
      }
    } else if (type.endsWith("StateEntered")) {
      const name = details?.name ?? ""
      const stateType = type.slice(0, -"StateEntered".length)
      const container = model ? parentContainer(model, name) : undefined
      // Anything still open on this thread that is not this state's container
      // has ended without an exit event — a Catch moved on from it.
      let keep = stack.length
      while (keep > 0) {
        const run = runOf(stack[keep - 1])
        if (!run) break
        const isParent = model
          ? run.name === container
          : (run.type === "Parallel" || run.type === "Map") && run.status === "running"
        if (isParent) break
        keep--
      }
      unwindTo(stack, keep, at)

      const path = iterationPathOf(stack)
      const scope = scopeOf(name)
      const run: StateRun = {
        key: String(id),
        name,
        type: stateType,
        status: "running",
        enteredEventId: id,
        start: at,
        input: details?.input,
        attempts: 0,
        retriedErrors: [],
        taskAttempts: [],
        iterationPath: path,
        parentKey: runOf(stack[stack.length - 1])?.key,
        scopeId: scope,
        eventIds: [],
      }
      trace.runs.push(run)
      trace.runsByKey.set(run.key, run)
      trace.runsByName.set(name, [...(trace.runsByName.get(name) ?? []), run])

      const ctx = contextKey(scope, path)
      const previous = lastInContext.get(ctx)
      const previousRun = previous ? trace.runsByKey.get(previous) : undefined
      if (previousRun) {
        trace.transitions.push({
          from: previousRun.name,
          to: name,
          viaCatch: previousRun.status === "caught" || previousRun.lastOutcome === "failed",
          eventId: id,
          iterationPath: path,
        })
      } else {
        trace.transitions.push({
          from: scope === "root" ? START_ID : forkId(scope),
          to: name,
          viaCatch: false,
          eventId: id,
          iterationPath: path,
        })
      }
      lastInContext.set(ctx, run.key)
      const parentRun = runOf(stack[stack.length - 1])
      if (parentRun) {
        const lanes = childLast.get(parentRun.key) ?? new Map()
        lanes.set(ctx, { runKey: run.key, scope })
        childLast.set(parentRun.key, lanes)
      }
      stack.push({ runKey: run.key })
      attributed = run.key
    } else if (type.endsWith("StateExited")) {
      const name = details?.name ?? ""
      const index = findFrame(stack, (run) => run.name === name && run.status === "running")
      if (index >= 0) {
        const run = runOf(stack[index])
        if (run) {
          run.output = details?.output
          run.exitedEventId = id
          attributed = run.key
          unwindTo(stack, index + 1, at)
          close(run, at)
          stack.length = index
        }
      }
    } else if (type === "MapStateStarted") {
      const index = findFrame(stack, (run) => run.type === "Map")
      const run = runOf(stack[index])
      if (run) {
        run.itemCount = details?.length
        run.iterations ??= new Map()
        attributed = run.key
        // A redriven Map re-runs only the iterations that did not succeed and
        // records nothing for the rest; carry those over from the attempt
        // before, so the Map still reads as N of N rather than a partial run.
        const earlier = (trace.runsByName.get(run.name) ?? []).filter(
          (r) => r !== run && r.iterations && samePath(r.iterationPath, run.iterationPath),
        )
        for (const info of earlier.at(-1)?.iterations?.values() ?? []) {
          if (info.status === "succeeded") run.iterations.set(info.index, { ...info })
        }
      }
    } else if (type.startsWith("MapIteration")) {
      const mapIndex = findFrame(stack, (run) => run.type === "Map" && run.status === "running")
      const mapRun = runOf(stack[mapIndex])
      const iteration = Number(details?.index ?? 0)
      if (mapRun) {
        attributed = mapRun.key
        mapRun.iterations ??= new Map()
        if (type === "MapIterationStarted") {
          unwindTo(stack, mapIndex + 1, at)
          stack[mapIndex] = { ...stack[mapIndex], iteration }
          mapRun.iterations.set(iteration, { index: iteration, status: "running", start: at })
        } else {
          const failed = type === "MapIterationFailed"
          const aborted = type === "MapIterationAborted"
          unwindTo(
            stack,
            mapIndex + 1,
            at,
            failed ? "failed" : aborted ? "aborted" : undefined,
            aborted ? "aborted" : undefined,
          )
          const info = mapRun.iterations.get(iteration) ?? {
            index: iteration,
            status: "running",
            start: at,
          }
          info.status = failed ? "failed" : aborted ? "aborted" : "succeeded"
          info.end = at
          mapRun.iterations.set(iteration, info)
          if (!failed && !aborted) {
            const frame = `${mapRun.name}[${iteration}]`
            recordJoins(mapRun, id, (ctx) => (ctx.split("|")[1] ?? "").split("/").includes(frame))
          }
          stack[mapIndex] = { runKey: stack[mapIndex].runKey }
        }
      }
    } else if (/^(Parallel|Map)State(Succeeded|Failed|Aborted)$/.test(type)) {
      const kind = type.startsWith("Parallel") ? "Parallel" : "Map"
      const index = findFrame(stack, (run) => run.type === kind && run.status === "running")
      const run = runOf(stack[index])
      if (run) {
        attributed = run.key
        const failed = type.endsWith("Failed")
        const aborted = type.endsWith("Aborted")
        unwindTo(
          stack,
          index + 1,
          at,
          failed ? "failed" : aborted ? "aborted" : undefined,
          aborted ? "aborted" : undefined,
        )
        if (!failed && !aborted) recordJoins(run, id)
        run.lastOutcome = failed || aborted ? "failed" : "succeeded"
        if (failed && !run.error) {
          const innerFailure = trace.runs.find(
            (r) =>
              r.status === "failed" &&
              r.iterationPath.length >= run.iterationPath.length &&
              r.enteredEventId > run.enteredEventId &&
              r.error,
          )
          run.error = innerFailure?.error ?? "States.BranchFailed"
          run.cause = innerFailure?.cause
        }
      }
    } else if (
      type === "ExecutionSucceeded" ||
      type === "ExecutionFailed" ||
      type === "ExecutionAborted" ||
      type === "ExecutionTimedOut"
    ) {
      trace.end = at
      trace.status =
        type === "ExecutionSucceeded"
          ? "SUCCEEDED"
          : type === "ExecutionFailed"
            ? "FAILED"
            : type === "ExecutionAborted"
              ? "ABORTED"
              : "TIMED_OUT"
      if (type === "ExecutionSucceeded") {
        trace.output = details?.output
        const lastRoot = lastInContext.get(contextKey("root", []))
        const lastRun = lastRoot ? trace.runsByKey.get(lastRoot) : undefined
        unwindTo(stack, 0, at)
        if (lastRun) {
          trace.transitions.push({
            from: lastRun.name,
            to: END_ID,
            viaCatch: false,
            eventId: id,
            iterationPath: [],
          })
        }
      } else {
        trace.error = details?.error
        trace.cause = details?.cause
        const aborted = type === "ExecutionAborted"
        const innermost = runOf(stack[stack.length - 1])
        if (innermost && !aborted) {
          innermost.error ??= details?.error
          innermost.cause ??= details?.cause
          // Name the state whose error failed the execution — the Task inside
          // a Parallel, not the Parallel its failure propagated through.
          const origin = [...trace.runs]
            .reverse()
            .find(
              (r) =>
                r.error !== undefined &&
                r.error === details?.error &&
                r.type !== "Parallel" &&
                r.type !== "Map",
            )
          trace.failedRunKey = (origin ?? innermost).key
        }
        // Every run still open anywhere ends with the execution, not just the
        // ones on this thread.
        unwindTo(
          stack,
          0,
          at,
          aborted ? "aborted" : type === "ExecutionTimedOut" ? "failed" : "failed",
          aborted ? "aborted" : "failed",
        )
        for (const run of trace.runs) close(run, at, aborted ? "aborted" : "failed")
        if (!trace.failedRunKey && !aborted) {
          const lastFailed = [...trace.runs].reverse().find((r) => r.status === "failed")
          trace.failedRunKey = lastFailed?.key
        }
      }
    } else {
      // Task, Lambda, activity, Wait and every other event belongs to the
      // innermost state still open on its thread.
      const run = runOf(stack[stack.length - 1])
      if (run) {
        attributed = run.key
        const attempt = run.taskAttempts.at(-1)
        if (SCHEDULE_EVENTS.test(type) && !type.startsWith("MapRun")) {
          run.attempts += 1
          if (details?.resource) run.resource = details.resource
          run.taskAttempts.push({
            scheduledEventId: id,
            scheduledAt: at,
            status: "running",
            resourceType: details?.resourceType,
            resource: details?.resource,
            region: details?.region,
            parameters: details?.parameters ?? details?.input,
          })
        } else if (attempt?.status === "running" && type.endsWith("Started")) {
          attempt.startedAt ??= at
        } else if (attempt?.status === "running" && isFailure(type)) {
          attempt.status = type.endsWith("TimedOut") ? "timedOut" : "failed"
          attempt.endAt = at
          attempt.error = details?.error
          attempt.cause = details?.cause
        } else if (attempt?.status === "running" && type.endsWith("Succeeded")) {
          attempt.status = "succeeded"
          attempt.endAt = at
          attempt.output = details?.output
        }
        if (isFailure(type)) {
          run.lastOutcome = "failed"
          run.error = details?.error
          run.cause = details?.cause
        } else if (type.endsWith("Succeeded")) {
          if (run.lastOutcome === "failed" && run.error) {
            run.retriedErrors.push({ error: run.error, cause: run.cause ?? "" })
            run.error = undefined
            run.cause = undefined
          }
          run.lastOutcome = "succeeded"
        } else if (SCHEDULE_EVENTS.test(type) && run.lastOutcome === "failed" && run.error) {
          // A new attempt after a failure: that failure was retried.
          run.retriedErrors.push({ error: run.error, cause: run.cause ?? "" })
          run.error = undefined
          run.cause = undefined
          run.lastOutcome = undefined
        }
      }
    }

    if (attributed) trace.runsByKey.get(attributed)?.eventIds.push(id)
    trace.eventContext.set(id, {
      runKey: attributed ?? stack[stack.length - 1]?.runKey,
      iterationPath: iterationPathOf(stack),
    })
    stacks.set(id, stack)
    previousStack = stack
  }

  return trace
}

// ─── Views over a trace ──────────────────────────────────────────────────────

/** Which iteration of each Map the viewer has narrowed the diagram to. */
export type IterationSelection = Record<string, number | undefined>

/** Whether a run or transition lies inside the iterations the viewer selected. */
export function matchesSelection(path: IterationFrame[], selection: IterationSelection): boolean {
  for (const frame of path) {
    const wanted = selection[frame.map]
    if (wanted !== undefined && wanted !== frame.index) return false
  }
  return true
}

export type NodeStatus = "idle" | RunStatus

export interface NodeSummary {
  status: NodeStatus
  runs: StateRun[]
  succeeded: number
  failed: number
  running: number
  /** The run the inspector shows by default: running, else failed, else the latest. */
  focusRun?: StateRun
}

const STATUS_PRIORITY: RunStatus[] = ["running", "failed", "aborted", "caught", "succeeded"]

export function summarizeNode(
  trace: ExecutionTrace | undefined,
  name: string,
  selection: IterationSelection,
): NodeSummary {
  const runs = (trace?.runsByName.get(name) ?? []).filter((r) =>
    matchesSelection(r.iterationPath, selection),
  )
  if (runs.length === 0) return { status: "idle", runs, succeeded: 0, failed: 0, running: 0 }
  // A state's colour is the worst outcome across Map iterations, but within
  // one iteration (or the top level) only its latest run counts: a state
  // that failed and then succeeded on a redrive, or went round a loop, is
  // where its last run left it.
  const latest = new Map<string, StateRun>()
  for (const r of runs) latest.set(r.iterationPath.map((f) => `${f.map}[${f.index}]`).join("/"), r)
  const current = [...latest.values()]
  let status: RunStatus = "succeeded"
  for (const candidate of STATUS_PRIORITY) {
    if (current.some((r) => r.status === candidate)) {
      status = candidate
      break
    }
  }
  const running = runs.filter((r) => r.status === "running").length
  const failed = runs.filter((r) => r.status === "failed").length
  const succeeded = runs.filter((r) => r.status === "succeeded" || r.status === "caught").length
  const focusRun =
    current.find((r) => r.status === "running") ??
    current.find((r) => r.status === "failed") ??
    runs.at(-1)
  return { status, runs, succeeded, failed, running, focusRun }
}

export interface EdgeSummary {
  /** Moves made normally along this pair. */
  count: number
  /** Moves made because the source state failed and a Catch fired. */
  catchCount: number
  /** The highest event id that took this edge — used to animate the newest move. */
  lastEventId: number
}

/** Counts transitions per `from→to` pair, within the selected iterations. */
export function summarizeTransitions(
  trace: ExecutionTrace | undefined,
  selection: IterationSelection,
): Map<string, EdgeSummary> {
  const out = new Map<string, EdgeSummary>()
  if (!trace) return out
  for (const t of trace.transitions) {
    if (!matchesSelection(t.iterationPath, selection)) continue
    const key = `${t.from}->${t.to}`
    const entry = out.get(key) ?? { count: 0, catchCount: 0, lastEventId: 0 }
    if (t.viaCatch) entry.catchCount += 1
    else entry.count += 1
    entry.lastEventId = Math.max(entry.lastEventId, t.eventId)
    out.set(key, entry)
  }
  return out
}

/** Milliseconds a run took, or has taken so far when still running. */
export function runDuration(run: { start: number; end?: number }, now: number): number {
  return Math.max(0, (run.end ?? now) - run.start)
}

/** "Map[3] › Branch" style breadcrumb for where a run happened. */
export function describeIterationPath(path: IterationFrame[]): string {
  return path.map((f) => `${f.map} #${f.index}`).join(" › ")
}
