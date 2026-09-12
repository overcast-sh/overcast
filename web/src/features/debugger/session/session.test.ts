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
      expect.objectContaining({ expression: "event.key", result: null }),
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

  it("reads every source map again on the new connection, since hot reload may have rebuilt it", async () => {
    // Given: a session whose first container's script carried a map
    const { session, bridge } = makeSession({ "src/index.ts": ORIGINAL })
    const first = await attach(session, bridge)
    parsed(first, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    expect(session.getState().hasSourceMaps).toBe(true)
    expect(session.getState().originalFiles).toHaveLength(1)

    // When: the container is replaced and the new one has not parsed anything yet
    first.serverClose(1012, "service restart")
    await vi.advanceTimersByTimeAsync(10)
    const second = bridge.latest()
    second.open()
    await flush()
    second.respondAll()
    await flush()

    // Then: nothing of the old container's maps is kept — a breakpoint on
    // the original file would translate through a map the new build may
    // have changed — until the new container reports the script
    expect(session.getState().hasSourceMaps).toBe(false)
    expect(session.getState().originalFiles).toEqual([])
    expect(session.isOriginalFile("src/index.ts")).toBe(false)

    parsed(second, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    expect(session.getState().hasSourceMaps).toBe(true)
    expect(session.isOriginalFile("src/index.ts")).toBe(true)
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
      internal: false,
      scopes: [
        { kind: "local", name: null, objectId: "o1" },
        { kind: "global", name: null, objectId: null },
      ],
    })
    expect(announced).toHaveLength(1)
    expect(session.getState().console.at(-1)).toMatchObject({
      kind: "marker",
      text: "Paused at src/index.ts:2 (step)",
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

describe("DebugSession > evaluation", () => {
  it("evaluates in the selected frame while paused and globally otherwise, never throwing", async () => {
    const { session, bridge } = makeSession()
    expect(await session.evaluate("1")).toEqual({ ok: false, error: "No session" })
    const socket = await attach(session, bridge)

    const running = session.evaluate("process.version")
    const global = socket.lastRequest("Runtime.evaluate")
    expect(global.params).toMatchObject({
      expression: "process.version",
      objectGroup: "console",
      includeCommandLineAPI: true,
      generatePreview: true,
      awaitPromise: true,
    })
    socket.respond(global.id, { result: { type: "string", value: "v22.0.0" } })
    expect(await running).toEqual({
      ok: true,
      value: { type: "string", subtype: null, description: '"v22.0.0"', objectId: null },
    })

    paused(socket, "plain.js", 1)
    const inFrame = session.evaluate("x", 0)
    const frame = socket.lastRequest("Debugger.evaluateOnCallFrame")
    expect(frame.params).toMatchObject({
      callFrameId: "f0",
      expression: "x",
      objectGroup: "console",
    })
    socket.respond(frame.id, {
      result: { type: "undefined" },
      exceptionDetails: {
        exceptionId: 1,
        text: "Uncaught",
        lineNumber: 0,
        columnNumber: 0,
        exception: { type: "object", description: "ReferenceError: x is not defined" },
      },
    })
    expect(await inFrame).toEqual({ ok: false, error: "ReferenceError: x is not defined" })

    const dropped = session.evaluate("y")
    socket.fail(
      socket.lastRequest("Debugger.evaluateOnCallFrame").id,
      -32000,
      "Cannot find context",
    )
    expect(await dropped).toEqual({ ok: false, error: "Cannot find context" })
  })

  it("reads own properties with previews, leaving getters unread, and lists internal rows last", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    const reading = session.getProperties("obj-1")
    const req = socket.lastRequest("Runtime.getProperties")
    expect(req.params).toEqual({ objectId: "obj-1", ownProperties: true, generatePreview: true })
    socket.respond(req.id, {
      result: [
        { name: "n", value: { type: "number", value: 1 }, configurable: true, enumerable: true },
        {
          name: "nested",
          value: {
            type: "object",
            className: "Object",
            description: "Object",
            objectId: "obj-2",
            preview: {
              type: "object",
              overflow: true,
              properties: [
                { name: "a", type: "string", value: "s" },
                { name: "b", type: "object", subtype: "array", value: "Array(2)" },
                { name: "c", type: "object", value: "Object" },
                { name: "d", type: "function", value: "" },
                { name: "e", type: "object", subtype: "null", value: "null" },
              ],
            },
          },
          configurable: true,
          enumerable: true,
        },
        {
          name: "fn",
          value: { type: "function", description: "function fn(a) {\n  return a\n}" },
          configurable: true,
          enumerable: true,
        },
        { name: "later", get: { type: "function" }, configurable: true, enumerable: false },
      ],
      internalProperties: [
        {
          name: "[[Prototype]]",
          value: { type: "object", description: "Object", objectId: "proto" },
        },
      ],
    })
    expect(await reading).toEqual([
      {
        name: "n",
        value: { type: "number", subtype: null, description: "1", objectId: null },
        accessor: false,
        enumerable: true,
        internal: false,
      },
      {
        name: "nested",
        value: {
          type: "object",
          subtype: null,
          description: '{a: "s", b: Array(2), c: {…}, d: ƒ, e: null, …}',
          objectId: "obj-2",
        },
        accessor: false,
        enumerable: true,
        internal: false,
      },
      {
        name: "fn",
        value: { type: "function", subtype: null, description: "function fn(a) {", objectId: null },
        accessor: false,
        enumerable: true,
        internal: false,
      },
      { name: "later", value: null, accessor: true, enumerable: false, internal: false },
      {
        name: "[[Prototype]]",
        value: { type: "object", subtype: null, description: "Object", objectId: "proto" },
        accessor: false,
        enumerable: false,
        internal: true,
      },
    ])
  })

  it("evaluates every watch on pause, on frame change and on edit, applying only answers for the current frame", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    const w = session.addWatch("a")
    expect(socket.requests("Debugger.evaluateOnCallFrame")).toHaveLength(0)

    socket.event("Debugger.paused", {
      reason: "other",
      callFrames: [0, 1].map((i) => ({
        callFrameId: `f${i}`,
        functionName: `fn${i}`,
        location: { scriptId: "s-plain.js", lineNumber: i },
        url: "file:///var/task/plain.js",
        scopeChain: [],
        this: { type: "undefined" as const },
      })),
    })
    const first = socket.lastRequest("Debugger.evaluateOnCallFrame")
    expect(first.params).toMatchObject({ callFrameId: "f0", expression: "a", objectGroup: "watch" })

    // The frame changes before the first answer lands: that answer is stale.
    session.selectFrame(1)
    const second = socket.lastRequest("Debugger.evaluateOnCallFrame")
    expect(second.params).toMatchObject({ callFrameId: "f1", expression: "a" })
    socket.respond(first.id, { result: { type: "number", value: 1 } })
    await flush()
    expect(session.getState().watches[0].result).toBeNull()
    socket.respond(second.id, { result: { type: "number", value: 2 } })
    await flush()
    expect(session.getState().watches[0]).toMatchObject({
      result: { description: "2" },
      error: null,
    })

    session.updateWatch(w.id, "b")
    expect(session.getState().watches[0]).toMatchObject({ expression: "b", result: null })
    expect(socket.lastRequest("Debugger.evaluateOnCallFrame").params).toMatchObject({
      expression: "b",
    })

    socket.event("Debugger.resumed", {})
    expect(socket.requests("Runtime.releaseObjectGroup").map((r) => r.params)).toEqual([
      { objectGroup: "watch" },
      { objectGroup: "console" },
    ])
  })

  it("runs a console command: echoes it, records the answer with its handle, and keeps history", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    const run = session.runConsoleCommand("  ({a: 1})  ")
    socket.respond(socket.lastRequest("Runtime.evaluate").id, {
      result: {
        type: "object",
        className: "Object",
        description: "Object",
        objectId: "o-1",
        preview: {
          type: "object",
          overflow: false,
          properties: [{ name: "a", type: "number", value: "1" }],
        },
      },
    })
    await run
    expect(session.getState().console).toEqual([
      expect.objectContaining({ kind: "input", text: "({a: 1})" }),
      expect.objectContaining({
        kind: "result",
        text: "{a: 1}",
        value: { type: "object", subtype: null, description: "{a: 1}", objectId: "o-1" },
      }),
    ])
    expect(session.getState().consoleHistory).toEqual(["({a: 1})"])

    const failing = session.runConsoleCommand("({a: 1})")
    socket.fail(socket.lastRequest("Runtime.evaluate").id, -32000, "boom")
    await failing
    expect(session.getState().console.at(-1)).toMatchObject({ kind: "error", text: "boom" })
    // A repeat of the last command is not a second history entry.
    expect(session.getState().consoleHistory).toEqual(["({a: 1})"])
    await session.runConsoleCommand("   ")
    expect(session.getState().console).toHaveLength(4)
  })

  it("marks frames outside the deployment as internal", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    socket.event("Debugger.paused", {
      reason: "other",
      callFrames: [
        {
          callFrameId: "f0",
          functionName: "handler",
          location: { scriptId: "s-plain.js", lineNumber: 0 },
          url: "file:///var/task/plain.js",
          scopeChain: [],
          this: { type: "undefined" },
        },
        {
          callFrameId: "f1",
          functionName: "processTicksAndRejections",
          location: { scriptId: "s-node", lineNumber: 90 },
          url: "node:internal/process/task_queues",
          scopeChain: [],
          this: { type: "undefined" },
        },
      ],
    })
    expect(session.getState().pause?.frames.map((f) => [f.internal, f.location.path])).toEqual([
      [false, "plain.js"],
      [true, "node:internal/process/task_queues"],
    ])
  })
})

