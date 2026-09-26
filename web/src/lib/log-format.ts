/**
 * Shared vocabulary for rendering log events.
 *
 * Four surfaces show CloudWatch log events — the generic `LogViewer` (Lambda's
 * monitor tab and the map's invocations drawer), the CloudWatch Logs stream
 * viewer, the log group detail page, and the map's stream peek — and each had
 * grown its own copy of the same four helpers. Copies drift: the two level
 * detectors were identical, but the two row-tint maps had already diverged over
 * whether a debug row gets a background, and the timestamp formatter existed
 * three times under three names.
 *
 * A message goes through `stripAnsi` (see [ansi.ts](./ansi.ts)) before any of
 * these read it: a colourised line starts with an escape sequence, not with
 * `{` or `ERROR`.
 */
import { LruCache } from "@/lib/lru-cache"
import { stripAnsi } from "@/lib/ansi"
import { isRecord } from "@/lib/utils"

// `highlightJSON` lives in `lib/highlight-code.ts` — the module that owns
// syntax highlighting — and is re-exported here so the log viewers (and this
// file's own tests) keep importing it from the log vocabulary they already
// know. See highlight-code.ts for the implementation and its tests.
export { highlightJSON } from "@/lib/highlight-code"

export type LogLevel = "error" | "warn" | "info" | "debug"

/**
 * Clock time of a log event, to the millisecond: `14:22:07.481`.
 *
 * Local by default — every long-standing call site reads that way — with an
 * explicit `utc` flag for the viewers that offer the AWS console's local/UTC
 * display toggle. One function, one flag: forking a UTC formatter per surface
 * is how the pre-consolidation copies drifted.
 */
export function formatLogTime(ts?: number | null, utc = false): string {
  if (ts == null || ts === 0) return "—"
  const d = new Date(ts)
  const hh = String(utc ? d.getUTCHours() : d.getHours()).padStart(2, "0")
  const mm = String(utc ? d.getUTCMinutes() : d.getMinutes()).padStart(2, "0")
  const ss = String(utc ? d.getUTCSeconds() : d.getSeconds()).padStart(2, "0")
  const ms = String(utc ? d.getUTCMilliseconds() : d.getMilliseconds()).padStart(3, "0")
  return `${hh}:${mm}:${ss}.${ms}`
}

/**
 * Full date and time, for a log group or stream's own metadata — creation,
 * last event, retention — where the day matters and the millisecond does not.
 */
export function formatLogDate(ts?: number | null, utc = false): string {
  if (!ts) return "—"
  return utc
    ? new Date(ts).toLocaleString(undefined, { timeZone: "UTC" })
    : new Date(ts).toLocaleString()
}

/**
 * The gap between a log event and its chronological predecessor, compactly:
 * `+3 ms`, `+1.24 s`, `+2m 5s`, `+1h 0m`.
 *
 * Display-only annotation beside real timestamps — it never stands in for one.
 * The sign is kept even when negative (out-of-order ingestion happens), because
 * a delta that hid the direction would imply event times the events don't have.
 */
export function formatLogDelta(deltaMs: number): string {
  const sign = deltaMs < 0 ? "-" : "+"
  const abs = Math.abs(deltaMs)
  if (abs < 1_000) return `${sign}${abs} ms`
  if (abs < 60_000) return `${sign}${(abs / 1_000).toFixed(2)} s`
  if (abs < 3_600_000) {
    const minutes = Math.floor(abs / 60_000)
    const seconds = Math.floor((abs % 60_000) / 1_000)
    return `${sign}${minutes}m ${seconds}s`
  }
  const hours = Math.floor(abs / 3_600_000)
  const minutes = Math.floor((abs % 3_600_000) / 60_000)
  return `${sign}${hours}h ${minutes}m`
}

/**
 * Guesses a log event's level from its text, for tinting the row and labelling it.
 *
 * A structured `"level"` field wins, because a line that carries one means it.
 * Otherwise only the first 80 characters are considered: `ERROR` appearing in
 * the body of a message is usually the *word*, not the severity — a request
 * that logged "retrying after ERROR response" is not itself an error.
 */
