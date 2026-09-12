import { SourceMapRegistry, scriptPath, scriptUrl } from "./source-maps"

/**
 * A four-line TypeScript handler and the five-line JavaScript `tsc` makes of
 * it. The VLQ segments are written by hand so the expected positions can be
 * read off the two listings:
 *
 *   src/index.ts                        dist/index.js
 *   1 export const handler = async…     1 "use strict";
 *   2   const x = 1                     2 exports.handler = async () => {   ← ts 1:0
 *   3   return x                        3     const x = 1;                  ← ts 2:2
 *   4 }                                 4     return x;                     ← ts 3:2
 *                                       5 };                                ← ts 4:0
 */
const ORIGINAL = "export const handler = async () => {\n  const x = 1\n  return x\n}\n"
const MAPPINGS = ";AAAA;IACE;IACA;AACF"

function mapJson(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    version: 3,
    file: "index.js",
    sources: ["../src/index.ts"],
    sourcesContent: [ORIGINAL],
    mappings: MAPPINGS,
    ...overrides,
  })
}

function inlineUrl(json: string): string {
  return `data:application/json;charset=utf-8;base64,${btoa(json)}`
}

function registry(files: Record<string, string>) {
  const fetchFile = vi.fn((path: string) =>
    Object.hasOwn(files, path)
      ? Promise.resolve(files[path])
      : Promise.reject(new Error(`no such file: ${path}`)),
  )
  const r = new SourceMapRegistry({ fetchFile, hasFile: (path) => path in files })
  return { r, fetchFile }
}

describe("scriptPath", () => {
  it("strips the deployment root and rejects anything outside it", () => {
    expect(scriptPath("file:///var/task/dist/index.js")).toBe("dist/index.js")
    expect(scriptPath("file:///var/task/")).toBeNull()
    expect(scriptPath("node:internal/main")).toBeNull()
    expect(scriptPath("file:///var/runtime/index.mjs")).toBeNull()
    expect(scriptUrl("dist/index.js")).toBe("file:///var/task/dist/index.js")
  })
})

