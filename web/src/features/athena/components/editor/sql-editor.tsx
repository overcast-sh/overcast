import { useEffect, useImperativeHandle, useRef, type Ref } from "react"
import Editor, { type OnMount } from "@monaco-editor/react"
import type * as Monaco from "monaco-editor"
import { MONACO_BASE_OPTIONS, monacoTheme } from "@/components/ui/monaco-setup"
import { useIsDarkTheme } from "@/hooks/use-theme"
import {
  completionItems,
  qualifierBefore,
  type CompletionContext,
  type CompletionKind,
} from "../../sql-completion"
import type { SqlPosition } from "../../sql-text"

/**
 * The query editor: Monaco's `sql` language with the console's shared
 * options and theme, completion from the loaded catalog and the Trino
 * functions, and the workspace's keys —
 *
 * - `⌘/Ctrl+⏎` runs the query, `⇧⌘⏎` runs the selection;
 * - `esc` stops the running query, and only while one runs and nothing of
 *   Monaco's own wants `esc` (completion, find, parameter hints, several
 *   cursors, rename).
 *
 * One Monaco model per query tab (`path`), kept when the tab is switched
 * away from (`keepCurrentModel`), so each tab keeps its own undo history,
 * cursor and scroll. `disposeSqlModel` (sql-model.ts) drops a closed tab's.
 */

export interface SqlSelection {
  sql: string
  /** Where the selection starts in the whole query, in characters. */
  offset: number
}

export interface SqlEditorHandle {
  /** Inserts text at the cursor, replacing any selection, and focuses the editor. */
  insert: (text: string) => void
  /** Replaces the whole query (Format), as one undoable edit. */
  replaceAll: (text: string) => void
  /** Runs the selected SQL, as `⇧⌘⏎` does; the whole query when nothing is selected. */
  runSelection: () => void
}

/** Where the last failed run points, and which run it was. */
export interface SqlError extends SqlPosition {
  message: string
  /** The failed execution: the editor scrolls to the error once per failure. */
  executionId: string
}

export interface SqlEditorProps {
  /** The query tab's id: one model, undo history and view state per tab. */
  path: string
  defaultValue: string
  onChange: (sql: string) => void
  /** Runs the query, or `selection` when the selection is being run. */
  onRun: (selection?: SqlSelection) => void
  onStop: () => void
  running: boolean
  completion: CompletionContext
  error?: SqlError
  ref?: Ref<SqlEditorHandle>
}

const MARKER_OWNER = "athena"
const RUNNING_KEY = "athenaQueryRunning"
/** Every Monaco state in which `esc` already means something. */
const ESC_IS_MONACOS = [
  "suggestWidgetVisible",
  "findWidgetVisible",
  "parameterHintsVisible",
  "editorHasMultipleSelections",
  "inlineSuggestionVisible",
  "renameInputVisible",
]
  .map((key) => `!${key}`)
  .join(" && ")

const COMPLETION_KIND: Record<CompletionKind, keyof typeof Monaco.languages.CompletionItemKind> = {
  keyword: "Keyword",
  function: "Function",
  catalog: "Module",
  database: "Module",
  table: "Struct",
  column: "Field",
}

/** Tables and columns first, then names, keywords and functions last. */
const SORT_PREFIX: Record<CompletionKind, string> = {
  column: "0",
  table: "1",
  database: "2",
  catalog: "3",
  keyword: "4",
  function: "5",
}

/** The selected SQL and where it starts, or undefined when nothing but blanks is selected. */
function selectedSql(editor: Monaco.editor.ICodeEditor): SqlSelection | undefined {
  const selection = editor.getSelection()
  const model = editor.getModel()
  if (!selection || !model) return undefined
  const sql = model.getValueInRange(selection)
  if (!sql.trim()) return undefined
  return { sql, offset: model.getOffsetAt(selection.getStartPosition()) }
}

/** Underlines the error (or clears the underline), and scrolls to it when `reveal`. */
function applyErrorMarker(
  editor: Monaco.editor.ICodeEditor,
  monaco: typeof Monaco,
  error: SqlError | undefined,
  reveal: boolean,
): void {
  const model = editor.getModel()
  if (!model) return
  if (!error) {
    monaco.editor.setModelMarkers(model, MARKER_OWNER, [])
    return
  }
  const line = Math.min(error.line, model.getLineCount())
  const column = Math.min(error.column, model.getLineMaxColumn(line))
  const end = model.getWordAtPosition({ lineNumber: line, column })?.endColumn ?? column + 1
  monaco.editor.setModelMarkers(model, MARKER_OWNER, [
    {
      severity: monaco.MarkerSeverity.Error,
      message: error.message,
      startLineNumber: line,
      startColumn: column,
      endLineNumber: line,
      endColumn: end,
    },
  ])
  if (reveal) editor.revealPositionInCenterIfOutsideViewport({ lineNumber: line, column })
}

