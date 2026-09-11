import { http, HttpResponse } from "msw"
import { TestTab } from "@/features/lambda/components/test-tab"
import { server } from "@/test/server"
import { act, render, renderWithData, screen } from "@/test/render"
import { FAKE_BRIDGE_URL, fakeDebugSession, renderWithDebugSession } from "@/test/debug-session"
import { debuggerTargetQueryOptions } from "@/features/debugger/data"
import type { DebuggerTarget, InvokeResult } from "@/types"
import { debugTarget } from "@/test/debug-target"

const baseResult: InvokeResult = {
  statusCode: 200,
  payload: '{"ok":true}',
  functionError: null,
  logResult: null,
  executedVersion: "$LATEST",
  logGroupName: null,
  logStreamName: null,
}

/** The BFF answers the invoke with SSE — one progress event, then the result. */
function respondWithLogTail(logResult: string) {
  server.use(
    http.get("/api/lambda/functions/:name/test-events", () => HttpResponse.json([])),
    http.post(
      "/api/lambda/functions/:name/invoke-with-progress",
      () =>
        new HttpResponse(
          `event: progress\ndata: Invoking\n\n` +
            `event: result\ndata: ${JSON.stringify({ ...baseResult, logResult })}\n\n`,
          { headers: { "Content-Type": "text/event-stream" } },
        ),
    ),
  )
}

/** The bytes of `text`, base64-encoded — what `LogResult` carries on the wire. */
function encode(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)))
}

async function invoke() {
  const { user } = render(<TestTab name="utf8-logger" />)
  await user.click(screen.getByRole("button", { name: "Test" }))
  expect(await screen.findByText("Execution succeeded")).toBeInTheDocument()
}

describe("TestTab > log output", () => {
  it("shows a non-ASCII log tail as the handler printed it", async () => {
    respondWithLogTail(encode("café au lait ☕\nこんにちは 🌍\n"))
    await invoke()
    expect(screen.getByText(/café au lait ☕/)).toBeInTheDocument()
  })

  it("says the log output is unavailable when the log tail is not base64", async () => {
    respondWithLogTail("@@@ not base64 @@@")
    await invoke()
    expect(screen.getByText(/Log output unavailable/)).toBeInTheDocument()
  })

  it("still shows the result panel when the log tail is not base64", async () => {
    respondWithLogTail("@@@ not base64 @@@")
    await invoke()
    expect(screen.getByText("Status: 200")).toBeInTheDocument()
  })
})

// The hint above Invoke (docs/plans/compute-debugger.md § 7): what the
// debugger does to the timeout, and nothing at all while it is off.
describe("TestTab > debugger hint", () => {
  function renderWithTarget(target: DebuggerTarget, timeoutSeconds = 3) {
    server.use(http.get("/api/lambda/functions/:name/test-events", () => HttpResponse.json([])))
    return renderWithData(<TestTab name="my-fn" timeoutSeconds={timeoutSeconds} />, [
      [debuggerTargetQueryOptions("lambda", "my-fn").queryKey, target],
    ])
  }

  it("says the clock is suspended while a debugger is attached", () => {
    renderWithTarget(debugTarget({ state: "attached", attachedSince: "2026-09-07T10:00:00Z" }))

    expect(screen.getByRole("status")).toHaveTextContent(
      "Debugger attached — the timeout clock is suspended.",
    )
  })

  it("names the real timeout while no client is attached", () => {
    renderWithTarget(debugTarget({ state: "listening" }), 30)

    expect(screen.getByRole("status")).toHaveTextContent(
      "No debugger attached — the 30 s timeout applies.",
    )
  })

  it("says the timeout still applies under the strict policy", () => {
    renderWithTarget(debugTarget({ state: "attached", timeoutPolicy: "strict" }))

    expect(screen.getByRole("status")).toHaveTextContent(
      "Debugger attached — the 3 s timeout still applies (timeout policy: strict).",
    )
  })

  it("shows nothing while the feature is off for the function", () => {
    renderWithTarget(debugTarget({ enabled: false, reason: "not tagged", state: "inert" }))

    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })
})

// The console debug session (docs/plans/compute-debugger-console.md § 3.5,
// Test tab): Invoke is what starts the container a waiting session needs.
describe("TestTab > console debug session", () => {
  it("wakes a session waiting for a container when Invoke is pressed", async () => {
    respondWithLogTail("")
    const { session, bridge } = fakeDebugSession()
    session.start(FAKE_BRIDGE_URL)
    act(() => bridge.latest().serverClose(1011, "no container"))
    expect(session.getState().status).toBe("waiting")
    expect(bridge.sockets).toHaveLength(1)

    const { user } = renderWithDebugSession(<TestTab name="utf8-logger" />, session)
    await user.click(screen.getByRole("button", { name: "Test" }))

    expect(bridge.sockets).toHaveLength(2)
    expect(await screen.findByText("Execution succeeded")).toBeInTheDocument()
    session.dispose()
  })
})
