/**
 * Monaco language id for a path, by extension. Shared by `CodeBrowser` and by
 * loaders that mint files of their own (a debugger's source-mapped originals).
 */
export function languageForPath(path: string): string {
  if (/\.tsx?$/.test(path)) return "typescript"
  if (/\.[mc]?jsx?$/.test(path)) return "javascript"
  if (/\.py$/.test(path)) return "python"
  if (/\.java$/.test(path)) return "java"
  if (/\.cs$/.test(path)) return "csharp"
  if (/\.json$/.test(path)) return "json"
  if (/\.ya?ml$/.test(path)) return "yaml"
  if (/\.md$/.test(path)) return "markdown"
  if (/\.html?$/.test(path)) return "html"
  if (/\.css$/.test(path)) return "css"
  if (/\.sh$|\.bash$/.test(path)) return "shell"
  if (/\.xml$/.test(path)) return "xml"
  return "plaintext"
}
