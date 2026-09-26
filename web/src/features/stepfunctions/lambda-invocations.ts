/**
 * Ties a Task attempt that called Lambda to the invocation it made, and that
 * invocation to the lines it wrote to CloudWatch Logs.
 *
 * Execution history never names the invocation. A `lambda:invoke` Task that
 * succeeded hands its next state the Invoke response, whose
 * `SdkResponseMetadata.RequestId` is exact. Every other attempt, and every
 * failed one, has only its window: scheduled at one instant, settled at
 * another. Lambda brackets each invocation's output with `START RequestId:` and
 * `REPORT RequestId:` (or the `platform.start` / `platform.report` records under
 * the JSON log format), so the invocations that started inside the window are
 * the candidates. A function the execution calls once at a time leaves exactly
 * one, and a Map fanning out over the same function leaves several, so the
 * match says which it is rather than guessing silently.
 *
 * Nothing here is Overcast-specific: it reads the same history and the same
 * log lines AWS writes.
 */
import type { StateRun, TaskAttempt } from "./execution-trace"
import { describeLogEvent, parsePlatformRecord, tryParseJSON } from "@/lib/log-format"
import { isRecord } from "@/lib/utils"

// ─── Which function an attempt called ────────────────────────────────────────

export interface LambdaTarget {
  functionName: string
  qualifier?: string
  /** The region the ARN names, when the attempt called one by ARN. */
  region?: string
  /** How the Task called it: a function ARN as its Resource, or the `lambda:invoke` integration. */
  via: "function-arn" | "lambda:invoke"
  /** The invocation type `lambda:invoke` asked for; `Event` returns before the function runs. */
  invocationType?: string
}

const FUNCTION_ARN = /^arn:[^:]+:lambda:([^:]*):[^:]*:function:([^:]+)(?::([^:]+))?$/
const PARTIAL_ARN = /^[^:]+:function:([^:]+)(?::([^:]+))?$/

/**
 * Reads a Lambda `FunctionName` in any of the forms Invoke accepts — a name, a
 * name with a qualifier, a full ARN or a partial `account:function:name` one.
 */
export function parseFunctionRef(
  ref: string,
): Pick<LambdaTarget, "functionName" | "qualifier" | "region"> | null {
  const arn = FUNCTION_ARN.exec(ref)
  if (arn) return { functionName: arn[2], qualifier: arn[3], region: arn[1] || undefined }
  const partial = PARTIAL_ARN.exec(ref)
  if (partial) return { functionName: partial[1], qualifier: partial[2] }
  const [name, qualifier] = ref.split(":")
  return name ? { functionName: name, qualifier } : null
}

function parseObject(json: string | undefined): Record<string, unknown> | null {
  if (!json) return null
  const parsed = tryParseJSON(json)
  return isRecord(parsed) ? parsed : null
}

/** The Lambda function an attempt called, or null when it called something else. */
export function lambdaTargetOf(attempt: TaskAttempt): LambdaTarget | null {
  if (attempt.resourceType === undefined && attempt.resource) {
    const ref = parseFunctionRef(attempt.resource)
    return ref && FUNCTION_ARN.test(attempt.resource) ? { ...ref, via: "function-arn" } : null
  }
  if (attempt.resourceType !== "lambda" || !attempt.resource?.startsWith("invoke")) return null
  const params = parseObject(attempt.parameters)
  const name = params?.FunctionName
  if (typeof name !== "string" || !name) return null
  const ref = parseFunctionRef(name)
  if (!ref) return null
  const qualifier = typeof params.Qualifier === "string" ? params.Qualifier : ref.qualifier
  return {
    ...ref,
    qualifier,
    region: ref.region ?? attempt.region,
    via: "lambda:invoke",
    invocationType: typeof params.InvocationType === "string" ? params.InvocationType : undefined,
  }
}

/** The Lambda function a run's Task called, from its latest attempt. */
export function lambdaTargetOfRun(run: StateRun | undefined): LambdaTarget | null {
  const attempt = run?.taskAttempts.at(-1)
  return attempt ? lambdaTargetOf(attempt) : null
}

/** A test event name made of the state's name, safe for any store: `sfn-<state>-<attempt>`. */
export function testEventName(stateName: string, attemptNumber: number): string {
  const slug = stateName.replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "state"
  return `sfn-${slug.slice(0, 40)}-${attemptNumber}`
}

