/**
 * lambda-instance-node — instance card rendered inside a LambdaGroupNode's
 * internally-scrollable instance list.  Displays per-instance status, stub
 * metrics, and a shrinking TTL countdown bar.
 *
 * Rendered as a plain list row (not a separate React Flow node) so the
 * LambdaGroupNode container can clip/scroll it with native overflow — React
 * Flow renders parent/child nodes as flat siblings positioned via absolute
 * transforms, so a child *node* can never be scrolled by its parent's DOM.
 */
import { memo, useEffect, useRef, useState } from "react"
import { FileText } from "lucide-react"
import { fieldLabel } from "@/lib/typography"
import { formatAge } from "@/lib/format"
import { cn } from "@/lib/utils"
import { ArnLink } from "@/components/ui/arn-link"
import { SERVICES } from "@/lib/service-registry"
import type { LambdaInstance } from "@/types"
import type { LogStreamTarget } from "./log-stream-peek"
import { useEventStream } from "@/hooks/use-event-stream"
import { EventType } from "@/services/event-types"

export const LAMBDA_INSTANCE_H = 100 // px — keep in sync with map-layout.ts's lambdaGroupHeight

/** How long a ghost (recently-vanished) instance lingers before removal (ms). */
export const LAMBDA_GHOST_TTL = 10_000

export interface LambdaInstanceCardProps {
  instance: LambdaInstance
  onPeek?: (target: LogStreamTarget) => void
  /** True when this instance has disappeared from the live list and is fading out. */
  isGhost?: boolean
  /** Epoch ms when this instance was last seen live — drives the ghost fade. */
  deletedAt?: number
  /** Why this (now-ghosted) instance was evicted, if known — from `lambda:InstanceEvicted`. */
  evictedReason?: LambdaInstance["evictedReason"]
}

interface LambdaInstanceEventPayload {
  instanceId: string
  functionName: string
  status: "starting" | "initializing" | "running" | "idle"
  startedAt: number
  lastUsed: number
  expiresAt: number
  lastInvocationStatus?: "succeeded" | "failed"
  lastInvocationError?: string
  initOrigin?: LambdaInstance["initOrigin"]
  evictedReason?: LambdaInstance["evictedReason"]
}

/** Short human labels for `lambda:InstanceEvicted`'s `evictedReason`. */
const EVICTED_REASON_LABELS: Record<NonNullable<LambdaInstance["evictedReason"]>, string> = {
  "idle-ttl": "idle timeout",
  "config-change": "config changed",
  "function-deleted": "function deleted",
  "container-died": "container died",
  unhealthy: "unhealthy",
  surplus: "surplus",
  "memory-pressure": "memory pressure",
  shutdown: "shutdown",
}

interface TriggerInfo {
  label: string
  arn?: string
  serviceKey?: keyof typeof SERVICES
}

/**
 * Extracts a compact, human-friendly trigger source label.
 * Never returns raw payload fragments — compact node UI should stay high-signal.
 */
function triggerServiceFromArn(arn: string): keyof typeof SERVICES | undefined {
  const parts = arn.split(":")
  if (parts.length < 6 || parts[0] !== "arn") return undefined
  const svc = parts[2]
  if (svc === "sqs") return "sqs"
  if (svc === "sns") return "sns"
  if (svc === "dynamodb") return "dynamodb"
  if (svc === "s3") return "s3"
  if (svc === "kinesis") return "kinesis"
  if (svc === "events") return "eventbridge"
  if (svc === "lambda") return "lambda"
  if (svc === "logs") return "logs"
  return undefined
}

