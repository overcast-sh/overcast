/**
 * `CodeBrowser` with a debug session drawn over it (docs/plans/
 * compute-debugger-console.md § 3.5, Code tab): gutter breakpoints, the
 * current-line marker, the toolbar, the *Original* file group and the
 * *Show compiled* toggle once a map has loaded, and a reveal of the paused
 * location. The stepping keys are bound by `DebugWorkspace`, which wraps
 * this and the panels, so they work from a panel as well as from the pane.
 *
 * With no session mounted, or a session that is idle on a resource the
 * server offers no console session for, this is exactly `CodeBrowser`: no
 * decorations, no toolbar, nothing subscribed. Idle on a resource that
 * *does* offer one, the gutter is live and the breakpoints the last session
 * left are drawn hollow, with a strip above the pane that starts a session
 * — so the reader who came to the code first sets breakpoints here and
 * starts from here, rather than finding the button on the Debug tab.
 * Everything session-shaped lives in `ActiveCodeBrowser`, which mounts only
 * while a session is open.
 */
import { useCallback, useMemo, useState } from "react"
import {
  CodeBrowser,
  type CodeBrowserProps,
  type LineDecoration,
  type LoadedFile,
} from "@/components/ui/code-browser"
import { Switch } from "@/components/ui/switch"
import { languageForPath } from "@/lib/language-for-path"
import { useDebugTarget } from "../hooks"
import { useDebugSessionState, useOptionalDebugSession } from "../session/hooks"
import type { Breakpoint, DebugSession, PauseState, SessionStatus } from "../session/session"
import { consoleDebugOf } from "../target"
import { BreakpointConditionEditor } from "./breakpoint-condition-editor"
import { StartConsoleDebugButton } from "./debug-session-controls"
import { DebugToolbar } from "./debug-toolbar"
import { DebugWaitToggle } from "./debug-wait-toggle"
import { DEBUG_CODE_HEIGHT } from "./debug-workspace"

const ORIGINAL_GROUP = "Original"

/** The resource a pane is about, and — for a Lambda function — the ARN its tags hang off. */
export interface DebugCodeBrowserTarget {
  service: string
  resource: string
  /** With it, the idle strip carries the *Wait for a debugger* switch. */
  resourceArn?: string
}

export interface DebugCodeBrowserProps extends CodeBrowserProps {
  /**
   * The resource the page is about. With it, an idle pane can ask the
   * server whether a console session is on offer and, if so, take
   * breakpoints and start one; without it, idle is a plain browser.
   */
  target?: DebugCodeBrowserTarget
}

export function DebugCodeBrowser({ target, ...props }: DebugCodeBrowserProps) {
  const session = useOptionalDebugSession()
  if (!session) return <CodeBrowser {...props} />
  return <SessionCodeBrowser session={session} target={target} {...props} />
}

function SessionCodeBrowser({
  session,
  target,
  ...props
}: CodeBrowserProps & { session: DebugSession; target?: DebugCodeBrowserTarget }) {
  const status = useDebugSessionState((s) => s.status)
  if (status !== "idle") return <ActiveCodeBrowser session={session} {...props} />
  if (!target) return <CodeBrowser {...props} />
  return <IdleCodeBrowser session={session} target={target} {...props} />
}

// ─── Gutter breakpoints, shared by the idle and the active pane ───────────

/** The gutter's click handling and the inline condition editor it opens. */
function useGutterBreakpoints(session: DebugSession) {
  const [conditionAt, setConditionAt] = useState<{ path: string; line: number } | null>(null)
  const onGutterClick = useCallback(
    (path: string, line: number, kind: "toggle" | "menu") => {
      if (kind === "toggle") session.toggleBreakpoint(path, line)
      else setConditionAt({ path, line })
    },
    [session],
  )
  const existing = conditionAt
    ? session.breakpointAt(conditionAt.path, conditionAt.line)
    : undefined
  const editor = conditionAt ? (
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
  ) : null
  return { onGutterClick, editor }
}

/** The breakpoint glyphs per file: filled once placed on a statement, hollow while waiting, dim when disabled. */
function breakpointDecorations(
  breakpoints: readonly Breakpoint[],
  status: SessionStatus,
): Record<string, LineDecoration[]> {
  const byFile: Record<string, LineDecoration[]> = {}
  for (const bp of breakpoints) {
    ;(byFile[bp.path] ??= []).push({
      line: bp.line,
      kind: !bp.enabled ? "glyph-muted" : bp.resolved ? "glyph" : "glyph-pending",
      title: breakpointTitle(bp, status),
    })
  }
  return byFile
}

