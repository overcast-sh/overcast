/**
 * `DebugSessionProvider` — one `DebugSession` per resource page, shared by
 * every tab under it (docs/plans/compute-debugger-console.md § 3.4). Mounted
 * at the function route so Code, Test and Debug read one session. The hooks
 * that read it are in `./hooks`.
 *
 * Cheap when idle: the session opens no socket until `start()`, and the
 * descriptor poll below runs only while a session is open — it is what
 * wakes a session waiting for a container once one appears.
 */
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { debuggerTargetQueryOptions } from "../data"
import { DebugSessionContext } from "./context"
import { DebugSession } from "./session"

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
  children,
}: DebugSessionProviderProps) {
  const [session] = useState(() => new DebugSession({ key: `${service}/${resource}`, fetchFile }))
  useEffect(() => () => session.dispose(), [session])

  useEffect(() => {
    if (files) session.setDeploymentFiles(files)
  }, [session, files])

  // While a session is open, watch the descriptor: a container appearing or
  // being replaced is what a `waiting` session is waiting for.
  const status = useSyncExternalStore(session.subscribe, () => session.getState().status)
  const { data: target } = useQuery({
    ...debuggerTargetQueryOptions(service, resource),
    enabled: status !== "idle" && status !== "error",
  })
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
