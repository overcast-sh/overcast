/**
 * Small readers over a `DebuggerTarget` descriptor, shared by the panel, the
 * overview line and the Test tab hint. Kept out of the component files so
 * those export only components (fast refresh).
 */
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
