/**
 * The Code tab's layout while a console session is open (docs/plans/
 * compute-debugger-console.md § 3.5): the code pane with the sidebar of
 * panels beside it and the drawer of Logs and Debug console below. With no
 * session mounted, or an idle one, this renders its child and nothing
 * else — no panel subscribes, no query polls.
 *
 * The stepping keys are bound here, on the workspace, rather than on the
 * code pane: a reader whose focus is in the Watch strip or the console's
 * input is still debugging, and F10 from there should still step. F5 is
 * swallowed whenever a session is open, paused or not — a debugger's F5 is
 * Continue, and the browser's is a reload that would end the session.
 *
 * The sidebar's width and the drawer's height are the reader's to drag
 * (`ResizableSplit`, § 6) and remembered in the browser; the code pane sits
 * at `DEBUG_CODE_HEIGHT`, which follows the drawer, so the whole page —
 * header, function overview, toolbar, code, panels and drawer — fits one
 * 1080p screen instead of the drawer landing below the fold under a pane
 * mostly empty. Below the app's narrow breakpoint the sidebar becomes a tab
 * strip between the code and the drawer, with only the drawer resizable.
 */
import type { CSSProperties, KeyboardEvent, ReactNode } from "react"
import { NARROW_SIDEBAR_QUERY } from "@/components/layout/use-sidebar-collapse"
import { ResizableSplit } from "@/components/ui/resizable-split"
import { useLocalStorage } from "@/hooks/use-local-storage"
import { useMediaQuery } from "@/hooks/use-media-query"
import { useDebugSession, useDebugSessionState, useOptionalDebugSession } from "../session/hooks"
import type { DebugSession } from "../session/session"
import { DebugDrawer } from "./debug-drawer"
import { DebugSidebar } from "./debug-sidebar"

/**
 * The panel sizes, in pixels. The sidebar must fit a real variable name
 * beside its value; the drawer must show a dozen lines of logs or console
 * under its tab strip. Neither may squeeze the code pane out, so both stop
 * well short of a wide window.
 */
const SIDEBAR_WIDTH = { default: 320, min: 240, max: 640 }
const DRAWER_HEIGHT = { default: 224, min: 160, max: 720 }
const SIDEBAR_WIDTH_KEY = "overcast-debug:sidebar-width"
const DRAWER_HEIGHT_KEY = "overcast-debug:drawer-height"

/**
 * The code pane's height while a session is open, in place of the Code
 * tab's idle `65vh`: the viewport less what the rest of the page takes
 * above and below it — the app header, the function overview and the tab
 * strip (about 26rem), the toolbar, the drawer at whatever height it has
 * been dragged to (`--debug-drawer-height`, set by the workspace), the
 * gaps and the page's padding — floored so a short window keeps a usable
 * editor and scrolls instead, and never taller than the idle pane. Idle,
 * the pane is not this component's to size.
 */
export const DEBUG_CODE_HEIGHT =
  "clamp(18rem, 100vh - 32rem - var(--debug-drawer-height, 14rem), 65vh)"

/**
 * The keys VS Code binds. Stepping acts only while paused; F5 is taken
 * whenever the workspace is open, so a reflex reload cannot drop a session.
 */
function handleDebugKey(session: DebugSession, paused: boolean, e: KeyboardEvent): void {
  switch (e.key) {
    case "F5":
      if (paused) session.resume()
      break
    case "F10":
      if (!paused) return
      session.stepOver()
      break
    case "F11":
      if (!paused) return
      if (e.shiftKey) session.stepOut()
      else session.stepInto()
      break
    default:
      return
  }
  e.preventDefault()
  e.stopPropagation()
}

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
  const session = useDebugSession()
  const paused = useDebugSessionState((s) => s.pause !== null)
  const narrow = useMediaQuery(NARROW_SIDEBAR_QUERY)
  // Controlled here rather than left to the split, because the code pane's
  // height budget reads it — see DEBUG_CODE_HEIGHT.
  const [drawerHeight, setDrawerHeight] = useLocalStorage(DRAWER_HEIGHT_KEY, DRAWER_HEIGHT.default)
  const style = { "--debug-drawer-height": `${drawerHeight}px` } as CSSProperties

  const codeRow = narrow ? (
    <div className="flex flex-col gap-3">
      <div className="min-w-0">{children}</div>
      <aside
        aria-label="Debug panels"
        className="rounded-md border border-border bg-bg-elevated px-2 pb-1"
      >
        <DebugSidebar narrow />
      </aside>
    </div>
  ) : (
    <ResizableSplit
      direction="horizontal"
      sized="second"
      label="Resize the debug panels"
      defaultSize={SIDEBAR_WIDTH.default}
      minSize={SIDEBAR_WIDTH.min}
      maxSize={SIDEBAR_WIDTH.max}
      storageKey={SIDEBAR_WIDTH_KEY}
      className="items-stretch"
      secondClassName="flex flex-col"
      first={children}
      // Stretched to the code pane's height and scrolling inside it: the
      // inner pane is absolutely positioned so the sidebar's content can
      // never make the row taller than the code.
      second={
        <aside
          aria-label="Debug panels"
          className="relative min-h-0 flex-1 rounded-md border border-border bg-bg-elevated"
        >
          <div className="absolute inset-0 overflow-auto">
            <DebugSidebar narrow={false} />
          </div>
        </aside>
      }
    />
  )

  return (
    <div
      className="flex flex-col"
      style={style}
      data-testid="debug-workspace"
      data-narrow={narrow}
      onKeyDown={(e) => handleDebugKey(session, paused, e)}
    >
      <ResizableSplit
        direction="vertical"
        sized="second"
        label="Resize the debug drawer"
        defaultSize={DRAWER_HEIGHT.default}
        minSize={DRAWER_HEIGHT.min}
        maxSize={DRAWER_HEIGHT.max}
        size={drawerHeight}
        onSizeChange={setDrawerHeight}
        secondClassName="flex flex-col"
        first={codeRow}
        second={
          <section
            aria-label="Debug drawer"
            className="flex min-h-0 flex-1 flex-col rounded-md border border-border bg-bg-elevated px-3 pb-2"
          >
            <DebugDrawer logGroup={logGroup} className="min-h-0 flex-1" />
          </section>
        }
      />
    </div>
  )
}
