import { fakeBridge, type FakeBridgeSocket } from "@/test/fake-bridge"
import { DebugSession, type PauseState } from "./session"

const BRIDGE = "ws://console/api/debugger/targets/lambda/my-fn/ws"

/** The same four-line handler and hand-written map as source-maps.test.ts. */
const ORIGINAL = "export const handler = async () => {\n  const x = 1\n  return x\n}\n"
const MAP = JSON.stringify({
  version: 3,
  file: "index.js",
  sources: ["../src/index.ts"],
  sourcesContent: [ORIGINAL],
  mappings: ";AAAA;IACE;IACA;AACF",
})
const INLINE_MAP = `data:application/json;base64,${btoa(MAP)}`

/**
 * Let the session's promise chains settle. Microtasks only, so it works the
 * same under fake timers — nothing between two protocol frames waits on a
 * timer.
 */
async function flush() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

const noFiles = () => Promise.reject(new Error("no files"))

function makeSession(files: Record<string, string> = {}) {
  const bridge = fakeBridge()
  const session = new DebugSession({
    key: "lambda/my-fn",
    fetchFile: (path) =>
      Object.hasOwn(files, path)
        ? Promise.resolve(files[path])
        : Promise.reject(new Error(`no such file: ${path}`)),
    dial: bridge.dial,
    backoffMs: [10],
  })
  session.setDeploymentFiles(Object.keys(files))
  return { session, bridge }
}

/** Open the socket and answer the enable handshake. */
async function attach(session: DebugSession, bridge: ReturnType<typeof fakeBridge>) {
  session.start(BRIDGE)
  const socket = bridge.latest()
  socket.open()
  await flush()
  socket.respondAll()
  await flush()
  return socket
}

function parsed(socket: FakeBridgeSocket, path: string, sourceMapURL?: string) {
  socket.event("Debugger.scriptParsed", {
    scriptId: `s-${path}`,
    url: `file:///var/task/${path}`,
    startLine: 0,
    startColumn: 0,
    endLine: 10,
    endColumn: 0,
    executionContextId: 1,
    hash: "h",
    sourceMapURL,
  })
}

function paused(socket: FakeBridgeSocket, path: string, line0: number, column0 = 0, extra = {}) {
  socket.event("Debugger.paused", {
    reason: "other",
    callFrames: [
      {
        callFrameId: "f0",
        functionName: "handler",
        location: { scriptId: `s-${path}`, lineNumber: line0, columnNumber: column0 },
        url: `file:///var/task/${path}`,
        scopeChain: [
          { type: "local", object: { type: "object", objectId: "o1" } },
          { type: "global", object: { type: "object" } },
        ],
        this: { type: "undefined" },
      },
    ],
    ...extra,
  })
}

beforeEach(() => localStorage.clear())

describe("DebugSession > breakpoints persist", () => {
  it("survive a new session for the same function, keyed per function", () => {
    const { session } = makeSession()
    session.addBreakpoint("index.js", 3, "x > 1")
    session.addBreakpoint("index.js", 5)
    session.updateBreakpoint(session.breakpointAt("index.js", 5)!.id, { enabled: false })
    session.addWatch("event.key")
    session.setPauseOnExceptions("uncaught")

    const again = new DebugSession({ key: "lambda/my-fn", fetchFile: noFiles })
    expect(again.getState().breakpoints).toEqual([
      expect.objectContaining({
        path: "index.js",
        line: 3,
        condition: "x > 1",
        enabled: true,
        bound: false,
      }),
      expect.objectContaining({ path: "index.js", line: 5, condition: "", enabled: false }),
    ])
    expect(again.getState().watches).toEqual([
      expect.objectContaining({ expression: "event.key", value: null }),
    ])
    expect(again.getState().pauseOnExceptions).toBe("uncaught")

    const other = new DebugSession({ key: "lambda/other-fn", fetchFile: noFiles })
    expect(other.getState().breakpoints).toEqual([])
  })

  it("toggle adds then removes, and a corrupt record reads as empty", () => {
    const { session } = makeSession()
    session.toggleBreakpoint("index.js", 2)
    expect(session.getState().breakpoints).toHaveLength(1)
    session.toggleBreakpoint("index.js", 2)
    expect(session.getState().breakpoints).toHaveLength(0)

    localStorage.setItem("overcast-debug:lambda/broken", "{not json")
    const broken = new DebugSession({ key: "lambda/broken", fetchFile: noFiles })
    expect(broken.getState().breakpoints).toEqual([])
  })
})

