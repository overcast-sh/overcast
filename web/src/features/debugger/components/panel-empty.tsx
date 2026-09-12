import type { ReactNode } from "react"

/** The one line a debug panel shows when it has nothing yet — same voice in every panel. */
export function PanelEmpty({ children }: { children: ReactNode }) {
  return <p className="px-1 py-2 text-xs text-fg-muted">{children}</p>
}