export function SqlEditor({
  path,
  defaultValue,
  onChange,
  onRun,
  onStop,
  running,
  completion,
  error,
  ref,
}: SqlEditorProps) {
  const isDark = useIsDarkTheme()
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null)
  const monacoRef = useRef<typeof Monaco | null>(null)
  const runningKey = useRef<Monaco.editor.IContextKey<boolean> | null>(null)
  const revealed = useRef<string | null>(null)
  // Monaco's listeners are registered once at mount, and `@monaco-editor/react`
  // keeps the first `onMount` it was given: both read the latest props here.
  const latest = useRef({ onRun, onStop, completion, running, error })
  useEffect(() => {
    latest.current = { onRun, onStop, completion, running, error }
  })

  useImperativeHandle(ref, () => ({
    insert: (text) => {
      const editor = editorRef.current
      const selection = editor?.getSelection()
      if (!editor || !selection) return
      editor.executeEdits("athena-insert", [{ range: selection, text, forceMoveMarkers: true }])
      editor.focus()
    },
    replaceAll: (text) => {
      const editor = editorRef.current
      const model = editor?.getModel()
      if (!editor || !model) return
      editor.executeEdits("athena-format", [{ range: model.getFullModelRange(), text }])
      editor.focus()
    },
    runSelection: () => {
      const editor = editorRef.current
      if (editor) latest.current.onRun(selectedSql(editor))
    },
  }))

  // ─── Keep Monaco in step with the props it cannot take directly ─────────
  useEffect(() => {
    runningKey.current?.set(running)
  }, [running])

  // Keyed on the error's parts, not its object: a new object each render
  // would re-mark and re-scroll on every keystroke.
  const errorKey = error ? `${error.executionId}:${error.line}:${error.column}` : ""
  useEffect(() => {
    const editor = editorRef.current
    const monaco = monacoRef.current
    if (!editor || !monaco) return
    const current = latest.current.error
    const reveal = current !== undefined && revealed.current !== current.executionId
    applyErrorMarker(editor, monaco, current, reveal)
    if (reveal) revealed.current = current.executionId
  }, [errorKey, path])

  const handleMount: OnMount = (editor, monaco) => {
    editorRef.current = editor
    monacoRef.current = monaco
    runningKey.current = editor.createContextKey(RUNNING_KEY, latest.current.running)
    // Mounted after the error arrived: mark it now, as the effect found no editor.
    const current = latest.current.error
    applyErrorMarker(editor, monaco, current, current !== undefined)
    if (current) revealed.current = current.executionId
    editor.addAction({
      id: "athena.run",
      label: "Run query",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter],
      run: () => latest.current.onRun(),
    })
    editor.addAction({
      id: "athena.run-selection",
      label: "Run selection",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.Enter],
      run: (ed) => latest.current.onRun(selectedSql(ed)),
    })
    editor.addAction({
      id: "athena.stop",
      label: "Stop query",
      keybindings: [monaco.KeyCode.Escape],
      precondition: `${RUNNING_KEY} && ${ESC_IS_MONACOS}`,
      run: () => latest.current.onStop(),
    })
    const provider = monaco.languages.registerCompletionItemProvider("sql", {
      triggerCharacters: ["."],
      provideCompletionItems: (model: Monaco.editor.ITextModel, position: Monaco.Position) => {
        // Only this editor's models: the provider is global to the language.
        if (model !== editor.getModel()) return { suggestions: [] }
        const word = model.getWordUntilPosition(position)
        const range = {
          startLineNumber: position.lineNumber,
          endLineNumber: position.lineNumber,
          startColumn: word.startColumn,
          endColumn: word.endColumn,
        }
        const before = model.getValueInRange({
          startLineNumber: position.lineNumber,
          startColumn: 1,
          endLineNumber: position.lineNumber,
          endColumn: position.column,
        })
        const items = completionItems(latest.current.completion, qualifierBefore(before))
        return {
          suggestions: items.map((item) => ({
            label: item.label,
            kind: monaco.languages.CompletionItemKind[COMPLETION_KIND[item.kind]],
            insertText: item.insertText,
            detail: item.detail,
            documentation: item.documentation,
            sortText: `${SORT_PREFIX[item.kind]}${item.label.toLowerCase()}`,
            range,
          })),
        }
      },
    })
    editor.onDidDispose(() => provider.dispose())
  }

  return (
    <Editor
      height="100%"
      path={path}
      defaultLanguage="sql"
      defaultValue={defaultValue}
      theme={monacoTheme(isDark)}
      onChange={(value) => onChange(value ?? "")}
      onMount={handleMount}
      keepCurrentModel
      saveViewState
      options={{ ...MONACO_BASE_OPTIONS, fixedOverflowWidgets: true, wordWrap: "off" }}
    />
  )
}
