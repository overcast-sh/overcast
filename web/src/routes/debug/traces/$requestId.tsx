import { createFileRoute } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { useState, Fragment, useMemo } from "react"
import { ArrowLeft, Clock, Server, AlertCircle, Loader2, ChevronDown, ChevronRight, Terminal, Check } from "lucide-react"
import { traceDetailQueryOptions, traceEventsQueryOptions, debugTraceKeys } from "@/features/debug-traces/data"
import { debugTrace } from "@/services/api/misc"
import { endpointResolver } from "@/services/api/base"
import { Spinner } from "@/components/ui/primitives"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { cn, isRecord } from "@/lib/utils"
import { useCopyToClipboard } from "@/hooks/use-clipboard"
import { Link as RouterLink } from "@tanstack/react-router"
import type { useNavigate } from "@tanstack/react-router"
import { nsToHuman, statusColor, statusMessage, formatTimestamp, traceToCurl, shellQuote } from "@/features/debug-traces/utils"
import { formatBodyForDisplay, bodyHintFromHeaders, formatStackTrace } from "@/lib/format-body"
import { Waterfall } from "@/features/debug-traces/components/waterfall"
import { SequenceDiagram } from "@/features/debug-traces/components/sequence-diagram"
import { FlowMap } from "@/features/debug-traces/components/flow-map"
import { BodyOmissionChip, BodyOmissionNotice } from "@/features/debug-traces/components/body-omission"
import type { TraceEntry, TraceHop, TraceLogEntry, TraceOmitReason } from "@/types"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

export const Route = createFileRoute("/debug/traces/$requestId")({
  head: ({ params }) => ({
    meta: [{ title: `Trace ${params.requestId.slice(0, 12)}… — Overcast` }],
  }),
  /**
   * `hop` selects one of the trace's hops on arrival.
   *
   * It lives in the URL rather than in component state because it is how the
   * search results link here: a deep match found in hop 42's response body has
   * to open hop 42, not the top of a three-hundred-hop trace. Told only which
   * trace to open, the reader would have to find the match by hand again —
   * which is the work the search just did for them.
   */
  validateSearch: (search: Record<string, unknown>): { hop?: string } => ({
    hop: typeof search.hop === "string" && search.hop ? search.hop : undefined,
  }),
  component: function TraceDetailRoute() {
    const { requestId } = Route.useParams()
    return <TraceDetailPage key={requestId} />
  },
})

const tabs = ["Overview", "Hops", "Logs", "Errors", "Events"] as const
type Tab = (typeof tabs)[number]
type HopView = "sequence" | "waterfall" | "flow"

const HOP_VIEWS = [
  { value: "sequence", label: "Sequence" },
  { value: "waterfall", label: "Waterfall" },
  { value: "flow", label: "Flow" },
] as const satisfies readonly SegmentedOption<HopView>[]

function TraceDetailPage() {
  const { requestId } = Route.useParams()
  const navigate = Route.useNavigate()
  const [tab, setTab] = useState<Tab>("Overview")

  const { data: trace, isLoading, error } = useQuery({
    ...traceDetailQueryOptions(requestId),
    refetchInterval: (query) => {
      const t = query.state.data
      if (!t) return false
      return t.statusCode === 0 || t.duration === 0 ? 1000 : false
    },
  })

  if (isLoading) return <Spinner />
  if (error || !trace) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 p-12">
        <AlertCircle className="h-12 w-12 text-fg-muted" />
        <p className="text-fg-muted text-lg">
          {error ? "Failed to load trace. Make sure OVERCAST_DEBUG=true." : "Trace not found."}
        </p>
        <Button variant="outline" onClick={() => navigate({ to: "/debug/traces" })}>
          <ArrowLeft className="h-4 w-4 mr-1" />
          Back to traces
        </Button>
      </div>
    )
  }

  const inFlight = trace.statusCode === 0 || trace.duration === 0

  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center gap-3">
        <Button
          variant="ghost"
          size="icon"
          onClick={() => navigate({ to: "/debug/traces" })}
        >
          <ArrowLeft className="h-5 w-5" />
        </Button>
        <div className="flex flex-col gap-0.5">
          <h2 className="text-lg font-semibold">{trace.method} {trace.path}</h2>
          <span className="flex items-center gap-2 text-sm text-fg-muted">
            {inFlight ? (
              <span className="flex items-center gap-1 text-warning">
                <Loader2 className="h-3 w-3 animate-spin" />
                Processing…
              </span>
            ) : (
              <span className={cn("font-mono", statusColor(trace.statusCode))}>
                {trace.statusCode}
              </span>
            )}
            <span>·</span>
            <span className="flex items-center gap-1">
              <Clock className="h-3 w-3" />
              {trace.duration > 0 ? nsToHuman(trace.duration) : "…"}
            </span>
            <span>·</span>
            <span className="flex items-center gap-1">
              <Server className="h-3 w-3" />
              {trace.service}
              {trace.operation ? ` / ${trace.operation}` : ""}
            </span>
          </span>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 border-b border-border">
        {tabs.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={cn(
              "px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors",
              tab === t
                ? "border-accent text-accent"
                : "border-transparent text-fg-muted hover:text-fg",
            )}
          >
            {t}
            {t === "Hops" && trace.hops?.length ? (
              <span className="ml-1 text-xs text-fg-muted">({trace.hops.length})</span>
            ) : null}
            {t === "Logs" && trace.logEntries?.length ? (
              <span className="ml-1 text-xs text-fg-muted">({trace.logEntries.length})</span>
            ) : null}
          </button>
        ))}
      </div>

      {/* Tab content */}
      <div className="min-h-0">
        {tab === "Overview" && <OverviewTab trace={trace} />}
        {tab === "Hops" && <HopsTab hops={trace.hops ?? []} requestId={requestId} navigate={navigate} />}
        {tab === "Logs" && <LogsTab entries={trace.logEntries ?? []} hops={trace.hops ?? []} />}
        {tab === "Errors" && <ErrorsTab trace={trace} />}
        {tab === "Events" && <EventsTab requestId={requestId} />}
      </div>
    </div>
  )
}