describe("DebugSession > binding", () => {
  it("enables the domains on open and binds a deployed-file breakpoint by URL regex", async () => {
    const { session, bridge } = makeSession()
    session.addBreakpoint("index.js", 3, "x > 1")
    session.start(BRIDGE)
    expect(session.getState().status).toBe("connecting")
    const socket = bridge.latest()
    socket.open()
    expect(session.getState().status).toBe("attached")
    await flush()
    expect(socket.sent.map((f) => f.method)).toEqual(["Debugger.enable"])
    socket.respondAll()
    await flush()
    socket.respondAll()
    await flush()

    const bind = socket.lastRequest("Debugger.setBreakpointByUrl")
    expect(bind.params).toEqual({
      lineNumber: 2,
      columnNumber: undefined,
      urlRegex: "file:///var/task/index\\.js$",
      condition: "x > 1",
    })
    expect(session.getState().breakpoints[0].bound).toBe(false)
    socket.respond(bind.id, { breakpointId: "bp:1", locations: [] })
    await flush()
    expect(session.getState().breakpoints[0].bound).toBe(true)
  })

  it("binds a breakpoint once when a script parses before the first bind is answered", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    session.addBreakpoint("index.js", 3)
    parsed(socket, "index.js")
    await flush()
    expect(socket.requests("Debugger.setBreakpointByUrl")).toHaveLength(1)
    socket.respondAll({ breakpointId: "bp:1", locations: [] })
    await flush()
    expect(session.getState().breakpoints[0].bound).toBe(true)
    expect(socket.requests("Debugger.setBreakpointByUrl")).toHaveLength(1)
  })

  it("re-binds a breakpoint whose file became a mapped original while its bind was in flight", async () => {
    const { session, bridge } = makeSession({ "src/index.ts": ORIGINAL })
    const socket = await attach(session, bridge)
    session.addBreakpoint("src/index.ts", 2)
    const raw = socket.lastRequest("Debugger.setBreakpointByUrl")
    parsed(socket, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    // Still only the raw bind out; the mapped one waits for its reply.
    expect(socket.requests("Debugger.setBreakpointByUrl")).toHaveLength(1)

    socket.respond(raw.id, { breakpointId: "bp:raw", locations: [] })
    await flush()
    expect(socket.lastRequest("Debugger.removeBreakpoint").params).toEqual({
      breakpointId: "bp:raw",
    })
    expect(socket.lastRequest("Debugger.setBreakpointByUrl").params).toMatchObject({
      lineNumber: 2,
      columnNumber: 4,
      urlRegex: "file:///var/task/dist/index\\.js$",
    })
  })

  it("removes the inspector's breakpoint when the client one goes, and skips a disabled one", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    const bp = session.addBreakpoint("index.js", 3)
    const bind = socket.lastRequest("Debugger.setBreakpointByUrl")
    socket.respond(bind.id, { breakpointId: "bp:1", locations: [] })
    await flush()

    session.updateBreakpoint(bp.id, { enabled: false })
    await flush()
    expect(socket.lastRequest("Debugger.removeBreakpoint").params).toEqual({ breakpointId: "bp:1" })
    expect(socket.requests("Debugger.setBreakpointByUrl")).toHaveLength(1)
    expect(session.getState().breakpoints[0].bound).toBe(false)

    session.removeBreakpoint(bp.id)
    expect(session.getState().breakpoints).toEqual([])
  })

  it("translates a breakpoint on an original file once its script's map has parsed", async () => {
    const { session, bridge } = makeSession({ "src/index.ts": ORIGINAL })
    const socket = await attach(session, bridge)
    // Set before the map is known: it binds to the raw path, which the map
    // then re-classifies, so the raw binding is dropped and re-bound mapped.
    session.addBreakpoint("src/index.ts", 2)
    await flush()
    const raw = socket.lastRequest("Debugger.setBreakpointByUrl")
    expect(raw.params).toMatchObject({
      lineNumber: 1,
      urlRegex: "file:///var/task/src/index\\.ts$",
    })
    socket.respond(raw.id, { breakpointId: "bp:raw", locations: [] })
    await flush()
    expect(session.getState().breakpoints[0].bound).toBe(true)

    parsed(socket, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    expect(socket.lastRequest("Debugger.removeBreakpoint").params).toEqual({
      breakpointId: "bp:raw",
    })
    expect(session.getState().hasSourceMaps).toBe(true)
    expect(session.getState().scripts).toEqual([{ path: "dist/index.js", mapped: true }])
    expect(session.getState().originalFiles).toEqual([
      { path: "src/index.ts", origin: "deployment", generated: ["dist/index.js"] },
    ])
    expect(session.isOriginalFile("src/index.ts")).toBe(true)
    await expect(session.originalContent("src/index.ts")).resolves.toBe(ORIGINAL)

    const bind = socket.lastRequest("Debugger.setBreakpointByUrl")
    expect(bind.params).toMatchObject({
      lineNumber: 2,
      columnNumber: 4,
      urlRegex: "file:///var/task/dist/index\\.js$",
    })
  })
})

