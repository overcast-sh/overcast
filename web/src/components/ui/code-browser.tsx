/**
 * CodeBrowser — a VS Code-style multi-file editor with explorer tree and tabs.
 *
 * Generic: no service coupling. Provide files, a loader, and an onChange handler.
 *
 * Usage:
 *   <CodeBrowser
 *     files={[{ name: "index.js", size: 245 }, { name: "lib/utils.js", size: 180 }]}
 *     initialFile="index.js"
 *     initialValue="console.log('hi')"
 *     language="javascript"
 *     loadFile={async (path) => ({ content: "...", language: "javascript" })}
 *     onChange={(path, value) => { ... }}
 *     height="60vh"
 *   />
 *
 * The optional editor-aware props — `decorations`, `onGutterClick`,
 * `revealPosition`, file groups and `explorerActions` — are what a debugger
 * (or any other annotator) needs from an editor, expressed without the
 * browser knowing who is asking: it paints markers where it is told, reports
 * gutter clicks, and scrolls to a position on request. Nothing is registered
 * with Monaco for them until a caller passes them, so a plain browser costs
 * nothing extra.
 */
import { useState, useCallback, useEffect, useMemo, useRef, type ReactNode } from "react"
import Editor, { type OnMount } from "@monaco-editor/react"
import type * as Monaco from "monaco-editor"
import { ChevronRight, ChevronDown, FileCode, FolderOpen, Folder, X } from "lucide-react"
import { languageForPath } from "@/lib/language-for-path"
import { sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"

// ─── Public types ──────────────────────────────────────────────────────────

export interface BrowserFile {
  /** Path inside the archive, e.g. "src/index.js" */
  name: string
  /** Uncompressed size in bytes */
  size: number
  /**
   * Explorer section this file is listed under. Ungrouped files form the
   * main tree; each group is a labelled tree below it. The path is still the
   * file's identity, so a grouped file and an ungrouped one must not share one.
   */
  group?: string
}

export interface LoadedFile {
  content: string
  language: string
  /** Lock this one file against edits, whatever the browser-wide `readOnly` says. */
  readOnly?: boolean
  /** A one-line note shown above the editor while this file is open — where the content came from, say. */
  notice?: string
}

/**
 * A marker on one line of one file. `glyph` and `glyph-muted` are dots in
 * the gutter (a breakpoint and a disabled one, to a debugger); `current` is
 * a whole-line highlight with an arrow in the gutter (the paused line).
 */
export interface LineDecoration {
  /** 1-based. */
  line: number
  kind: "glyph" | "glyph-muted" | "current"
  /** Hover text for the gutter marker. */
  title?: string
}

/** `toggle` is a plain click; `menu` is a right-click or a modifier-click — the caller decides what each means. */
export type GutterClickKind = "toggle" | "menu"

export interface RevealPosition {
  path: string
  /** 1-based. */
  line: number
  /** 0-based; defaults to the line start. */
  column?: number
  /** Change to reveal the same position again — a second pause on one line. */
  key?: number
}

export interface CodeBrowserProps {
  /** Flat list of every file in the project. */
  files: BrowserFile[]
  /** Which file to show initially. */
  initialFile?: string
  /** Initial source content for the initial file (avoids an extra load). */
  initialValue?: string
  /** Language hint for the initial file. */
  language?: string
  /** Async loader: given a file path, return its content + language. */
  loadFile?: (path: string) => Promise<LoadedFile>
  /** Called whenever the user edits. */
  onChange?: (path: string, value: string) => void
  /** Called when the active (visible) file changes. */
  onActiveFileChange?: (path: string) => void
  /** Editor height. Default "60vh". */
  height?: string
  /** Read-only mode. */
  readOnly?: boolean
  /** Extra CSS class on the root container. */
  className?: string
  /**
   * Line markers, keyed by file path. Applied to whichever file is open and
   * re-applied when the file or the map changes; the gutter margin is shown
   * only while this or `onGutterClick` is set.
   */
  decorations?: Readonly<Record<string, readonly LineDecoration[]>>
  /**
   * A click in the gutter (glyph margin or line numbers) of the open file.
   * Left click reports `toggle`; right click, or a click with Ctrl, Cmd or
   * Alt held, reports `menu`. F9 while the editor has focus reports `toggle`
   * on the cursor line, so the gutter is reachable without a mouse.
   */
  onGutterClick?: (path: string, line: number, kind: GutterClickKind) => void
  /**
   * Open a file and scroll to a line, moving the cursor and focus there.
   * Acted on when the object changes (by identity or `key`); `null` reveals nothing.
   */
  revealPosition?: RevealPosition | null
  /** Controls rendered at the right of the explorer header — a toggle, a filter. */
  explorerActions?: ReactNode
}

// ─── Helpers ───────────────────────────────────────────────────────────────

function basename(path: string): string {
  const i = path.lastIndexOf("/")
  return i < 0 ? path : path.slice(i + 1)
}

function fileIconColor(name: string): string {
  if (/\.[jt]sx?$/.test(name)) return "text-cat-3"
  if (/\.py$/.test(name)) return "text-cat-7"
  // JSON keeps a slot of its own rather than sharing amber with .js/.ts:
  // a bundle lists both side by side, which is exactly where two file
  // types reading as one colour costs the reader something.
  if (/\.json$/.test(name)) return "text-cat-5"
  if (/\.ya?ml$/.test(name)) return "text-cat-10"
  if (/\.md$/.test(name)) return "text-cat-6"
  if (/\.css$/.test(name)) return "text-cat-9"
  if (/\.html$/.test(name)) return "text-cat-2"
  if (/\.java$/.test(name)) return "text-cat-1"
  if (/\.cs$/.test(name)) return "text-cat-4"
  return "text-fg-muted"
}

/** Monaco decoration options per kind. The classes are styled in styles/global.css. */
function decorationOptions(
  monaco: typeof Monaco,
  decoration: LineDecoration,
): Monaco.editor.IModelDecorationOptions {
  const hover = decoration.title ? { value: decoration.title } : undefined
  const stickiness = monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges
  switch (decoration.kind) {
    case "glyph":
      return { glyphMarginClassName: "oc-gutter-glyph", glyphMarginHoverMessage: hover, stickiness }
    case "glyph-muted":
      return {
        glyphMarginClassName: "oc-gutter-glyph oc-gutter-glyph-muted",
        glyphMarginHoverMessage: hover,
        stickiness,
      }
    case "current":
      return {
        isWholeLine: true,
        className: "oc-line-current",
        glyphMarginClassName: "oc-gutter-current",
        glyphMarginHoverMessage: hover,
        stickiness,
      }
  }
}

function isGutterTarget(monaco: typeof Monaco, target: Monaco.editor.IMouseTarget): boolean {
  return (
    target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN ||
    target.type === monaco.editor.MouseTargetType.GUTTER_LINE_NUMBERS
  )
}

// ─── Tree data structure ───────────────────────────────────────────────────

interface TreeNode {
  name: string // segment name ("src", "index.js")
  path: string // full path ("src/index.js")
  isDir: boolean
  children: TreeNode[]
  size: number
}

function buildTree(files: BrowserFile[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", isDir: true, children: [], size: 0 }

  for (const f of files) {
    const parts = f.name.split("/")
    let node = root
    for (let i = 0; i < parts.length; i++) {
      const segment = parts[i]
      const isLast = i === parts.length - 1
      const childPath = parts.slice(0, i + 1).join("/")
      let child = node.children.find((c) => c.name === segment)
      if (!child) {
        child = {
          name: segment,
          path: childPath,
          isDir: !isLast,
          children: [],
          size: isLast ? f.size : 0,
        }
        node.children.push(child)
      }
      node = child
    }
  }

  // Sort: directories first, then alphabetical
  function sortTree(nodes: TreeNode[]) {
    nodes.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
      return a.name.localeCompare(b.name)
    })
    for (const n of nodes) {
      if (n.isDir) sortTree(n.children)
    }
  }
  sortTree(root.children)

  // Collapse single-child directories: "src" -> "lib" -> "file.js" becomes "src/lib" -> "file.js"
  function collapse(nodes: TreeNode[]): TreeNode[] {
    return nodes.map((n) => {
      if (n.isDir) {
        n.children = collapse(n.children)
        if (n.children.length === 1 && n.children[0].isDir) {
          const merged = n.children[0]
          return {
            ...merged,
            name: n.name + "/" + merged.name,
            children: merged.children,
          }
        }
      }
      return n
    })
  }

  return collapse(root.children)
}

