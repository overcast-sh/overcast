import { describe, expect, it } from "vitest"
import { formatAge, formatCount, formatPreciseTimeOfDay } from "./format"

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
