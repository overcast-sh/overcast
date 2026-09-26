/**
 * EventConsole — generic virtualized event stream viewer.
 *
 * Renders a bounded, auto-scrolling, monospace console that shows events
 * received from useEventStream. Designed to stay open indefinitely — the
 * virtual list keeps DOM node count constant regardless of event count.
 *
 * Auto-scroll behaviour:
 *   - Pinned to bottom by default — new events scroll into view.
 *   - Scrolling up unpins; scrolling to the bottom re-pins.
 *
 * Generic — works for any event source. Per-source payload summaries are
 * provided by the renderSummary prop; a sensible default is used if omitted.
 *
 * Rows carrying a request id (see internal/events.Event.RequestID) get a
 * hover-revealed link to that request's trace, when the server is recording
 * traces at all — see traceLinkFor.
 */
import { useRef, useEffect, useState, useCallback } from "react"
import { useVirtualizer } from "@tanstack/react-virtual"
import { Link } from "@tanstack/react-router"
import { Code2, X, Wifi, WifiOff, Workflow } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { CopyButton } from "@/components/ui/copy-button"
import { useDebugEnabled } from "@/hooks/use-server-info"
import type { StreamEvent } from "@/hooks/use-event-stream"
import { cn, isRecord } from "@/lib/utils"
import { highlightJSON } from "@/lib/highlight-code"
import { defaultEventSummary } from "./event-summary"
import { ArnLink, LinkifiedText } from "./arn-link"
import { formatQuantity } from "@/lib/format"

// ─── Types ────────────────────────────────────────────────────────────────────

export interface EventConsoleProps {
  events: StreamEvent[]
  connected: boolean
  onClear: () => void
  /**
   * Optional function to produce a one-line summary string for an event's
   * payload. Return undefined to fall back to the default JSON truncation.
   */
  renderSummary?: (event: StreamEvent) => string | undefined
  /** When true, auto-scroll is disabled and a "Paused" indicator is shown. */
  paused?: boolean
  className?: string
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

/** Short, human-readable event type label. */
function eventLabel(type: string): string {
  // "s3:ObjectCreated:*" → "ObjectCreated"
  const parts = type.split(":")
  if (parts.length >= 2) return parts.slice(1).join(":").replace(":*", "")
  return type
}

/** Color variant for the event type badge, driven by action semantics. */
function eventColor(type: string): "default" | "success" | "danger" | "warning" {
  if (type === "request:Received") return "default"
  if (type === "service:Error") return "danger"
  // ESM-specific colours — check before generic pattern matching.
  if (type === "lambda:ESMRecordFiltered") return "warning"
  if (type === "lambda:ESMInvoked") return "success"
  // Image pull events — pulling is informational, complete is success.
  if (type === "lambda:ImagePulling") return "warning"
  if (type === "lambda:ImagePullComplete") return "success"
  if (
    type.includes("Created") ||
    type.includes("Put") ||
    type.includes("Insert") ||
    type.includes("Started") ||
    type.includes("Launched") ||
    type.includes("Registered")
  )
    return "success"
  if (
    type.includes("Removed") ||
    type.includes("Delete") ||
    type.includes("Remove") ||
    type.includes("Died") ||
    type.includes("OOM") ||
    type.includes("Failed")
  )
    return "danger"
  if (
    type.includes("Modified") ||
    type.includes("Modify") ||
    type.includes("Updated") ||
    type.includes("Stopped")
  )
    return "warning"
  return "default"
}

/**
 * Source → categorical ramp slot.
 *
 * The colour encodes *identity* — which service emitted the event — so it can't
 * collapse onto one accent. It resolves through the `--cat-1…10` tokens
 * (src/styles/global.css) rather than a fixed Tailwind hue, because a single
 * shade cannot be legible on both the light card and the dark one: each slot
 * carries a light value and a dark value at the same hue, so a service keeps
 * its colour identity when the theme flips.
 *
 * Twenty-eight sources share ten slots. Sharing is deliberate, not incidental:
 * services grouped onto one slot are ones that rarely stream side by side, and
 * the two pairs that were literally indistinguishable before (ecr/eventbridge
 * both `rose-400`, kinesis/cloudformation both `cyan-300`) were split apart.
 * Colour is never the sole carrier of meaning here — the source name is printed
 * as text immediately beside it.
 */
const SOURCE_COLOR: Record<string, string> = {
  // red
  secretsmanager: "text-cat-1",
  ecr: "text-cat-1",
  // orange
  s3: "text-cat-2",
  ssm: "text-cat-2",
  ses: "text-cat-2",
  // amber
  sqs: "text-cat-3",
  kms: "text-cat-3",
  iam: "text-cat-3",
  // green
  ecs: "text-cat-4",
  apigateway: "text-cat-4",
  elasticache: "text-cat-4",
  // teal
  logs: "text-cat-5",
  stepfunctions: "text-cat-5",
  // cyan
  request: "text-cat-6",
  pipes: "text-cat-6",
  kinesis: "text-cat-6",
  cloudformation: "text-cat-6",
  // blue
  dynamodb: "text-cat-7",
  ec2: "text-cat-7",
  docker: "text-cat-7",
  msk: "text-cat-7",
  // violet
  lambda: "text-cat-8",
  rds: "text-cat-8",
  cognito: "text-cat-8",
  // magenta
  cloudfront: "text-cat-9",
  eventbridge: "text-cat-9",
  // rose
  sns: "text-cat-10",
  appsync: "text-cat-10",
  // Uncoloured on purpose: STS signs for other services rather than having an
  // identity of its own in a stream, and `inbox` is the local mail sink.
  sts: "text-fg-subtle",
  inbox: "text-fg-muted",
}

/** Categorical ramp token for the source badge. */
function sourceColor(source: string): string {
  return SOURCE_COLOR[source.toLowerCase()] ?? "text-fg-muted"
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso)
    const hh = String(d.getUTCHours()).padStart(2, "0")
    const mm = String(d.getUTCMinutes()).padStart(2, "0")
    const ss = String(d.getUTCSeconds()).padStart(2, "0")
    const ms = String(d.getUTCMilliseconds()).padStart(3, "0")
    return `${hh}:${mm}:${ss}.${ms}`
  } catch {
    return iso
  }
}

