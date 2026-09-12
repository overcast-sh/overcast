/**
 * `DebugSessionProvider` — one `DebugSession` per resource page, shared by
 * every tab under it (docs/plans/compute-debugger-console.md § 3.4). Mounted
 * at the function route so Code, Test and Debug read one session. The hooks
 * that read it are in `./hooks`.
 *
 * Cheap when idle: the session opens no socket until `start()`, and the
 * descriptor poll below runs only while a session is open — it is what
 * wakes a session waiting for a container once one appears — or, once, for
 * a function whose session was open when its page was last left, which is
 * started again here (§ 6, auto-restore). A function the reader never
 * opened a session on asks the server nothing.
 */
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { debuggerTargetQueryOptions } from "../data"
import { bridgeUrl, consoleDebugOf } from "../target"
import { DebugSessionContext } from "./context"
import { DebugSession, type DebugSessionOptions } from "./session"

/** How long the status line says "Session restored" before the note goes on its own. */
const RESTORED_NOTE_MS = 15_000

export interface DebugSessionProviderProps {
  service: string
  resource: string
  /**
   * Read a deployed file by root-relative path — the service's source
   * endpoint. Captured when the session is created, which is once per
   * resource: a later change to the prop is not picked up.
   */
  fetchFile: (path: string) => Promise<string>
  /** Root-relative paths of the deployed files, once the source query has them. */
  files?: readonly string[]
  /**
   * Called when the descriptor names a different container than the one
   * the session was last seen on — hot reload replaced it — so the page
   * can re-read what the container runs. Pass a stable callback.
   */
  onContainerReplaced?: () => void
  /** Test seam: the session's own seams — a scripted bridge, a storage, short backoffs. */
  sessionOptions?: Pick<DebugSessionOptions, "dial" | "storage" | "backoffMs">
  children: ReactNode
}

export function DebugSessionProvider(props: DebugSessionProviderProps) {
  // A different resource is a different session, breakpoints and all.
  return <ScopedProvider key={`${props.service}/${props.resource}`} {...props} />
}

function ScopedProvider({
  service,
  resource,
  fetchFile,
  files,
  onContainerReplaced,
  sessionOptions,
  children,
}: DebugSessionProviderProps) {
  const [session] = useState(
    () => new DebugSession({ key: `${service}/${resource}`, fetchFile, ...sessionOptions }),
  )
  useEffect(() => () => session.dispose(), [session])

  useEffect(() => {
    if (files) session.setDeploymentFiles(files)
  }, [session, files])

  // A session left open on this function is started again once the
  // descriptor confirms the console is still on offer; that question, asked
  // once, is the one time an idle page reads the descriptor at all.
  const restorable = useSyncExternalStore(session.subscribe, () => session.restorable)

  // While a session is open, watch the descriptor: a container appearing or
  // being replaced is what a `waiting` session is waiting for.
  const status = useSyncExternalStore(session.subscribe, () => session.getState().status)
  const { data: target, isError } = useQuery({
    ...debuggerTargetQueryOptions(service, resource),
    enabled: restorable || (status !== "idle" && status !== "error"),
  })
  useEffect(() => {
    if (!restorable || (target === undefined && !isError)) return
    session.settleRestore()
    if (!target) return
    const { available, bridgePath } = consoleDebugOf(target)
    if (available && target.enabled && bridgePath !== null) {
      session.start(bridgeUrl(bridgePath), { restored: true })
    }
  }, [restorable, target, isError, session])

  const restored = useSyncExternalStore(session.subscribe, () => session.getState().restored)
  useEffect(() => {
    if (!restored) return
    const id = setTimeout(() => session.dismissRestoredNote(), RESTORED_NOTE_MS)
    return () => clearTimeout(id)
  }, [restored, session])

  const containerId = target?.containerId ?? ""
  const seenContainerRef = useRef<string | null>(null)
  useEffect(() => {
    if (!containerId) return
    session.containerChanged()
    if (seenContainerRef.current !== null && seenContainerRef.current !== containerId) {
      onContainerReplaced?.()
    }
    seenContainerRef.current = containerId
  }, [session, containerId, onContainerReplaced])

  return <DebugSessionContext.Provider value={session}>{children}</DebugSessionContext.Provider>
}
