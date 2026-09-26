/**
 * MetricsPage — Metrics & Health, at /metrics.
 *
 * Laid out most-answered question first, since almost every visit is a healthy
 * emulator and the page should say so in the first screenful:
 *
 * 1. Summary band — one wrapping row of pills: storage mode and
 *    healthy/degraded status, live journal mode, last flush, uptime, then the
 *    static runtime facts (startup, Go version, CPUs, GC runs, start time).
 *    Every one is a single scalar, so they share a row rather than each
 *    claiming a section.
 * 2. Runtime — the live sparkline cards, the only thing on the page that
 *    moves, in one uniform grid.
 * 3. Advisories — what to *do*, when there is anything (see
 *    internal/router/advisories.go). Collapses to a single line when there
 *    isn't, which is the usual case.
 * 4. Storage activity, the Athena engine, then Docker connectivity —
 *    per-subsystem diagnostics, read when something is already suspected.
 *
 * Data comes from GET /_overcast/metrics (polled every 3 seconds, drives the
 * sparklines), GET /_overcast/health (always available), and GET
 * /_overcast/debug/metrics (debug-gated; degrades gracefully when
 * OVERCAST_DEBUG is off — see HealthPills and AdvisoriesList).
 */
import { useQuery } from "@tanstack/react-query"
import { cn } from "@/lib/utils"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { BarChart2, AlertCircle, Info } from "lucide-react"
import { useMetrics } from "@/hooks/use-metrics"
import type { MetricsSnapshot } from "@/types"
import { Sparkline } from "@/components/ui/sparkline"
import { Tooltip } from "@/components/ui/tooltip"
import { PageHeader } from "@/components/ui/primitives"
import { Spinner } from "@/components/ui/primitives"
import { StartupCard } from "./startup-timeline"
import { StatPill } from "./stat-pill"
import { HealthPills } from "./health-pills"
import { AdvisoriesList } from "./advisories"
import { StorageActivity } from "./storage-activity"
import { AthenaEnginePanel } from "./athena-engine-panel"
import { DockerHealthPanel } from "./docker-health"
import { debugMetricsQueryOptions } from "./data"

// ─── Formatters ────────────────────────────────────────────────────────────

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

function formatMs(ms: number): string {
  if (ms < 0.01) return "< 0.01 ms"
  if (ms < 1) return `${ms.toFixed(2)} ms`
  return `${ms.toFixed(1)} ms`
}

// ─── MetricCard ────────────────────────────────────────────────────────────

interface MetricCardProps {
  title: string
  value: string
  sub?: string
  info?: string
  sparkData: number[]
  color: string
}

function MetricCard({ title, value, sub, info, sparkData, color }: MetricCardProps) {
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-bg-elevated p-4">
      <div className="flex items-start justify-between">
        <div className="flex items-center gap-1">
          <p className={cn(fieldLabel, "text-fg-muted")}>{title}</p>
          {info && (
            <Tooltip content={info}>
              {/* A tooltip is not a label: it is not read until the control is hovered
                  or focused, and this button holds nothing but an icon — so it needs a
                  name of its own, and a 24px hit area (WCAG 2.5.8) around a 12px glyph. */}
              <button
                type="button"
                aria-label={`About ${title}`}
                className="-m-1.5 flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:text-fg"
              >
                <Info size={12} aria-hidden />
              </button>
            </Tooltip>
          )}
        </div>
      </div>

      <div className="flex items-end justify-between gap-2">
        <div>
          <p className="font-mono text-2xl font-semibold text-fg tabular-nums">{value}</p>
          {sub && <p className="mt-0.5 text-xs text-fg-muted">{sub}</p>}
        </div>
        <div className={color} style={{ minWidth: 100 }}>
          <Sparkline data={sparkData} color="currentColor" width={120} height={48} />
        </div>
      </div>
    </div>
  )
}

// ─── Component ─────────────────────────────────────────────────────────────

