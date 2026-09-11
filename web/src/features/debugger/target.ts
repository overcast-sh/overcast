/**
 * Small readers over a `DebuggerTarget` descriptor, shared by the panel, the
 * overview line and the Test tab hint. Kept out of the component files so
 * those export only components (fast refresh).
 */
import { API_BASE, endpointResolver } from "@/services/api/base"
import { DEFAULT_ENDPOINT, type EmulatorEndpoint } from "@/services/discovery"
import type { DebuggerTarget } from "@/types"

/** The states in which a client holds the port — the clock is suspended in both. */
export const ATTACHED_STATES: ReadonlySet<string> = new Set(["attached", "paused"])

/** `host:port` as an editor's attach dialog wants it. */
export function listenAddress(target: Pick<DebuggerTarget, "listen">): string {
  return `${target.listen.host}:${target.listen.port}`
}

/** Docker's own short form of a container id. */
export function shortContainerId(id: string): string {
  return id.slice(0, 12)
}

// ─── Console debugging (phase 2) ──────────────────────────────────────────

/**
 * What the descriptor says about debugging inside the console
 * (docs/plans/compute-debugger-console.md § 3.2): `consoleDebug` is the
 * offer, `bridgePath` where to take it up. An entry synthesised for an
 * untagged resource carries neither.
 */
export interface ConsoleDebug {
  /** The server offers an in-console session for this target. */
  available: boolean
  /** The `/_overcast/...` bridge path, when offered. */
  bridgePath: string | null
}

export function consoleDebugOf(
  target: Pick<DebuggerTarget, "consoleDebug" | "bridgePath">,
): ConsoleDebug {
  const bridgePath = target.bridgePath === "" ? null : target.bridgePath
  return { available: target.consoleDebug && bridgePath !== null, bridgePath }
}

/** The BFF's proxy prefix for the emulator's own endpoints — `/_overcast/x` is served at `/api/x`. */
const EMULATOR_PREFIX = /^\/_overcast(?=\/)/

/**
 * The WebSocket URL for a bridge path, on the console's own origin and
 * through the BFF's `/api` prefix, as every other emulator endpoint is
 * reached (§ 2, "same origin"). The endpoint-selection headers `apiFetch`
 * sends cannot ride on a WebSocket upgrade, so a console pointed at an
 * emulator other than its default names it in the query instead — `ep`,
 * the parameter the BFF reads for the same reason on its event stream.
 * On the default endpoint nothing is added and the BFF's own default
 * applies, which is the same server.
 */
export function bridgeUrl(
  bridgePath: string,
  endpoint: Pick<EmulatorEndpoint, "baseUrl"> = endpointResolver.get(),
  location: { protocol: string; host: string } = window.location,
): string {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:"
  const url = `${scheme}//${location.host}${API_BASE}${bridgePath.replace(EMULATOR_PREFIX, "")}`
  if (endpoint.baseUrl === DEFAULT_ENDPOINT.baseUrl) return url
  return `${url}?${new URLSearchParams({ ep: endpoint.baseUrl }).toString()}`
}