describe("DebugSession > binding > where the breakpoint landed", () => {
  it("moves a breakpoint to the statement the inspector placed it on, once its script is loaded", async () => {
    // Given: a breakpoint on a blank line of a script already parsed
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "index.js")
    await flush()
    const bp = session.addBreakpoint("index.js", 2)
    expect(session.getState().breakpoints[0]).toMatchObject({ bound: false, resolved: false })

    // When: the inspector answers with the next statement's location
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:1",
      locations: [{ scriptId: "s-index.js", lineNumber: 3, columnNumber: 2 }],
    })
    await flush()

    // Then: the breakpoint sits on that line, resolved, and is persisted there
    expect(session.getState().breakpoints).toEqual([
      expect.objectContaining({ id: bp.id, line: 4, bound: true, resolved: true }),
    ])
    const again = new DebugSession({ key: "lambda/my-fn", fetchFile: noFiles })
    expect(again.getState().breakpoints[0]).toMatchObject({ line: 4, resolved: false })
  })

  it("stays pending until breakpointResolved names the location, even one that arrives before the bind's reply", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    session.addBreakpoint("index.js", 3)
    const bind = socket.lastRequest("Debugger.setBreakpointByUrl")

    // The script loads and resolves the breakpoint while the reply is still in flight.
    parsed(socket, "index.js")
    socket.event("Debugger.breakpointResolved", {
      breakpointId: "bp:1",
      location: { scriptId: "s-index.js", lineNumber: 2, columnNumber: 0 },
    })
    socket.respond(bind.id, { breakpointId: "bp:1", locations: [] })
    await flush()
    expect(session.getState().breakpoints[0]).toMatchObject({ line: 3, bound: true, resolved: true })

    // A second breakpoint, held but unresolved, resolves later through the event alone.
    session.addBreakpoint("index.js", 8)
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:2",
      locations: [],
    })
    await flush()
    expect(session.getState().breakpoints[1]).toMatchObject({ bound: true, resolved: false })
    socket.event("Debugger.breakpointResolved", {
      breakpointId: "bp:2",
      location: { scriptId: "s-index.js", lineNumber: 7, columnNumber: 4 },
    })
    expect(session.getState().breakpoints[1]).toMatchObject({ line: 8, resolved: true })
  })

  it("drops a breakpoint the inspector moved onto a line that already has one", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "index.js")
    await flush()
    const kept = session.addBreakpoint("index.js", 4)
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:kept",
      locations: [{ scriptId: "s-index.js", lineNumber: 3, columnNumber: 2 }],
    })
    await flush()
    session.addBreakpoint("index.js", 2)
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:moved",
      locations: [{ scriptId: "s-index.js", lineNumber: 3, columnNumber: 2 }],
    })
    await flush()
    expect(session.getState().breakpoints.map((b) => b.id)).toEqual([kept.id])
    expect(socket.lastRequest("Debugger.removeBreakpoint").params).toEqual({
      breakpointId: "bp:moved",
    })
  })

  it("maps a resolved location on a compiled script back to the original line", async () => {
    const { session, bridge } = makeSession({ "src/index.ts": ORIGINAL })
    const socket = await attach(session, bridge)
    parsed(socket, "dist/index.js", INLINE_MAP)
    await flush()
    await flush()
    session.addBreakpoint("src/index.ts", 2)
    await flush()
    // The inspector settles on generated line 4 (0-based 3), which the map says is original line 3.
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:1",
      locations: [{ scriptId: "s-dist/index.js", lineNumber: 3, columnNumber: 4 }],
    })
    await flush()
    expect(session.getState().breakpoints[0]).toMatchObject({
      path: "src/index.ts",
      line: 3,
      resolved: true,
    })
  })
})