/** The request id `lambda:invoke` returned, which only a successful attempt has. */
export function requestIdOf(attempt: TaskAttempt): string | undefined {
  const meta = parseObject(attempt.output)?.SdkResponseMetadata
  if (isRecord(meta)) {
    const id = meta.RequestId
    if (typeof id === "string" && id) return id
  }
  return undefined
}

/**
 * The event the function was handed: a function ARN Task's input as it is, or
 * `lambda:invoke`'s `Payload` parameter — what to replay to reproduce it.
 */
export function invocationPayloadOf(attempt: TaskAttempt, target: LambdaTarget): string {
  if (target.via === "function-arn") return attempt.parameters ?? "{}"
  const payload = parseObject(attempt.parameters)?.Payload
  if (payload === undefined) return "{}"
  return typeof payload === "string" ? payload : JSON.stringify(payload, null, 2)
}

/** The log group a function writes to unless its LoggingConfig names another. */
export function defaultLogGroup(functionName: string): string {
  return `/aws/lambda/${functionName}`
}

// ─── Errors ──────────────────────────────────────────────────────────────────

export interface ParsedCause {
  errorType?: string
  errorMessage: string
  /** Stack frames, one per line, when the runtime reported them. */
  stack: string[]
}

/**
 * A Lambda error's cause is the function's error payload as JSON —
 * `errorType`, `errorMessage` and a `trace` (Node) or `stackTrace` (Python,
 * Java) array. Anything else comes back as its own message.
 */
export function parseCause(cause: string | undefined): ParsedCause | null {
  if (!cause) return null
  const parsed = parseObject(cause)
  if (parsed && typeof parsed.errorMessage === "string") {
    const raw = parsed.trace ?? parsed.stackTrace
    const stack = Array.isArray(raw)
      ? raw
          .flatMap((frame) => (typeof frame === "string" ? frame.split("\n") : []))
          .map((line) => line.trimEnd())
          .filter(Boolean)
      : []
    return {
      errorType: typeof parsed.errorType === "string" ? parsed.errorType : undefined,
      errorMessage: parsed.errorMessage,
      stack,
    }
  }
  return { errorMessage: cause, stack: [] }
}

/**
 * What an error name means, for the ones whose name alone does not say where
 * to look. Keyed by the ASL error name, with a matcher for families.
 */
const ERROR_HINTS: Array<{ match: (error: string, cause: string) => boolean; hint: string }> = [
  {
    match: (e) => e === "Lambda.ResourceNotFoundException",
    hint: "Lambda could not find the function, so it never ran. Check the function name, qualifier and region the state calls.",
  },
  {
    match: (e) => e === "Lambda.TooManyRequestsException",
    hint: "Lambda throttled the call before the function ran: the function's reserved concurrency (or Overcast's instance limit) was used up. A Retry on Lambda.TooManyRequestsException rides it out.",
  },
  {
    match: (e) => e === "States.Timeout" || e === "States.HeartbeatTimeout",
    hint: "The Task's own TimeoutSeconds (or HeartbeatSeconds) ran out before the function answered. The invocation may still have finished after Step Functions gave up on it.",
  },
  {
    match: (e, c) => e === "Sandbox.Timedout" || /Task timed out after/i.test(c),
    hint: "The function hit its own configured timeout. Raise the function's Timeout, or find what it was waiting on in the logs below.",
  },
  {
    match: (e) => e === "Runtime.ExitError",
    hint: "The runtime process exited mid-invocation — a crash, an explicit exit, or running out of memory. Check Max memory used against the memory size.",
  },
  {
    match: (e) => e.startsWith("Runtime.ImportModule") || e === "Runtime.HandlerNotFound",
    hint: "The function failed to load: the handler setting does not point at an exported function, or an import failed at startup.",
  },
  {
    match: (e) => e === "Lambda.Unknown",
    hint: "The function failed without reporting an error type — usually an unhandled crash. Its logs are the only record of why.",
  },
]

export function errorHint(
  error: string | undefined,
  cause: string | undefined,
): string | undefined {
  if (!error) return undefined
  return ERROR_HINTS.find((h) => h.match(error, cause ?? ""))?.hint
}

/** An error that means the function was never run, so there are no logs to find. */
export function neverRan(error: string | undefined): boolean {
  return error === "Lambda.ResourceNotFoundException" || error === "Lambda.TooManyRequestsException"
}