export function detectLogLevel(msg: string): LogLevel | null {
  // A Lambda system log record carries no "level" of its own; AWS assigns it
  // one, and that assignment is the only thing that distinguishes a failed
  // invocation's report from a successful one.
  const platform = parsePlatformRecord(msg)
  if (platform) return platformRecordLevel(platform)
  const levelMatch = /"level"\s*:\s*"(\w+)"/i.exec(msg)
  if (levelMatch) {
    const l = levelMatch[1].toLowerCase()
    if (l === "error" || l === "fatal" || l === "critical") return "error"
    if (l === "warn" || l === "warning") return "warn"
    if (l === "info") return "info"
    if (l === "debug" || l === "trace") return "debug"
  }
  const prefix = msg.slice(0, 80).toUpperCase()
  if (/\bERROR\b|\bFATAL\b|\bCRITICAL\b/.test(prefix)) return "error"
  if (/\bWARN(ING)?\b/.test(prefix)) return "warn"
  if (/\bDEBUG\b|\bTRACE\b/.test(prefix)) return "debug"
  // INFO is matched last and earns no tint, so for a long time it was not
  // matched at all — nothing downstream would have looked different. It matters
  // now that the level is also a badge: a Node runtime writes `console.info` and
  // `console.log` with an INFO column, and leaving those the one severity with a
  // column but no label reads as a detection failure rather than as a choice.
  if (/\bINFO\b/.test(prefix)) return "info"
  return null
}

/**
 * Row tint by level. Info is deliberately blank: it is the common case, and a
 * tint on every second row is noise rather than signal.
 */
export const logLevelRowClass: Record<LogLevel, string> = {
  error: "border-l-danger/60 bg-danger/5",
  warn: "border-l-warning/60 bg-warning/5",
  info: "",
  debug: "border-l-fg-muted/30 bg-fg-muted/3",
}

/**
 * Badge tint by level, for the viewers that label the row as well as tint it.
 *
 * Every colour is a semantic token, so each already carries its own light and
 * dark value and the badge needs no `dark:` pair. The raw Tailwind hues these
 * replaced did need one — on a light row `text-yellow-400` is lighter than the
 * paper behind it and the label disappears, which is the one thing a badge
 * cannot do.
 */
export const logLevelBadgeClass: Record<LogLevel, string> = {
  error: "bg-danger/15 text-danger",
  warn: "bg-warning/15 text-warning",
  info: "bg-accent/15 text-accent",
  debug: "bg-fg-muted/15 text-fg-muted",
}

/**
 * Parses a message that is entirely one JSON document, for the viewers'
 * pretty-print and syntax-highlight modes. Anything else — a plain line, a
 * prefixed line, a truncated document — is not JSON for this purpose and comes
 * back null, which the callers render as text.
 */
export function tryParseJSON(msg: string): object | null {
  const trimmed = msg.trim()
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null
  try {
    return JSON.parse(trimmed) as object
  } catch {
    return null
  }
}

/** Serialises a parsed message back out, indented or on one line. */
export function stringifyJSON(obj: object, pretty: boolean): string {
  return JSON.stringify(obj, null, pretty ? 2 : 0)
}

// One entry per (form, message); cost is what the entry actually retains.
// ~8M characters ≈ a few hundred typical documents in both forms, or a
// handful of 256 KB monsters — either way the formatting pass for a mounted
// window is map hits, not parses.
const jsonTextCache = new LruCache<string | null>(8_000_000)

/**
 * The message as a re-serialised JSON document — pretty or single-line — or
 * null when it is not one JSON document.
 *
 * This is the formatting pass, memoised. The virtualizer mounts and unmounts
 * rows continuously, and `LogMessage` needs this text at render time (it IS
 * the row's content, and its height), so the parse + stringify used to run
 * per mount per row: scrolling back and forth over the same heavy documents
 * re-paid ~1–3 ms each at CloudWatch's largest sizes, and a Format toggle
 * re-paid it for every mounted row at once. Per distinct message and form it
 * is now computed exactly once. Deliberately synchronous — a worker or
 * rAF/idle deferral cannot help here, because text that arrives late changes
 * the row's height after measurement (the same reason plan §4 keeps
 * Format-mode pretty-printing out of async upgrades).
 */
