import { render, screen, within, waitFor } from "@/test/render"
import { latestFakeEditor, resetFakeEditors } from "@/test/monaco"
import { CodeBrowser, type LineDecoration } from "./code-browser"

vi.mock("@monaco-editor/react", async () => {
  const { FakeMonacoEditor } = await import("@/test/monaco")
  return { default: FakeMonacoEditor }
})

const FILES = [
  { name: "index.js", size: 10 },
  { name: "lib/util.js", size: 20 },
]

const loadFile = vi.fn((path: string) =>
  Promise.resolve({
    content: `// ${path}`,
    language: "javascript",
    readOnly: path.startsWith("src/"),
    notice: path.startsWith("src/") ? "Read-only: from the source map" : undefined,
  }),
)

beforeEach(() => {
  resetFakeEditors()
  loadFile.mockClear()
})

describe("CodeBrowser > explorer groups", () => {
  it("lists grouped files under their own labelled section, after the main tree, with the header actions", () => {
    render(
      <CodeBrowser
        files={[...FILES, { name: "src/index.ts", size: 30, group: "Original" }]}
        initialFile="index.js"
        initialValue="// index"
        explorerActions={<button type="button">Show compiled</button>}
      />,
    )
    const group = screen.getByRole("group", { name: "Original" })
    expect(within(group).getByRole("button", { name: "index.ts" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Show compiled" })).toBeInTheDocument()
    // The grouped file is not in the main tree.
    expect(screen.getAllByRole("button", { name: "index.ts" })).toHaveLength(1)
  })

  it("shows the explorer for one file when there are header actions to show", () => {
    render(
      <CodeBrowser
        files={[FILES[0]]}
        initialFile="index.js"
        initialValue=""
        explorerActions={<span>actions</span>}
      />,
    )
    expect(screen.getByText("Explorer")).toBeInTheDocument()
  })
})

describe("CodeBrowser > gutter", () => {
  it("reports a left click as toggle, a right or modifier click as menu, and ignores the text area", () => {
    const onGutterClick = vi.fn()
    render(
      <CodeBrowser
        files={FILES}
        initialFile="index.js"
        initialValue=""
        onGutterClick={onGutterClick}
      />,
    )
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-glyph-margin", "true")
    const editor = latestFakeEditor()

    editor.clickGutter(4)
    expect(onGutterClick).toHaveBeenLastCalledWith("index.js", 4, "toggle")
    editor.clickGutter(5, { right: true })
    expect(onGutterClick).toHaveBeenLastCalledWith("index.js", 5, "menu")
    editor.clickGutter(6, { ctrl: true })
    expect(onGutterClick).toHaveBeenLastCalledWith("index.js", 6, "menu")
    editor.clickGutter(7, { inText: true })
    expect(onGutterClick).toHaveBeenCalledTimes(3)
  })

  it("offers F9 as a keyboard toggle on the cursor line", () => {
    const onGutterClick = vi.fn()
    render(
      <CodeBrowser
        files={FILES}
        initialFile="index.js"
        initialValue=""
        onGutterClick={onGutterClick}
      />,
    )
    const editor = latestFakeEditor()
    editor.position = { lineNumber: 9, column: 3 }
    editor.runAction("code-browser.gutter-toggle")
    expect(onGutterClick).toHaveBeenCalledWith("index.js", 9, "toggle")
  })

  it("keeps the gutter off when nothing asks for it", () => {
    render(<CodeBrowser files={FILES} initialFile="index.js" initialValue="" />)
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-glyph-margin", "false")
  })
})

describe("CodeBrowser > decorations", () => {
  const decorations: Record<string, LineDecoration[]> = {
    "index.js": [
      { line: 2, kind: "glyph", title: "Breakpoint" },
      { line: 3, kind: "glyph-muted" },
      { line: 5, kind: "current", title: "Paused here" },
    ],
    "lib/util.js": [{ line: 1, kind: "glyph" }],
  }

  it("paints the open file's markers and swaps them when the file changes", async () => {
    const { user } = render(
      <CodeBrowser
        files={FILES}
        initialFile="index.js"
        initialValue=""
        loadFile={loadFile}
        decorations={decorations}
      />,
    )
    const editor = latestFakeEditor()
    expect(
      editor.decorations.map((d) => [d.range.startLineNumber, d.options.glyphMarginClassName]),
    ).toEqual([
      [2, "oc-gutter-glyph"],
      [3, "oc-gutter-glyph oc-gutter-glyph-muted"],
      [5, "oc-gutter-current"],
    ])
    expect(editor.decorations[2].options).toMatchObject({
      isWholeLine: true,
      className: "oc-line-current",
      glyphMarginHoverMessage: { value: "Paused here" },
    })

    // Loading a file remounts the editor; the new one paints the new file's markers.
    await user.click(screen.getByRole("button", { name: "util.js" }))
    await waitFor(() => expect(latestFakeEditor()).not.toBe(editor))
    const next = latestFakeEditor()
    expect(next.path).toBe("lib/util.js")
    expect(next.decorations.map((d) => d.range.startLineNumber)).toEqual([1])
  })
})

describe("CodeBrowser > reveal", () => {
  it("opens the file, scrolls to the line, places the cursor and takes focus", async () => {
    const { rerender } = render(
      <CodeBrowser files={FILES} initialFile="index.js" initialValue="" loadFile={loadFile} />,
    )
    const first = latestFakeEditor()

    rerender(
      <CodeBrowser
        files={FILES}
        initialFile="index.js"
        initialValue=""
        loadFile={loadFile}
        revealPosition={{ path: "lib/util.js", line: 7, column: 2 }}
      />,
    )
    // The file loads into a fresh editor; the reveal lands there, not on the disposed one.
    await waitFor(() => expect(latestFakeEditor()).not.toBe(first))
    const editor = latestFakeEditor()
    await waitFor(() => expect(editor.revealLineInCenter).toHaveBeenCalledWith(7))
    expect(first.revealLineInCenter).not.toHaveBeenCalled()
    expect(loadFile).toHaveBeenCalledWith("lib/util.js")
    expect(editor.setPosition).toHaveBeenCalledWith({ lineNumber: 7, column: 3 })
    expect(editor.focus).toHaveBeenCalled()
    expect(screen.getByTestId("monaco")).toHaveAttribute("data-path", "lib/util.js")
  })

  it("reveals the same position again only when the key changes", async () => {
    const at = { path: "index.js", line: 2 }
    const { rerender } = render(
      <CodeBrowser files={FILES} initialFile="index.js" initialValue="" revealPosition={at} />,
    )
    const editor = latestFakeEditor()
    await waitFor(() => expect(editor.revealLineInCenter).toHaveBeenCalledTimes(1))

    rerender(
      <CodeBrowser files={FILES} initialFile="index.js" initialValue="" revealPosition={at} />,
    )
    expect(editor.revealLineInCenter).toHaveBeenCalledTimes(1)

    rerender(
      <CodeBrowser
        files={FILES}
        initialFile="index.js"
        initialValue=""
        revealPosition={{ ...at, key: 2 }}
      />,
    )
    await waitFor(() => expect(editor.revealLineInCenter).toHaveBeenCalledTimes(2))
  })
})

describe("CodeBrowser > per-file loader flags", () => {
  it("locks a file the loader marks read-only and shows its notice", async () => {
    const { user } = render(
      <CodeBrowser
        files={[...FILES, { name: "src/index.ts", size: 1, group: "Original" }]}
        initialFile="index.js"
        initialValue=""
        loadFile={loadFile}
      />,
    )
    expect(screen.getByRole("textbox", { name: "Editor" })).not.toHaveAttribute("readonly")

    await user.click(screen.getByRole("button", { name: "index.ts" }))
    expect(await screen.findByRole("note")).toHaveTextContent("Read-only: from the source map")
    expect(screen.getByRole("textbox", { name: "Editor" })).toHaveAttribute("readonly")
  })
})
