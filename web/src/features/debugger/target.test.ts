import { DEFAULT_ENDPOINT } from "@/services/discovery"
import { debugTarget } from "@/test/debug-target"
import { bridgeUrl, consoleDebugOf } from "./target"

const PATH = "/_overcast/debugger/targets/lambda/my-fn/ws"
const HTTP = { protocol: "http:", host: "console.local:3000" }

describe("bridgeUrl", () => {
  it("is the bridge path under /api on the console's own origin, ws or wss to match", () => {
    expect(bridgeUrl(PATH, DEFAULT_ENDPOINT, HTTP)).toBe(
      "ws://console.local:3000/api/debugger/targets/lambda/my-fn/ws",
    )
    expect(bridgeUrl(PATH, DEFAULT_ENDPOINT, { protocol: "https:", host: "h" })).toBe(
      "wss://h/api/debugger/targets/lambda/my-fn/ws",
    )
  })

  it("names a non-default emulator in the query, since the upgrade cannot carry the header", () => {
    expect(bridgeUrl(PATH, { baseUrl: "http://localhost:4590" }, HTTP)).toBe(
      "ws://console.local:3000/api/debugger/targets/lambda/my-fn/ws?ep=http%3A%2F%2Flocalhost%3A4590",
    )
  })

  it("reads the console's current endpoint when none is given", () => {
    expect(bridgeUrl(PATH, undefined, HTTP)).toBe(
      "ws://console.local:3000/api/debugger/targets/lambda/my-fn/ws",
    )
  })
})

describe("consoleDebugOf", () => {
  it("offers a session only when the descriptor says so and names a bridge", () => {
    expect(consoleDebugOf(debugTarget())).toEqual({ available: false, bridgePath: null })
    expect(consoleDebugOf(debugTarget({ consoleDebug: true, bridgePath: PATH }))).toEqual({
      available: true,
      bridgePath: PATH,
    })
    expect(consoleDebugOf(debugTarget({ consoleDebug: true }))).toEqual({
      available: false,
      bridgePath: null,
    })
  })
})
