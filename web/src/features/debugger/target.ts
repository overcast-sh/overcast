/**
 * Small readers over a `DebuggerTarget` descriptor, shared by the panel, the
 * overview line and the Test tab hint. Kept out of the component files so
 * those export only components (fast refresh).
 */
import { API_BASE } from "@/services/api/base"
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
 * (docs/plans/compute-debugger-console.md § 3.2). The two fields —
 * `consoleDebug` and `bridgePath` — arrive with the regenerated `api.gen.ts`
 * from the backend phase; until then they are read here as optional, so a
 * descriptor without them means "not offered" rather than a crash, and no
 * generated file is edited by hand.
 */
export interface ConsoleDebug {
  /** The server offers an in-console session for this target. */
  available: boolean
  /** The `/_overcast/...` bridge path, when offered. */
  bridgePath: string | null
}

export function consoleDebugOf(target: DebuggerTarget): ConsoleDebug {
  const extra = target as { consoleDebug?: unknown; bridgePath?: unknown }
  const bridgePath =
    typeof extra.bridgePath === "string" && extra.bridgePath !== "" ? extra.bridgePath : null
  return { available: extra.consoleDebug === true && bridgePath !== null, bridgePath }
}

/** The BFF's proxy prefix for the emulator's own endpoints — `/_overcast/x` is served at `/api/x`. */
const EMULATOR_PREFIX = /^\/_overcast(?=\/)/

/**
 * The WebSocket URL for a bridge path, on the console's own origin and
 * through the BFF's `/api` prefix, as every other emulator endpoint is
 * reached (§ 2, "same origin"). The endpoint-selection headers `apiFetch`
 * sends cannot ride on a WebSocket upgrade; the BFF falls back to its
 * configured emulator for a request without them, which is the console's
 * default endpoint.
 */
export function bridgeUrl(
  bridgePath: string,
  location: { protocol: string; host: string } = window.location,
): string {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:"
  return `${scheme}//${location.host}${API_BASE}${bridgePath.replace(EMULATOR_PREFIX, "")}`
}
