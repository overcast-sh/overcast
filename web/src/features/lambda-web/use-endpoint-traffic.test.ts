import { describe, expect, it } from "vitest"
import { binPerMinute } from "./use-endpoint-traffic"

const NOW = Date.UTC(2026, 9, 18, 12, 0, 0)
const minutesAgo = (n: number) => new Date(NOW - n * 60_000)

describe("binPerMinute", () => {
  it("gives an hour of zeros for no datapoints", () => {
    const bins = binPerMinute([], NOW)
    expect(bins).toHaveLength(60)
    expect(bins.every((b) => b === 0)).toBe(true)
  })

  it("places each datapoint in its minute, oldest first", () => {
    const bins = binPerMinute(
      [
        { Timestamp: minutesAgo(59), Sum: 5 },
        { Timestamp: minutesAgo(1), Sum: 7 },
      ],
      NOW,
    )
    expect(bins[1]).toBe(5)
    expect(bins[59]).toBe(7)
    expect(bins.reduce((a, b) => a + b, 0)).toBe(12)
  })

  it("adds datapoints that share a minute", () => {
    const bins = binPerMinute(
      [
        { Timestamp: new Date(NOW - 90_000), Sum: 2 },
        { Timestamp: new Date(NOW - 100_000), Sum: 3 },
      ],
      NOW,
    )
    expect(bins.reduce((a, b) => a + b, 0)).toBe(5)
    expect(bins.filter((b) => b > 0)).toHaveLength(1)
  })

  it("leaves out what is outside the window or has no time", () => {
    const bins = binPerMinute(
      [
        { Timestamp: minutesAgo(61), Sum: 9 },
        { Timestamp: new Date(NOW + 60_000), Sum: 9 },
        { Sum: 9 },
      ],
      NOW,
    )
    expect(bins.reduce((a, b) => a + b, 0)).toBe(0)
  })
})
