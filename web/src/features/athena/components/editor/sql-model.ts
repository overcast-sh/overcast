import { loader } from "@monaco-editor/react"
import type * as Monaco from "monaco-editor"

/** Drops a closed query tab's model, which `keepCurrentModel` otherwise keeps for good. */
export function disposeSqlModel(path: string): void {
  const monaco = loader.__getMonacoInstance() as typeof Monaco | null
  monaco?.editor.getModel(monaco.Uri.parse(path))?.dispose()
}
