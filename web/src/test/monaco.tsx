/**
 * A stand-in for `@monaco-editor/react` for component tests. The real
 * package loads Monaco from a CDN at mount, which never completes under
 * jsdom, so `CodeBrowser` and anything built on it would otherwise render a
 * permanent "Loading…". Install it at the top of a test file:
 *
 *   vi.mock("@monaco-editor/react", async () => {
 *     const { FakeMonacoEditor } = await import("@/test/monaco")
 *     return { default: FakeMonacoEditor }
 *   })
 *
 * The fake renders a textarea (so keys and edits have somewhere to land),
 * calls `onMount` once with a `FakeEditor` and a minimal `monaco` namespace,
 * and fires the model-change listeners when `path` changes, as the real
 * editor does when it swaps models. `latestFakeEditor()` returns the editor
 * the component under test mounted, with helpers to click its gutter and
 * read the decorations it was handed.
 */
import { useEffect, useRef } from "react"
import { vi } from "vitest"
import type * as Monaco from "monaco-editor"

type Disposable = { dispose(): void }
type MouseHandler = (e: Monaco.editor.IEditorMouseEvent) => void

/** What the fake `monaco` namespace carries: the enums and constructors CodeBrowser reads. */
export const fakeMonacoNamespace = {
  editor: {
    MouseTargetType: { GUTTER_GLYPH_MARGIN: 2, GUTTER_LINE_NUMBERS: 3, CONTENT_TEXT: 6 },
    TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 },
  },
  Range: class {
    startLineNumber: number
    startColumn: number
    endLineNumber: number
    endColumn: number
    constructor(
      startLineNumber: number,
      startColumn: number,
      endLineNumber: number,
      endColumn: number,
    ) {
      this.startLineNumber = startLineNumber
      this.startColumn = startColumn
      this.endLineNumber = endLineNumber
      this.endColumn = endColumn
    }
  },
  KeyCode: { F9: 67 },
} as unknown as typeof Monaco

export class FakeEditor {
  path: string
  /** The decorations most recently handed to the collection. */
  decorations: Monaco.editor.IModelDeltaDecoration[] = []
  actions: Monaco.editor.IActionDescriptor[] = []
  position = { lineNumber: 1, column: 1 }
  readonly revealLineInCenter = vi.fn()
  readonly setPosition = vi.fn((p: { lineNumber: number; column: number }) => {
    this.position = p
  })
  readonly focus = vi.fn()
  readonly updateOptions = vi.fn()

  private readonly mouseDown: MouseHandler[] = []
  private readonly contextMenu: MouseHandler[] = []
  private readonly modelChange: Array<() => void> = []

  constructor(path: string) {
    this.path = path
  }

  createDecorationsCollection(): Monaco.editor.IEditorDecorationsCollection {
    const set = (decorations: Monaco.editor.IModelDeltaDecoration[]) => {
      this.decorations = decorations
      return []
    }
    return {
      set,
      clear: () => {
        this.decorations = []
      },
      length: 0,
      onDidChange: () => ({ dispose() {} }),
      getRange: () => null,
      getRanges: () => [],
      has: () => false,
      append: () => [],
    }
  }

  onMouseDown(handler: MouseHandler): Disposable {
    this.mouseDown.push(handler)
    return { dispose() {} }
  }

  onContextMenu(handler: MouseHandler): Disposable {
    this.contextMenu.push(handler)
    return { dispose() {} }
  }

  onDidChangeModel(handler: () => void): Disposable {
    this.modelChange.push(handler)
    return { dispose() {} }
  }

  addAction(action: Monaco.editor.IActionDescriptor): Disposable {
    this.actions.push(action)
    return { dispose() {} }
  }

  getPosition() {
    return this.position
  }

  getModel() {
    return { uri: { path: `/${this.path}` } }
  }

  // ─── Scripting ──────────────────────────────────────────────────────────

  /** A mouse-down in the gutter on `line`; `right` or a modifier makes it a menu click. */
  clickGutter(
    line: number,
    {
      right = false,
      ctrl = false,
      inText = false,
    }: { right?: boolean; ctrl?: boolean; inText?: boolean } = {},
  ): void {
    const type = inText
      ? fakeMonacoNamespace.editor.MouseTargetType.CONTENT_TEXT
      : fakeMonacoNamespace.editor.MouseTargetType.GUTTER_GLYPH_MARGIN
    const event = {
      target: { type, position: { lineNumber: line, column: 1 } },
      event: {
        rightButton: right,
        leftButton: !right,
        ctrlKey: ctrl,
        metaKey: false,
        altKey: false,
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
      },
    } as unknown as Monaco.editor.IEditorMouseEvent
    for (const handler of this.mouseDown) handler(event)
    if (right) for (const handler of this.contextMenu) handler(event)
  }

  /** Run a registered action by id — the keyboard path to a gutter toggle. */
  runAction(id: string): void {
    const action = this.actions.find((a) => a.id === id)
    if (!action) throw new Error(`no action ${id}`)
    void action.run(this as unknown as Monaco.editor.ICodeEditor)
  }

  /** @internal */
  swapModel(path: string): void {
    this.path = path
    for (const handler of this.modelChange) handler()
  }
}

const editors: FakeEditor[] = []

/** The editor the most recently mounted fake component created. */
export function latestFakeEditor(): FakeEditor {
  const editor = editors.at(-1)
  if (!editor) throw new Error("no fake Monaco editor has mounted")
  return editor
}

export function resetFakeEditors(): void {
  editors.length = 0
}

interface FakeEditorProps {
  path?: string
  defaultValue?: string
  defaultLanguage?: string
  onChange?: (value: string | undefined) => void
  onMount?: (editor: Monaco.editor.IStandaloneCodeEditor, monaco: typeof Monaco) => void
  options?: { readOnly?: boolean; glyphMargin?: boolean }
}

export function FakeMonacoEditor({
  path = "",
  defaultValue = "",
  defaultLanguage,
  onChange,
  onMount,
  options,
}: FakeEditorProps) {
  const editorRef = useRef<FakeEditor | null>(null)
  const onMountRef = useRef(onMount)
  useEffect(() => {
    onMountRef.current = onMount
  }, [onMount])

  useEffect(() => {
    const editor = new FakeEditor(path)
    editorRef.current = editor
    editors.push(editor)
    onMountRef.current?.(
      editor as unknown as Monaco.editor.IStandaloneCodeEditor,
      fakeMonacoNamespace,
    )
    // The path is the mount-time one by design; later changes are model swaps below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const editor = editorRef.current
    if (editor && editor.path !== path) editor.swapModel(path)
  }, [path])

  useEffect(() => {
    editorRef.current?.updateOptions(options)
  }, [options])

  return (
    <div
      data-testid="monaco"
      data-path={path}
      data-language={defaultLanguage}
      data-glyph-margin={options?.glyphMargin ? "true" : "false"}
    >
      <textarea
        aria-label="Editor"
        key={path}
        defaultValue={defaultValue}
        readOnly={options?.readOnly}
        onChange={(e) => onChange?.(e.target.value)}
      />
    </div>
  )
}
