import { fakeBridge } from "@/test/fake-bridge"
import { CdpClient, CdpClosedError, CdpError, type CdpClientStatus } from "./cdp-client"

const URL = "ws://console/api/debugger/targets/lambda/my-fn/ws"

function client(backoffMs = [100, 200]) {
  const bridge = fakeBridge()
  const statuses: CdpClientStatus[] = []
  const c = new CdpClient({ url: URL, dial: bridge.dial, backoffMs })
  c.onStatus((s) => statuses.push(s))
  return { c, bridge, statuses }
}

/** Lets a settled promise's callbacks run without advancing any timer. */
const flush = () => new Promise<void>((resolve) => queueMicrotask(resolve))

describe("CdpClient > correlation", () => {
  it("dials the URL, numbers requests from 1 and resolves each with its own result", async () => {
    const { c, bridge } = client()
    c.open()
    const socket = bridge.latest()
    expect(socket.url).toBe(URL)
    socket.open()

    const first = c.send("Debugger.enable", {})
    const second = c.send("Runtime.enable", {})
    expect(socket.sent).toEqual([
      { id: 1, method: "Debugger.enable", params: {} },
      { id: 2, method: "Runtime.enable", params: {} },
    ])

    // Out of order, to prove the id — not arrival — decides who gets what.
    socket.respond(2, {})
    socket.respond(1, { debuggerId: "d1" })
    await expect(first).resolves.toEqual({ debuggerId: "d1" })
    await expect(second).resolves.toEqual({})
  })

  it("rejects a command the protocol answers with an error frame", async () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().open()

    const reply = c.send("Debugger.removeBreakpoint", { breakpointId: "nope" })
    bridge.latest().fail(1, -32000, "Breakpoint not found")

    await expect(reply).rejects.toMatchObject({
      name: "CdpError",
      code: -32000,
      method: "Debugger.removeBreakpoint",
      message: "Breakpoint not found",
    })
    await expect(reply).rejects.toBeInstanceOf(CdpError)
  })

  it("rejects a command sent while the socket is not open", async () => {
    const { c } = client()
    await expect(c.send("Debugger.pause", {})).rejects.toBeInstanceOf(CdpClosedError)
    c.open()
    // Dialled but not yet accepted.
    await expect(c.send("Debugger.pause", {})).rejects.toBeInstanceOf(CdpClosedError)
  })

  it("ignores frames it cannot read and replies it did not ask for", () => {
    const { c, bridge } = client()
    c.open()
    const socket = bridge.latest()
    socket.open()
    expect(() => {
      socket.raw("not json")
      socket.raw(new ArrayBuffer(4))
      socket.respond(99, {})
    }).not.toThrow()
    expect(c.status).toBe("open")
  })
})

describe("CdpClient > events", () => {
  it("fans an event out to every subscriber and honours unsubscribe", () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().open()

    const a = vi.fn()
    const b = vi.fn()
    const offA = c.on("Debugger.resumed", a)
    c.on("Debugger.resumed", b)

    bridge.latest().event("Debugger.resumed", {})
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)

    offA()
    bridge.latest().event("Debugger.resumed", {})
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(2)
  })

  it("delivers the event's params", () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().open()
    const paused = vi.fn()
    c.on("Debugger.paused", paused)

    bridge.latest().event("Debugger.paused", { callFrames: [], reason: "other" })
    expect(paused).toHaveBeenCalledWith({ callFrames: [], reason: "other" })
  })
})

describe("CdpClient > connection life", () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it("reconnects with backoff on 1012 and announces each open", async () => {
    const { c, bridge, statuses } = client([100, 200])
    const opened = vi.fn()
    c.onOpen(opened)
    c.open()
    bridge.latest().open()
    expect(opened).toHaveBeenCalledTimes(1)

    const inFlight = c.send("Debugger.enable", {})
    bridge.latest().serverClose(1012, "service restart")
    await expect(inFlight).rejects.toBeInstanceOf(CdpClosedError)
    expect(c.status).toBe("reconnecting")
    expect(bridge.sockets).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(99)
    expect(bridge.sockets).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(bridge.sockets).toHaveLength(2)

    // A second drop before the socket opens backs off further.
    bridge.latest().serverClose(1006, "")
    await vi.advanceTimersByTimeAsync(100)
    expect(bridge.sockets).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(100)
    expect(bridge.sockets).toHaveLength(3)

    bridge.latest().open()
    expect(opened).toHaveBeenCalledTimes(2)
    expect(c.status).toBe("open")
    expect(statuses).toEqual(["connecting", "open", "reconnecting", "open"])
  })

  it("stops on 1011 (no container) and dials again only when asked", async () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().serverClose(1011, "no container")
    expect(c.status).toBe("no-container")

    await vi.advanceTimersByTimeAsync(10_000)
    expect(bridge.sockets).toHaveLength(1)

    c.open()
    expect(c.status).toBe("connecting")
    expect(bridge.sockets).toHaveLength(2)
  })

  it("close() is final: the socket is closed, nothing reconnects, pending commands reject", async () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().open()
    const inFlight = c.send("Debugger.enable", {})

    c.close()
    expect(bridge.latest().closedBy).toEqual({ code: 1000, reason: "session stopped" })
    expect(c.status).toBe("closed")
    await expect(inFlight).rejects.toBeInstanceOf(CdpClosedError)

    // A late close event from the old socket changes nothing.
    bridge.latest().serverClose(1006, "")
    await vi.advanceTimersByTimeAsync(10_000)
    expect(bridge.sockets).toHaveLength(1)
    expect(c.status).toBe("closed")
  })

  it("a close while a reconnect is scheduled cancels it", async () => {
    const { c, bridge } = client()
    c.open()
    bridge.latest().open()
    bridge.latest().serverClose(1012, "service restart")
    expect(c.status).toBe("reconnecting")

    c.close()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(bridge.sockets).toHaveLength(1)
  })

  it("a dial that throws is retried like a dropped socket", async () => {
    let calls = 0
    const bridge = fakeBridge()
    const c = new CdpClient({
      url: URL,
      dial: (url) => {
        calls += 1
        if (calls === 1) throw new Error("blocked")
        return bridge.dial(url)
      },
      backoffMs: [50],
    })
    c.open()
    expect(c.status).toBe("reconnecting")
    await vi.advanceTimersByTimeAsync(50)
    expect(bridge.sockets).toHaveLength(1)
    await flush()
  })
})