describe("SourceMapRegistry > registering", () => {
  it("fetches a relative map beside the script and lists its sources as deployment files", async () => {
    const { r, fetchFile } = registry({
      "dist/index.js.map": mapJson(),
      "src/index.ts": ORIGINAL,
    })
    expect(r.hasMaps).toBe(false)

    await expect(r.register({ path: "dist/index.js", sourceMapURL: "index.js.map" })).resolves.toBe(
      true,
    )

    expect(fetchFile).toHaveBeenCalledWith("dist/index.js.map")
    expect(r.hasMaps).toBe(true)
    expect(r.isMapped("dist/index.js")).toBe(true)
    expect(r.originalFiles()).toEqual([
      { path: "src/index.ts", origin: "deployment", content: null, generated: ["dist/index.js"] },
    ])
    await expect(r.originalContent("src/index.ts")).resolves.toBe(ORIGINAL)
  })

  it("decodes an inline map without touching the source endpoint", async () => {
    const { r, fetchFile } = registry({ "src/index.ts": ORIGINAL })

    await r.register({ path: "dist/index.js", sourceMapURL: inlineUrl(mapJson()) })

    expect(fetchFile).not.toHaveBeenCalled()
    expect(r.isOriginal("src/index.ts")).toBe(true)
  })

  it("falls back to sourcesContent when the deployment lacks the source, and to unavailable when the map has no text", async () => {
    const { r } = registry({ "dist/index.js.map": mapJson() })
    await r.register({ path: "dist/index.js", sourceMapURL: "index.js.map" })
    expect(r.originalFile("src/index.ts")).toMatchObject({ origin: "map", content: ORIGINAL })
    await expect(r.originalContent("src/index.ts")).resolves.toBe(ORIGINAL)

    const bare = registry({ "dist/other.js.map": mapJson({ sourcesContent: undefined }) })
    await bare.r.register({ path: "dist/other.js", sourceMapURL: "other.js.map" })
    expect(bare.r.originalFile("src/index.ts")).toMatchObject({
      origin: "unavailable",
      content: null,
    })
    await expect(bare.r.originalContent("src/index.ts")).resolves.toBeNull()
  })

  it("keeps a source outside the deployment under its resolved absolute path", async () => {
    const { r } = registry({
      "dist/index.js.map": mapJson({ sources: ["../../../home/me/app/src/index.ts"] }),
    })
    await r.register({ path: "dist/index.js", sourceMapURL: "index.js.map" })
    expect(r.originalFiles().map((f) => f.path)).toEqual(["/home/me/app/src/index.ts"])
    expect(r.originalFile("/home/me/app/src/index.ts")?.origin).toBe("map")
  })

  it("leaves a script unmapped when there is no map URL, the fetch fails, or the map is not JSON", async () => {
    const { r } = registry({ "dist/bad.js.map": "{not json" })
    await expect(r.register({ path: "dist/plain.js" })).resolves.toBe(false)
    await expect(r.register({ path: "dist/lost.js", sourceMapURL: "lost.js.map" })).resolves.toBe(
      false,
    )
    await expect(r.register({ path: "dist/bad.js", sourceMapURL: "bad.js.map" })).resolves.toBe(
      false,
    )
    await expect(
      r.register({ path: "dist/remote.js", sourceMapURL: "https://cdn.example/remote.js.map" }),
    ).resolves.toBe(false)
    expect(r.hasMaps).toBe(false)
    expect(r.originalFiles()).toEqual([])
  })

  it("registers a script once and merges a second map naming the same source", async () => {
    const { r, fetchFile } = registry({
      "dist/a.js.map": mapJson(),
      "dist/b.js.map": mapJson(),
    })
    await r.register({ path: "dist/a.js", sourceMapURL: "a.js.map" })
    await r.register({ path: "dist/a.js", sourceMapURL: "a.js.map" })
    await r.register({ path: "dist/b.js", sourceMapURL: "b.js.map" })
    expect(fetchFile).toHaveBeenCalledTimes(2)
    expect(r.originalFile("src/index.ts")?.generated).toEqual(["dist/a.js", "dist/b.js"])
  })

  it("clear() forgets every map and file", async () => {
    const { r } = registry({ "dist/index.js.map": mapJson() })
    await r.register({ path: "dist/index.js", sourceMapURL: "index.js.map" })
    r.clear()
    expect(r.hasMaps).toBe(false)
    expect(r.isOriginal("src/index.ts")).toBe(false)
  })
})

describe("SourceMapRegistry > positions", () => {
  async function mapped() {
    const { r } = registry({ "dist/index.js.map": mapJson(), "src/index.ts": ORIGINAL })
    await r.register({ path: "dist/index.js", sourceMapURL: "index.js.map" })
    return r
  }

  it("maps a generated position back to the original file", async () => {
    const r = await mapped()
    expect(r.toOriginal({ path: "dist/index.js", line: 3, column: 4 })).toEqual({
      path: "src/index.ts",
      line: 2,
      column: 2,
    })
    expect(r.toOriginal({ path: "dist/index.js", line: 2, column: 0 })).toEqual({
      path: "src/index.ts",
      line: 1,
      column: 0,
    })
    // A column past the last segment on the line still resolves to that segment.
    expect(r.toOriginal({ path: "dist/index.js", line: 4, column: 12 })).toMatchObject({
      path: "src/index.ts",
      line: 3,
    })
  })

  it("maps an original line to the first generated position on it", async () => {
    const r = await mapped()
    expect(r.toGenerated({ path: "src/index.ts", line: 2, column: 0 })).toEqual({
      path: "dist/index.js",
      line: 3,
      column: 4,
    })
    expect(r.toGenerated({ path: "src/index.ts", line: 4, column: 0 })).toEqual({
      path: "dist/index.js",
      line: 5,
      column: 0,
    })
  })

  it("answers null for a frame no map resolves", async () => {
    const r = await mapped()
    // The generated prologue has no mapping.
    expect(r.toOriginal({ path: "dist/index.js", line: 1, column: 0 })).toBeNull()
    // A script that never registered a map.
    expect(r.toOriginal({ path: "dist/unmapped.js", line: 1, column: 0 })).toBeNull()
    // An original line nothing was generated from, and a file no map names.
    expect(r.toGenerated({ path: "src/index.ts", line: 9, column: 0 })).toBeNull()
    expect(r.toGenerated({ path: "src/elsewhere.ts", line: 1, column: 0 })).toBeNull()
  })
})
