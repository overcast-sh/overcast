import type { ReactElement } from "react"
import { DebugSessionContext } from "@/features/debugger/session/context"
import { DebugSession } from "@/features/debugger/session/session"
import type { CdpEvents, CdpScope } from "@/features/debugger/session/cdp-protocol"
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

/**
 * A pause with the given frames as the inspector reports them — for the
 * panels, which need scopes, several frames, and internal ones. Each frame
 * is `{ path, line0, functionName, scopes, internal }`; an internal frame
 * carries a `node:` URL, which is what the session folds.
 */
export function pausedWith(
  socket: FakeBridgeSocket,
  frames: Array<{
    path: string
    line0?: number
    column0?: number
    functionName?: string
    scopes?: Array<{ type: CdpScope["type"]; name?: string; objectId?: string }>
    internal?: boolean
  }>,
  extra: Partial<CdpEvents["Debugger.paused"]> = {},
) {
  socket.event("Debugger.paused", {
    reason: "other",
    callFrames: frames.map((frame, i) => ({
      callFrameId: `f${i}`,
      functionName: frame.functionName ?? `fn${i}`,
      location: {
        scriptId: `s-${frame.path}`,
        lineNumber: frame.line0 ?? 0,
        columnNumber: frame.column0 ?? 0,
      },
      url: frame.internal ? `node:${frame.path}` : `file:///var/task/${frame.path}`,
      scopeChain: (frame.scopes ?? []).map((scope) => ({
        type: scope.type,
        name: scope.name,
        object: { type: "object", objectId: scope.objectId },
      })),
      this: { type: "undefined" },
    })),
    ...extra,
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
