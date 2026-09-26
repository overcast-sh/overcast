/**
 * The stream viewer's display preferences, persisted across visits.
 *
 * One versioned localStorage key holding one JSON object: read once at mount
 * (a lazy `useState` initializer — never during a render path that runs per
 * row or per scroll frame), written once per change. Anything corrupt,
 * missing, or of the wrong type falls back to the defaults silently — a
 * preferences store must never be the reason a log viewer fails to open.
 *
 * Live-session state (the Tail toggle, the cleared-through cut, per-row
 * expansion) deliberately does not persist: those describe *this* visit, not
 * how the user likes logs rendered.
 */
import { useCallback, useEffect, useRef, useState } from "react"
import { isRecord } from "@/lib/utils"

export interface LogViewPrefs {
  displayMode: "table" | "plain"
  formatted: boolean
  syntaxHighlight: boolean
  wrapLines: boolean
  sortAsc: boolean
  /** Render timestamps in UTC instead of local time. */
  utc: boolean
  /** Show the gap to the chronologically previous event beside the time. */
  showDeltas: boolean
  /**
   * Render every row as one fixed-height truncated line, expandable per row.
   * Default OFF: the AWS console collapses by default, and flipping ours to
   * match is a deliberate product decision left open in the plan doc.
   */
  collapsed: boolean
}

export const LOG_VIEW_PREFS_KEY = "overcast.logs.viewPrefs.v1"

export const defaultLogViewPrefs: LogViewPrefs = {
  displayMode: "table",
  formatted: false,
  syntaxHighlight: true,
  wrapLines: true,
  sortAsc: true,
  utc: false,
  showDeltas: false,
  collapsed: false,
}

function readBool(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback
}

/** One read, at initialization. Every failure shape lands on the defaults. */
function loadPrefs(): LogViewPrefs {
  try {
    const raw = localStorage.getItem(LOG_VIEW_PREFS_KEY)
    if (!raw) return defaultLogViewPrefs
    const stored: unknown = JSON.parse(raw)
    if (!isRecord(stored)) return defaultLogViewPrefs
    return {
      displayMode: stored.displayMode === "plain" ? "plain" : defaultLogViewPrefs.displayMode,
      formatted: readBool(stored.formatted, defaultLogViewPrefs.formatted),
      syntaxHighlight: readBool(stored.syntaxHighlight, defaultLogViewPrefs.syntaxHighlight),
      wrapLines: readBool(stored.wrapLines, defaultLogViewPrefs.wrapLines),
      sortAsc: readBool(stored.sortAsc, defaultLogViewPrefs.sortAsc),
      utc: readBool(stored.utc, defaultLogViewPrefs.utc),
      showDeltas: readBool(stored.showDeltas, defaultLogViewPrefs.showDeltas),
      collapsed: readBool(stored.collapsed, defaultLogViewPrefs.collapsed),
    }
  } catch {
    return defaultLogViewPrefs
  }
}

export function useLogViewPrefs() {
  const [prefs, setPrefs] = useState<LogViewPrefs>(loadPrefs)

  // Written from an effect so the state updater stays pure, and skipped on the
  // first run so mounting never writes: one serialized write per change.
  const mountedRef = useRef(false)
  useEffect(() => {
    if (!mountedRef.current) {
      mountedRef.current = true
      return
    }
    try {
      localStorage.setItem(LOG_VIEW_PREFS_KEY, JSON.stringify(prefs))
    } catch {
      // Storage full or unavailable — the session keeps its in-memory prefs.
    }
  }, [prefs])

  const setPref = useCallback(<K extends keyof LogViewPrefs>(key: K, value: LogViewPrefs[K]) => {
    setPrefs((prev) => (prev[key] === value ? prev : { ...prev, [key]: value }))
  }, [])

  return { prefs, setPref }
}
