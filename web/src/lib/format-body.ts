import { highlightCode } from "@/lib/highlight-code"
import { highlightGoStack } from "@/lib/go-stack-prism"

export type BodyLanguage = "json" | "xml" | "text"

export interface FormattedBody {
  text: string
  html?: string
}

export interface FormatBodyOptions {
  /**
   * Treat HTML void tags (`<br>`, `<img>`, `<meta>`, …) as self-closing when
   * indenting markup. Defaults to sniffing `contentType` for HTML.
   */
  htmlVoidTags?: boolean
}

/**
 * Bodies larger than this render as plain text — no pretty-printing or
 * syntax highlighting. Parsing/highlighting a large (up to 1 MiB) body on
 * every render is too expensive, especially on pages that re-render every
 * second while a trace is in flight.
 */
export const MAX_FORMAT_BYTES = 256 * 1024

export function formatBodyForDisplay(
  raw: string,
  hint: BodyLanguage,
  contentType?: string,
  opts?: FormatBodyOptions,
): FormattedBody {
  if (raw.length > MAX_FORMAT_BYTES) return { text: raw }
  if (isFormEncoded(contentType) || (hint === "text" && looksLikeFormEncoded(raw))) {
    return formatFormBody(raw)
  }
  const { text, language } = formatBodyText(raw, hint, contentType, opts)
  // Cached (`highlightCode`), so pages that re-render while a body stays
  // on screen re-use the tokenisation instead of paying Prism each time.
  if (language !== null) return { text, html: highlightCode(text, language) }
  return { text }
}

/**
 * The pretty-printing half of `formatBodyForDisplay`, as *policy*: the
 * formatted text plus the Prism language it should highlight as, or null for
 * text that renders plain (unparseable JSON, an un-hinted body). No HTML is
 * produced — a consumer rendering through the highlight kernel's shared
 * component (`HighlightedCode`) hands it the language and lets the kernel
 * pick a backend, instead of being handed markup it may never use.
 *
 * The `MAX_FORMAT_BYTES` gate stays with the callers — they differ on what a
 * refusal means (`formatBodyForDisplay` renders plain silently, the S3
 * preview says so).
 */
export function formatBodyText(
  raw: string,
  hint: BodyLanguage,
  contentType?: string,
  opts?: FormatBodyOptions,
): { text: string; language: "json" | "markup" | null } {
  if (hint === "json" || (hint === "text" && looksLikeJSON(raw))) {
    try {
      return { text: JSON.stringify(JSON.parse(raw), null, 2), language: "json" }
    } catch {
      return { text: raw, language: null }
    }
  }
  if (hint === "xml") {
    const formatted = formatXML(raw, opts?.htmlVoidTags ?? isHtmlContentType(contentType))
    return { text: formatted, language: "markup" }
  }
  return { text: raw, language: null }
}

function looksLikeJSON(s: string): boolean {
  const t = s.trim()
  return (t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))
}

function looksLikeFormEncoded(s: string): boolean {
  return /^[A-Za-z][\w.]*=[^&]*(&[A-Za-z][\w.]*=[^&]*)*$/.test(s.trim())
}

export function contentTypeToHint(contentType: string): BodyLanguage {
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase()
  if (/(^application\/(json|x-ndjson)$|\+json$)/i.test(mediaType)) return "json"
  if (/(^(application|text)\/(xml|xhtml\+xml|html)$|\+xml$)/i.test(mediaType)) return "xml"
  return "text"
}

export function bodyHintFromHeaders(headers: Record<string, string[]>): BodyLanguage {
  const ct = (headers["Content-Type"] ??
    headers["content-type"] ??
    headers["content_type"] ?? [""])[0]
  return contentTypeToHint(ct)
}

/** HTML tags that never take a closing tag — they must not increase indentation. */
const HTML_VOID_TAGS = new Set([
  "area",
  "base",
  "br",
  "col",
  "embed",
  "hr",
  "img",
  "input",
  "link",
  "meta",
  "param",
  "source",
  "track",
  "wbr",
])

function isHtmlContentType(contentType?: string): boolean {
  if (!contentType) return false
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase()
  return /^(text|application)\/html$/.test(mediaType)
}