describe("DebugSession > pause > labels and runtime internals", () => {
  it("names a pause by what it stopped on: a breakpoint, an exception, else a step", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "index.js")
    await flush()
    session.addBreakpoint("index.js", 3)
    socket.respond(socket.lastRequest("Debugger.setBreakpointByUrl").id, {
      breakpointId: "bp:1",
      locations: [{ scriptId: "s-index.js", lineNumber: 2, columnNumber: 0 }],
    })
    await flush()

    // V8 reports a breakpoint hit as reason "other" and the ids it stopped on.
    paused(socket, "index.js", 2, 0, { hitBreakpoints: ["bp:1"] })
    expect(session.getState().console.at(-1)?.text).toBe("Paused at index.js:3 (breakpoint)")
    expect(session.getState().pause?.hitBreakpointIds).toEqual([
      session.getState().breakpoints[0].id,
    ])
    socket.event("Debugger.resumed", {})

    paused(socket, "index.js", 4, 0, { reason: "ambiguous" })
    expect(session.getState().console.at(-1)?.text).toBe("Paused at index.js:5 (step)")
    socket.event("Debugger.resumed", {})

    paused(socket, "index.js", 4, 0, {
      reason: "exception",
      data: { type: "object", className: "Error", description: "Error: boom" },
    })
    expect(session.getState().console.at(-1)?.text).toBe("Paused at index.js:5 (exception)")
    expect(session.getState().pause?.exception).toBe("Error: boom")
    expect(session.getState().pauseCount).toBe(3)
  })

  it("steps back out of the runtime's own frames a step landed in, and keeps a breakpoint there", async () => {
    const { session, bridge } = makeSession()
    const socket = await attach(session, bridge)
    parsed(socket, "index.js")
    await flush()
    const announced: PauseState[] = []
    session.onPause((p) => announced.push(p))

    // A step into console.log lands in the runtime's patched console.
    session.stepInto()
    socket.event("Debugger.resumed", {})
    const internal = (functionName: string) =>
      socket.event("Debugger.paused", {
        reason: "other",
        callFrames: [
          {
            callFrameId: "f-internal",
            functionName,
            location: { scriptId: "s-runtime", lineNumber: 669, columnNumber: 10 },
            url: "",
            scopeChain: [],
            this: { type: "undefined" },
          },
          {
            callFrameId: "f-user",
            functionName: "handler",
            location: { scriptId: "s-index.js", lineNumber: 6, columnNumber: 2 },
            url: "file:///var/task/index.js",
            scopeChain: [],
            this: { type: "undefined" },
          },
        ],
      })
    internal("console.info")

    // Then: the session asks to step out rather than announcing a pause nothing can show
    expect(socket.lastRequest().method).toBe("Debugger.stepOut")
    expect(session.getState().pause).toBeNull()
    expect(announced).toHaveLength(0)

    // Back in the deployment, the pause is a real one.
    socket.event("Debugger.resumed", {})
    paused(socket, "index.js", 7)
    expect(session.getState().pause?.frames[0].location).toMatchObject({ path: "index.js", line: 8 })
    expect(announced).toHaveLength(1)

    // An exception thrown inside the runtime is a real stop: kept, and the
    // frame named as the runtime's.
    session.resume()
    socket.event("Debugger.resumed", {})
    session.stepInto()
    socket.event("Debugger.resumed", {})
    socket.event("Debugger.paused", {
      reason: "exception",
      data: { type: "object", className: "TypeError", description: "TypeError: boom" },
      callFrames: [
        {
          callFrameId: "f-internal",
          functionName: "console.info",
          location: { scriptId: "s-runtime", lineNumber: 669, columnNumber: 10 },
          url: "",
          scopeChain: [],
          this: { type: "undefined" },
        },
      ],
    })
    expect(session.getState().pause?.frames[0]).toMatchObject({
      internal: true,
      location: { path: "(runtime internals)", line: 670 },
    })
    expect(session.getState().console.at(-1)?.text).toBe(
      "Paused at (runtime internals):670 (exception)",
    )
  })
})

describe("DebugSession > reconnect", () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it("counts connections and says in the console that the container was replaced", async () => {
    const { session, bridge } = makeSession()
    // A session that waited through a "no container" close first: that
    // socket reached nothing and is not a connection.
    session.start(BRIDGE)
    const waiting = bridge.latest()
    waiting.open()
    waiting.serverClose(1011, "no container")
    expect(session.getState().status).toBe("waiting")
    expect(session.getState().connections).toBe(0)
    session.retry()
    const first = bridge.latest()
    first.open()
    await flush()
    first.respondAll()
    await flush()
    first.respondAll()
    await flush()
    expect(session.getState().connections).toBe(1)
    expect(session.getState().console).toEqual([])

    first.serverClose(1012, "service restart")
    await vi.advanceTimersByTimeAsync(10)
    const second = bridge.latest()
    second.open()
    await flush()
    // Not yet: the new inspector has not answered.
    expect(session.getState().connections).toBe(1)
    second.respondAll()
    await flush()
    second.respondAll()
    await flush()
    expect(session.getState().connections).toBe(2)
    expect(session.getState().console.at(-1)).toMatchObject({
      kind: "marker",
      text: expect.stringMatching(/^Container replaced — reconnected/),
    })
  })
})
