export function formatBytes(bytes: number, decimals = 1): string {
  if (bytes === 0) return "0 B"
  const k = 1024
  const sizes = ["B", "KB", "MB", "GB", "TB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))} ${sizes[i]}`
}

/**
 * A count for display, grouped by thousands: 20000 reads as 20,000.
 *
 * Here rather than inline at each call site so every count in the UI groups
 * the same way — which is also what `local/prefer-shared-formatter`
 * asks for when it flags a bare `toLocaleString`.
 */
export function formatCount(value: number): string {
  return value.toLocaleString()
}

/**
 * A count with its noun, singular for exactly one: `1 row`, `1,204 rows`,
 * `2 row groups`. `plural` is for the nouns an `s` does not make plural.
 */
export function formatQuantity(count: number, noun: string, plural = `${noun}s`): string {
  return `${formatCount(count)} ${count === 1 ? noun : plural}`
}

export function formatDate(date: string | Date | number | undefined): string {
  if (!date) return "—"
  try {
    return new Intl.DateTimeFormat(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }).format(
      typeof date === "number" ? new Date(date) : typeof date === "string" ? new Date(date) : date,
    )
  } catch {
    return String(date)
  }
}

/**
 * Wall-clock time of day in the viewer's locale, e.g. `14:22:07` — for "as of"
 * stamps, where the date is either implied or beside the point.
 */
export function formatTimeOfDay(value: number | string | Date): string {
  return new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

/** Precise wall-clock time for closely-spaced events where seconds alone are ambiguous. */
export function formatPreciseTimeOfDay(value: number | string | Date): string {
  return new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    fractionalSecondDigits: 3,
  })
}

/**
 * An elapsed time as the coarse "Xs ago" / "Xm ago" / "Xh ago" a live line
 * wants — a map node's last invoke, a debugger's attached-since. Negative
 * ages (a clock that is a little ahead of the server's) read as "0s ago", and
 * an age that is not a number (a timestamp the server left empty) as "—".
 */
export function formatAge(ms: number): string {
  if (!Number.isFinite(ms)) return "—"
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  return `${Math.floor(m / 60)}h ago`
}

/**
 * A span of time at the precision a reader wants for it: "340 ms", "2.40 s",
 * "3 m 12 s", "1 h 4 m" — sub-second work stays in milliseconds, long runs
 * drop the digits that stopped meaning anything. "—" when there is no span.
 */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined || !Number.isFinite(ms)) return "—"
  if (ms < 1000) return `${Math.round(ms)} ms`
  const s = ms / 1000
  if (s < 60) return `${s < 10 ? s.toFixed(2) : s.toFixed(1)} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} m ${Math.round(s % 60)} s`
  return `${Math.floor(m / 60)} h ${m % 60} m`
}

export function formatStorageClass(sc: string): string {
  return sc
    .replace(/_/g, " ")
    .toLowerCase()
    .replace(/\b\w/g, (c) => c.toUpperCase())
}

export function wordsFromIdentifier(value: string): string[] {
  return value
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .split(/[\s._-]+/)
    .map((part) => part.trim())
    .filter(Boolean)
}

export function toTitleCase(value: string): string {
  const words = wordsFromIdentifier(value)
  if (words.length === 0) return value
  return words.map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase()).join(" ")
}
