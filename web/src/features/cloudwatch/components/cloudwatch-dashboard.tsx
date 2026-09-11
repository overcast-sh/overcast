/**
 * The CloudWatch Metrics page: a namespace-grouped metric browser on the
 * left, the selected metric's chart, summary and datapoints on the right,
 * and the alarms panel underneath.
 *
 * The chart is the Monitor tab's `MetricLineChart` — axes, gridlines, hover
 * readout and a legend summary — rather than a bare polyline, so a metric
 * here reads the way the same metric reads on its service's Monitor tab.
 * Layout is defensive about width throughout: every metric name, namespace
 * and dimension value is one long token more often than not (a function ARN,
 * a table name with a stage suffix), and each sits on a `min-w-0` column
 * that either truncates with a title or wraps, never spills.
 */
import { useMemo, useState } from "react"
import { Link } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import type { Datapoint, Dimension, Metric, Statistic } from "@aws-sdk/client-cloudwatch"
import { Activity, RefreshCw, ScrollText, Search, X } from "lucide-react"
import {
  cloudwatchAlarmsQueryOptions,
  cloudwatchMetricsQueryOptions,
  cloudwatchMetricStatisticsQueryOptions,
  metricIdentity,
} from "@/features/cloudwatch/metrics/data"
import { AlarmsPanel } from "@/features/cloudwatch/components/alarms-panel"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import {
  MetricLineChart,
  type ChartSeriesInput,
} from "@/features/monitoring/components/metric-line-chart"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { QueryListState, PageHeader } from "@/components/ui/primitives"
import { Select } from "@/components/ui/select"
import { formatDate } from "@/lib/format"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"

const STAT_OPTIONS: Statistic[] = ["Average", "Sum", "SampleCount", "Minimum", "Maximum"]

/**
 * Range presets with the period that keeps each under GetMetricStatistics'
 * 1,440-datapoint ceiling while staying at a multiple of 60 seconds.
 */
const RANGE_OPTIONS = [
  { hours: 1, label: "Last 1 hour", period: 60 },
  { hours: 6, label: "Last 6 hours", period: 300 },
  { hours: 24, label: "Last 24 hours", period: 900 },
  { hours: 24 * 7, label: "Last 7 days", period: 3600 },
  { hours: 24 * 30, label: "Last 30 days", period: 3600 },
] as const

function formatPeriod(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

function readDatapointValue(datapoint: Datapoint, stat: Statistic): number | undefined {
  switch (stat) {
    case "Sum":
      return datapoint.Sum
    case "SampleCount":
      return datapoint.SampleCount
    case "Minimum":
      return datapoint.Minimum
    case "Maximum":
      return datapoint.Maximum
    case "Average":
    default:
      return datapoint.Average
  }
}

const numberFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 })

/**
 * The common CloudWatch units abbreviated the way the chart abbreviates
 * them, so a summary tile reads "62.5 ms" rather than a truncated
 * "62.5 Millis…"; anything else keeps AWS's own unit name.
 */
const UNIT_ABBREVIATIONS: Record<string, string> = {
  Count: "",
  None: "",
  Milliseconds: "ms",
  Microseconds: "µs",
  Seconds: "s",
  Bytes: "B",
  Kilobytes: "KB",
  Megabytes: "MB",
  Gigabytes: "GB",
  Percent: "%",
  "Bytes/Second": "B/s",
  "Count/Second": "/s",
}

function formatValue(value: number | undefined, unit?: string): string {
  if (value == null) return "—"
  const text = numberFormat.format(value)
  const suffix = unit == null ? "" : (UNIT_ABBREVIATIONS[unit] ?? unit)
  return suffix ? `${text} ${suffix}` : text
}

/** What the metric browser's filter matches against — everything visible on the row. */
function metricSearchText(metric: Metric): string {
  const dimensions = (metric.Dimensions ?? []).map((d) => `${d.Name}=${d.Value}`).join(" ")
  return `${metric.Namespace ?? ""} ${metric.MetricName ?? ""} ${dimensions}`.toLowerCase()
}

/**
 * A metric's dimensions as chips — one per `Name=Value`, each truncating on
 * its own so a long value (a function ARN, a stream name) shortens itself
 * rather than pushing the row wide. The joined "a=b, c=d" string this
 * replaced was the wrapping problem: one unbreakable run of text.
 */
function DimensionChips({
  dimensions,
  className,
}: {
  dimensions?: Dimension[]
  className?: string
}) {
  if (!dimensions?.length) {
    return <div className={cn("text-xs text-fg-subtle", className)}>No dimensions</div>
  }
  return (
    <div className={cn("flex min-w-0 flex-wrap gap-1", className)}>
      {dimensions.map((dimension) => (
        <span
          key={`${dimension.Name}=${dimension.Value}`}
          title={`${dimension.Name}=${dimension.Value}`}
          className="inline-flex max-w-full min-w-0 items-baseline rounded border border-border bg-bg-muted px-1.5 py-0.5 font-mono text-2xs"
        >
          <span className="shrink-0 text-fg-muted">{dimension.Name}=</span>
          <span className="min-w-0 truncate text-fg">{dimension.Value}</span>
        </span>
      ))}
    </div>
  )
}

