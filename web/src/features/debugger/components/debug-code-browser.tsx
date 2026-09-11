/**
 * `CodeBrowser` with a debug session drawn over it (docs/plans/
 * compute-debugger-console.md § 3.5, Code tab): gutter breakpoints, the
 * current-line marker, the toolbar and its keys, the *Original* file group
 * and the *Show compiled* toggle once a map has loaded, and a reveal of the
 * paused location.
 *
 * With no session mounted, or a session that is idle, this is exactly
 * `CodeBrowser`: no decorations, no toolbar, no key handling, nothing
 * subscribed. Everything session-shaped lives in `ActiveCodeBrowser`, which
 * mounts only while a session is open.
 */
import { useCallback, useMemo, useState } from "react"
import {
  CodeBrowser,
  type CodeBrowserProps,
  type LineDecoration,
  type LoadedFile,
} from "@/components/ui/code-browser"
import { languageForPath } from "@/lib/language-for-path"
import { useDebugSessionState, useOptionalDebugSession } from "../session/hooks"
import type { DebugSession, PauseState } from "../session/session"
import { BreakpointConditionEditor } from "./breakpoint-condition-editor"
import { DebugToolbar } from "./debug-toolbar"
import { DEBUG_CODE_HEIGHT } from "./debug-workspace"

const ORIGINAL_GROUP = "Original"

export function DebugCodeBrowser(props: CodeBrowserProps) {
  const session = useOptionalDebugSession()
  if (!session) return <CodeBrowser {...props} />
  return <SessionCodeBrowser session={session} {...props} />
}

function SessionCodeBrowser({ session, ...props }: CodeBrowserProps & { session: DebugSession }) {
  const status = useDebugSessionState((s) => s.status)
  if (status === "idle") return <CodeBrowser {...props} />
  return <ActiveCodeBrowser session={session} {...props} />
}

/** The keys VS Code binds, acted on only while paused and only inside the pane. */
function handleDebugKey(session: DebugSession, paused: boolean, e: React.KeyboardEvent) {
  if (!paused) return
  switch (e.key) {
    case "F5":
      session.resume()
      break
    case "F10":
      session.stepOver()
      break
    case "F11":
      if (e.shiftKey) session.stepOut()
      else session.stepInto()
      break
    default:
      return
  }
  e.preventDefault()
  e.stopPropagation()
}

