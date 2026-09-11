import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { AlarmHistoryItem, MetricAlarm } from "@aws-sdk/client-cloudwatch"
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react"
import {
  alarmVariant,
  comparisonSummary,
  formatAlarmDimensions,
} from "@/features/cloudwatch/alarms/display"
import {
  cloudwatchAlarmHistoryQueryOptions,
  cloudwatchAlarmsQueryOptions,
} from "@/features/cloudwatch/metrics/data"
import { Badge } from "@/components/ui/badge"
import { QueryListState } from "@/components/ui/primitives"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"

function formatTimestamp(value?: Date): string {
  if (!value) return "—"
  return value.toLocaleString()
}

/** Recent state transitions for one alarm, newest first. */
function AlarmHistory({ alarmName }: { alarmName: string }) {
  const historyQuery = useQuery(cloudwatchAlarmHistoryQueryOptions(alarmName))
  const items: AlarmHistoryItem[] = historyQuery.data ?? []

  return (
    <div className="mt-3 rounded-lg border border-border bg-bg p-3">
      <div className={cn(fieldLabel, "mb-2 text-fg-muted")}>Recent transitions</div>
      <QueryListState
        isLoading={historyQuery.isLoading}
        isEmpty={items.length === 0}
        error={historyQuery.error}
        emptyTitle="No history yet"
        emptyDescription="Alarm history records every state change, configuration update and action."
        errorTitle="Unable to load alarm history"
      />
      {items.length > 0 && (
        <ul className="space-y-1.5">
          {items.map((item, index) => (
            <li
              key={`${item.Timestamp?.toISOString() ?? index}-${item.HistoryItemType ?? ""}`}
              className="flex flex-col gap-0.5 border-b border-border/60 pb-1.5 text-xs last:border-0 last:pb-0 sm:flex-row sm:items-baseline sm:gap-2"
            >
              <span className="shrink-0 font-mono text-fg-muted">
                {formatTimestamp(item.Timestamp)}
              </span>
              <Badge variant="outline" className="shrink-0">
                {item.HistoryItemType ?? "—"}
              </Badge>
              <span className="text-fg">{item.HistorySummary}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** One alarm row: live state, why, and what is being evaluated. */
function AlarmRow({ alarm, highlighted }: { alarm: MetricAlarm; highlighted: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const alarmName = alarm.AlarmName ?? ""

  return (
    <div
      className={cn(
        "rounded-lg border p-3",
        highlighted ? "border-accent bg-accent-muted/20" : "border-border bg-bg-elevated",
      )}
    >
      {/* Every text line under the name is `break-words` on a `min-w-0`
          column: an alarm name, a comparison summary or a dimension list is
          one long token more often than not, and without both the card
          spilled past its border instead of wrapping. */}
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-3">
        <div className="min-w-0 flex-1">
          <button
            type="button"
            onClick={() => setExpanded((open) => !open)}
            aria-expanded={expanded}
            className="flex max-w-full items-start gap-1.5 text-left text-sm font-medium text-fg hover:text-accent"
          >
            {expanded ? (
              <ChevronDown className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            ) : (
              <ChevronRight className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            )}
            <span className="min-w-0 break-words">{alarmName || "(unnamed alarm)"}</span>
          </button>
          <div className="mt-1 font-mono text-xs break-words text-fg-muted">
            {comparisonSummary(alarm)}
          </div>
          <div className="text-xs break-words text-fg-muted">
            {alarm.Namespace ?? "—"} · {formatAlarmDimensions(alarm)}
          </div>
          {alarm.StateReason && (
            <p className="mt-1.5 text-xs break-words text-fg-muted">{alarm.StateReason}</p>
          )}
        </div>
        <div className="flex shrink-0 flex-row flex-wrap items-center gap-2 sm:flex-col sm:items-end sm:gap-1">
          <Badge variant={alarmVariant(alarm.StateValue)}>{alarm.StateValue ?? "UNKNOWN"}</Badge>
          <span className="text-xs whitespace-nowrap text-fg-muted">
            {formatTimestamp(alarm.StateUpdatedTimestamp)}
          </span>
          {alarm.ActionsEnabled === false && <Badge variant="outline">Actions disabled</Badge>}
        </div>
      </div>

      {expanded && alarmName && <AlarmHistory alarmName={alarmName} />}
    </div>
  )
}

/**
 * Live alarm state for every alarm in the emulator.
 *
 * Alarms are evaluated by a background loop, so this is the page's most
 * useful live surface: it shows what each alarm is watching, what state it is
 * in, the reason the evaluator gave, and — expanded — its recent transitions.
 * Alarms on the metric selected above are highlighted rather than filtered to,
 * because an alarm whose metric has never been published still matters.
 */
export function AlarmsPanel({
  highlightNamespace,
  highlightMetricName,
}: {
  highlightNamespace?: string
  highlightMetricName?: string
}) {
  const alarmsQuery = useQuery(cloudwatchAlarmsQueryOptions())
  const alarms = alarmsQuery.data ?? []

  const inAlarm = alarms.filter((alarm) => alarm.StateValue === "ALARM").length

  return (
    <section className="rounded-xl border border-border bg-bg-elevated p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h2 className="font-mono text-sm font-semibold text-fg">Alarms</h2>
          <p className="text-sm text-fg-muted">
            {alarms.length} alarm{alarms.length === 1 ? "" : "s"}
            {inAlarm > 0 ? ` · ${inAlarm} in ALARM` : ""}
          </p>
        </div>
      </div>

      <QueryListState
        isLoading={alarmsQuery.isLoading}
        isEmpty={alarms.length === 0}
        error={alarmsQuery.error}
        emptyIcon={<AlertTriangle className="h-10 w-10" />}
        emptyTitle="No alarms configured"
        emptyDescription="Create an alarm with PutMetricAlarm and its state will be evaluated automatically."
        errorTitle="Unable to load alarms"
      />

      {alarms.length > 0 && (
        <div className="space-y-2">
          {alarms.map((alarm) => (
            <AlarmRow
              key={alarm.AlarmArn ?? alarm.AlarmName}
              alarm={alarm}
              highlighted={
                Boolean(highlightMetricName) &&
                alarm.MetricName === highlightMetricName &&
                alarm.Namespace === highlightNamespace
              }
            />
          ))}
        </div>
      )}
    </section>
  )
}
