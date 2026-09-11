import type { ReactElement } from "react"
import { DebugSessionContext } from "@/features/debugger/session/context"
import { DebugSession } from "@/features/debugger/session/session"
import { fakeBridge, type FakeBridgeSocket } from "@/test/fake-bridge"
import { render } from "@/test/render"

export const FAKE_BRIDGE_URL = "ws://console/api/debugger/targets/lambda/my-fn/ws"

/**
 * A `DebugSession` over a scripted bridge, for component tests: nothing
 * persists, the reconnect delay is short, and deployed files come from the
 * map given. Returns the session with the bridge the test scripts.
 */
export function fakeDebugSession(files: Record<string, string> = {}) {
  const bridge = fakeBridge()
  const session = new DebugSession({
    key: "lambda/my-fn",
    fetchFile: (path) =>
      Object.hasOwn(files, path)
        ? Promise.resolve(files[path])
        : Promise.reject(new Error(`no such file: ${path}`)),
    dial: bridge.dial,
    storage: null,
    backoffMs: [10],
  })
  session.setDeploymentFiles(Object.keys(files))
  return { session, bridge }
}

/** Let the session's promise chains settle without touching timers. */
export async function settle() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

/** Start the session, accept the socket and answer the enable handshake. */
export async function attachFakeSession(
  session: DebugSession,
  bridge: ReturnType<typeof fakeBridge>,
): Promise<FakeBridgeSocket> {
  session.start(FAKE_BRIDGE_URL)
  const socket = bridge.latest()
  socket.open()
  await settle()
  socket.respondAll()
  await settle()
  socket.respondAll()
  await settle()
  return socket
}

/** A `scriptParsed` for a root-relative path, optionally with a map URL. */
export function scriptParsed(socket: FakeBridgeSocket, path: string, sourceMapURL?: string) {
  socket.event("Debugger.scriptParsed", {
    scriptId: `s-${path}`,
    url: `file:///var/task/${path}`,
    startLine: 0,
    startColumn: 0,
    endLine: 100,
    endColumn: 0,
    executionContextId: 1,
    hash: "h",
    sourceMapURL,
  })
}

/** A single-frame pause in `path` at a 0-based line, as the inspector reports it. */
export function pausedAt(socket: FakeBridgeSocket, path: string, line0: number, column0 = 0) {
  socket.event("Debugger.paused", {
    reason: "other",
    callFrames: [
      {
        callFrameId: "f0",
        functionName: "handler",
        location: { scriptId: `s-${path}`, lineNumber: line0, columnNumber: column0 },
        url: `file:///var/task/${path}`,
        scopeChain: [],
        this: { type: "undefined" },
      },
    ],
  })
}

/** Render under a `DebugSessionContext` carrying `session`. */
export function renderWithDebugSession(
  ui: ReactElement,
  session: DebugSession,
  options?: Parameters<typeof render>[1],
) {
  return render(
    <DebugSessionContext.Provider value={session}>{ui}</DebugSessionContext.Provider>,
    options,
  )
}