interface DecodedString {
  formatted: string
  json: boolean
}

const BASE64_RE = /^[A-Za-z0-9+/_-]+={0,2}$/

function decodeBase64Value(value: string): DecodedString | null {
  const compact = value.trim()
  if (compact.length < 8 || compact.length % 4 === 1 || !BASE64_RE.test(compact)) return null

  try {
    const normalized = compact.replace(/-/g, "+").replace(/_/g, "/")
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=")
    const binary = atob(padded)
    if (!binary) return null

    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
    const decoded = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
    if (!isUsefulDecodedText(decoded)) return null

    const formattedJSON = formatJSONString(decoded)
    return {
      formatted: formattedJSON ?? decoded,
      json: formattedJSON !== null,
    }
  } catch {
    return null
  }
}

function isUsefulDecodedText(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed) return false

  let printable = 0
  for (const char of value) {
    const code = char.charCodeAt(0)
    if (code === 9 || code === 10 || code === 13 || code >= 32) printable++
  }
  return printable / value.length > 0.9
}

function formatJSONString(value: string): string | null {
  const trimmed = value.trim()
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return null
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return null
  }
}

function highlightedJSON(value: string) {
  return { __html: highlightJSON(value) }
}

function jsonLiteral(value: string | number | boolean | null): string {
  return JSON.stringify(value)
}

/**
 * Prism token classes, borrowed for the hand-rolled JSON tree below.
 *
 * `src/styles/global.css` already themes `.token.string`, `.token.property`,
 * `.token.number`, `.token.boolean` and friends for both light and dark, and
 * the JSON editor renders through them via Prism. Reusing the class names
 * instead of picking hues here means the expanded payload agrees with the
 * editor about what a string looks like, adapts to the theme for free, and —
 * because a decoded base64 payload in the very same view *is* rendered by
 * Prism — the two halves of one expanded event can't disagree.
 *
 * The class names are the exact ones Prism's JSON grammar emits (see
 * src/lib/prism.ts), so the mapping stays honest.
 */
