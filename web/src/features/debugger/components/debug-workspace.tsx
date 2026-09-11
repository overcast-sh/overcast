/**
 * The Code tab's layout while a console session is open (docs/plans/
 * compute-debugger-console.md § 3.5): the code pane with the sidebar of
 * panels beside it and the drawer of Logs and Debug console below. With no
 * session mounted, or an idle one, this renders its child and nothing
 * else — no panel subscribes, no query polls.
 *
 * Widths are fixed (the repo has no resizable-panel primitive): a 20rem
 * sidebar that scrolls inside the code pane's height, a 16rem drawer.
 * Below the app's narrow breakpoint the sidebar becomes a tab strip
 * between the code and the drawer.
 */
import type { ReactNode } from "react"
import { cn } from "@/lib/utils"
import { NARROW_SIDEBAR_QUERY } from "@/components/layout/use-sidebar-collapse"
import { useMediaQuery } from "@/hooks/use-media-query"
import { useDebugSessionState, useOptionalDebugSession } from "../session/hooks"
import { DebugDrawer } from "./debug-drawer"
import { DebugSidebar } from "./debug-sidebar"

export interface DebugWorkspaceProps {
  /** The function's log group, for the Logs drawer; `null` when it has none. */
  logGroup: string | null
  /** The code pane — a `DebugCodeBrowser`. */
  children: ReactNode
}

export function DebugWorkspace({ logGroup, children }: DebugWorkspaceProps) {
  const session = useOptionalDebugSession()
  if (!session) return <>{children}</>
  return <SessionWorkspace logGroup={logGroup}>{children}</SessionWorkspace>
}

function SessionWorkspace({ logGroup, children }: DebugWorkspaceProps) {
  const open = useDebugSessionState((s) => s.status !== "idle")
  if (!open) return <>{children}</>
  return <OpenWorkspace logGroup={logGroup}>{children}</OpenWorkspace>
}

function OpenWorkspace({ logGroup, children }: DebugWorkspaceProps) {
  const narrow = useMediaQuery(NARROW_SIDEBAR_QUERY)
  return (
    <div className="flex flex-col gap-3" data-testid="debug-workspace" data-narrow={narrow}>
      <div className={cn("flex gap-3", narrow ? "flex-col" : "items-stretch")}>
        <div className="min-w-0 flex-1">{children}</div>
        {narrow ? (
          <aside
            aria-label="Debug panels"
            className="rounded-md border border-border bg-bg-elevated px-2 pb-1"
          >
            <DebugSidebar narrow />
          </aside>
        ) : (
          // Stretched to the code pane's height and scrolling inside it: the
          // inner pane is absolutely positioned so the sidebar's content can
          // never make the row taller than the code.
          <aside
            aria-label="Debug panels"
            className="relative w-80 shrink-0 rounded-md border border-border bg-bg-elevated"
          >
            <div className="absolute inset-0 overflow-auto">
              <DebugSidebar narrow={false} />
            </div>
          </aside>
        )}
      </div>
      <section
        aria-label="Debug drawer"
        className="flex h-64 flex-col rounded-md border border-border bg-bg-elevated px-3 pb-2"
      >
        <DebugDrawer logGroup={logGroup} className="min-h-0 flex-1" />
      </section>
    </div>
  )
}
