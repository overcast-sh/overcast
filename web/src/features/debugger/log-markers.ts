import type { LogViewerEvent } from "@/components/logs/log-viewer"
import type { ConsoleEntry } from "./session/session"

/** What the marker rows say they came from, in the viewer's stream column. */
const MARKER_STREAM = "debugger"

/** The session's pause/resume markers as log viewer rows. */
export function markerEvents(entries: readonly ConsoleEntry[]): LogViewerEvent[] {
  return entries
    .filter((e) => e.kind === "marker")
    .map((e) => ({
      timestamp: e.timestamp,
      message: `── ${e.text} ──`,
      logStreamName: MARKER_STREAM,
    }))
}

/** Both sources in time order; a marker and an event at the same millisecond keep the event first. */
export function mergeByTime(events: LogViewerEvent[], markers: LogViewerEvent[]): LogViewerEvent[] {
  return [...events, ...markers]
    .map((event, index) => ({ event, index }))
    .sort((a, b) => (a.event.timestamp ?? 0) - (b.event.timestamp ?? 0) || a.index - b.index)
    .map(({ event }) => event)
}
