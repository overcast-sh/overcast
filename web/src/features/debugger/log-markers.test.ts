import { markerEvents, mergeByTime } from "./log-markers"

describe("log markers", () => {
  it("turns pause and resume markers, and only those, into rows on a debugger stream", () => {
    expect(
      markerEvents([
        { id: 1, kind: "log", text: "hello", timestamp: 10 },
        { id: 2, kind: "marker", text: "Paused at index.js:3 (breakpoint)", timestamp: 20 },
        { id: 3, kind: "marker", text: "Resumed", timestamp: 30 },
      ]),
    ).toEqual([
      {
        timestamp: 20,
        message: "── Paused at index.js:3 (breakpoint) ──",
        logStreamName: "debugger",
      },
      { timestamp: 30, message: "── Resumed ──", logStreamName: "debugger" },
    ])
  })

  it("merges by time, an event before a marker at the same millisecond", () => {
    const rows = mergeByTime(
      [
        { timestamp: 10, message: "a" },
        { timestamp: 30, message: "c" },
      ],
      [
        { timestamp: 10, message: "m1" },
        { timestamp: 20, message: "m2" },
      ],
    )
    expect(rows.map((r) => r.message)).toEqual(["a", "m1", "m2", "c"])
  })
})
