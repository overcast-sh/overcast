import { beforeEach, describe, expect, it } from "vitest"
import { act } from "react"
import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { render, screen } from "@/test/render"
import { endpointStore } from "@/services/endpoint-store"
import { ConnectionGate } from "./connection-gate"

const ENDPOINT_KEY = "overcast:endpoint"
const SHELL = "the app shell"

function renderGate() {
  return render(
    <ConnectionGate>
      <div>{SHELL}</div>
    </ConnectionGate>,
  )
}

describe("ConnectionGate", () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  describe("with no endpoint configured", () => {
    it("asks for a host rather than pretending to connect to one", async () => {
      renderGate()
      expect(
        await screen.findByRole("heading", { name: "Connect to Overcast" }),
      ).toBeInTheDocument()
    })

    it("does not show the cold-boot screen", () => {
      renderGate()
      expect(screen.queryByText(/^connecting to /)).not.toBeInTheDocument()
    })

    // The dialog is prefilled with the default endpoint, so accepting it sets
    // the store to the values it already held. The store used to skip
    // notifying on an unchanged endpoint, so the gate never noticed the
    // endpoint was now configured and the dialog stayed up until a reload.
    it("hands over to the app when the prefilled default is accepted", async () => {
      const { user } = renderGate()
      await user.click(await screen.findByRole("button", { name: "Connect" }))
      expect(await screen.findByText(SHELL)).toBeInTheDocument()
    })
  })

  describe("with a configured but unreachable endpoint", () => {
    beforeEach(() => {
      localStorage.setItem(ENDPOINT_KEY, JSON.stringify({ baseUrl: "http://localhost:4566" }))
      server.use(http.get("/api/health", () => new HttpResponse(null, { status: 503 })))
    })

    it("shows the cold-boot screen for the configured host", async () => {
      renderGate()
      expect(await screen.findByText("connecting to localhost:4566")).toBeInTheDocument()
    })

    it("withholds the app shell while the emulator is silent", async () => {
      renderGate()
      await screen.findByText("connecting to localhost:4566")
      expect(screen.queryByText(SHELL)).not.toBeInTheDocument()
    })

    it("does not fall back to the connection dialog — the host is already known", async () => {
      renderGate()
      await screen.findByText("connecting to localhost:4566")
      expect(screen.queryByRole("heading", { name: "Connect to Overcast" })).not.toBeInTheDocument()
    })
  })

  describe("when the endpoint is cleared at runtime", () => {
    beforeEach(() => {
      localStorage.setItem(ENDPOINT_KEY, JSON.stringify({ baseUrl: "http://localhost:4566" }))
      server.use(http.get("*/_overcast/health", () => HttpResponse.json({ status: "ok" })))
    })

    // The settings control calls endpointStore.reset(). The gate used to
    // snapshot isConfigured() into useState, so it never re-evaluated and the
    // dialog stayed unreachable until a manual reload — "change connection"
    // silently did nothing.
    it("returns to the connection dialog without needing a reload", async () => {
      renderGate()
      expect(await screen.findByText(SHELL)).toBeInTheDocument()

      act(() => {
        endpointStore.reset()
      })

      expect(
        await screen.findByRole("heading", { name: "Connect to Overcast" }),
      ).toBeInTheDocument()
      expect(screen.queryByText(SHELL)).not.toBeInTheDocument()
    })
  })

  describe("with a reachable endpoint", () => {
    beforeEach(() => {
      localStorage.setItem(ENDPOINT_KEY, JSON.stringify({ baseUrl: "http://localhost:4566" }))
    })

    it("renders the app shell once the probe answers", async () => {
      renderGate()
      expect(await screen.findByText(SHELL)).toBeInTheDocument()
    })

    it("leaves no trace of the cold-boot screen", async () => {
      renderGate()
      await screen.findByText(SHELL)
      expect(screen.queryByText(/^connecting to /)).not.toBeInTheDocument()
    })
  })
})
