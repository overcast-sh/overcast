/**
 * Locals: the scope chain of the selected frame, each scope a root of the
 * `ValueTree` and its variables fetched only when opened (docs/plans/
 * compute-debugger-console.md § 3.5). The Local scope opens by itself; the
 * global scope, which is thousands of rows, never does unasked.
 *
 * The tree is keyed on the pause and the frame, so a step or a frame change
 * throws away every open node: the handles it held name the old frame's
 * values and are meaningless in the new one.
 */
import { useCallback } from "react"
import { scopeLabel } from "../scopes"
import { useDebugSession, useDebugSessionState } from "../session/hooks"
import { PanelEmpty } from "./panel-empty"
import { ValueTree, type TreeRoot } from "./value-tree"

export function LocalsPanel() {
  const session = useDebugSession()
  const pause = useDebugSessionState((s) => s.pause)
  const loadChildren = useCallback((objectId: string) => session.getProperties(objectId), [session])

  const frame = pause?.frames.at(pause.selectedFrame)
  if (!pause || !frame) return <PanelEmpty>Pause to see the frame's variables.</PanelEmpty>

  const roots: TreeRoot[] = frame.scopes.map((scope, index) => ({
    key: `${index}:${scope.kind}`,
    name: scopeLabel(scope),
    value: scope.objectId
      ? { type: "object", subtype: "scope", description: "", objectId: scope.objectId }
      : null,
    expanded: index === 0 && scope.kind !== "global",
  }))
  if (roots.length === 0) return <PanelEmpty>This frame has no scopes to show.</PanelEmpty>

  return (
    <ValueTree
      key={`${pause.id}:${pause.selectedFrame}`}
      roots={roots}
      loadChildren={loadChildren}
      aria-label="Locals"
    />
  )
}