function fmtTrigger(event: unknown): TriggerInfo {
  if (!event) return { label: "DIRECT" }

  const normalize = (src: string): string => {
    const s = src.trim().toLowerCase().replace(/^aws:/, "")
    if (s.includes("sqs")) return "SQS"
    if (s.includes("sns")) return "SNS"
    if (s.includes("kinesis")) return "KINESIS"
    if (s.includes("dynamodb")) return "DDB"
    if (s.includes("s3")) return "S3"
    if (s.includes("eventbridge") || s.includes("events")) return "EVENTBRIDGE"
    if (s.includes("apigateway") || s.includes("api gateway") || s.includes("api-gateway"))
      return "API GW"
    if (s.includes("stepfunctions")) return "STEPFN"
    return src.toUpperCase().slice(0, 12)
  }

  const parseCandidate = (value: unknown): unknown => {
    if (typeof value !== "string") return value
    try {
      return JSON.parse(value)
    } catch {
      // Sometimes trigger payloads are base64-encoded JSON strings.
      try {
        const decoded = atob(value)
        return JSON.parse(decoded)
      } catch {
        return null
      }
    }
  }

  const obj = parseCandidate(event)
  if (!obj || typeof obj !== "object") return { label: "UNKNOWN" }
  const root = obj as Record<string, unknown>

  const extractArn = (record: Record<string, unknown>): string | undefined => {
    const arn =
      record.eventSourceARN ??
      record.EventSourceArn ??
      record.eventSourceArn ??
      (Array.isArray(record.resources) && typeof record.resources[0] === "string"
        ? record.resources[0]
        : undefined)
    return typeof arn === "string" && arn.startsWith("arn:") ? arn : undefined
  }

  const records = root.Records
  if (Array.isArray(records) && records.length > 0) {
    const first = records[0] as Record<string, unknown>
    const src = first.eventSource ?? first.EventSource
    const arn = extractArn(first)
    if (arn) {
      return {
        label: typeof src === "string" ? normalize(src) : "UNKNOWN",
        arn,
        serviceKey: triggerServiceFromArn(arn),
      }
    }
    if (typeof src === "string") return { label: normalize(src) }
  }

  const source = root.source
  if (typeof source === "string") {
    const arn =
      Array.isArray(root.resources) && typeof root.resources[0] === "string"
        ? root.resources[0]
        : undefined
    if (arn && arn.startsWith("arn:")) {
      return {
        label: normalize(source),
        arn,
        serviceKey: triggerServiceFromArn(arn),
      }
    }
    return { label: normalize(source) }
  }

  const eventSource = root.eventSource ?? root.EventSource
  if (typeof eventSource === "string") return { label: normalize(eventSource) }

  if (root.requestContext && typeof root.requestContext === "object") return { label: "API GW" }

  return { label: "UNKNOWN" }
}