// ─── Invocations in the logs ─────────────────────────────────────────────────

export interface LogLine {
  timestamp?: number
  message?: string
  logStreamName?: string
  eventId?: string
}

export interface ReportMetrics {
  durationMs?: number
  billedDurationMs?: number
  memorySizeMb?: number
  maxMemoryUsedMb?: number
  /** Present only on the invocation that started a fresh execution environment. */
  initDurationMs?: number
  /** `timeout` or `error` when the platform ended the invocation; absent otherwise. */
  status?: string
}

export interface InvocationLog {
  requestId: string
  logStreamName?: string
  /** When START was written. */
  start?: number
  /** When REPORT was written. */
  end?: number
  report?: ReportMetrics
  lines: LogLine[]
}

const TEXT_START = /^START RequestId:\s*(\S+)/
const TEXT_REPORT = /^REPORT RequestId:\s*(\S+)/

function reportFromText(line: string): ReportMetrics {
  const num = (label: string) => {
    const m = new RegExp(`${label}:\\s*([\\d.]+)`).exec(line)
    return m ? Number(m[1]) : undefined
  }
  const status = /\bStatus:\s*(\w+)/.exec(line)?.[1]
  return {
    durationMs: num("\\tDuration"),
    billedDurationMs: num("Billed Duration"),
    memorySizeMb: num("Memory Size"),
    maxMemoryUsedMb: num("Max Memory Used"),
    initDurationMs: num("Init Duration"),
    ...(status ? { status } : {}),
  }
}

function reportFromRecord(record: Record<string, unknown>): ReportMetrics {
  const metrics = (record.metrics ?? {}) as Record<string, unknown>
  const n = (v: unknown) => (typeof v === "number" ? v : undefined)
  const status = typeof record.status === "string" ? record.status : undefined
  return {
    durationMs: n(metrics.durationMs),
    billedDurationMs: n(metrics.billedDurationMs),
    memorySizeMb: n(metrics.memorySizeMB),
    maxMemoryUsedMb: n(metrics.maxMemoryUsedMB),
    initDurationMs: n(metrics.initDurationMs),
    ...(status && status !== "success" && status !== "failure" ? { status } : {}),
  }
}

type Marker = { kind: "start" | "report"; requestId: string; report?: ReportMetrics }

function markerOf(message: string): Marker | null {
  const trimmed = message.trimStart()
  const start = TEXT_START.exec(trimmed)
  if (start) return { kind: "start", requestId: start[1] }
  const report = TEXT_REPORT.exec(trimmed)
  if (report) return { kind: "report", requestId: report[1], report: reportFromText(trimmed) }
  const rec = parsePlatformRecord(trimmed)
  const id = rec && typeof rec.record.requestId === "string" ? rec.record.requestId : undefined
  if (rec && id && rec.type === "platform.start") return { kind: "start", requestId: id }
  if (rec && id && rec.type === "platform.report")
    return { kind: "report", requestId: id, report: reportFromRecord(rec.record) }
  return null
}

/**
 * Splits a window of a function's log events into invocations.
 *
 * A log stream belongs to one execution environment, which runs one
 * invocation at a time, so within a stream every line between a START and its
 * REPORT is that invocation's — including the lines that carry no request id,
 * which is everything a Python `print` writes. Lines before the first START in
 * a stream are the environment starting up (an import that throws lands here),
 * so they go to the invocation that follows them; lines after a REPORT and
 * before the next START are the invocation that just ended still writing — a
 * handler that ran past its timeout — so they stay with it.
 */