function breakpointTitle(bp: Breakpoint, status: SessionStatus): string {
  const what = bp.condition ? `Conditional breakpoint: ${bp.condition}` : "Breakpoint"
  if (!bp.enabled) return `${what} (disabled)`
  if (status === "idle") return `${what} — binds when a session starts`
  if (bp.resolved) return what
  return `${what} — not bound yet: it binds when the container loads ${bp.path}`
}

// ─── Idle: the gutter takes breakpoints, the strip starts a session ───────

function IdleCodeBrowser({
  session,
  target,
  ...props
}: CodeBrowserProps & { session: DebugSession; target: DebugCodeBrowserTarget }) {
  const { data: descriptor } = useDebugTarget(target.service, target.resource)
  const breakpoints = useDebugSessionState((s) => s.breakpoints)
  const { onGutterClick, editor } = useGutterBreakpoints(session)
  const decorations = useMemo(() => breakpointDecorations(breakpoints, "idle"), [breakpoints])

  if (!descriptor || !consoleDebugOf(descriptor).available) return <CodeBrowser {...props} />

  const count = breakpoints.length
  return (
    <div className="flex flex-col gap-2">
      <div
        role="note"
        className="flex flex-wrap items-center gap-3 rounded-md bg-bg-muted px-2 py-1 text-xs text-fg-muted"
      >
        <StartConsoleDebugButton target={descriptor} />
        <span>
          {count === 0
            ? "Click a line's gutter to set a breakpoint, then start a session and invoke from the Test tab: execution pauses here."
            : count === 1
              ? "1 breakpoint set — it binds when a session starts."
              : `${count} breakpoints set — they bind when a session starts.`}
        </span>
        {target.resourceArn && descriptor.service === "lambda" && (
          <DebugWaitToggle
            service={target.service}
            resource={target.resource}
            resourceArn={target.resourceArn}
            className="basis-full"
          />
        )}
      </div>
      {editor}
      <CodeBrowser {...props} decorations={decorations} onGutterClick={onGutterClick} />
    </div>
  )
}

// ─── Active: the session drawn over the pane ──────────────────────────────

function ActiveCodeBrowser({
  session,
  files,
  loadFile,
  ...props
}: CodeBrowserProps & { session: DebugSession }) {
  const status = useDebugSessionState((s) => s.status)
  const breakpoints = useDebugSessionState((s) => s.breakpoints)
  const pause = useDebugSessionState((s) => s.pause)
  const originalFiles = useDebugSessionState((s) => s.originalFiles)
  const hasSourceMaps = useDebugSessionState((s) => s.hasSourceMaps)
  const connections = useDebugSessionState((s) => s.connections)
  const sourceMaps = useDebugSessionState((s) => s.sourceMaps)
  const showCompiled = useDebugSessionState((s) => s.showCompiled)
  const { onGutterClick, editor: conditionEditor } = useGutterBreakpoints(session)

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
    const byFile = breakpointDecorations(breakpoints, status)
    const frame = pause?.frames.at(pause.selectedFrame)
    if (frame && !frame.internal) {
      ;(byFile[frame.location.path] ??= []).push({
        line: frame.location.line,
        kind: "current",
        title: `Paused in ${frame.functionName}`,
      })
    }
    return byFile
  }, [breakpoints, pause, status])

  const revealPosition = useMemo(() => revealFor(pause), [pause])

  return (
    <div className="flex flex-col gap-2">
      <DebugToolbar />
      {conditionEditor}
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
        // A new container may run new code — hot reload is why it was
        // replaced — so what the pane shows is read again on each one.
        contentVersion={connections}
        explorerActions={
          <span className="flex flex-col items-end gap-0.5 font-mono text-2xs tracking-normal normal-case">
            {/* Off, the session reads no map: every file is debugged as
                deployed, and a breakpoint in an original file waits. */}
            <label
              className="flex cursor-pointer items-center gap-1"
              title={
                sourceMaps
                  ? "Source maps are read: original files are listed and breakpoints translated"
                  : "Source maps are off: compiled files only, breakpoints on compiled lines"
              }
            >
              <Switch
                size="sm"
                checked={sourceMaps}
                onCheckedChange={(on) => session.setSourceMaps(on)}
              />
              Source maps
            </label>
            {hasSourceMaps && (
              <label className="flex cursor-pointer items-center gap-1">
                <input
                  type="checkbox"
                  checked={showCompiled}
                  onChange={(e) => session.setShowCompiled(e.target.checked)}
                  className="accent-accent"
                />
                Show compiled
              </label>
            )}
          </span>
        }
      />
    </div>
  )
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