export function jsonDocumentText(message: string, pretty: boolean): string | null {
  const key = (pretty ? "p " : "c ") + message
  const cached = jsonTextCache.get(key)
  if (cached !== undefined) return cached
  const json = tryParseJSON(stripAnsi(message))
  const text = json === null ? null : stringifyJSON(json, pretty)
  jsonTextCache.put(key, text, message.length + (text?.length ?? 0))
  return text
}

/**
 * What every viewer needs to know about a log event before it can draw a row:
 * the message without its escape sequences, the level that tints and labels it,
 * and — for a Lambda system log record — the line it stands in for.
 */
export interface LogEventMeta {
  /** The message as it reads without styling: what the level detector saw, and what Copy yields. */
  plain: string
  level: LogLevel | null
  /** A Lambda system log record's summary line, or null when the message is not one. */
  summary: string | null
  /**
   * The one request id the line carries, or null. Feeds the viewer's
   * click-to-filter affordance, which only ever hands the id back as a quoted
   * FilterLogEvents term — so detection has no server semantics to get wrong,
   * but must not claim an id a line does not carry.
   */
  requestId: string | null
}

/** A canonical UUID, which is what Lambda request ids are. */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * Finds the request id a log line carries, in the three places Lambda puts
 * one: the `RequestId: <id>` label of a Text-format START/END/REPORT line, a
 * JSON document's `"requestId"` field (platform records and structured
 * application logs alike), and the tab-separated UUID column the Node runtime
 * writes between the timestamp and the level. First match wins — one id per
 * event.
 */
export function detectRequestId(plain: string): string | null {
  const labelled = /\bRequestId:\s*([0-9a-fA-F-]{4,})/.exec(plain)
  if (labelled) return labelled[1]
  const field = /"requestId"\s*:\s*"([^"\\]+)"/i.exec(plain)
  if (field) return field[1]
  const columns = plain.split("\t")
  if (columns.length >= 3 && UUID_RE.test(columns[1])) return columns[1]
  return null
}

/**
 * Derived row metadata for one event, computed once per event object.
 *
 * The viewers used to re-derive this for their whole list whenever anything
 * changed, so a live tail paid to re-scan all of its history for every event
 * that arrived. Keyed on the event itself, history is never re-read: the query
 * cache and the tail buffer both hand out stable objects, and a `WeakMap` lets
 * an event that falls out of the buffer take its metadata with it.
 */
const metaCache = new WeakMap<object, LogEventMeta>()

export function describeLogEvent(event: { message?: string }): LogEventMeta {
  const cached = metaCache.get(event)
  if (cached) return cached

  const plain = stripAnsi(event.message ?? "")
  // One parse, read twice: `detectLogLevel` would otherwise parse the record
  // again to ask the same question this already has the answer to.
  const platform = parsePlatformRecord(plain)
  const meta: LogEventMeta = {
    plain,
    level: platform ? platformRecordLevel(platform) : detectLogLevel(plain),
    summary: platform ? formatPlatformRecord(platform) : null,
    requestId: detectRequestId(plain),
  }
  metaCache.set(event, meta)
  return meta
}

// ─── Lambda system log records ─────────────────────────────────────────────
//
// A Lambda function whose `LogFormat` is `JSON` no longer writes the plain-text
// START / END / REPORT lines. It writes one Telemetry-API-shaped event per line
// instead, and every viewer that shows a Lambda log stream has to cope:
//
//   {"time":"2026-08-09T08:37:29.512Z","type":"platform.report","record":{…}}
//
// Rendered raw that is a blob in the middle of the function's own output, so
// the viewers show the summary line these helpers produce and keep the record
// itself behind the "Format" toggle. Shapes and level assignment mirror
// internal/services/lambda/logging_json.go, which cites AWS's references.

/** One system log record: the Telemetry API event envelope, unwrapped. */
export interface PlatformLogRecord {
  /** Event type, always prefixed `platform.` — e.g. `platform.report`. */
  type: string
  /** Millisecond-precision UTC timestamp the record carries. */
  time?: string
  record: Record<string, unknown>
}

/**
 * The substring every system log record contains and almost nothing else does.
 * Checked before paying for a parse, because this runs once per log line and
 * most lines are the function's own output.
 */
const PLATFORM_TYPE_HINT = '"platform.'

/**
 * Reads one log line as a system log record, or null if it is anything else —
 * an application record, plain text, a truncated line. Never throws.
 */
