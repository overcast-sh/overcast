import type { SessionStatus } from "./session"

/**
 * One sentence per session status, shared by the Debug tab's controls and
 * the Code tab's toolbar so the two never disagree about what "waiting"
 * means. `error` carries its own text. `restored` — the page started this
 * session again for one left open — is said first, briefly, so the reader
 * knows why a session they did not start is up.
 */
export function sessionStatusLine(
  status: SessionStatus,
  error: string | null,
  restored = false,
): string {
  const line = statusSentence(status, error)
  return restored ? `Session restored. ${line}` : line
}

function statusSentence(status: SessionStatus, error: string | null): string {
  switch (status) {
    case "idle":
      return "No console session."
    case "connecting":
      return "Connecting to the debugger…"
    case "attached":
      return "Attached — set breakpoints in the Code tab and invoke from the Test tab."
    case "waiting":
      return "Waiting for a container — invoke once and the session attaches to it."
    case "reconnecting":
      return "Container replaced — reconnecting and re-applying breakpoints…"
    case "error":
      return error ?? "The session stopped."
  }
}

/** Whether a session exists at all, whatever it is doing. */
export function isSessionOpen(status: SessionStatus): boolean {
  return status !== "idle"
}