export function MetricsPage() {
  const { snapshots, latest, error } = useMetrics()
  const debugMetricsQuery = useQuery(debugMetricsQueryOptions())
  // Both halves of the result are rendered: the diagnostics when they're
  // there, and otherwise the reason they aren't (see debugMetricsQueryOptions
  // for why unavailability arrives as data rather than as a query error).
  const debug = debugMetricsQuery.data
  const diagnostics = debug?.available ? debug.metrics : undefined
  const unavailable = debug && !debug.available ? debug.reason : undefined

  const extract = (fn: (s: MetricsSnapshot) => number) => snapshots.map(fn)

  return (
    <div className="flex w-full flex-col gap-6">
      <PageHeader
        title="Metrics & Health"
        description="Storage health, advisories, and live Go runtime statistics — sampled every 3 seconds."
        actions={
          latest ? (
            <div className="flex items-center gap-1.5 rounded-full bg-success-muted px-2.5 py-1 font-mono text-xs font-medium text-success">
              <span className="h-1.5 w-1.5 rounded-full bg-success" />
              Live
            </div>
          ) : error ? (
            <div className="flex items-center gap-1.5 rounded-full bg-danger-muted px-2.5 py-1 font-mono text-xs font-medium text-danger">
              <AlertCircle className="h-3 w-3" />
              Disconnected
            </div>
          ) : (
            <div className="flex items-center gap-1.5 text-xs text-fg-muted">
              <Spinner className="h-3 w-3" />
              Connecting…
            </div>
          )
        }
      />

      {error && (
        <div className="flex items-center gap-2 rounded-md border border-danger/30 bg-danger-muted px-4 py-3 text-sm text-danger">
          <AlertCircle className="h-4 w-4 shrink-0" />
          {error}
        </div>
      )}

      {/* ── Summary band ──────────────────────────────────────────────── */}
      {/* Storage health and the static runtime facts share one wrapping row:
          each is a single scalar, and a wide window fits the lot on one or two
          lines. Uptime lives here, goroutines have a sparkline card below —
          neither is repeated. */}
      <div className="flex flex-wrap items-stretch gap-2">
        <HealthPills uptime={latest?.uptime} />
        {latest && (
          <>
            <StartupCard
              totalMs={latest.startup_duration_ms}
              preInitMs={latest.pre_init_ms}
              phases={latest.startup_phases}
            />
            <StatPill label="Go Version" value={latest.go_version} />
            <StatPill label="CPUs" value={String(latest.num_cpu)} />
            <StatPill label="GC Runs" value={String(latest.num_gc)} />
            <StatPill label="Started" value={new Date(latest.start_time).toLocaleTimeString()} />
          </>
        )}
      </div>

      {/* ── Sparkline metric cards ───────────────────────────────────── */}
      {!latest && !error && (
        <div className="flex items-center justify-center py-20">
          <Spinner className="h-6 w-6" />
        </div>
      )}

      {latest && (
        <div className="flex flex-col gap-2">
          <h2 className={cn(sectionLabel, "text-fg-muted")}>Runtime</h2>
          {/* One grid for all six, not four-up then a two-up remainder: the
              second row's cards were twice the width of the first row's for no
              reason but their count. */}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <MetricCard
              title="Heap Allocated"
              value={formatBytes(latest.heap_alloc_bytes)}
              sub={`of ${formatBytes(latest.heap_sys_bytes)} heap sys`}
              info="The heap is a region of memory used for data that needs to live beyond a single function call — like request objects, cached items, and queued messages. This shows how much heap memory is currently in use by live data. 'Heap sys' is the total amount the runtime has reserved from the operating system for heap use (some of it may be free, waiting to be reused)."
              sparkData={extract((s) => s.heap_alloc_bytes)}
              color="text-cat-6"
            />
            <MetricCard
              title="System Memory"
              value={formatBytes(latest.sys_bytes)}
              sub={`${formatBytes(latest.heap_inuse_bytes)} heap in-use`}
              info="Total memory the emulator process has obtained from the operating system. This includes everything: the heap (long-lived data), the stack (short-lived function call data), and internal bookkeeping. 'Heap in-use' is the portion of the heap that currently holds live data, as opposed to free space waiting to be reused."
              sparkData={extract((s) => s.sys_bytes)}
              color="text-cat-8"
            />
            <MetricCard
              title="Goroutines"
              value={String(latest.goroutines)}
              sub="concurrent goroutines"
              info="Goroutines are lightweight threads managed by the Go runtime. Each handles a concurrent task like serving a request or running a background job."
              sparkData={extract((s) => s.goroutines)}
              color="text-cat-5"
            />
            <MetricCard
              title="Last GC Pause"
              value={formatMs(latest.gc_pause_last_ms)}
              sub={`${formatMs(latest.gc_pause_total_ms)} total`}
              info="Garbage collection (GC) is the process that automatically finds and frees memory that is no longer being used. During a GC pause, the program is briefly stopped while the runtime cleans up. This shows how long the most recent pause lasted. Lower is better — pauses above 1 ms may cause noticeable latency spikes in request handling."
              sparkData={extract((s) => s.gc_pause_last_ms)}
              color="text-cat-3"
            />
            <MetricCard
              title="Stack In-use"
              value={formatBytes(latest.stack_inuse_bytes)}
              sub="goroutine stacks"
              info="The stack is a region of memory where each goroutine stores its local variables and tracks which functions it is currently executing. Every goroutine gets its own small stack that grows automatically as needed. This shows the total memory used by all goroutine stacks combined. High values usually mean there are many active goroutines or deeply nested function calls."
              sparkData={extract((s) => s.stack_inuse_bytes)}
              color="text-cat-10"
            />
            <MetricCard
              title="Next GC Target"
              value={formatBytes(latest.next_gc_bytes)}
              sub="heap threshold for next GC"
              info="Garbage collection (GC) runs automatically when the heap grows large enough. This value is the heap size threshold that will trigger the next GC cycle. The Go runtime adjusts this target dynamically — by default, it allows the heap to roughly double in size before collecting again. A rising target means the program is holding more live data over time."
              sparkData={extract((s) => s.next_gc_bytes)}
              color="text-cat-2"
            />
          </div>
          {/* The sampling footnote belongs to the cards it describes, not to
              the foot of the page. */}
          <p className="text-xs text-fg-subtle">
            Last sample:{" "}
            {new Date(latest.timestamp).toLocaleTimeString(undefined, {
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            })}{" "}
            &middot; {snapshots.length} samples collected
          </p>
        </div>
      )}

      {/* ── Advisories ────────────────────────────────────────────────── */}
      <AdvisoriesList
        advisories={diagnostics?.advisories}
        isLoading={debugMetricsQuery.isLoading}
        error={unavailable}
      />

      {/* ── Storage activity (reads/writes, memory vs SQL for hybrid) ───── */}
      <StorageActivity stores={diagnostics?.stores} isLoading={debugMetricsQuery.isLoading} />

      {/* ── Athena query engine ───────────────────────────────────────── */}
      <AthenaEnginePanel />

      {/* ── Docker connectivity ────────────────────────────────────────── */}
      <DockerHealthPanel />
    </div>
  )
}

export { BarChart2 as MetricsIcon }
