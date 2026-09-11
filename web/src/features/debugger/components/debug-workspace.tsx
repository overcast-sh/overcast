/**
 * The Code tab's layout while a console session is open (docs/plans/
 * compute-debugger-console.md § 3.5): the code pane with the sidebar of
 * panels beside it and the drawer of Logs and Debug console below. With no
 * session mounted, or an idle one, this renders its child and nothing
 * else — no panel subscribes, no query polls.
 *
 * Sizes are fixed (the repo has no resizable-panel primitive): a 20rem
 * sidebar that scrolls inside the code pane's height, a 12rem drawer, and
 * the code pane at `DEBUG_CODE_HEIGHT`, so the whole page — header,
 * function overview, toolbar, code, panels and drawer — fits one 1080p
 * screen instead of the drawer landing below the fold under a pane mostly
 * empty. Below the app's narrow breakpoint the sidebar becomes a tab strip
 * between the code and the drawer.
 */
import type { ReactNode } from "react"
import { cn } from "@/lib/utils"
import { NARROW_SIDEBAR_QUERY } from "@/components/layout/use-sidebar-collapse"
import { useMediaQuery } from "@/hooks/use-media-query"
import { useDebugSessionState, useOptionalDebugSession } from "../session/hooks"
import { DebugDrawer } from "./debug-drawer"
import { DebugSidebar } from "./debug-sidebar"

/**
 * The code pane's height while a session is open, in place of the Code
 * tab's idle `65vh`: the viewport less what the rest of the page takes
 * above and below it — the app header, the function overview and the tab
 * strip (about 26rem), the toolbar, the drawer, the gaps and the page's
 * padding — floored so a short window keeps a usable editor and scrolls
 * instead, and never taller than the idle pane. Idle, the pane is not this
 * component's to size.
 */
export const DEBUG_CODE_HEIGHT = "clamp(18rem, 100vh - 44rem, 65vh)"

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
        className="flex h-48 flex-col rounded-md border border-border bg-bg-elevated px-3 pb-2"
      >
        <DebugDrawer logGroup={logGroup} className="min-h-0 flex-1" />
      </section>
    </div>
  )
}
