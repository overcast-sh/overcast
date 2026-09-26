import type * as Monaco from "monaco-editor"

/**
 * The editor options and theme every Monaco editor in the console shares —
 * the code browser, the Lambda create wizard, the Athena SQL editor — so
 * they read as one editor. A caller spreads these and adds its own.
 */
export const MONACO_BASE_OPTIONS: Monaco.editor.IStandaloneEditorConstructionOptions = {
  fontSize: 13,
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
}

/** Monaco takes a theme name, not the CSS tokens: its built-in pair, following the app's theme. */
export function monacoTheme(isDark: boolean): string {
  return isDark ? "vs-dark" : "light"
}