/** One headline number over the selected range. */
function StatTile({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-lg border border-border bg-bg px-3 py-2">
      <div className={cn(fieldLabel, "text-fg-subtle")}>{label}</div>
      <div className="mt-0.5 truncate font-mono text-sm text-fg tabular-nums" title={value}>
        {value}
      </div>
    </div>
  )
}

export function CloudwatchDashboard() {
  const [selectedMetricId, setSelectedMetricId] = useState<string>()
  const [selectedStat, setSelectedStat] = useState<Statistic>("Average")
  const [rangeHours, setRangeHours] = useState<number>(1)
  const [metricFilter, setMetricFilter] = useState("")
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()
  // The chart's window ends where the statistics were last read (the queryFn
  // stamps its own endTime at fetch time); until the first read lands, at
  // the moment the page opened. Never a bare Date.now() in render.
  const [openedAtMs] = useState(() => Date.now())

  const metricsQuery = useQuery(cloudwatchMetricsQueryOptions())
  const alarmsQuery = useQuery(cloudwatchAlarmsQueryOptions())

  const metrics = useMemo(() => metricsQuery.data ?? [], [metricsQuery.data])

  const selectedMetric = useMemo(
    () => metrics.find((metric) => metricIdentity(metric) === selectedMetricId) ?? metrics.at(0),
    [metrics, selectedMetricId],
  )
  const selectedIdentity = selectedMetric ? metricIdentity(selectedMetric) : undefined

  // Grouped by namespace, each group sorted by name then dimensions, so a
  // namespace's metrics read as a block rather than interleaving with every
  // other namespace's in publish order.
  const groups = useMemo(() => {
    const needle = metricFilter.trim().toLowerCase()
    const byNamespace = new Map<string, Metric[]>()
    for (const metric of metrics) {
      if (needle && !metricSearchText(metric).includes(needle)) continue
      const namespace = metric.Namespace ?? "(no namespace)"
      const bucket = byNamespace.get(namespace)
      if (bucket) bucket.push(metric)
      else byNamespace.set(namespace, [metric])
    }
    return [...byNamespace.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([namespace, items]) => ({
        namespace,
        items: [...items].sort((a, b) => metricIdentity(a).localeCompare(metricIdentity(b))),
      }))
  }, [metrics, metricFilter])
  const shownCount = groups.reduce((n, group) => n + group.items.length, 0)

  const rangeConfig =
    RANGE_OPTIONS.find((option) => option.hours === rangeHours) ?? RANGE_OPTIONS[0]

  const statisticsQuery = useQuery(
    cloudwatchMetricStatisticsQueryOptions({
      namespace: selectedMetric?.Namespace ?? "",
      metricName: selectedMetric?.MetricName ?? "",
      dimensions: selectedMetric?.Dimensions ?? [],
      stat: selectedStat,
      period: rangeConfig.period,
      rangeHours: rangeConfig.hours,
    }),
  )
  const datapoints = useMemo(() => statisticsQuery.data?.datapoints ?? [], [statisticsQuery.data])
  const unit = datapoints.find((d) => d.Unit)?.Unit ?? "None"

  const chartSeries = useMemo<ChartSeriesInput[]>(
    () => [
      {
        key: selectedStat,
        label: selectedStat,
        unit,
        statistic: selectedStat,
        points: datapoints.flatMap((datapoint) => {
          const value = readDatapointValue(datapoint, selectedStat)
          return value != null && datapoint.Timestamp
            ? [{ timestamp: datapoint.Timestamp.toISOString(), value }]
            : []
        }),
      },
    ],
    [datapoints, selectedStat, unit],
  )
  const rangeEndMs = statisticsQuery.dataUpdatedAt || openedAtMs
  const rangeStartMs = rangeEndMs - rangeConfig.hours * 60 * 60 * 1000

  const summary = useMemo(() => {
    const values = datapoints
      .map((datapoint) => readDatapointValue(datapoint, selectedStat))
      .filter((value): value is number => value != null)
    if (values.length === 0) return null
    return {
      latest: values[values.length - 1],
      min: Math.min(...values),
      max: Math.max(...values),
      mean: values.reduce((a, b) => a + b, 0) / values.length,
    }
  }, [datapoints, selectedStat])

  // Newest first in the table: the chart already shows the shape over time,
  // and what a reader scanning a table wants is the most recent bucket.
  const tableRows = useMemo(() => [...datapoints].reverse(), [datapoints])

  const refetchAll = () => {
    void metricsQuery.refetch()
    void alarmsQuery.refetch()
    void statisticsQuery.refetch()
  }

  return (
    <div className="flex w-full min-w-0 flex-col gap-4">
      <PageHeader
        title="CloudWatch Metrics"
        description="Browse published metrics, inspect aggregated datapoints, and review matching alarms."
        actions={
          <>
            <ServiceDocsButton
              service="cloudwatch"
              label="CloudWatch"
              open={docsOpen}
              onOpen={openDocs}
              onClose={closeDocs}
            />
            <Button
              size="sm"
              variant="ghost"
              onClick={refetchAll}
              disabled={metricsQuery.isFetching}
            >
              <RefreshCw
                className={cn("mr-1.5 h-3.5 w-3.5", metricsQuery.isFetching && "animate-spin")}
              />
              Refresh
            </Button>
            <Button size="sm" variant="ghost" asChild>
              <Link to="/cloudwatch/logs">
                <ScrollText className="mr-1.5 h-3.5 w-3.5" />
                Open Logs
              </Link>
            </Button>
          </>
        }
      />

      <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        {/* Metric browser */}
        <section className="flex min-w-0 flex-col rounded-xl border border-border bg-bg-elevated p-4">
          <div className="mb-3 flex items-baseline justify-between gap-3">
            <h2 className="font-mono text-sm font-semibold text-fg">Metrics</h2>
            <p className="font-mono text-2xs text-fg-muted tabular-nums">
              {metricFilter
                ? `${shownCount} of ${metrics.length}`
                : `${metrics.length} metric${metrics.length === 1 ? "" : "s"}`}
            </p>
          </div>

          {metrics.length > 0 && (
            <div className="relative mb-3">
              <Search
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-fg-muted"
              />
              <Input
                value={metricFilter}
                onChange={(event) => setMetricFilter(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setMetricFilter("")
                }}
                placeholder="Filter by name, namespace or dimension"
                aria-label="Filter metrics"
                className="pr-8 pl-8"
              />
              {metricFilter && (
                <button
                  type="button"
                  onClick={() => setMetricFilter("")}
                  aria-label="Clear metric filter"
                  className="absolute top-1/2 right-2 -translate-y-1/2 rounded p-0.5 text-fg-muted hover:text-fg"
                >
                  <X aria-hidden className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
          )}

          <QueryListState
            isLoading={metricsQuery.isLoading}
            isEmpty={metrics.length === 0}
            error={metricsQuery.error}
            emptyIcon={<Activity className="h-10 w-10" />}
            emptyTitle="No metrics published yet"
            emptyDescription="Publish CloudWatch datapoints and they will appear here."
            errorTitle="Unable to load metrics"
          />

          {metrics.length > 0 && shownCount === 0 && (
            <p className="py-6 text-center text-sm text-fg-muted">No metrics match the filter.</p>
          )}

          {shownCount > 0 && (
            <div className="min-h-0 space-y-4 lg:max-h-[calc(100vh-18rem)] lg:overflow-y-auto">
              {groups.map((group) => (
                <div key={group.namespace}>
                  <div
                    className={cn(
                      sectionLabel,
                      "sticky top-0 z-[1] mb-1.5 flex items-baseline justify-between gap-2 bg-bg-elevated py-1 text-fg-muted",
                    )}
                  >
                    <span className="min-w-0 truncate" title={group.namespace}>
                      {group.namespace}
                    </span>
                    <span className="shrink-0 tabular-nums">{group.items.length}</span>
                  </div>
                  <div className="space-y-1.5">
                    {group.items.map((metric) => {
                      const identity = metricIdentity(metric)
                      const isSelected = identity === selectedIdentity
                      return (
                        <button
                          key={identity}
                          type="button"
                          onClick={() => setSelectedMetricId(identity)}
                          aria-pressed={isSelected}
                          className={cn(
                            "w-full min-w-0 rounded-lg border px-3 py-2 text-left transition-colors",
                            isSelected
                              ? "border-accent bg-accent-muted/30"
                              : "border-border bg-bg hover:border-accent",
                          )}
                        >
                          <div
                            className="truncate font-mono text-sm font-medium text-fg"
                            title={metric.MetricName}
                          >
                            {metric.MetricName}
                          </div>
                          <DimensionChips dimensions={metric.Dimensions} className="mt-1" />
                        </button>
                      )
                    })}
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        {/* Selected metric */}
        <section className="min-w-0 space-y-4 rounded-xl border border-border bg-bg-elevated p-4">
          {selectedMetric ? (
            <>
              <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
                <div className="min-w-0 flex-1 basis-64 space-y-1.5">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <h2
                      className="min-w-0 font-mono text-lg font-semibold break-all text-fg"
                      title={selectedMetric.MetricName}
                    >
                      {selectedMetric.MetricName}
                    </h2>
                    <Badge variant="outline" className="max-w-full truncate">
                      {selectedMetric.Namespace}
                    </Badge>
                  </div>
                  <DimensionChips dimensions={selectedMetric.Dimensions} />
                </div>
                <div className="flex flex-wrap items-end gap-2">
                  <label className={cn(fieldLabel, "flex flex-col gap-1 text-fg-muted")}>
                    Statistic
                    <Select
                      value={selectedStat}
                      onChange={(event) => setSelectedStat(event.target.value as Statistic)}
                      className="w-36"
                    >
                      {STAT_OPTIONS.map((stat) => (
                        <option key={stat} value={stat}>
                          {stat}
                        </option>
                      ))}
                    </Select>
                  </label>
                  <label className={cn(fieldLabel, "flex flex-col gap-1 text-fg-muted")}>
                    Time range
                    <Select
                      value={String(rangeHours)}
                      onChange={(event) => setRangeHours(Number(event.target.value))}
                      className="w-40"
                    >
                      {RANGE_OPTIONS.map((option) => (
                        <option key={option.hours} value={option.hours}>
                          {option.label}
                        </option>
                      ))}
                    </Select>
                  </label>
                </div>
              </div>

              <QueryListState
                isLoading={statisticsQuery.isLoading}
                isEmpty={datapoints.length === 0}
                error={statisticsQuery.error}
                emptyIcon={<Activity className="h-10 w-10" />}
                emptyTitle="No datapoints in range"
                emptyDescription="Try a wider time range or publish more metric data."
                errorTitle="Unable to load metric statistics"
              />

              {datapoints.length > 0 && (
                <>
                  {summary && (
                    <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                      <StatTile label="Latest" value={formatValue(summary.latest, unit)} />
                      <StatTile
                        label={
                          selectedStat === "Sum" || selectedStat === "SampleCount"
                            ? "Total"
                            : "Mean"
                        }
                        value={formatValue(
                          selectedStat === "Sum" || selectedStat === "SampleCount"
                            ? summary.mean * datapoints.length
                            : summary.mean,
                          unit,
                        )}
                      />
                      <StatTile label="Min" value={formatValue(summary.min, unit)} />
                      <StatTile label="Max" value={formatValue(summary.max, unit)} />
                    </div>
                  )}

                  <div className="space-y-2">
                    <MetricLineChart
                      series={chartSeries}
                      rangeStartMs={rangeStartMs}
                      rangeEndMs={rangeEndMs}
                      periodSeconds={rangeConfig.period}
                      height={220}
                    />
                    <p className="text-xs text-fg-muted">
                      {selectedStat} per {formatPeriod(rangeConfig.period)} period over the{" "}
                      {rangeConfig.label.toLowerCase()} — local emulator data only, not the real AWS
                      CloudWatch console.
                    </p>
                  </div>

                  <div className="min-w-0 rounded-lg border border-border bg-bg p-4">
                    <div className="mb-3 flex items-baseline justify-between gap-2">
                      <h3 className="font-mono text-sm font-semibold text-fg">Datapoints</h3>
                      <span className="font-mono text-2xs text-fg-muted tabular-nums">
                        {datapoints.length} returned · newest first
                      </span>
                    </div>
                    <div tabIndex={0} className="max-h-80 overflow-auto">
                      <table className="w-full font-mono text-xs whitespace-nowrap">
                        <thead className="sticky top-0 bg-bg">
                          <tr
                            className={cn(
                              fieldLabel,
                              "border-b border-border text-left text-fg-muted",
                            )}
                          >
                            <th className="py-2 pr-4 font-medium">Timestamp</th>
                            <th className="py-2 pr-4 text-right font-medium">{selectedStat}</th>
                            <th className="py-2 font-medium">Unit</th>
                          </tr>
                        </thead>
                        <tbody>
                          {tableRows.map((datapoint, index) => (
                            <tr
                              key={`${datapoint.Timestamp?.toISOString() ?? index}`}
                              className="border-b border-border/60 last:border-0"
                            >
                              <td className="py-1.5 pr-4 text-fg-muted">
                                {formatDate(datapoint.Timestamp)}
                              </td>
                              <td className="py-1.5 pr-4 text-right text-fg tabular-nums">
                                {formatValue(readDatapointValue(datapoint, selectedStat))}
                              </td>
                              <td className="py-1.5 text-fg-muted">{datapoint.Unit ?? "—"}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </div>
                </>
              )}
            </>
          ) : (
            <div className="flex min-h-64 items-center justify-center text-sm text-fg-muted">
              Select a metric to inspect its datapoints.
            </div>
          )}
        </section>
      </div>

      <AlarmsPanel
        highlightNamespace={selectedMetric?.Namespace}
        highlightMetricName={selectedMetric?.MetricName}
      />
    </div>
  )
}