describe("DebugSession > binding > after a service restart", () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it("re-applies every breakpoint on the new connection", async () => {
    const { session, bridge } = makeSession()
    const first = await attach(session, bridge)
    session.addBreakpoint("index.js", 3)
    first.respond(first.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:1",
      locations: [],
    })
    await flush()
    expect(session.getState().breakpoints[0].bound).toBe(true)

    first.serverClose(1012, "service restart")
    expect(session.getState().status).toBe("reconnecting")
    expect(session.getState().breakpoints[0].bound).toBe(false)

    await vi.advanceTimersByTimeAsync(10)
    const second = bridge.latest()
    expect(second).not.toBe(first)
    second.open()
    await flush()
    second.respondAll()
    await flush()
    second.respondAll()
    await flush()
    expect(second.sent.map((f) => f.method)).toEqual([
      "Debugger.enable",
      "Runtime.enable",
      "Debugger.setBreakpointByUrl",
    ])
    expect(session.getState().status).toBe("attached")
  })
})

describe("DebugSession > pause", () => {
  it("maps call frames through the source map, announces the pause, and clears on resume", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    const announced: PauseState[] = []
    session.onPause((p) => announced.push(p))

    paused(socket, "dist/index.js", 2, 4, { hitBreakpoints: ["bp:9"] })
    const pause = session.getState().pause
    expect(pause).toMatchObject({
      id: 1,
      reason: "other",
      selectedFrame: 0,
      hitBreakpointIds: [],
      exception: null,
    })
    expect(pause?.frames[0]).toEqual({
      id: "f0",
      functionName: "handler",
      location: { path: "src/index.ts", line: 2, column: 2 },
      generated: { path: "dist/index.js", line: 3, column: 4 },
      mapped: true,
      scopes: [
        { kind: "local", name: null, objectId: "o1" },
        { kind: "global", name: null, objectId: null },
      ],
    })
    expect(announced).toHaveLength(1)
    expect(session.getState().console.at(-1)).toMatchObject({
      kind: "marker",
      text: "Paused at src/index.ts:2 (other)",
    })

    session.resume()
    expect(socket.lastRequest().method).toBe("Debugger.resume")
    socket.event("Debugger.resumed", {})
    expect(session.getState().pause).toBeNull()
    expect(session.getState().console.at(-1)).toMatchObject({ kind: "marker", text: "Resumed" })
  })

  it("keeps the generated location, unmapped, for a frame no map resolves", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "plain.js")
    await flush()

    paused(socket, "plain.js", 6, 1)
    expect(session.getState().pause?.frames[0]).toMatchObject({
      location: { path: "plain.js", line: 7, column: 1 },
      mapped: false,
    })
  })

  it("names the hit breakpoint by its client id and reports the exception on an exception pause", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    const bp = session.addBreakpoint("plain.js", 7)
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:7",
      locations: [],
    })
    await flush()
    parsed(socket, "plain.js")
    await flush()

    paused(socket, "plain.js", 6, 0, {
      reason: "exception",
      hitBreakpoints: ["bp:7"],
      data: { type: "object", description: "Error: boom" },
    })
    expect(session.getState().pause).toMatchObject({
      hitBreakpointIds: [bp.id],
      exception: "Error: boom",
    })
  })

  it("selects a frame within range and steps with the matching commands", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    paused(socket, "plain.js", 1)
    session.selectFrame(5)
    expect(session.getState().pause?.selectedFrame).toBe(0)

    session.stepOver()
    session.stepInto()
    session.stepOut()
    session.pause()
    session.setPauseOnExceptions("all")
    expect(socket.sent.slice(-5).map((f) => f.method)).toEqual([
      "Debugger.stepOver",
      "Debugger.stepInto",
      "Debugger.stepOut",
      "Debugger.pause",
      "Debugger.setPauseOnExceptions",
    ])
    expect(socket.lastRequest("Debugger.setPauseOnExceptions").params).toEqual({ state: "all" })
    expect(session.getState().pauseOnExceptions).toBe("all")
  })
})