/** Formats ms remaining to spelled-out "X minutes Y seconds" label. */
function fmtRemaining(ms: number): string {
  if (ms <= 0) return "expired"
  const s = Math.round(ms / 1000)
  const m = Math.floor(s / 60)
  const rem = s % 60
  if (m > 0 && rem > 0) return `${m} min ${rem} sec`
  if (m > 0) return `${m} minutes`
  return `${rem} seconds`
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(1, Math.round(ms))}ms`
  const s = ms / 1000
  if (s < 10) return `${s.toFixed(1)}s`
  return `${Math.round(s)}s`
}

/** Pill fill fraction for a given minute slot (0-indexed, 0 = last minute). */
function pillFrac(
  slotIndex: number,
  totalPills: number,
  remainingMs: number,
  totalMs: number,
): number {
  // Slots go right-to-left: slot 0 is the rightmost (last to drain)
  // Actually left-to-right: slot 0 is leftmost (first filled, last to drain)
  // Total idle TTL is 15 minutes. Each pill = 1 minute.
  const msPerPill = totalMs / totalPills
  // How many full pills remain?
  const pillsRemaining = remainingMs / msPerPill
  // Slot i (0-indexed from left) is full if i < floor(pillsRemaining),
  // partially filled if i === floor(pillsRemaining), empty otherwise.
  const fullPills = Math.floor(pillsRemaining)
  if (slotIndex < fullPills) return 1
  if (slotIndex === fullPills) return pillsRemaining - fullPills
  return 0
}

/** Picks bar color based on fraction remaining (0-1). */
function barColor(frac: number): string {
  if (frac > 0.5) return "bg-success"
  if (frac > 0.2) return "bg-warning"
  return "bg-danger"
}

function areLambdaInstanceCardPropsEqual(
  prev: LambdaInstanceCardProps,
  next: LambdaInstanceCardProps,
): boolean {
  const pi = prev.instance
  const ni = next.instance
  return (
    pi.instanceId === ni.instanceId &&
    pi.status === ni.status &&
    pi.startedAt === ni.startedAt &&
    pi.expiresAt === ni.expiresAt &&
    pi.functionName === ni.functionName &&
    pi.provisioned === ni.provisioned &&
    pi.initOrigin === ni.initOrigin &&
    prev.isGhost === next.isGhost &&
    prev.deletedAt === next.deletedAt &&
    prev.evictedReason === next.evictedReason &&
    prev.onPeek === next.onPeek
  )
}

/** Invocation timing folded out of the lambda event stream. */
interface InvokeTiming {
  /** When the invocation now running started, or null between invocations. */
  startMs: number | null
  /** How long the last completed invocation took, or null before the first. */
  lastDurationMs: number | null
}

const NO_INVOKE: InvokeTiming = { startMs: null, lastDurationMs: null }

export const LambdaInstanceCard = memo(function LambdaInstanceCard({
  instance,
  onPeek,
  isGhost = false,
  deletedAt = 0,
  evictedReason,
}: LambdaInstanceCardProps) {
  const { events: lambdaEvents } = useEventStream({ source: "lambda" })
  const eventCursorRef = useRef(0)
  // Both halves are rendered, so both are state. `startMs` used to be a ref read
  // during render: nothing re-rendered when it moved, and the running counter
  // only looked live because the 200 ms tick below happened to re-render anyway.
  const [invoke, setInvoke] = useState<InvokeTiming>(NO_INVOKE)

  // Re-render at 200 ms so the pill bar drains smoothly without a CSS transition.
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 200)
    return () => clearInterval(id)
  }, [])

  // eslint-disable-next-line react-hooks/purity
  const now = Date.now()
  // A provisioned environment is held open by its reservation, so it has no
  // idle TTL to count down — the server sends expiresAt: 0 for those.
  const isProvisioned = Boolean(instance.provisioned)
  const totalMs = instance.expiresAt - instance.startedAt
  const remainingMs = Math.max(0, instance.expiresAt - now)
  const frac = totalMs > 0 ? remainingMs / totalMs : 0
  const isExpired = !isProvisioned && instance.expiresAt <= now

  useEffect(() => {
    if (lambdaEvents.length < eventCursorRef.current) {
      eventCursorRef.current = 0
    }
    for (let i = eventCursorRef.current; i < lambdaEvents.length; i++) {
      const ev = lambdaEvents[i]
      if (
        ev.type !== EventType.lambda.InstanceAcquired &&
        ev.type !== EventType.lambda.InstanceReady &&
        ev.type !== EventType.lambda.InstanceInitializing &&
        ev.type !== EventType.lambda.InstanceReleased
      )
        continue
      const payload = ev.payload as LambdaInstanceEventPayload | undefined
      if (!payload || payload.instanceId !== instance.instanceId) continue
      const t = new Date(ev.time).getTime()
      if (!Number.isFinite(t)) continue

      switch (ev.type) {
        case EventType.lambda.InstanceAcquired:
        case EventType.lambda.InstanceReady:
        case EventType.lambda.InstanceInitializing:
          if (payload.status === "running") {
            setInvoke((prev) => ({ ...prev, startMs: t }))
          }
          break
        case EventType.lambda.InstanceReleased:
          setInvoke((prev) =>
            prev.startMs != null && t >= prev.startMs
              ? { startMs: null, lastDurationMs: t - prev.startMs }
              : { ...prev, startMs: null },
          )
          break
      }
    }
    eventCursorRef.current = lambdaEvents.length
  }, [lambdaEvents, instance.instanceId])

  const runningDurationMs =
    instance.status === "running" && invoke.startMs != null
      ? Math.max(0, now - invoke.startMs)
      : null

  const startupDurationMs =
    instance.status === "starting" || instance.status === "initializing"
      ? Math.max(0, now - instance.startedAt)
      : null

  const durationLabel =
    runningDurationMs != null
      ? `${fmtDuration(runningDurationMs)} run`
      : startupDurationMs != null
        ? `${fmtDuration(startupDurationMs)} ${instance.status === "initializing" ? "initializing" : "starting"}`
        : invoke.lastDurationMs != null
          ? fmtDuration(invoke.lastDurationMs)
          : "--"

  const hasLogs = Boolean(instance.logGroup && instance.logStream)
  const shortId = instance.instanceId ? instance.instanceId.slice(0, 8) : "????????"
  const statusDotClass = {
    running: "bg-success",
    starting: "animate-pulse bg-warning",
    initializing: "animate-pulse bg-accent",
    idle: "bg-fg-muted/40",
  }[instance.status]
  const statusBadgeClass = {
    running: "bg-success-muted text-success",
    starting: "bg-warning-muted text-warning",
    initializing: "bg-accent-muted text-accent",
    idle: "bg-fg-muted/15 text-fg-muted",
  }[instance.status]

  // Ghost fade — recomputed each render from the current time so the fade
  // progresses smoothly across the component's natural re-render cadence
  // (driven by the lambda event stream above), same easing as the rest of
  // the map's ghost-tracked entities (see sqs-visual-messages.ts).
  const ghostOpacity = isGhost ? Math.max(0.2, 1 - (now - deletedAt) / LAMBDA_GHOST_TTL) : 1

  return (
    <div
      style={{ height: LAMBDA_INSTANCE_H, opacity: ghostOpacity, transition: "opacity 1s linear" }}
    >
      <div
        className={cn(
          "relative flex flex-col gap-1 rounded border px-2 pt-1.5 pb-4.5 text-2xs shadow-sm",
          "overflow-hidden border-cat-9/30 bg-bg-elevated",
          isExpired && "opacity-55",
        )}
        style={{ width: "100%", height: LAMBDA_INSTANCE_H - 4 }}
      >
        {/* Top row: status dot + short ID + buttons */}
        <div className="nodrag nopan pointer-events-auto flex items-center gap-1.5">
          <span
            className={cn("h-2 w-2 shrink-0 rounded-full", statusDotClass)}
            title={instance.status}
          />
          <span className="flex-1 truncate font-mono text-fg-subtle" title={instance.instanceId}>
            {shortId}
          </span>
          {isProvisioned && (
            <span
              className="rounded bg-cat-9/20 px-1 py-0.5 font-mono text-2xs font-semibold text-cat-9 uppercase"
              title="Provisioned concurrency — kept warm, exempt from the idle timeout"
            >
              prov
            </span>
          )}
          {instance.initOrigin === "proactive" && (
            <span
              className="rounded bg-cat-5/20 px-1 py-0.5 font-mono text-2xs font-semibold text-cat-5 uppercase"
              title="Proactively initialized after a deploy settled — created ahead of traffic, mirroring AWS proactive initialization"
            >
              proactive
            </span>
          )}
          {isGhost && evictedReason ? (
            <span
              className="rounded bg-fg-muted/15 px-1 py-0.5 font-mono text-2xs font-semibold text-fg-muted uppercase"
              title={`Evicted — ${EVICTED_REASON_LABELS[evictedReason]}`}
            >
              {EVICTED_REASON_LABELS[evictedReason]}
            </span>
          ) : (
            <span
              className={cn(
                "rounded px-1 py-0.5 font-mono text-2xs font-semibold uppercase",
                statusBadgeClass,
              )}
            >
              {instance.status}
            </span>
          )}
          {hasLogs && onPeek && (
            <button
              type="button"
              data-peek-trigger
              className="ml-0.5 flex items-center rounded p-0.5 text-cat-9 hover:bg-cat-9/15"
              title="Peek log stream"
              onClick={(e) => {
                e.stopPropagation()
                onPeek({
                  title: instance.functionName,
                  subtitle: instance.instanceId.slice(0, 8),
                  logGroup: instance.logGroup,
                  logStream: instance.logStream,
                  triggerEvent: instance.triggerEvent || undefined,
                })
              }}
            >
              <FileText className="h-3 w-3" />
            </button>
          )}
        </div>

        {/* Trigger + last invocation metadata row */}
        <TriggerRow
          event={instance.triggerEvent}
          lastUsed={instance.lastUsed}
          durationLabel={durationLabel}
          status={instance.status}
          now={now}
        />

        {/* Stub metric bars */}
        <div className="flex items-center gap-2">
          <MetricBar label="MEM" value={instance.memoryUsedMB} max={256} unit="MB" />
          <MetricBar label="CPU" value={Math.round(instance.cpuPercent)} max={100} unit="%" />
        </div>

        {/* TTL countdown — 15 minute-pills fused to the bottom border. A
            provisioned environment never expires on idleness, so it shows a
            solid reserved bar instead of a draining countdown. */}
        <div className="absolute right-0 bottom-0 left-0 overflow-hidden rounded-b">
          {/* label above pills */}
          <div className="flex items-center justify-center pt-0.5">
            {/* At the scale floor and at full --fg-muted: 7px mono at half opacity was
                the smallest and faintest text in the console, and it is a countdown a
                reader is meant to follow. */}
            <span className="font-mono text-2xs font-normal tracking-widest text-fg-muted">
              {isProvisioned ? "reserved" : fmtRemaining(remainingMs)}
            </span>
          </div>
          {/* pill row */}
          <div className="flex gap-px px-1 pb-px">
            {Array.from({ length: 15 }, (_, i) => {
              const f = isProvisioned ? 1 : pillFrac(i, 15, remainingMs, totalMs || 15 * 60 * 1000)
              return (
                <div
                  key={i}
                  className="relative h-1.5 flex-1 overflow-hidden rounded-sm bg-fg-muted/15"
                >
                  <div
                    className={cn(
                      "absolute inset-y-0 left-0",
                      isProvisioned ? "bg-cat-9" : barColor(frac),
                    )}
                    style={{ width: `${f * 100}%`, opacity: 0.7 }}
                  />
                </div>
              )
            })}
          </div>
        </div>
      </div>
    </div>
  )
}, areLambdaInstanceCardPropsEqual)

/** Hover-to-peek row showing the trigger source and invocation timing. */
function TriggerRow({
  event,
  lastUsed,
  durationLabel,
  status,
  now,
}: {
  event: unknown
  lastUsed: number
  durationLabel: string
  status: string
  now: number
}) {
  const trigger = fmtTrigger(event)
  const TriggerIcon = trigger.serviceKey ? SERVICES[trigger.serviceKey].icon : undefined
  const statusClass: Record<string, string | undefined> = {
    starting: "font-medium text-warning",
    initializing: "font-medium text-accent",
    running: "text-success",
  }

  return (
    <div className="nodrag nopan pointer-events-auto flex w-full items-center gap-1.5 rounded bg-transparent text-2xs text-fg-muted/55">
      <span className="flex min-w-0 flex-1 items-center gap-1">
        {TriggerIcon ? <TriggerIcon className="h-2.5 w-2.5 shrink-0" /> : <span>⚡</span>}
        {trigger.arn ? (
          <span className="nodrag nopan pointer-events-auto min-w-0 truncate">
            <ArnLink arn={trigger.arn} label={trigger.arn} className="truncate text-2xs" />
          </span>
        ) : (
          <span className="truncate">src:{trigger.label}</span>
        )}
      </span>
      <span
        className={cn("ml-auto shrink-0 font-mono tabular-nums", statusClass[status])}
        title={`Last invoke ${formatAge(now - lastUsed)} (${durationLabel})`}
      >
        {durationLabel}
      </span>
    </div>
  )
}

function MetricBar({
  label,
  value,
  max,
  unit,
}: {
  label: string
  value: number
  max: number
  unit: string
}) {
  const frac = max > 0 ? Math.min(value / max, 1) : 0
  return (
    <div className="flex flex-1 items-center gap-1">
      <span className={cn(fieldLabel, "w-7 shrink-0 text-fg-muted/60")}>{label}</span>
      <div className="h-1 flex-1 overflow-hidden rounded-full bg-fg-muted/15">
        <div
          className="h-full rounded-full bg-cat-9/60"
          style={{ width: `${Math.round(frac * 100)}%` }}
        />
      </div>
      <span className="w-9 text-right font-mono text-2xs text-fg-muted/70 tabular-nums">
        {value}
        {unit}
      </span>
    </div>
  )
}