function ActiveCodeBrowser({
  session,
  files,
  loadFile,
  ...props
}: CodeBrowserProps & { session: DebugSession }) {
  const breakpoints = useDebugSessionState((s) => s.breakpoints)
  const pause = useDebugSessionState((s) => s.pause)
  const originalFiles = useDebugSessionState((s) => s.originalFiles)
  const hasSourceMaps = useDebugSessionState((s) => s.hasSourceMaps)
  const [showCompiled, setShowCompiled] = useState(false)
  const [conditionAt, setConditionAt] = useState<{ path: string; line: number } | null>(null)

  // ── Files: original sources in their own group; compiled ones behind the toggle ──
  const browserFiles = useMemo(() => {
    if (!hasSourceMaps) return files
    const originalPaths = new Set(originalFiles.map((f) => f.path))
    const generated = new Set(originalFiles.flatMap((f) => f.generated))
    const deployed = files.filter(
      (f) =>
        !originalPaths.has(f.name) &&
        (showCompiled || (!generated.has(f.name) && !f.name.endsWith(".map"))),
    )
    const originals = originalFiles.map((f) => ({ name: f.path, size: 0, group: ORIGINAL_GROUP }))
    return [...deployed, ...originals]
  }, [files, originalFiles, hasSourceMaps, showCompiled])

  const loadDebugFile = useCallback(
    async (path: string): Promise<LoadedFile> => {
      const original = originalFiles.find((f) => f.path === path)
      if (!original || original.origin === "deployment") {
        if (!loadFile) throw new Error(`no loader for ${path}`)
        return loadFile(path)
      }
      const content = await session.originalContent(path)
      return {
        content: content ?? "",
        language: languageForPath(path),
        readOnly: true,
        notice:
          original.origin === "map"
            ? "Read-only — the source map carries this file's text; the deployment does not."
            : "No content — the source map names this file but nothing carries its text.",
      }
    },
    [session, originalFiles, loadFile],
  )

  // ── Decorations: breakpoints as glyphs, the selected frame's line as current ──
  const decorations = useMemo(() => {
    const byFile: Record<string, LineDecoration[]> = {}
    const push = (path: string, d: LineDecoration) => (byFile[path] ??= []).push(d)
    for (const bp of breakpoints) {
      push(bp.path, {
        line: bp.line,
        kind: bp.enabled ? "glyph" : "glyph-muted",
        title: breakpointTitle(bp.condition, bp.enabled, bp.bound),
      })
    }
    const frame = pause?.frames.at(pause.selectedFrame)
    if (frame && !frame.internal) {
      push(frame.location.path, {
        line: frame.location.line,
        kind: "current",
        title: `Paused in ${frame.functionName}`,
      })
    }
    return byFile
  }, [breakpoints, pause])

  const revealPosition = useMemo(() => revealFor(pause), [pause])

  const onGutterClick = useCallback(
    (path: string, line: number, kind: "toggle" | "menu") => {
      if (kind === "toggle") session.toggleBreakpoint(path, line)
      else setConditionAt({ path, line })
    },
    [session],
  )

  const paused = pause !== null
  const existing = conditionAt
    ? session.breakpointAt(conditionAt.path, conditionAt.line)
    : undefined

  return (
    <div className="flex flex-col gap-2" onKeyDown={(e) => handleDebugKey(session, paused, e)}>
      <DebugToolbar />
      {conditionAt && (
        <BreakpointConditionEditor
          key={`${conditionAt.path}:${conditionAt.line}`}
          path={conditionAt.path}
          line={conditionAt.line}
          breakpoint={existing}
          onSave={(condition) => {
            if (existing) session.updateBreakpoint(existing.id, { condition })
            else session.addBreakpoint(conditionAt.path, conditionAt.line, condition)
            setConditionAt(null)
          }}
          onRemove={() => {
            if (existing) session.removeBreakpoint(existing.id)
            setConditionAt(null)
          }}
          onClose={() => setConditionAt(null)}
        />
      )}
      <CodeBrowser
        {...props}
        // The workspace's budget, not the idle tab's: the drawer has to fit
        // under the pane on one screen.
        height={DEBUG_CODE_HEIGHT}
        files={browserFiles}
        loadFile={loadDebugFile}
        decorations={decorations}
        onGutterClick={onGutterClick}
        revealPosition={revealPosition}
        explorerActions={
          hasSourceMaps ? (
            <label className="flex cursor-pointer items-center gap-1 font-mono text-2xs tracking-normal normal-case">
              <input
                type="checkbox"
                checked={showCompiled}
                onChange={(e) => setShowCompiled(e.target.checked)}
                className="accent-accent"
              />
              Show compiled
            </label>
          ) : undefined
        }
      />
    </div>
  )
}

function breakpointTitle(condition: string, enabled: boolean, bound: boolean): string {
  const what = condition ? `Conditional breakpoint: ${condition}` : "Breakpoint"
  if (!enabled) return `${what} (disabled)`
  return bound ? what : `${what} (not yet bound — waiting for the script)`
}

/** Where to scroll on a pause: the selected frame, keyed so a new pause on the same line still reveals. */
function revealFor(pause: PauseState | null) {
  const frame = pause?.frames.at(pause.selectedFrame)
  // An internal frame is outside the deployment: nothing to open for it.
  if (!pause || !frame || frame.internal) return null
  return {
    path: frame.location.path,
    line: frame.location.line,
    column: frame.location.column,
    key: pause.id * 1_000 + pause.selectedFrame,
  }
}
