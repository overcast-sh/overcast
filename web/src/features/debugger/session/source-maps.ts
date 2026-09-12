/**
 * Source maps for the console debugger (docs/plans/compute-debugger-console.md
 * § 3.3): resolve each parsed script's `sourceMapURL`, parse it with
 * `@jridgewell/trace-mapping`, register the map's `sources` as *original*
 * files, and translate positions both ways — a gutter breakpoint on an
 * original file to the generated line/column the inspector wants, and a
 * paused call frame's generated location back to the original file.
 *
 * Paths are relative to the deployment root (`/var/task` in the container),
 * which is how the source endpoint names files. A script outside the root
 * (Node's own modules) has no path here and is never registered.
 *
 * Lines are 1-based and columns 0-based throughout, as in `source-map`
 * and trace-mapping, and *unlike* CDP, whose lines are 0-based — the
 * session converts at the protocol edge.
 */
import {
  LEAST_UPPER_BOUND,
  TraceMap,
  generatedPositionFor,
  originalPositionFor,
  sourceContentFor,
} from "@jridgewell/trace-mapping"

const TASK_ROOT = "/var/task"
const TASK_ROOT_URL = `file://${TASK_ROOT}/`

/** A position in a file, 1-based line and 0-based column. */
export interface Position {
  path: string
  line: number
  column: number
}

/**
 * Where an original file's content comes from. `deployment` is a file the
 * zip carries at that path (opened through the source endpoint); `map` is
 * the map's own `sourcesContent`; `unavailable` is a source the map names
 * but nothing has the text of.
 */
export type OriginalContentOrigin = "deployment" | "map" | "unavailable"

export interface OriginalFile {
  /** Display path: root-relative inside the deployment, the map's resolved path outside it. */
  path: string
  origin: OriginalContentOrigin
  /** The text, when it came from the map; `null` otherwise. */
  content: string | null
  /** The generated scripts this file maps into, root-relative. */
  generated: string[]
}

export interface SourceMapRegistryOptions {
  /** Read a file from the deployment by root-relative path — the source endpoint. */
  fetchFile: (path: string) => Promise<string>
  /** Whether the deployment carries a file at a root-relative path. */
  hasFile: (path: string) => boolean
}

/** `file:///var/task/dist/index.js` → `dist/index.js`; `null` for anything outside the root. */
export function scriptPath(url: string): string | null {
  if (!url.startsWith(TASK_ROOT_URL)) return null
  const rest = url.slice(TASK_ROOT_URL.length)
  return rest === "" ? null : rest
}

/** The URL the inspector reports for a root-relative path. */
export function scriptUrl(path: string): string {
  return TASK_ROOT_URL + path
}

/** A resolved `file:` URL back to a display path: root-relative inside the deployment, absolute outside it. */
function displayPath(resolved: string): { path: string; inDeployment: boolean } {
  const inside = scriptPath(resolved)
  if (inside !== null) return { path: inside, inDeployment: true }
  if (resolved.startsWith("file://"))
    return { path: resolved.slice("file://".length), inDeployment: false }
  return { path: resolved, inDeployment: false }
}

/** Decode a `data:` source map URL — base64 or percent-encoded JSON. */
function decodeDataUrl(url: string): string | null {
  const comma = url.indexOf(",")
  if (comma < 0) return null
  const meta = url.slice(5, comma)
  const payload = url.slice(comma + 1)
  try {
    if (meta.split(";").includes("base64")) {
      const bytes = Uint8Array.from(atob(payload), (c) => c.charCodeAt(0))
      return new TextDecoder().decode(bytes)
    }
    return decodeURIComponent(payload)
  } catch {
    return null
  }
}

interface RegisteredMap {
  generatedPath: string
  map: TraceMap
  /** Display path per `resolvedSources` index. */
  sourcePaths: string[]
}

export class SourceMapRegistry {
  private readonly fetchFile: SourceMapRegistryOptions["fetchFile"]
  private readonly hasFile: SourceMapRegistryOptions["hasFile"]
  private readonly maps = new Map<string, RegisteredMap>()
  private readonly originals = new Map<string, OriginalFile>()

  constructor({ fetchFile, hasFile }: SourceMapRegistryOptions) {
    this.fetchFile = fetchFile
    this.hasFile = hasFile
  }

  get hasMaps(): boolean {
    return this.maps.size > 0
  }