const TOKEN_STRING = "token string"
const TOKEN_PROPERTY = "token property"
const TOKEN_NUMBER = "token number"
const TOKEN_BOOLEAN = "token boolean"
const TOKEN_NULL = "token null keyword"
const TOKEN_PUNCTUATION = "token punctuation"
const TOKEN_OPERATOR = "token operator"

function JsonString({ value, path }: { value: string; path: string }) {
  const [showRaw, setShowRaw] = useState(false)
  const decoded = decodeBase64Value(value)

  if (!decoded) {
    // ARN auto-linking: a whole-string ARN renders as ArnLink directly;
    // an ARN embedded in a longer string (e.g. an error message) is
    // linkified in place via LinkifiedText. Both fall back to plain text
    // when the ARN's service has no mapped UI route — see arn-link.tsx.
    if (value.startsWith("arn:")) {
      return (
        <span className={TOKEN_STRING}>
          "<ArnLink arn={value} />"
        </span>
      )
    }
    if (value.includes("arn:")) {
      return (
        <span className={TOKEN_STRING}>
          "<LinkifiedText text={value} />"
        </span>
      )
    }
    return <span className={TOKEN_STRING}>{jsonLiteral(value)}</span>
  }

  const visible = showRaw ? jsonLiteral(value) : decoded.formatted
  return (
    <span className="inline-flex max-w-full flex-wrap items-start gap-1 align-top">
      <button
        type="button"
        aria-label={showRaw ? `Show decoded value at ${path}` : `Show raw value at ${path}`}
        title={showRaw ? "Show decoded value" : "Show raw value"}
        className={cn(
          "mt-0.5 inline-flex h-4 w-4 items-center justify-center rounded",
          // A tint of --accent rather than --accent-muted: the row underneath
          // turns --accent-muted on hover, and the "showing decoded" fill has
          // to stay visible against both surfaces.
          "border border-accent/40 text-accent hover:bg-accent/25",
          !showRaw && "bg-accent-muted",
        )}
        onClick={(event) => {
          event.stopPropagation()
          setShowRaw(!showRaw)
        }}
      >
        <Code2 className="h-3 w-3" aria-hidden="true" />
      </button>
      <span className="text-accent">
        {showRaw ? "raw" : decoded.json ? "decoded JSON" : "decoded"}
      </span>
      {decoded.json && !showRaw ? (
        <span
          className="block whitespace-pre-wrap text-fg-muted"
          dangerouslySetInnerHTML={highlightedJSON(visible)}
        />
      ) : (
        <span className={cn(showRaw ? TOKEN_STRING : "text-fg-muted")}>{visible}</span>
      )}
    </span>
  )
}

function JsonValue({ value, path }: { value: unknown; path: string }) {
  if (value === null) return <span className={TOKEN_NULL}>null</span>

  if (Array.isArray(value)) {
    if (value.length === 0) return <span className={TOKEN_PUNCTUATION}>[]</span>
    return (
      <span>
        <span className={TOKEN_PUNCTUATION}>[</span>
        {value.map((item, index) => (
          <span key={`${path}.${index}`} className="block pl-4">
            <JsonValue value={item} path={`${path}[${index}]`} />
            {index < value.length - 1 ? <span className={TOKEN_PUNCTUATION}>,</span> : null}
          </span>
        ))}
        <span className={cn("block", TOKEN_PUNCTUATION)}>]</span>
      </span>
    )
  }

  if (isRecord(value)) {
    const entries = Object.entries(value)
    if (entries.length === 0) return <span className={TOKEN_PUNCTUATION}>{"{}"}</span>
    return (
      <span>
        <span className={TOKEN_PUNCTUATION}>{"{"}</span>
        {entries.map(([key, item], index) => (
          <span key={`${path}.${key}`} className="block pl-4">
            <span className={TOKEN_PROPERTY}>{jsonLiteral(key)}</span>
            <span className={TOKEN_OPERATOR}>:</span>{" "}
            <JsonValue value={item} path={`${path}.${key}`} />
            {index < entries.length - 1 ? <span className={TOKEN_PUNCTUATION}>,</span> : null}
          </span>
        ))}
        <span className={cn("block", TOKEN_PUNCTUATION)}>{"}"}</span>
      </span>
    )
  }

  if (typeof value === "string") return <JsonString value={value} path={path} />
  if (typeof value === "number") return <span className={TOKEN_NUMBER}>{jsonLiteral(value)}</span>
  if (typeof value === "boolean") return <span className={TOKEN_BOOLEAN}>{jsonLiteral(value)}</span>
  return <span className="text-fg-muted">{jsonLiteral(String(value))}</span>
}

