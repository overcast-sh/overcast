import { describe, expect, it } from "vitest"
import {
  formatAge,
  formatCount,
  formatDuration,
  formatPreciseTimeOfDay,
  formatQuantity,
} from "./format"

describe("formatPreciseTimeOfDay", () => {
  it("includes milliseconds so closely spaced scheduler events remain distinguishable", () => {
    // Given: an event timestamp with sub-second precision.
    const timestamp = "2026-08-05T07:06:31.482Z"

    // When: the compact event time is formatted.
    const formatted = formatPreciseTimeOfDay(timestamp)

    // Then: the viewer can distinguish multiple events emitted within the same second.
    expect(formatted).toMatch(/:\d{2}\.482/)
  })
})

describe("formatCount", () => {
  it("groups thousands", () => {
    expect(formatCount(20000)).toBe((20000).toLocaleString())
    expect(formatCount(20000)).toMatch(/^20.000$/)
  })

  it("leaves small numbers alone", () => {
    expect(formatCount(0)).toBe("0")
    expect(formatCount(7)).toBe("7")
  })
})

describe("formatQuantity", () => {
  it("uses the singular for exactly one and the plural otherwise", () => {
    expect(formatQuantity(1, "row")).toBe("1 row")
    expect(formatQuantity(0, "row")).toBe("0 rows")
    expect(formatQuantity(2, "row group")).toBe("2 row groups")
  })

  it("groups the count as formatCount does", () => {
    expect(formatQuantity(1204, "row")).toBe(`${formatCount(1204)} rows`)
  })

  it("takes an irregular plural", () => {
    expect(formatQuantity(3, "entry", "entries")).toBe("3 entries")
  })
})

describe("formatAge", () => {
  it("rounds to seconds, then minutes, then hours", () => {
    expect(formatAge(1_400)).toBe("1s ago")
    expect(formatAge(59_000)).toBe("59s ago")
    expect(formatAge(60_000)).toBe("1m ago")
    expect(formatAge(59 * 60_000)).toBe("59m ago")
    expect(formatAge(3 * 3_600_000 + 5_000)).toBe("3h ago")
  })

  it("never reports a negative age", () => {
    expect(formatAge(-2_000)).toBe("0s ago")
  })
})

describe("formatDuration", () => {
  it("keeps the precision that still means something at each scale", () => {
    expect(formatDuration(340)).toBe("340 ms")
    expect(formatDuration(2_400)).toBe("2.40 s")
    expect(formatDuration(42_000)).toBe("42.0 s")
    expect(formatDuration(192_000)).toBe("3 m 12 s")
    expect(formatDuration(3_840_000)).toBe("1 h 4 m")
  })

  it("renders a missing span as an em dash", () => {
    expect(formatDuration(undefined)).toBe("—")
    expect(formatDuration(Number.NaN)).toBe("—")
  })
})