describe("DebugSession > console", () => {
  it("records console output and thrown exceptions as they arrive", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    socket.event("Runtime.consoleAPICalled", {
      type: "warning",
      args: [
        { type: "string", value: "count" },
        { type: "number", value: 3 },
        { type: "object", description: "Object", objectId: "o" },
      ],
      executionContextId: 1,
      timestamp: 1_700_000_000_000,
    })
    socket.event("Runtime.exceptionThrown", {
      timestamp: 1_700_000_000_001,
      exceptionDetails: {
        exceptionId: 1,
        text: "Uncaught",
        lineNumber: 0,
        columnNumber: 0,
        exception: { type: "object", description: "TypeError: nope" },
      },
    })
    expect(session.getState().console).toEqual([
      { id: 1, kind: "warn", text: "count 3 Object", timestamp: 1_700_000_000_000 },
      { id: 2, kind: "exception", text: "TypeError: nope", timestamp: 1_700_000_000_001 },
    ])
    session.clearConsole()
    expect(session.getState().console).toEqual([])
  })
})

describe("DebugSession > waiting for a container", () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it("reads 1011 as waiting, retries on invoke until attached, and on a container change", async () => {
    const { session, bridge } = makeSession()
    session.start(BRIDGE)
    bridge.latest().serverClose(1011, "no container")
    expect(session.getState().status).toBe("waiting")
    expect(bridge.sockets).toHaveLength(1)

    session.invokeStarted()
    expect(bridge.sockets).toHaveLength(2)
    bridge.latest().serverClose(1011, "no container")
    await vi.advanceTimersByTimeAsync(1_500)
    expect(bridge.sockets).toHaveLength(3)
    bridge.latest().open()
    expect(session.getState().status).toBe("attached")
    await vi.advanceTimersByTimeAsync(10_000)
    expect(bridge.sockets).toHaveLength(3)

    bridge.latest().serverClose(1011, "no container")
    session.containerChanged()
    expect(bridge.sockets).toHaveLength(4)
  })

  it("stop() closes the socket, clears scripts and pause, and keeps breakpoints unbound", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    session.addBreakpoint("plain.js", 1)
    socket.respondAll({ breakpointId: "bp:1", locations: [] })
    await flush()
    parsed(socket, "plain.js")
    await flush()
    paused(socket, "plain.js", 0)

    session.stop()
    expect(socket.closedBy?.code).toBe(1000)
    expect(session.getState()).toMatchObject({
      status: "idle",
      pause: null,
      scripts: [],
      hasSourceMaps: false,
      breakpoints: [expect.objectContaining({ path: "plain.js", line: 1, bound: false })],
    })
    expect(session.isOpen).toBe(false)
  })
})