export function parsePlatformRecord(msg: string): PlatformLogRecord | null {
  if (!msg.includes(PLATFORM_TYPE_HINT)) return null
  const parsed = tryParseJSON(msg)
  if (!parsed || Array.isArray(parsed)) return null
  const { type, time, record } = parsed as {
    type?: unknown
    time?: unknown
    record?: unknown
  }
  if (typeof type !== "string" || !type.startsWith("platform.")) return null
  if (!isRecord(record)) return null
  return {
    type,
    time: typeof time === "string" ? time : undefined,
    record,
  }
}

/**
 * The level AWS assigns a system log record — its "System log level event
 * mapping" table. A run that did not succeed is a warning, which is what makes
 * a failed invocation's rows worth tinting; everything else is routine.
 */
export function platformRecordLevel(rec: PlatformLogRecord): LogLevel {
  const succeeded = rec.record.status === "success"
  if (rec.type === "platform.runtimeDone") return succeeded ? "debug" : "warn"
  if (rec.type === "platform.report") return succeeded ? "info" : "warn"
  return "info"
}

function textField(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined
}

function numberField(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined
}

function metricsOf(rec: PlatformLogRecord): Record<string, unknown> {
  const metrics = rec.record.metrics
  return isRecord(metrics) ? metrics : {}
}

function labelled(label: string, value: string | undefined): string | undefined {
  return value == null ? undefined : `${label}: ${value}`
}

function millis(label: string, value: unknown): string | undefined {
  const ms = numberField(value)
  return ms == null ? undefined : `${label}: ${ms.toFixed(2)} ms`
}

function whole(label: string, value: unknown, unit: string): string | undefined {
  const n = numberField(value)
  return n == null ? undefined : `${label}: ${n} ${unit}`
}

/** Tab-separated, the way AWS separates the fields of a plain-text REPORT. */
function joinFields(parts: (string | undefined)[]): string {
  return parts.filter((p): p is string => p != null).join("\t")
}

/**
 * A system log record as the one line it replaced under the Text log format,
 * plus what the record carries and the text line had nowhere to put — the
 * invocation status and the size of the response.
 *
 * Returns null for a `platform.*` type Overcast does not emit, so a viewer
 * falls back to the record itself rather than to an invented summary.
 */
export function formatPlatformRecord(rec: PlatformLogRecord): string | null {
  const requestId = textField(rec.record.requestId)
  const head = (verb: string) => (requestId ? `${verb} RequestId: ${requestId}` : verb)
  const metrics = metricsOf(rec)
  switch (rec.type) {
    case "platform.start": {
      const version = textField(rec.record.version)
      return version ? `${head("START")} Version: ${version}` : head("START")
    }
    case "platform.runtimeDone":
      return joinFields([
        head("END"),
        labelled("Status", textField(rec.record.status)),
        labelled("Error Type", textField(rec.record.errorType)),
        millis("Duration", metrics.durationMs),
        whole("Produced Bytes", metrics.producedBytes, "bytes"),
      ])
    case "platform.report":
      return joinFields([
        head("REPORT"),
        millis("Duration", metrics.durationMs),
        whole("Billed Duration", metrics.billedDurationMs, "ms"),
        whole("Memory Size", metrics.memorySizeMB, "MB"),
        whole("Max Memory Used", metrics.maxMemoryUsedMB, "MB"),
        millis("Init Duration", metrics.initDurationMs),
        // AWS's text REPORT names a status only when the environment itself
        // ended the invocation, so a successful one stays unadorned here too.
        rec.record.status === "success"
          ? undefined
          : labelled("Status", textField(rec.record.status)),
        labelled("Error Type", textField(rec.record.errorType)),
      ])
    default:
      return null
  }
}

/**
 * A block of Lambda log output — an `X-Amz-Log-Result` tail, say — with every
 * system log record replaced by its summary. Application records and plain-text
 * output pass through byte for byte.
 */
export function summarisePlatformRecords(text: string): string {
  if (!text.includes(PLATFORM_TYPE_HINT)) return text
  return text
    .split("\n")
    .map((line) => {
      const rec = parsePlatformRecord(line)
      return (rec && formatPlatformRecord(rec)) ?? line
    })
    .join("\n")
}
