import type { TraceCountResponse } from "@/types"
import { formatQuantity } from "@/lib/format"

/**
 * What to say at the end of the trace list.
 *
 * A list that simply stops is indistinguishable from a bug: the reader cannot
 * tell whether the request they are looking for was never traced, or was traced
 * and reclaimed. This turns the retention counters into that sentence.
 *
 * Returns null when nothing has been reclaimed — there is no honest sentence to
 * write, and "0 traces dropped" is noise on a fresh emulator.
 */
export interface RetentionNotice {
  /** e.g. "1,204 older traces dropped" */
  headline: string
  /** The rules that reclaimed them, in the order they are worth reading. */
  reasons: string[]
}

const NUMBER = new Intl.NumberFormat()

/** "1h" / "45m" / "90s" from a Go duration in nanoseconds. */
export function formatWindow(nanos: number): string {
  const seconds = Math.round(nanos / 1e9)
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

export function describeRetention(count: TraceCountResponse | undefined): RetentionNotice | null {
  if (!count?.dropped) return null

  const { capacity = 0, aged = 0, bytes = 0, pinnedCap = 0 } = count.dropped
  // Internal polling recycles itself constantly and is deliberately excluded:
  // it would swamp the number a reader actually cares about.
  const total = capacity + aged + bytes + pinnedCap
  if (total === 0) return null

  const reasons: string[] = []
  if (aged > 0) {
    reasons.push(
      count.window
        ? `${NUMBER.format(aged)} aged out after ${formatWindow(count.window)}`
        : `${NUMBER.format(aged)} aged out`,
    )
  }
  if (capacity > 0) {
    reasons.push(`${NUMBER.format(capacity)} pushed out by newer traces`)
  }
  if (bytes > 0) {
    reasons.push(`${NUMBER.format(bytes)} reclaimed to stay within the memory budget`)
  }
  if (pinnedCap > 0) {
    reasons.push(`${NUMBER.format(pinnedCap)} older failures displaced by newer ones`)
  }

  return {
    headline: `${formatQuantity(total, "older trace")} no longer retained`,
    reasons,
  }
}
