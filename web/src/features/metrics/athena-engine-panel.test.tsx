import { describe, expect, it } from "vitest"
import { renderWithData, screen } from "@/test/render"
import type { AthenaEngineStatus, HealthResponse } from "@/types"
import { AthenaEnginePanel } from "./athena-engine-panel"
import { metricsHealthKeys } from "./data"

function health(athenaEngine?: AthenaEngineStatus): HealthResponse {
  return {
    status: "ok",
    timestamp: new Date().toISOString(),
    version: "test",
    services: ["athena"],
    serviceTiers: {},
    serviceGoalTiers: {},
    storage: { default: "memory" },
    athenaEngine,
  }
}

function renderPanel(engine?: AthenaEngineStatus) {
  return renderWithData(<AthenaEnginePanel />, [[metricsHealthKeys.health(), health(engine)]])
}

describe("AthenaEnginePanel", () => {
  it("shows a running engine's memory and uptime", () => {
    renderPanel({
      engine: "trino",
      state: "ready",
      memoryBytes: 2 * 1024 ** 3,
      uptimeMillis: 192_000,
      runningQueries: 1,
      image: "ghcr.io/overcast-sh/athena-engine:483@sha256:15ff23b9",
    })

    expect(screen.getByRole("heading", { name: "Athena engine" })).toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent("Engine ready")
    expect(screen.getByText("2 GB")).toBeInTheDocument()
    expect(screen.getByText("3 m 12 s")).toBeInTheDocument()
    expect(screen.getByText("ghcr.io/overcast-sh/athena-engine:483")).toBeInTheDocument()
  })

  it("shows a starting engine as starting, with no uptime", () => {
    renderPanel({ engine: "trino", state: "pulling", memoryBytes: 1024 ** 3, runningQueries: 1 })

    expect(screen.getByRole("status")).toHaveTextContent("Starting engine")
    expect(screen.getByText("Uptime").nextSibling).toHaveTextContent("—")
  })

  it("shows an inert engine as off, without engine resources", () => {
    renderPanel({ engine: "inert", state: "off", runningQueries: 0 })

    expect(screen.getByRole("status")).toHaveTextContent("Engine off")
    expect(screen.queryByText("Memory")).not.toBeInTheDocument()
  })

  it("is absent when Athena is not enabled", () => {
    renderPanel(undefined)

    expect(screen.queryByRole("heading", { name: "Athena engine" })).not.toBeInTheDocument()
  })
})