/**
 * The event as both rendered and copied. One shape for both so the JSON a user
 * copies is exactly the JSON they were looking at.
 */
function eventEnvelope(event: StreamEvent): Record<string, unknown> {
  return {
    type: event.type,
    time: event.time,
    source: event.source,
    // Lifted to the envelope (rather than left inside payload) so events whose
    // resource ARN lives at the envelope level — see
    // internal/events.Event.ResourceARN — still get an auto-linked ARN even
    // when the payload itself has no ARN field.
    ...(event.resourceArn ? { resourceArn: event.resourceArn } : {}),
    // Same reasoning as resourceArn: it lives on the envelope, so copying the
    // payload alone would lose the one field that says which call caused this.
    ...(event.requestId ? { requestId: event.requestId } : {}),
    payload: event.payload,
  }
}

/**
 * The "open the trace behind this event" affordance, or nothing.
 *
 * Two conditions, for two different reasons. No requestId means the event was
 * not caused by an API call at all — a poller, a timer, a container dying —
 * and there is no trace to open. Debug off means traces are not being
 * recorded: internal/middleware.DebugTrace is an identity function unless
 * OVERCAST_DEBUG is set, so the link would resolve to a "trace not found"
 * page for every event on the console. The id itself still travels with the
 * event either way — it is in the expanded envelope, in what the copy button
 * writes, and in what the search box matches.
 *
 * A plain function rather than a component because it holds no state and
 * calls no hooks — the debug answer is read once for the whole console, not
 * per row (a hook could not be called per row in any case).
 */
function traceLinkFor(event: StreamEvent, enabled: boolean) {
  if (!enabled || !event.requestId) return null
  const requestId = event.requestId
  return (
    <Link
      to="/debug/traces/$requestId"
      params={{ requestId }}
      aria-label={`Open trace for request ${requestId}`}
      title={`Open trace ${requestId}`}
      // stopPropagation: the row itself toggles the expanded payload, and a
      // click meant for the link should navigate rather than also leaving an
      // expanded row behind on a page the reader is walking away from.
      onClick={(e) => e.stopPropagation()}
      // Weighted to match the copy control beside it — text-fg-subtle, not a
      // dimmed variant. The two share a cluster, so a difference in weight
      // reads as one of them being disabled, and 40% alpha over the light
      // theme's hover tint is the pairing that fails contrast first.
      className="text-fg-subtle opacity-0 transition-opacity group-hover/row:opacity-100 hover:text-fg focus-visible:opacity-100"
    >
      <Workflow aria-hidden className="h-3.5 w-3.5" />
    </Link>
  )
}

function EventPayloadDetails({ event }: { event: StreamEvent }) {
  return (
    // bg-bg-muted rather than the console's own surface: the expanded payload
    // has to read as a nested block in both themes, so it steps *away* from
    // --bg-elevated in whichever direction the theme has room for.
    <div className="mt-1 rounded bg-bg-muted p-2 text-xs break-all whitespace-pre-wrap text-fg-muted">
      <JsonValue value={eventEnvelope(event)} path="$" />
    </div>
  )
}

// ─── Component ────────────────────────────────────────────────────────────────