  /**
   * Resolve and register the map for a generated script, if it names one.
   * Returns whether a map is now registered for it. A map that cannot be
   * fetched or parsed leaves the script unmapped — the generated file is
   * still debuggable on its own.
   */
  async register(script: { path: string; sourceMapURL?: string }): Promise<boolean> {
    if (this.maps.has(script.path)) return true
    const url = script.sourceMapURL
    if (!url) return false

    let text: string | null
    let mapUrl: string
    if (url.startsWith("data:")) {
      text = decodeDataUrl(url)
      mapUrl = scriptUrl(script.path)
    } else {
      let resolved: URL
      try {
        resolved = new URL(url, scriptUrl(script.path))
      } catch {
        return false
      }
      const mapPath = scriptPath(resolved.href)
      if (mapPath === null) return false
      mapUrl = resolved.href
      try {
        text = await this.fetchFile(mapPath)
      } catch {
        return false
      }
    }
    if (text === null) return false

    let map: TraceMap
    try {
      map = new TraceMap(text, mapUrl)
    } catch {
      return false
    }
    // The await above may have raced a second registration of the same script.
    if (this.maps.has(script.path)) return true

    const sourcePaths = map.resolvedSources.map((resolved, i) => {
      const { path, inDeployment } = displayPath(resolved)
      const content = sourceContentFor(map, resolved) ?? this.sourceContentAt(map, i)
      let origin: OriginalContentOrigin
      if (inDeployment && this.hasFile(path)) origin = "deployment"
      else if (content !== null) origin = "map"
      else origin = "unavailable"
      const existing = this.originals.get(path)
      if (existing) {
        if (!existing.generated.includes(script.path)) existing.generated.push(script.path)
        // A better origin from a later map wins; an unavailable one never downgrades.
        if (existing.origin === "unavailable" && origin !== "unavailable") {
          existing.origin = origin
          existing.content = origin === "map" ? content : null
        }
      } else {
        this.originals.set(path, {
          path,
          origin,
          content: origin === "map" ? content : null,
          generated: [script.path],
        })
      }
      return path
    })
    this.maps.set(script.path, { generatedPath: script.path, map, sourcePaths })
    return true
  }

  /** `sourcesContent` by index, for a source whose name trace-mapping cannot match back. */
  private sourceContentAt(map: TraceMap, index: number): string | null {
    return map.sourcesContent?.[index] ?? null
  }

  /** Every original file every registered map names, in registration order. */
  originalFiles(): OriginalFile[] {
    return [...this.originals.values()]
  }

  originalFile(path: string): OriginalFile | undefined {
    return this.originals.get(path)
  }

  isOriginal(path: string): boolean {
    return this.originals.has(path)
  }

  /** Whether a generated script has a map registered. */
  isMapped(generatedPath: string): boolean {
    return this.maps.has(generatedPath)
  }

  /** The text of an original file: from the map, or fetched from the deployment. */
  async originalContent(path: string): Promise<string | null> {
    const file = this.originals.get(path)
    if (!file) return null
    if (file.origin === "map") return file.content
    if (file.origin === "deployment") return this.fetchFile(path)
    return null
  }

  /**
   * An original position to the first generated position on that line, or
   * `null` when no map names the file or nothing on that line was mapped —
   * a blank line, a comment, a type-only declaration.
   */
  toGenerated(original: Position): Position | null {
    for (const entry of this.maps.values()) {
      const index = entry.sourcePaths.indexOf(original.path)
      if (index < 0) continue
      const hit = generatedPositionFor(entry.map, {
        source: entry.map.resolvedSources[index],
        line: original.line,
        column: original.column,
        bias: LEAST_UPPER_BOUND,
      })
      if (hit.line !== null) {
        return { path: entry.generatedPath, line: hit.line, column: hit.column }
      }
    }
    return null
  }

  /** A generated position back to its original, or `null` when the script has no map or the map has no entry. */
  toOriginal(generated: Position): Position | null {
    const entry = this.maps.get(generated.path)
    if (!entry) return null
    const hit = originalPositionFor(entry.map, { line: generated.line, column: generated.column })
    if (hit.source === null) return null
    const index = entry.map.resolvedSources.indexOf(hit.source)
    const path = index >= 0 ? entry.sourcePaths[index] : displayPath(hit.source).path
    return { path, line: hit.line, column: hit.column }
  }

  /** Forget everything — a stopped session starts over. */
  clear(): void {
    this.maps.clear()
    this.originals.clear()
  }
}
