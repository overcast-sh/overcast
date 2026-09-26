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
 * - `esc` stops the running query, and only while one runs and no widget
 *   (completion, find) is open, so it never steals Monaco's own `esc`.
 *
 * One Monaco model per query tab (`path`), so each tab keeps its own undo
 * history and cursor.
 */

export interface SqlEditorHandle {
  /** Inserts text at the cursor, replacing any selection, and focuses the editor. */
  insert: (text: string) => void
  /** Replaces the whole query (Format), as one undoable edit. */
  replaceAll: (text: string) => void
  /** Runs the selected SQL, as `⇧⌘⏎` does; the whole query when nothing is selected. */
  runSelection: () => void
}

export interface SqlEditorProps {
  /** The query tab's id: one model, undo history and view state per tab. */
  path: string
  defaultValue: string
  onChange: (sql: string) => void
  /** Runs the query, or `selection` when the selection is being run. */
  onRun: (selection?: string) => void
  onStop: () => void
  running: boolean
  completion: CompletionContext
  /** Where the last run's error points, underlined. */
  error?: SqlPosition & { message: string }
  ref?: Ref<SqlEditorHandle>
}

const MARKER_OWNER = "athena"
const RUNNING_KEY = "athenaQueryRunning"

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

/** The selected SQL, or undefined when nothing but blanks is selected. */
function selectedText(editor: Monaco.editor.ICodeEditor): string | undefined {
  const selection = editor.getSelection()
  const text = selection ? editor.getModel()?.getValueInRange(selection) : undefined
  return text?.trim() ? text : undefined
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
  // Monaco's listeners are registered once at mount; they read the latest props here.
  const latest = useRef({ onRun, onStop, completion })
  useEffect(() => {
    latest.current = { onRun, onStop, completion }
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
      if (editor) latest.current.onRun(selectedText(editor))
    },
  }))

  // ─── Keep Monaco in step with the props it cannot take directly ─────────
  useEffect(() => {
    runningKey.current?.set(running)
  }, [running])

  useEffect(() => {
    const editor = editorRef.current
    const monaco = monacoRef.current
    const model = editor?.getModel()
    if (!editor || !monaco || !model) return
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
    editor.revealPositionInCenterIfOutsideViewport({ lineNumber: line, column })
  }, [error, path])

  const handleMount: OnMount = (editor, monaco) => {
    editorRef.current = editor
    monacoRef.current = monaco
    runningKey.current = editor.createContextKey(RUNNING_KEY, running)
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
      run: (ed) => latest.current.onRun(selectedText(ed)),
    })
    editor.addAction({
      id: "athena.stop",
      label: "Stop query",
      keybindings: [monaco.KeyCode.Escape],
      precondition: `${RUNNING_KEY} && !suggestWidgetVisible && !findWidgetVisible`,
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
      saveViewState
      options={{ ...MONACO_BASE_OPTIONS, fixedOverflowWidgets: true, wordWrap: "off" }}
    />
  )
}