function markupTagName(tag: string): string {
  const match = /^<\/?\s*([A-Za-z][\w:.-]*)/.exec(tag)
  return match?.[1]?.toLowerCase() ?? ""
}

function isSelfClosingMarkupTag(tag: string, useHtmlVoidTags: boolean): boolean {
  return /\/\s*>$/.test(tag) || (useHtmlVoidTags && HTML_VOID_TAGS.has(markupTagName(tag)))
}

function findMarkupTagEnd(text: string, start: number): number {
  let quote: string | null = null
  for (let i = start + 1; i < text.length; i += 1) {
    const char = text[i]
    if (quote) {
      if (char === quote) quote = null
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      continue
    }
    if (char === ">") return i
  }
  return -1
}

function formatXML(text: string, useHtmlVoidTags = false): string {
  const compact = text.trim()
  if (!compact) return text
  let depth = 0
  const lines: string[] = []

  const pushLine = (line: string, lineDepth = depth) => {
    const trimmed = line.trim()
    if (trimmed) lines.push(`${"  ".repeat(lineDepth)}${trimmed}`)
  }

  for (let i = 0; i < compact.length; ) {
    if (compact[i] !== "<") {
      const nextTag = compact.indexOf("<", i)
      const end = nextTag === -1 ? compact.length : nextTag
      for (const line of compact.slice(i, end).split(/\r?\n/)) pushLine(line)
      i = end
      continue
    }

    const tagEnd = findMarkupTagEnd(compact, i)
    if (tagEnd === -1) {
      pushLine(compact.slice(i))
      break
    }

    const tag = compact.slice(i, tagEnd + 1)
    const isClosing = /^<\//.test(tag)
    const isOpening = /^<[^!?/]/.test(tag)
    if (isClosing) depth = Math.max(0, depth - 1)
    pushLine(tag)
    if (isOpening && !isSelfClosingMarkupTag(tag, useHtmlVoidTags)) depth += 1
    i = tagEnd + 1
  }

  return lines.join("\n")
}

function isFormEncoded(contentType?: string): boolean {
  return !!contentType && /application\/x-www-form-urlencoded/i.test(contentType.split(";")[0])
}

function formatFormBody(raw: string): FormattedBody {
  const params = new URLSearchParams(raw)
  if (Array.from(params.entries()).length === 0) return { text: raw }
  let html = '<table class="w-full text-sm">'
  html +=
    '<thead><tr class="border-b border-border text-fg-muted text-left"><th class="px-3 py-2 font-medium text-xs">Name</th><th class="px-3 py-2 font-medium text-xs">Value</th></tr></thead>'
  html += "<tbody>"
  for (const [k, v] of params.entries()) {
    html += `<tr class="border-b border-border"><td class="px-3 py-2 font-mono text-xs text-accent whitespace-nowrap">${escapeHTML(k)}</td><td class="px-3 py-2 font-mono text-xs break-all">${escapeHTML(v)}</td></tr>`
  }
  html += "</tbody></table>"
  return { text: raw, html }
}

function escapeHTML(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
}

/** Strip CaptureStack + caller frames, then apply Prism highlighting. */
export function formatStackTrace(raw: string): string {
  return highlightGoStack(stripTracingFrames(raw))
}

function stripTracingFrames(s: string): string {
  const lines = s.split("\n")
  const out = [lines[0]] // goroutine header
  for (let i = 1; i < lines.length; i++) {
    if (
      lines[i].includes("internal/trace.CaptureStack") ||
      lines[i].includes("internal/trace.(*Recorder).AddHop")
    ) {
      i += 1 // file:line
      i += 1 // caller func
      i += 1 // caller file:line
      continue
    }
    // Strip function arguments and PC offsets — they're hex pointers, not useful for tracing.
    let ln = lines[i]
    if (!ln.startsWith("\t") && ln.includes("(") && ln.includes(")")) {
      ln = ln.replace(/\(.*\)$/, "()")
    }
    if (ln.startsWith("\t")) {
      ln = ln.replace(/ \+0x[0-9a-f]+$/, "")
    }
    out.push(ln)
  }
  return out.join("\n")
}