// ─── Component ─────────────────────────────────────────────────────────────

export function CodeBrowser({
  files,
  initialFile,
  initialValue,
  language,
  loadFile,
  onChange,
  onActiveFileChange,
  height = "60vh",
  readOnly = false,
  className = "",
  decorations,
  onGutterClick,
  revealPosition,
  explorerActions,
}: CodeBrowserProps) {
  // ── File cache: path → { content, language } ──────────────────────────
  const [fileCache, setFileCache] = useState<Record<string, LoadedFile | undefined>>(() => {
    if (initialFile && initialValue != null) {
      return {
        [initialFile]: {
          content: initialValue,
          language: language ?? languageForPath(initialFile),
        },
      }
    }
    return {}
  })
  const [loadingFile, setLoadingFile] = useState<string | null>(null)

  // ── Open tabs & active tab ────────────────────────────────────────────
  const [openTabs, setOpenTabs] = useState<string[]>(() =>
    initialFile ? [initialFile] : files.length > 0 ? [files[0].name] : [],
  )
  const [activeFile, setActiveFile] = useState<string>(
    initialFile ?? (files.length > 0 ? files[0].name : ""),
  )

  // ── Explorer collapsed dirs ───────────────────────────────────────────
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  // ── Monaco ref ────────────────────────────────────────────────────────
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null)
  const monacoRef = useRef<typeof Monaco | null>(null)

  // ── Editor-aware props, read from refs by Monaco listeners ─────────────
  //
  // The listeners are registered once at mount and must see the latest
  // props without being re-registered per render, so the props are mirrored
  // into refs from effects. `activeFileRef` names the file the listeners
  // are reporting about.
  const decorationsRef = useRef(decorations)
  const gutterClickRef = useRef(onGutterClick)
  const activeFileRef = useRef("")
  const decorationCollectionRef = useRef<Monaco.editor.IEditorDecorationsCollection | null>(null)
  const pendingRevealRef = useRef<RevealPosition | null>(null)
  const lastRevealRef = useRef<RevealPosition | null>(null)
  const wantsGutter = Boolean(decorations || onGutterClick)

  // ── Detect dark mode ──────────────────────────────────────────────────
  const isDark =
    typeof document !== "undefined" &&
    (document.documentElement.getAttribute("data-theme") === "dark" ||
      (document.documentElement.getAttribute("data-theme") == null &&
        window.matchMedia("(prefers-color-scheme: dark)").matches))

  // ── Tree ──────────────────────────────────────────────────────────────
  const tree = useMemo(() => buildTree(files.filter((f) => !f.group)), [files])
  const groups = useMemo(() => {
    const byGroup = new Map<string, BrowserFile[]>()
    for (const f of files) {
      if (!f.group) continue
      const list = byGroup.get(f.group) ?? []
      list.push(f)
      byGroup.set(f.group, list)
    }
    return [...byGroup.entries()].map(([name, list]) => ({ name, tree: buildTree(list) }))
  }, [files])
  const hasExplorer = files.length > 1 || explorerActions != null
  const openFile = useCallback(
    async (path: string) => {
      // Add to tabs if not already open
      setOpenTabs((tabs) => (tabs.includes(path) ? tabs : [...tabs, path]))
      setActiveFile(path)
      onActiveFileChange?.(path)

      // Load content if not cached
      if (!fileCache[path] && loadFile) {
        setLoadingFile(path)
        try {
          const loaded = await loadFile(path)
          setFileCache((prev) => ({ ...prev, [path]: loaded }))
        } catch {
          // show fallback
          setFileCache((prev) => ({
            ...prev,
            [path]: { content: `// Failed to load ${path}`, language: languageForPath(path) },
          }))
        } finally {
          setLoadingFile(null)
        }
      }
    },
    [fileCache, loadFile, onActiveFileChange],
  )

  // ── Close a tab ───────────────────────────────────────────────────────
  const closeTab = useCallback(
    (path: string, e?: React.MouseEvent) => {
      e?.stopPropagation()
      setOpenTabs((tabs) => {
        const next = tabs.filter((t) => t !== path)
        if (next.length === 0 && files.length > 0) {
          // Always keep at least one tab open
          const fallback = initialFile ?? files[0].name
          setActiveFile(fallback)
          onActiveFileChange?.(fallback)
          return [fallback]
        }
        if (activeFile === path) {
          const idx = tabs.indexOf(path)
          const newActive = next[Math.min(idx, next.length - 1)]
          setActiveFile(newActive)
          onActiveFileChange?.(newActive)
        }
        return next
      })
    },
    [activeFile, files, initialFile, onActiveFileChange],
  )

  // ── Current file data ─────────────────────────────────────────────────
  const currentData = fileCache[activeFile]
  const currentLanguage = currentData?.language ?? language ?? languageForPath(activeFile)
  const currentValue = currentData?.content ?? initialValue ?? ""
  const isLoading = loadingFile === activeFile
  const currentReadOnly = readOnly || currentData?.readOnly === true

  // ── Decorations & reveal (imperative, against the mounted editor) ─────
  const applyDecorations = useCallback(() => {
    const editor = editorRef.current
    const monaco = monacoRef.current
    if (!editor || !monaco) return
    const list = decorationsRef.current?.[activeFileRef.current] ?? []
    const collection = (decorationCollectionRef.current ??= editor.createDecorationsCollection())
    collection.set(
      list.map((d) => ({
        range: new monaco.Range(d.line, 1, d.line, 1),
        options: decorationOptions(monaco, d),
      })),
    )
  }, [])

  /** Scroll to the pending position once the editor shows its file. */
  const consumePendingReveal = useCallback(() => {
    const editor = editorRef.current
    const pending = pendingRevealRef.current
    if (!editor || !pending || pending.path !== activeFileRef.current) return
    pendingRevealRef.current = null
    editor.revealLineInCenter(pending.line)
    editor.setPosition({ lineNumber: pending.line, column: (pending.column ?? 0) + 1 })
    editor.focus()
  }, [])

  // ── Monaco mount ──────────────────────────────────────────────────────
  const handleMount: OnMount = useCallback(
    (editor, monaco) => {
      editorRef.current = editor
      monacoRef.current = monaco
      decorationCollectionRef.current = null

      // Report gutter clicks; swallow the context menu there so a right
      // click is a report, not Monaco's own menu.
      editor.onMouseDown((e) => {
        const line = e.target.position?.lineNumber
        if (!isGutterTarget(monaco, e.target) || !line) return
        const menu = e.event.rightButton || e.event.ctrlKey || e.event.metaKey || e.event.altKey
        gutterClickRef.current?.(activeFileRef.current, line, menu ? "menu" : "toggle")
      })
      editor.onContextMenu((e) => {
        if (!isGutterTarget(monaco, e.target)) return
        e.event.preventDefault()
        e.event.stopPropagation()
      })
      editor.addAction({
        id: "code-browser.gutter-toggle",
        label: "Toggle gutter marker on the current line",
        keybindings: [monaco.KeyCode.F9],
        run: (ed) => {
          const position = ed.getPosition()
          if (position) {
            gutterClickRef.current?.(activeFileRef.current, position.lineNumber, "toggle")
          }
        },
      })
      // Switching files swaps the model, which drops the decorations with it.
      editor.onDidChangeModel(() => {
        decorationCollectionRef.current = null
        applyDecorations()
        consumePendingReveal()
      })
      applyDecorations()
      consumePendingReveal()
    },
    [applyDecorations, consumePendingReveal],
  )

  useEffect(() => {
    gutterClickRef.current = onGutterClick
  }, [onGutterClick])

  // The editor is unmounted while a file loads and a new one mounts after,
  // so the handle must not outlive it: a reveal aimed at the old editor
  // would scroll a disposed instance and be lost. Declared before the
  // effect below so a load and a file change in one commit see no editor.
  useEffect(() => {
    if (isLoading) {
      editorRef.current = null
      decorationCollectionRef.current = null
    }
  }, [isLoading])

  useEffect(() => {
    activeFileRef.current = activeFile
    decorationsRef.current = decorations
    applyDecorations()
    consumePendingReveal()
  }, [activeFile, decorations, applyDecorations, consumePendingReveal])

  useEffect(() => {
    if (!revealPosition || revealPosition === lastRevealRef.current) return
    lastRevealRef.current = revealPosition
    pendingRevealRef.current = revealPosition
    if (revealPosition.path !== activeFileRef.current) void openFile(revealPosition.path)
    else consumePendingReveal()
  }, [revealPosition, openFile, consumePendingReveal])

  // ── Breadcrumb segments ───────────────────────────────────────────────
  const breadcrumb = activeFile.split("/")

  // ── Toggle dir ────────────────────────────────────────────────────────
  const toggleDir = useCallback((path: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }, [])

  // ── Sync initial file into cache ───────────────────────────────────────
  //
  // The lazy initialiser above covers the usual case; this catches the one it
  // cannot — `initialFile` arriving (or changing) after mount, which is what
  // happens when the source query resolves. Adjusting during render rather than
  // from an effect keeps it to a single paint: React discards this render and
  // re-runs with the seeded cache instead of painting the empty one first. The
  // "already cached" guard is what makes it converge.
  if (initialFile && initialValue != null && !fileCache[initialFile]) {
    setFileCache((prev) => ({
      ...prev,
      [initialFile]: {
        content: initialValue,
        language: language ?? languageForPath(initialFile),
      },
    }))
  }

  // ─── Render tree node ─────────────────────────────────────────────────
  function renderNode(node: TreeNode, depth: number = 0) {
    const indent = depth * 12

    if (node.isDir) {
      const isOpen = !collapsed.has(node.path)
      return (
        <div key={node.path}>
          <button
            onClick={() => toggleDir(node.path)}
            className="flex w-full items-center gap-1 py-0.5 text-left font-mono text-xs transition-colors hover:bg-accent-muted"
            style={{ paddingLeft: indent + 4 }}
          >
            {isOpen ? (
              <ChevronDown className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
            )}
            {isOpen ? (
              <FolderOpen className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
            ) : (
              <Folder className="h-3.5 w-3.5 shrink-0 text-fg-muted" />
            )}
            <span className="truncate text-fg-muted">{node.name}</span>
          </button>
          {isOpen && node.children.map((c) => renderNode(c, depth + 1))}
        </div>
      )
    }

    const isActive = node.path === activeFile
    return (
      <button
        key={node.path}
        onClick={() => openFile(node.path)}
        className={cn(
          "flex w-full items-center gap-1.5 py-0.5 text-left font-mono text-xs transition-colors",
          isActive
            ? "bg-accent-muted text-fg"
            : "text-fg-muted hover:bg-accent-muted hover:text-fg",
        )}
        style={{ paddingLeft: indent + 22 }}
        title={node.path}
      >
        <FileCode className={cn("h-3.5 w-3.5 shrink-0", fileIconColor(node.name))} />
        <span className="truncate">{node.name}</span>
      </button>
    )
  }

  return (
    <div
      className={cn("flex overflow-hidden rounded-md border border-border", className)}
      style={{ height }}
    >
      {/* ── Explorer sidebar (multi-file only) ──────────────────────── */}
      {hasExplorer && (
        <div
          className="flex shrink-0 flex-col overflow-hidden border-r border-white/10"
          style={{
            width: 220,
            backgroundColor: isDark ? "#181818" : "var(--color-bg-subtle, #f8f8f8)",
          }}
        >
          {/* Sidebar header */}
          <div
            className={cn(sectionLabel, "flex items-center justify-between gap-2 px-3 py-1.5")}
            style={{ color: isDark ? "#888" : "var(--color-fg-muted)" }}
          >
            <span>Explorer</span>
            {explorerActions}
          </div>
          {/* File tree, then one labelled tree per group */}
          <div className="flex-1 overflow-x-hidden overflow-y-auto py-0.5">
            {tree.map((n) => renderNode(n, 0))}
            {groups.map((group) => (
              <div key={group.name} role="group" aria-label={group.name}>
                <div
                  className={cn(sectionLabel, "mt-2 px-3 py-1")}
                  style={{ color: isDark ? "#888" : "var(--color-fg-muted)" }}
                >
                  {group.name}
                </div>
                {group.tree.map((n) => renderNode(n, 0))}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* ── Editor area ─────────────────────────────────────────────── */}
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Tab bar */}
        <div
          className="flex overflow-x-auto border-b border-white/10"
          style={{
            backgroundColor: isDark ? "#1e1e1e" : "var(--color-bg-elevated, #fff)",
          }}
        >
          {openTabs.map((tab) => {
            const isActive = tab === activeFile
            return (
              <button
                key={tab}
                onClick={() => openFile(tab)}
                className={cn(
                  "group relative flex shrink-0 items-center gap-1.5 border-r border-white/5 px-3 py-1.5 font-mono text-xs transition-colors",
                  isActive ? "text-fg" : "text-fg-muted hover:text-fg",
                )}
                style={{
                  backgroundColor: isActive
                    ? isDark
                      ? "#1e1e1e"
                      : "#fff"
                    : isDark
                      ? "#2d2d2d"
                      : "#f0f0f0",
                }}
              >
                {/* Active tab top highlight */}
                {isActive && <span className="absolute inset-x-0 top-0 h-0.5 bg-accent" />}
                <FileCode className={cn("h-3.5 w-3.5 shrink-0", fileIconColor(tab))} />
                <span className="max-w-32 truncate">{basename(tab)}</span>
                <span
                  onClick={(e) => closeTab(tab, e)}
                  className={cn(
                    "ml-1 flex h-4 w-4 items-center justify-center rounded-sm transition-colors hover:bg-accent-muted hover:text-accent",
                    !isActive && "opacity-0 group-hover:opacity-100",
                  )}
                  role="button"
                  tabIndex={-1}
                  aria-label={`Close ${basename(tab)}`}
                >
                  <X className="h-3 w-3" />
                </span>
              </button>
            )
          })}
        </div>

        {/* Breadcrumb bar */}
        <div
          className="flex items-center gap-1 px-3 py-1 font-mono text-xs"
          style={{
            backgroundColor: isDark ? "#1e1e1e" : "#fff",
            color: isDark ? "#888" : "var(--color-fg-muted)",
          }}
        >
          {breadcrumb.map((seg, i) => (
            <span key={i} className="flex items-center gap-1">
              {i > 0 && <ChevronRight className="h-3 w-3" />}
              <span className={cn(i === breadcrumb.length - 1 && "text-fg")}>{seg}</span>
            </span>
          ))}
        </div>

        {/* A note the loader attached to this file */}
        {currentData?.notice && (
          <div
            role="note"
            className="border-t border-white/10 px-3 py-1 font-mono text-xs text-fg-muted"
            style={{ backgroundColor: isDark ? "#1e1e1e" : "#fff" }}
          >
            {currentData.notice}
          </div>
        )}

        {/* Editor */}
        <div className="min-h-0 flex-1">
          {isLoading ? (
            <div
              className="flex h-full items-center justify-center"
              style={{ backgroundColor: isDark ? "#1e1e1e" : "#fff" }}
            >
              <div className="h-5 w-5 animate-spin rounded-full border-2 border-accent border-t-transparent" />
            </div>
          ) : (
            <Editor
              height="100%"
              path={activeFile}
              defaultLanguage={currentLanguage}
              defaultValue={currentValue}
              theme={isDark ? "vs-dark" : "light"}
              onChange={(val) => onChange?.(activeFile, val ?? "")}
              onMount={handleMount}
              saveViewState
              options={{
                fontSize: 13,
                readOnly: currentReadOnly,
                glyphMargin: wantsGutter,
                minimap: { enabled: false },
                scrollBeyondLastLine: false,
                wordWrap: "on",
                lineNumbers: "on",
                renderLineHighlight: "line",
                padding: { top: 8, bottom: 8 },
                automaticLayout: true,
                cursorBlinking: "smooth",
                smoothScrolling: true,
                renderWhitespace: "selection",
              }}
            />
          )}
        </div>
      </div>
    </div>
  )
}