export function groupInvocations(events: LogLine[]): InvocationLog[] {
  const byId = new Map<string, InvocationLog>()
  const get = (requestId: string, stream: string | undefined) => {
    let inv = byId.get(requestId)
    if (!inv) {
      inv = { requestId, logStreamName: stream, lines: [] }
      byId.set(requestId, inv)
    }
    return inv
  }

  const byStream = new Map<string, LogLine[]>()
  for (const e of events) {
    const key = e.logStreamName ?? ""
    const list = byStream.get(key) ?? []
    list.push(e)
    byStream.set(key, list)
  }

  for (const [stream, lines] of byStream) {
    // Stable sort: lines in the same millisecond keep the order they arrived in.
    const ordered = lines
      .map((line, i) => ({ line, i }))
      .sort((a, b) => (a.line.timestamp ?? 0) - (b.line.timestamp ?? 0) || a.i - b.i)
      .map((x) => x.line)
    let current: InvocationLog | undefined
    let lastClosed: InvocationLog | undefined
    let pending: LogLine[] = []
    for (const line of ordered) {
      const message = line.message ?? ""
      const marker = markerOf(message)
      if (marker?.kind === "start") {
        current = get(marker.requestId, stream || undefined)
        current.start ??= line.timestamp
        current.lines.push(...pending, line)
        pending = []
        continue
      }
      const tagged = marker?.requestId ?? describeLogEvent(line).requestId ?? undefined
      // A line that names its request id belongs to it even when its START
      // fell before the window; an untagged one to whatever is open.
      const owner = tagged ? get(tagged, stream || undefined) : (current ?? lastClosed)
      if (!owner) {
        pending.push(line)
        continue
      }
      owner.lines.push(line)
      if (marker?.kind === "report") {
        owner.end = line.timestamp
        owner.report = marker.report
        if (owner === current) current = undefined
        lastClosed = owner
      }
    }
  }

  return [...byId.values()].sort((a, b) => (a.start ?? a.end ?? 0) - (b.start ?? b.end ?? 0))
}

// ─── Matching an attempt ─────────────────────────────────────────────────────

export type MatchQuality = "request-id" | "error-message" | "only-one" | "best-guess" | "none"

export interface AttemptMatch {
  invocation?: InvocationLog
  quality: MatchQuality
  /** Every invocation that started inside the attempt's window, the match included. */
  candidates: InvocationLog[]
}

/** How far outside an attempt's own events its invocation's START may land. */
const WINDOW_SLACK_MS = 1000

/** The window to read logs for: the attempt, plus room for lines written just around it. */
export function attemptWindow(
  attempt: TaskAttempt,
  fallbackEnd: number,
): { startMs: number; endMs: number } {
  return {
    startMs: attempt.scheduledAt - WINDOW_SLACK_MS * 2,
    endMs: (attempt.endAt ?? fallbackEnd) + WINDOW_SLACK_MS * 5,
  }
}

export function matchAttempt(
  attempt: TaskAttempt,
  invocations: InvocationLog[],
  fallbackEnd: number,
): AttemptMatch {
  const requestId = requestIdOf(attempt)
  if (requestId) {
    const exact = invocations.find((inv) => inv.requestId === requestId)
    if (exact) return { invocation: exact, quality: "request-id", candidates: [exact] }
  }
  const from = attempt.scheduledAt - WINDOW_SLACK_MS
  const to = (attempt.endAt ?? fallbackEnd) + WINDOW_SLACK_MS
  const candidates = invocations.filter((inv) => {
    const at = inv.start ?? inv.end
    return at !== undefined && at >= from && at <= to
  })
  if (candidates.length === 0) return { quality: "none", candidates }
  if (candidates.length === 1) return { invocation: candidates[0], quality: "only-one", candidates }

  // Several ran at once (a Map, or another execution calling the same
  // function). A failed attempt carries the function's own error message,
  // and runtimes log it: the one invocation that wrote it is this attempt's.
  const message = parseCause(attempt.cause)?.errorMessage
  const wrote =
    message && message.length >= 8
      ? candidates.filter((inv) => inv.lines.some((l) => l.message?.includes(message)))
      : []
  if (wrote.length === 1) return { invocation: wrote[0], quality: "error-message", candidates }
  // Otherwise prefer the one whose start and end sit closest to the
  // attempt's, and whose outcome agrees with it.
  const began = attempt.startedAt ?? attempt.scheduledAt
  const failed = attempt.status === "failed" || attempt.status === "timedOut"
  const score = (inv: InvocationLog) => {
    let s = Math.abs((inv.start ?? began) - began)
    if (attempt.endAt !== undefined && inv.end !== undefined) s += Math.abs(inv.end - attempt.endAt)
    const errored = inv.report?.status !== undefined || inv.lines.some(isErrorLine)
    if (failed !== errored) s += 10_000
    return s
  }
  // A retry's earlier attempt can fall inside this one's window and have
  // logged the same error; narrowing to the ones that did still helps.
  const pool = wrote.length > 1 ? wrote : candidates
  const best = [...pool].sort((a, b) => score(a) - score(b))[0]
  return { invocation: best, quality: "best-guess", candidates }
}

function isErrorLine(line: LogLine): boolean {
  const level = describeLogEvent(line).level
  return level === "error"
}