export function EventConsole({
  events,
  connected,
  onClear,
  renderSummary,
  paused = false,
  className,
}: EventConsoleProps) {
  "use no memo"
  const scrollRef = useRef<HTMLDivElement>(null)
  const [pinned, setPinned] = useState(true)
  const [expanded, setExpanded] = useState<number | null>(null)
  // One read for the whole console: the answer is the same for every row, and
  // the rows are rendered in a loop where a hook could not go anyway.
  const traceLinksEnabled = useDebugEnabled()

  const virtualizer = useVirtualizer({
    count: events.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 34,
    measureElement: (el) => el.getBoundingClientRect().height,
    overscan: 20,
  })

  // Auto-scroll to bottom when new events arrive and we're pinned and not paused.
  useEffect(() => {
    if (!paused && pinned && events.length > 0) {
      virtualizer.scrollToIndex(events.length - 1, { align: "end" })
    }
  }, [events.length, pinned, paused]) // eslint-disable-line react-hooks/exhaustive-deps

  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 32
    setPinned(atBottom)
  }, [])

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {/* Toolbar */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {connected ? (
            <Wifi className="h-3.5 w-3.5 text-success" />
          ) : (
            <WifiOff className="h-3.5 w-3.5 text-fg-subtle" />
          )}
          <span className="font-mono text-xs text-fg-muted">
            {paused ? "Paused" : connected ? "Live" : "Disconnected"}
            {" · "}
            {formatQuantity(events.length, "event")}
          </span>
          {!pinned && (
            <button
              className="text-xs text-accent underline underline-offset-2"
              onClick={() => {
                setPinned(true)
                if (events.length > 0) {
                  virtualizer.scrollToIndex(events.length - 1, { align: "end" })
                }
              }}
            >
              ↓ scroll to latest
            </button>
          )}
        </div>
        <Button variant="ghost" size="icon-sm" title="Clear" onClick={onClear}>
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>

      {/* Console window. A log view, not a terminal emulator: the surface is the
          same card token every other panel in the app sits on, so it reads as
          light on light and dark on dark. Density and the mono face carry the
          console character, not a hardcoded near-black slab. */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="overflow-auto rounded-lg border border-border bg-bg-elevated font-mono text-xs"
        style={{ height: "calc(100vh - 220px)", minHeight: 300 }}
      >
        {events.length === 0 ? (
          <div className="flex h-full items-center justify-center text-fg-subtle">
            {connected ? "Waiting for events…" : "Not connected"}
          </div>
        ) : (
          <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
            {virtualizer.getVirtualItems().map((vr) => {
              const ev = events[vr.index]
              const isExpanded = expanded === vr.index
              const summary = renderSummary?.(ev) ?? defaultEventSummary(ev)
              const label = eventLabel(ev.type)
              const color = eventColor(ev.type)

              return (
                <div
                  key={vr.key}
                  data-index={vr.index}
                  ref={virtualizer.measureElement}
                  style={{
                    position: "absolute",
                    top: 0,
                    left: 0,
                    width: "100%",
                    transform: `translateY(${vr.start}px)`,
                  }}
                  className="group/row cursor-pointer border-b border-border-muted px-3 py-1.5 transition-colors hover:bg-accent-muted"
                  onClick={() => setExpanded(isExpanded ? null : vr.index)}
                >
                  <div className="flex min-w-0 items-baseline gap-2">
                    <span className="shrink-0 font-mono text-xs text-fg-subtle tabular-nums">
                      {formatTime(ev.time)}
                    </span>
                    <span className={cn("shrink-0 text-xs font-semibold", sourceColor(ev.source))}>
                      {ev.source}
                    </span>
                    <Badge variant={color} className="shrink-0 text-xs">
                      {label}
                    </Badge>
                    <span className="min-w-0 truncate text-sm text-fg-muted">{summary}</span>
                    {/* Actions — right-aligned as one cluster so it sits in the
                        same place whether or not the row carries a request id,
                        and stays out of the way until the row is hovered: the
                        console is dense, and permanent glyphs on every row
                        would compete with the summary text for the eye. */}
                    <div className="ml-auto flex shrink-0 items-center gap-0.5 self-center">
                      {traceLinkFor(ev, traceLinksEnabled)}
                      <CopyButton
                        value={JSON.stringify(eventEnvelope(ev), null, 2)}
                        noun="event"
                        tone="inline"
                        className="opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100"
                      />
                    </div>
                  </div>
                  {isExpanded && <EventPayloadDetails event={ev} />}
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
