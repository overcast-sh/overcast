/**
 * Logs: the function's CloudWatch log group, live, with the session's
 * pause and resume markers interleaved by time (docs/plans/
 * compute-debugger-console.md § 3.5). The same `LogViewer` the Monitor
 * tab draws, fed here rather than through `LogPanel` because the rows are
 * two sources merged: the query's events and the session's markers.
 *
 * The invoke stream does not carry a request id — it reports progress
 * steps and then the result — so this is the whole tail of the group over
 * the last 15 minutes, polled while the drawer is open, and the note says
 * so. A per-request filter is the day the stream names the request.
 */
import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { LogViewer } from "@/components/logs/log-viewer"
import { logPanelQueryOptions } from "@/features/cloudwatch/logs/data"
import type { LogFilter } from "@/features/cloudwatch/logs/log-filter"
import { markerEvents, mergeByTime } from "../log-markers"
import { useDebugSessionState } from "../session/hooks"

const POLL_MS = 2_000
const LIMIT = 200

export function DebugLogs({ logGroup }: { logGroup: string | null }) {
  const consoleEntries = useDebugSessionState((s) => s.console)
  const filter = useMemo<LogFilter>(
    () => ({ group: logGroup ?? "", time: { kind: "relative", token: "15m" }, limit: LIMIT }),
    [logGroup],
  )
  const { data, isLoading, isError, error } = useQuery({
    ...logPanelQueryOptions(filter),
    enabled: logGroup !== null,
    refetchInterval: POLL_MS,
  })

  const rows = useMemo(
    () => mergeByTime((data?.events ?? []).slice(-LIMIT), markerEvents(consoleEntries)),
    [data, consoleEntries],
  )

  if (logGroup === null) {
    return <p className="text-xs text-fg-muted">No log group is configured for this function.</p>
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-1">
      <p className="text-2xs text-fg-muted">
        Live tail of <span className="font-mono">{logGroup}</span>, last 15 minutes — the invoke
        stream carries no request id, so this is the whole group, not one request. Pauses and
        resumes are marked in place.
      </p>
      <LogViewer
        events={rows}
        loading={isLoading}
        error={
          isError ? (error instanceof Error ? error.message : "Failed to load log events") : null
        }
        emptyMessage="No log events in the last 15 minutes."
        defaultMode="table"
        showModeToggle={false}
        follow
        className="min-h-0 flex-1"
      />
    </div>
  )
}
