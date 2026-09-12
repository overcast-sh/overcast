import { useCallback } from "react"
import { AlertCircle } from "lucide-react"
import { Spinner } from "@/components/ui/primitives"
import { DebugCodeBrowser } from "@/features/debugger/components/debug-code-browser"
import { DebugWorkspace } from "@/features/debugger/components/debug-workspace"
import { lambda } from "@/services/api"
import type { LambdaFunctionSource } from "@/types"

export function CodeTab({
  source,
  sourceLoading,
  sourceError,
  currentEditorValue,
  setEditedFiles,
  setActiveFilePath,
  name,
  logGroup,
}: {
  source: LambdaFunctionSource | undefined
  sourceLoading: boolean
  sourceError: boolean
  currentEditorValue: string
  setEditedFiles: React.Dispatch<React.SetStateAction<Record<string, string>>>
  setActiveFilePath: (path: string) => void
  name: string
  /** The function's log group, for the debug drawer's Logs; `null` when it has none. */
  logGroup: string | null
}) {
  const loadFile = useCallback(
    async (path: string) => {
      const data = await lambda.getSource(name, path)
      return { content: data.source, language: data.language }
    },
    [name],
  )

  if (sourceLoading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Spinner className="h-5 w-5" />
      </div>
    )
  }
  if (sourceError) {
    return <p className="text-sm text-danger">Failed to load source. Is the emulator running?</p>
  }

  return (
    <div className="flex flex-col gap-3">
      {source?.placeholder && <PlaceholderSourceNotice />}
      {/* A plain CodeBrowser until a console debug session opens over it —
          then the panels sit beside it and the drawer below. */}
      <DebugWorkspace logGroup={logGroup}>
        <DebugCodeBrowser
          files={(source?.files ?? []).map((f) => ({ name: f.name, size: f.size }))}
          initialFile={source?.filename}
          initialValue={currentEditorValue}
          language={source?.language}
          loadFile={loadFile}
          onChange={(path, value) => setEditedFiles((prev) => ({ ...prev, [path]: value }))}
          onActiveFileChange={setActiveFilePath}
          height="65vh"
          target={{ service: "lambda", resource: name }}
        />
      </DebugWorkspace>
    </div>
  )
}

/**
 * Shown when the emulator could not read a deployment package and is falling
 * back to an example. Without it the editor presents a stub as though it were
 * the deployed code, which makes a function that cannot run look perfectly fine.
 */
function PlaceholderSourceNotice() {
  return (
    <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning-muted px-3 py-2 text-sm text-fg-muted">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
      <span>
        This is an example, not this function&rsquo;s code. No readable deployment package is stored
        for it, so the emulator cannot show what would run.
      </span>
    </div>
  )
}
