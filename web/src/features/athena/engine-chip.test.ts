import type { AthenaEngineStatus } from "@/types"
import { engineChip, isInert } from "./engine-chip"

const base: AthenaEngineStatus = { engine: "trino", state: "ready", runningQueries: 0 }

describe("engineChip", () => {
  it("says an inert engine is off because of the setting", () => {
    expect(engineChip({ ...base, engine: "inert", state: "off" }, 0)).toMatchObject({
      label: "Engine off",
      detail: "ATHENA_ENGINE=inert",
      tone: "warning",
    })
  })

  it("says an engine with no Docker is off because of that", () => {
    expect(engineChip({ ...base, state: "off" }, 0).detail).toBe("no Docker")
  })

  it("shows a pull as progress", () => {
    expect(engineChip({ ...base, state: "pulling" }, 0)).toMatchObject({
      label: "Starting engine",
      detail: "pulling image",
      busy: true,
    })
  })

  it("ticks the time a start has taken", () => {
    const status = { ...base, state: "starting" as const, startedAt: "2026-09-26T10:00:00Z" }
    expect(engineChip(status, Date.parse("2026-09-26T10:00:04.5Z")).detail).toBe(
      "starting · 4.50 s",
    )
  })

  it("says how long a ready engine took to start", () => {
    expect(engineChip({ ...base, startMillis: 9700 }, 0)).toMatchObject({
      label: "Engine ready",
      detail: "started in 9.70 s",
      tone: "success",
    })
  })

  it("carries a failed start's error", () => {
    expect(engineChip({ ...base, state: "failed", lastError: "pull denied" }, 0).detail).toBe(
      "pull denied",
    )
  })
})

describe("isInert", () => {
  it("is true only while the engine is off", () => {
    expect([isInert({ ...base, state: "off" }), isInert(base), isInert(undefined)]).toEqual([
      true,
      false,
      false,
    ])
  })
})