function OverviewTab({ trace }: { trace: TraceEntry }) {
  const [hopView, setHopView] = useState<HopView>("sequence")
  // Seeded from the URL so a link from a search result opens the hop the match
  // was found in. Selecting another one afterwards is ordinary local state —
  // the URL names where you arrived, not every click since.
  const { hop: initialHopId } = Route.useSearch()
  const [selectedHopId, setSelectedHopId] = useState<string | null>(initialHopId ?? null)

  const hops = trace.hops ?? []
  const hasHops = hops.length > 0
  const inFlight = trace.statusCode === 0 || trace.duration === 0
  const reqHint = bodyHintFromHeaders(trace.requestHeaders)
  const resHint = bodyHintFromHeaders(trace.responseHeaders)
  // Memoize: this page re-renders every second while a trace is in flight,
  // and the curl command / stack highlight can involve large payloads.
  const curl = useMemo(() => traceToCurl(trace, endpointResolver.get().baseUrl), [trace])
  const stackHtml = useMemo(() => (trace.stack ? formatStackTrace(trace.stack) : null), [trace.stack])

  return (
    <div className="flex flex-col gap-6">
      {/* Summary header */}
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-bg-elevated p-4">
        <div className="flex items-center gap-2">
          <span className="font-mono font-semibold">
            {trace.method} {trace.path}
          </span>
        </div>
        <div className="flex items-center gap-2">
          {inFlight ? (
            <span className="flex items-center gap-1 text-sm text-warning">
              <Loader2 className="h-3 w-3 animate-spin" />
              Processing…
            </span>
          ) : (
            <span className={cn("font-mono text-sm font-medium", statusColor(trace.statusCode))}>
              {trace.statusCode}
              {statusMessage(trace.statusCode) && ` (${statusMessage(trace.statusCode)})`}
            </span>
          )}
          <span className="text-fg-muted text-sm">·</span>
          <span className="flex items-center gap-1 text-sm text-fg-muted">
            <Clock className="h-3.5 w-3.5" />
            {trace.duration > 0 ? nsToHuman(trace.duration) : "…"}
          </span>
          <span className="text-fg-muted text-sm">·</span>
          <span className="flex items-center gap-1 text-sm text-fg-muted">
            <Server className="h-3.5 w-3.5" />
            {trace.service}
            {trace.operation ? ` / ${trace.operation}` : ""}
          </span>
        </div>
        <div className="ml-auto">
          <CopyButton value={curl} noun="curl command" />
        </div>
      </div>

      {/* Metadata grid */}
      <div className="grid grid-cols-2 gap-4">
        <Field label="Request ID" value={trace.requestId} />
        <Field label="Timestamp" value={formatTimestamp(trace.timestamp)} />
        <Field label="Duration" value={trace.duration > 0 ? nsToHuman(trace.duration) : "…"} />
        <Field label="Method" value={trace.method} />
        <Field label="Path" value={trace.path} />
        <Field label="Host" value={trace.host} />
        <Field label="Service" value={trace.service} />
        <Field label="Operation" value={trace.operation ?? "—"} />
        <Field label="Region" value={trace.region} />
        <Field
          label="Status"
          value={
            inFlight
              ? "…"
              : statusMessage(trace.statusCode)
                ? `${trace.statusCode} (${statusMessage(trace.statusCode)})`
                : String(trace.statusCode)
          }
          className={cn(!inFlight && statusColor(trace.statusCode))}
        />
        <Field label="Remote Addr" value={trace.remoteAddr ?? "—"} />
        <Field label="User Agent" value={trace.userAgent ?? "—"} />
        {trace.referer && <Field label="Referer" value={trace.referer} />}
        {trace.awsErrorCode && (
          <Field label="AWS Error" value={`${trace.awsErrorCode}: ${trace.awsErrorMessage ?? ""}`} />
        )}
        {trace.streaming && <Field label="Streaming" value="Yes" />}
      </div>

      {trace.parentRequestId && (
        <div className="text-sm flex items-center gap-1">
          <span className="text-fg-muted">Called by</span>
          <RouterLink to="/debug/traces/$requestId" params={{ requestId: trace.parentRequestId }} className="font-mono text-accent hover:underline text-xs">{trace.parentRequestId}</RouterLink>
        </div>
      )}

      {/* Side-by-side Request / Response */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <h3 className="text-sm font-medium text-fg-muted mb-2">Request</h3>
          <HeadersBodyView
            headers={trace.requestHeaders}
            body={trace.requestBody}
            omitted={trace.requestBodyOmitted}
            hint={reqHint}
          />
        </div>
        <div>
          <h3 className="text-sm font-medium text-fg-muted mb-2">Response</h3>
          <HeadersBodyView
            headers={trace.responseHeaders}
            body={trace.responseBody}
            omitted={trace.responseBodyOmitted}
            hint={resHint}
          />
        </div>
      </div>

      {/* Call graph */}
      {hasHops && (
        <div>
          <div className="flex items-center gap-2 mb-2">
            <h3 className="text-sm font-medium text-fg-muted">Call Graph</h3>
            <SegmentedControl
              label="Call graph view"
              value={hopView}
              options={HOP_VIEWS}
              onChange={setHopView}
              className="ml-auto"
            />
          </div>
          <div tabIndex={0} className="overflow-x-auto">
            {hopView === "sequence" && (
              <SequenceDiagram
                hops={hops}
                onSelectHop={setSelectedHopId}
                selectedHopId={selectedHopId}
              />
            )}
            {hopView === "waterfall" && (
              <Waterfall
                hops={hops}
                totalDuration={trace.duration}
                startTime={trace.timestamp}
                onSelectHop={setSelectedHopId}
                selectedHopId={selectedHopId}
              />
            )}
            {hopView === "flow" && (
              <FlowMap trace={trace} onSelectHop={setSelectedHopId} selectedHopId={selectedHopId} />
            )}
          </div>
        </div>
      )}
      {stackHtml && (
        <details className="mt-2">
          <summary className="text-sm font-medium text-fg-muted cursor-pointer hover:text-fg">Stack trace</summary>
          <pre className="bg-bg rounded-lg border border-border p-4 text-xs font-mono overflow-x-auto max-h-64 mt-2 whitespace-pre-wrap" dangerouslySetInnerHTML={{ __html: stackHtml }} />
        </details>
      )}
      {selectedHopId && (
        <HopDetailPanel hopId={selectedHopId} hops={hops} onClose={() => setSelectedHopId(null)} />
      )}
    </div>
  )
}

function HopDetailPanel({ hopId, hops, onClose }: { hopId: string; hops: TraceHop[]; onClose: () => void }) {
  const hop = hops.find((h) => h.id === hopId)
  const { copy, copied } = useCopyToClipboard()
  if (!hop) return null

  return (
    <div className="rounded-lg border border-accent/40 bg-bg-elevated p-4 mt-3">
      <div className="flex items-center justify-between mb-3">
        <h4 className="text-sm font-medium text-accent">
          {hop.callerService} → {hop.service}.{hop.operation}
        </h4>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon-sm"
            title="Copy as curl"
            aria-label="Copy hop as curl"
            onClick={() => copy(hopCurl(hop), { noun: "hop curl" })}
          >
            {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Terminal className="h-3.5 w-3.5" />}
          </Button>
          <button onClick={onClose} className="text-xs text-fg-muted hover:text-fg">✕</button>
        </div>
      </div>
      <div className="grid grid-cols-3 gap-3 text-xs mb-3">
        <div>
          <span className="text-fg-muted">Status: </span>
          <span className={cn("font-mono", statusColor(hop.responseStatus))}>
            {hop.responseStatus}
            {statusMessage(hop.responseStatus) && ` (${statusMessage(hop.responseStatus)})`}
          </span>
        </div>
        <div><span className="text-fg-muted">Duration: </span><span className="font-mono">{nsToHuman(hop.duration)}</span></div>
        {hop.targetUri && <div className="col-span-3"><span className="text-fg-muted">Target: </span><span className="font-mono">{hop.targetUri}</span></div>}
        {hop.error && <div className="col-span-3 text-danger font-mono">{hop.error}</div>}
      </div>
      <HopBodySection label="Request Body" hop={hop} which="request" className="mb-2" />
      <HopBodySection label="Response Body" hop={hop} which="response" />
    </div>
  )
}

/**
 * One hop body, or an explanation of why it is not here.
 *
 * Renders nothing when the hop genuinely carried no body, and the omission
 * notice when one was dropped — the case a CDK deploy hits every time it
 * exhausts the per-trace hop-body budget, and which used to render as no
 * section at all.
 */
function HopBodySection({
  label,
  hop,
  which,
  className,
}: {
  label: string
  hop: TraceHop
  which: "request" | "response"
  className?: string
}) {
  const body = which === "request" ? hop.requestBody : hop.responseBody
  const omitted = which === "request" ? hop.requestBodyOmitted : hop.responseBodyOmitted
  if (!body && !omitted) return null
  return (
    <div className={className}>
      <div className="text-xs text-fg-muted mb-1">
        {label}
        <BodyOmissionChip reason={omitted} hasBody={!!body} />
      </div>
      <BodyOmissionNotice reason={omitted} hasBody={!!body} ownRequestId={hop.requestId} />
      {body && <HopBody raw={String(body)} headers={hop.requestHeaders} />}
    </div>
  )
}

function hopCurl(hop: TraceHop): string {
  if (!hop.targetUri) return ""
  try {
    const url = new URL(hop.targetUri)
    const lines: string[] = ["curl"]
    if (hop.requestHeaders) {
      for (const [k, vs] of Object.entries(hop.requestHeaders)) {
        const lk = k.toLowerCase()
        if (lk === "host" || lk === "content-length" || lk === "connection") continue
        for (const v of vs) lines.push(`  -H ${shellQuote(`${k}: ${v}`)}`)
      }
    }
    if (hop.requestBody) lines.push(`  -d ${shellQuote(hop.requestBody)}`)
    lines.push(`  ${shellQuote(String(url))}`)
    return lines.join(" \\\n")
  } catch {
    return ""
  }
}

function Field({ label, value, className }: { label: string; value: string; className?: string }) {
  return (
    <div className="rounded-lg border border-border bg-bg-elevated p-3">
      <div className="text-xs text-fg-muted mb-1">{label}</div>
      <div className="flex items-center gap-1">
        <span className={cn("text-sm font-mono truncate", className)}>{value}</span>
        <CopyButton value={value} noun={label} />
      </div>
    </div>
  )
}

function HeadersBodyView({
  headers,
  body,
  omitted,
  hint,
}: {
  headers: Record<string, string[]>
  body?: string
  omitted?: TraceOmitReason
  hint: "json" | "xml" | "text"
}) {
  const ct = (headers["Content-Type"] ?? headers["content-type"] ?? [""])[0]
  // Memoized: bodies can be large and this component re-renders every second
  // while the trace is in flight.
  const formatted = useMemo(
    () => (body !== undefined ? formatBodyForDisplay(body, hint, ct) : null),
    [body, hint, ct],
  )
  return (
    <div className="flex flex-col gap-3">
      <div>
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-fg-muted text-left">
                <th className="px-3 py-2 font-medium text-xs">Name</th>
                <th className="px-3 py-2 font-medium text-xs">Value</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(headers).map(([key, values]) => (
                <tr key={key} className="border-b border-border">
                  <td className="px-3 py-2 font-mono text-xs text-accent whitespace-nowrap">
                    {key}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs break-all">
                    {values.join(", ")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      {(body !== undefined || omitted) && (
        <div>
          <div className="text-xs text-fg-muted mb-1">
            Body
            <BodyOmissionChip reason={omitted} hasBody={!!body} />
          </div>
          <BodyOmissionNotice reason={omitted} hasBody={!!body} />
          {body !== undefined &&
            (formatted?.html ? (
              <pre className="rounded-lg border border-border bg-bg-elevated p-4 text-xs font-mono overflow-x-auto max-h-96" dangerouslySetInnerHTML={{ __html: formatted.html }} />
            ) : (
              <pre className="rounded-lg border border-border bg-bg-elevated p-4 text-xs font-mono overflow-x-auto max-h-96">{body}</pre>
            ))}
        </div>
      )}
    </div>
  )
}

function HopsTab({ hops, requestId, navigate }: { hops: TraceHop[]; requestId: string; navigate: ReturnType<typeof useNavigate> }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const { copy, copied } = useCopyToClipboard()

  const { data: upstream } = useQuery({
    queryKey: [...debugTraceKeys.list(), "hopsFor", requestId],
    queryFn: () => debugTrace.list({ hopsFor: requestId, limit: 50 }),
  })
  const upstreamTraces = upstream?.traces ?? []

  if (hops.length === 0 && upstreamTraces.length === 0) {
    return (
      <div className="text-center text-fg-muted py-8">
        No service calls recorded for this request.
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {upstreamTraces.length > 0 && (
        <div>
          <h3 className="text-sm font-medium text-cat-5 mb-2">↑ Called by ({upstreamTraces.length})</h3>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-fg-muted text-left">
                  <th className="px-3 py-2 font-medium">Service</th>
                  <th className="px-3 py-2 font-medium">Operation</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium">Duration</th>
                  <th className="px-3 py-2 font-medium">Request ID</th>
                </tr>
              </thead>
              <tbody>
                {upstreamTraces.map((t) => (
                  <tr key={t.requestId} className="border-b border-border hover:bg-bg-elevated cursor-pointer" onClick={() => navigate({ to: "/debug/traces/$requestId", params: { requestId: t.requestId } })}>
                    <td className="px-3 py-2"><Badge variant="outline" className="text-xs">{t.service}</Badge></td>
                    <td className="px-3 py-2 font-mono text-xs">{t.operation ?? "—"}</td>
                    <td className={cn("px-3 py-2 font-mono text-xs", statusColor(t.statusCode))}>{t.statusCode > 0 ? `${t.statusCode}${statusMessage(t.statusCode) ? ` (${statusMessage(t.statusCode)})` : ""}` : "—"}</td>
                    <td className="px-3 py-2 font-mono text-xs text-fg-muted">{nsToHuman(t.duration)}</td>
                    <td className="px-3 py-2">
                      <RouterLink to="/debug/traces/$requestId" params={{ requestId: t.requestId }} className="font-mono text-xs text-accent hover:underline" onClick={(e) => e.stopPropagation()}>{t.requestId.slice(0, 12)}…</RouterLink>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {hops.length > 0 && (
        <div>
          <h3 className="text-sm font-medium text-cat-3 mb-2">↓ Called ({hops.length})</h3>
          <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-border text-fg-muted text-left">
            <th className="px-3 py-2 font-medium w-6" />
            <th className="px-3 py-2 font-medium w-8">#</th>
            <th className="px-3 py-2 font-medium">Caller</th>
            <th className="px-3 py-2 font-medium">Service</th>
              <th className="px-3 py-2 font-medium">Operation</th>
              <th className="px-3 py-2 font-medium">Request ID</th>
              <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 font-medium">Duration</th>
          </tr>
        </thead>
        <tbody>
          {hops.map((hop) => {
            const isExpanded = expanded === hop.id
            return (
              <Fragment key={hop.id}>
                <tr
                  className="border-b border-border hover:bg-bg-elevated cursor-pointer transition-colors"
                  onClick={() => setExpanded(isExpanded ? null : hop.id)}
                >
                  <td className="px-3 py-2 text-fg-muted">
                    {isExpanded ? (
                      <ChevronDown className="h-3.5 w-3.5" />
                    ) : (
                      <ChevronRight className="h-3.5 w-3.5" />
                    )}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{hop.order}</td>
                  <td className="px-3 py-2">
                    <Badge variant="outline" className="text-xs">{hop.callerService}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="outline" className="text-xs">{hop.service}</Badge>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">{hop.operation}</td>
                  <td className="px-3 py-2">
                    {hop.requestId ? (
                      <RouterLink to="/debug/traces/$requestId" params={{ requestId: hop.requestId }} className="font-mono text-xs text-accent hover:underline" onClick={(e) => e.stopPropagation()}>
                        {hop.requestId.slice(0, 12)}…
                      </RouterLink>
                    ) : (
                      <span className="text-fg-muted text-xs">—</span>
                    )}
                  </td>
                  <td className={cn("px-3 py-2 font-mono text-xs", statusColor(hop.responseStatus))}>
                    {hop.responseStatus}
                    {statusMessage(hop.responseStatus) && ` (${statusMessage(hop.responseStatus)})`}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">
                    {nsToHuman(hop.duration)}
                  </td>
                </tr>
                {isExpanded && (
                  <tr key={`${hop.id}-detail`} className="bg-bg-elevated">
                    <td colSpan={8} className="px-4 py-3">
                      <div className="text-xs space-y-2">
                        {hop.requestId && (
                          <div>
                            <span className="text-fg-muted">Request ID: </span>
                            <RouterLink to="/debug/traces/$requestId" params={{ requestId: hop.requestId }} className="font-mono text-accent hover:underline">{hop.requestId}</RouterLink>
                          </div>
                        )}
                        {hop.targetUri && (
                          <div className="flex items-center gap-2">
                            <span className="text-fg-muted">Target: </span>
                            <span className="font-mono">{hop.targetUri}</span>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Copy as curl"
                              aria-label="Copy hop as curl"
                              onClick={() => copy(hopCurl(hop), { noun: "hop curl" })}
                            >
                              {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Terminal className="h-3.5 w-3.5" />}
                            </Button>
                          </div>
                        )}
                        <HopBodySection label="Request Body" hop={hop} which="request" />
                        <HopBodySection label="Response Body" hop={hop} which="response" />
                        {hop.error && (
                          <div className="text-danger font-mono">{hop.error}</div>
                        )}
                        {hop.stack ? (
                          <details className="mt-2">
                            <summary className="text-xs text-fg-muted cursor-pointer hover:text-fg">Stack trace</summary>
                            <pre className="bg-bg p-2 rounded text-xs font-mono overflow-x-auto max-h-48 mt-1 whitespace-pre-wrap" dangerouslySetInnerHTML={{ __html: formatStackTrace(hop.stack) }} />
                          </details>
                        ) : (
                          // Stacks are budgeted per trace — a deploy with hundreds of hops
                          // captures them only for the first hops and for failures. Say so
                          // rather than leaving the reader to wonder where the panel went.
                          <div className="text-fg-muted mt-2 text-xs">
                            Stack trace not captured — only the first hops of a trace, and hops that failed, record one.
                          </div>
                        )}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
      </div>
      )}
    </div>
  )
}

function LogsTab({ entries, hops }: { entries: TraceLogEntry[]; hops: TraceHop[] }) {
  const [expanded, setExpanded] = useState<number | null>(null)
  const [showHopLogs, setShowHopLogs] = useState(false)

  // Merge main logs + hop logs (when toggle on), sorted by timestamp.
  const allLogs = useMemo(() => {
    const base: (TraceLogEntry & { source?: string })[] = entries.map((e) => ({ ...e }))
    if (!showHopLogs || hops.length === 0) return base
    const merged = [...base]
    // Logs within hops don't exist in the current data model. Since we
    // capture request-level logs and hop-level logs separately, hop logs
    // come from the hop's own trace (which we'd need to fetch). For now,
    // this toggle is ready for when hop log entries are captured.
    return merged.sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime())
  }, [entries, hops, showHopLogs])

  if (allLogs.length === 0) {
    return (
      <div className="text-center text-fg-muted py-8">
        No log entries captured for this request.
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      {hops.length > 0 && (
        <div className="flex items-center gap-2">
          <Button variant={showHopLogs ? "default" : "ghost"} size="sm" onClick={() => setShowHopLogs((v) => !v)} className="gap-1 text-xs h-7">
            {showHopLogs ? "Hide hop logs" : "Show hop logs"}
          </Button>
        </div>
      )}
      <div className="overflow-x-auto rounded-lg border border-border">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-fg-muted text-left">
              <th className="px-3 py-2 font-medium w-6" />
              <th className="px-3 py-2 font-medium w-24">Level</th>
              <th className="px-3 py-2 font-medium w-44">Timestamp</th>
              {showHopLogs && <th className="px-3 py-2 font-medium w-24">Source</th>}
              <th className="px-3 py-2 font-medium">Message</th>
            </tr>
          </thead>
          <tbody>
            {allLogs.map((entry, i) => {
              const isExpanded = expanded === i
              const k = `${entry.timestamp}-${entry.level}-${i}`
              return (
                <Fragment key={k}>
                  <tr className="border-b border-border hover:bg-bg-elevated cursor-pointer" onClick={() => setExpanded(isExpanded ? null : i)}>
                    <td className="px-3 py-2 text-fg-muted">{isExpanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}</td>
                    <td className="px-3 py-2">
                      <Badge variant="outline" className={cn("text-xs", entry.level === "ERROR" ? "border-danger/50 text-danger" : entry.level === "WARN" ? "border-warning/50 text-warning" : "border-success/50 text-success")}>{entry.level}</Badge>
                    </td>
                    <td className="px-3 py-2 font-mono text-xs text-fg-muted whitespace-nowrap">{formatTimestamp(entry.timestamp)}</td>
                    {showHopLogs && <td className="px-3 py-2 text-xs text-fg-muted">{entry.source ?? "request"}</td>}
                    <td className="px-3 py-2 text-xs">{entry.message}</td>
                  </tr>
                  {isExpanded && entry.fields && Object.keys(entry.fields).length > 0 && (
                    <tr key={`${i}-detail`} className="bg-bg-elevated">
                      <td colSpan={showHopLogs ? 5 : 4} className="px-4 py-3">
                        <HopBody raw={JSON.stringify(entry.fields)} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function ErrorsTab({ trace }: { trace: TraceEntry }) {
  const hopErrors = (trace.hops ?? []).filter((h) => h.error || h.responseStatus >= 400)
  const hasEntryError = !!trace.awsErrorCode
  const hasHopErrors = hopErrors.length > 0

  if (!hasEntryError && !hasHopErrors) {
    return <div className="text-center text-fg-muted py-8">No errors recorded.</div>
  }

  return (
    <div className="flex flex-col gap-6">
      {hasEntryError && (
        <div>
          <h3 className="text-sm font-medium text-fg-muted mb-2">Request Error</h3>
          <div className="rounded-lg border border-danger/30 bg-danger-muted p-4">
            <div className="flex items-center gap-2 mb-1">
              <AlertCircle className="h-4 w-4 text-danger" />
              <span className="font-mono text-sm text-danger">{trace.awsErrorCode}</span>
            </div>
            {trace.awsErrorMessage && <p className="text-sm text-fg-muted">{trace.awsErrorMessage}</p>}
          </div>
        </div>
      )}
      {hasHopErrors && (
        <div>
          <h3 className="text-sm font-medium text-fg-muted mb-2">Hop Errors ({hopErrors.length})</h3>
          <div className="flex flex-col gap-3">
            {hopErrors.map((hop) => (
              <div key={hop.id} className="rounded-lg border border-border bg-bg-elevated p-3">
                <div className="flex items-center gap-2 mb-1">
                  <Badge variant="outline" className="text-xs">{hop.callerService} → {hop.service}</Badge>
                  <span className="text-xs font-mono text-fg-muted">{hop.operation}</span>
                  <span className={cn("text-xs font-mono", statusColor(hop.responseStatus))}>
                    {hop.responseStatus}
                    {statusMessage(hop.responseStatus) && ` (${statusMessage(hop.responseStatus)})`}
                  </span>
                </div>
                {hop.error && <pre className="text-xs font-mono text-danger mt-1 overflow-x-auto max-h-32">{hop.error}</pre>}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function HopBody({ raw, headers }: { raw: string; headers?: Record<string, string[]> }) {
  const ct = (headers?.["Content-Type"] ?? headers?.["content-type"] ?? [""])[0]
  // Memoized: formatting tries several parsers over a potentially large body.
  const html = useMemo(() => {
    if (ct) {
      const formatted = formatBodyForDisplay(raw, "text", ct)
      if (formatted.html && formatted.html !== formatted.text) return formatted.html
    }
    // Fallback: try to detect format from content.
    for (const hint of ["text", "json", "xml"] as const) {
      const formatted = formatBodyForDisplay(raw, hint)
      if (formatted.html && formatted.html !== formatted.text) return formatted.html
    }
    return null
  }, [raw, ct])
  if (html) return <pre className="bg-bg p-2 rounded text-xs font-mono overflow-x-auto max-h-48" dangerouslySetInnerHTML={{ __html: html }} />
  return <pre className="bg-bg p-2 rounded text-xs font-mono overflow-x-auto max-h-48">{raw}</pre>
}

function EventsTab({ requestId }: { requestId: string }) {
  const { data, isLoading, isError } = useQuery(traceEventsQueryOptions(requestId))
  const [expanded, setExpanded] = useState<number | null>(null)

  if (isLoading) return <Spinner />
  if (isError) {
    return (
      <div className="flex items-center justify-center gap-2 text-danger py-8 text-sm">
        <AlertCircle className="h-4 w-4" />
        Failed to load events for this request.
      </div>
    )
  }
  // apiFetch resolves an empty body to {}, so guard the shape before mapping.
  const events = Array.isArray(data) ? data : []
  if (events.length === 0) {
    return <div className="text-center text-fg-muted py-8">No events captured for this request.</div>
  }

  return (
    <div className="overflow-auto rounded-lg border border-border bg-bg-elevated font-mono text-xs" style={{ maxHeight: "calc(100vh - 400px)" }}>
      {events.map((ev, i) => {
        const isExpanded = expanded === i
        const label = eventTypeLabel(ev.type)
        const variant = eventBadgeVariant(ev.type)
        const srcColor = sourceColorClass(ev.source)
        return (
          <div
            key={i}
            className="group/row cursor-pointer border-b border-border-muted px-3 py-1.5 transition-colors hover:bg-accent-muted"
            onClick={() => setExpanded(isExpanded ? null : i)}
          >
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="shrink-0 text-fg-subtle tabular-nums">
                {formatTimestamp(ev.time)}
              </span>
              <span className={cn("shrink-0 font-semibold", srcColor)}>
                {ev.source}
              </span>
              <Badge variant={variant} className="shrink-0 text-xs">{label}</Badge>
              {ev.resourceArn && (
                <span className="min-w-0 truncate text-sm text-fg-muted">{ev.resourceArn}</span>
              )}
            </div>
            {isExpanded && (
              <div className="mt-1 rounded bg-bg-muted p-2 break-all whitespace-pre-wrap text-fg-muted">
                <JsonTree value={ev.payload} path="$" />
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

const T = {
  string: "token string", property: "token property",
  number: "token number", boolean: "token boolean",
  null: "token null keyword", punctuation: "token punctuation",
  operator: "token operator",
}

function JsonTree({ value, path }: { value: unknown; path: string }) {
  if (value === null || value === undefined) return <span className={T.null}>null</span>
  if (typeof value === "boolean") return <span className={T.boolean}>{String(value)}</span>
  if (typeof value === "number") return <span className={T.number}>{JSON.stringify(value)}</span>
  if (typeof value === "string") {
    const s = JSON.stringify(value)
    if (value.startsWith("arn:")) return <span className={T.string}>"<span className="underline">{value}</span>"</span>
    return <span className={T.string}>{s}</span>
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className={T.punctuation}>[]</span>
    return (
      <span>
        <span className={T.punctuation}>[</span>
        {value.map((v, i) => (
          <span key={i} className="block pl-4">
            <JsonTree value={v} path={`${path}[${i}]`} />
            {i < value.length - 1 && <span className={T.punctuation}>,</span>}
          </span>
        ))}
        <span className={cn("block", T.punctuation)}>]</span>
      </span>
    )
  }
  if (isRecord(value)) {
    const entries = Object.entries(value)
    if (entries.length === 0) return <span className={T.punctuation}>{'{}'}</span>
    return (
      <span>
        <span className={T.punctuation}>{'{'}</span>
        {entries.map(([k, v], i) => (
          <span key={k} className="block pl-4">
            <span className={T.property}>{JSON.stringify(k)}</span>
            <span className={T.operator}>:</span>{" "}
            <JsonTree value={v} path={`${path}.${k}`} />
            {i < entries.length - 1 && <span className={T.punctuation}>,</span>}
          </span>
        ))}
        <span className={cn("block", T.punctuation)}>{'}'}</span>
      </span>
    )
  }
  return <span className="text-fg-muted">{JSON.stringify(String(value))}</span>
}

function eventTypeLabel(type: string): string {
  const parts = type.split(":")
  if (parts.length >= 2) return parts.slice(1).join(":").replace(":*", "")
  return type
}

function eventBadgeVariant(type: string): "default" | "success" | "danger" | "warning" {
  if (type === "request:Received") return "default"
  if (type === "service:Error") return "danger"
  if (type.includes("Created") || type.includes("Insert") || type.includes("Started") || type.includes("Launched") || type.includes("Registered")) return "success"
  if (type.includes("Removed") || type.includes("Delete") || type.includes("Remove") || type.includes("Died") || type.includes("OOM") || type.includes("Failed")) return "danger"
  if (type.includes("Modified") || type.includes("Modify") || type.includes("Updated") || type.includes("Stopped")) return "warning"
  return "default"
}

function sourceColorClass(source: string): string {
  const map: Record<string, string> = {
    secretsmanager: "text-cat-1", ecr: "text-cat-1",
    s3: "text-cat-2", ssm: "text-cat-2", ses: "text-cat-2",
    sqs: "text-cat-3", kms: "text-cat-3", iam: "text-cat-3",
    ecs: "text-cat-4", apigateway: "text-cat-4", elasticache: "text-cat-4",
    logs: "text-cat-5", stepfunctions: "text-cat-5",
    request: "text-cat-6", pipes: "text-cat-6", kinesis: "text-cat-6", cloudformation: "text-cat-6",
    dynamodb: "text-cat-7", ec2: "text-cat-7", docker: "text-cat-7", msk: "text-cat-7",
    lambda: "text-cat-8", rds: "text-cat-8",
    events: "text-cat-9", eventbridge: "text-cat-9", appsync: "text-cat-9", shield: "text-cat-9",
    sns: "text-cat-10", cognito: "text-cat-10", waf: "text-cat-10", cloudfront: "text-cat-10",
    autoscaling: "text-cat-2", route53: "text-cat-5", acm: "text-cat-1", sts: "text-fg-subtle",
    inbox: "text-fg-muted",
  }
  return map[source.toLowerCase()] ?? "text-fg-muted"
}
