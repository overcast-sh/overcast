import { diffLines, foldUnchanged, type DiffLine } from "./line-diff"

/** The diff as `" a"`, `"-b"`, `"+c"` lines, the way a unified diff prints it. */
function unified(lines: DiffLine[]): string[] {
  const sign = { same: " ", removed: "-", added: "+" } as const
  return lines.map((line) => sign[line.kind] + line.text)
}

describe("diffLines", () => {
  it("marks every line same when the texts match", () => {
    expect(unified(diffLines("a\nb", "a\nb"))).toEqual([" a", " b"])
  })

  it("finds a line added in the middle", () => {
    expect(unified(diffLines("a\nc", "a\nb\nc"))).toEqual([" a", "+b", " c"])
  })

  it("finds a changed line as a removal then an addition", () => {
    expect(unified(diffLines("a\nb\nc", "a\nB\nc"))).toEqual([" a", "-b", "+B", " c"])
  })

  it("numbers each side's lines from one", () => {
    const [, added, same] = diffLines("a\nc", "a\nb\nc")
    expect([added, same]).toEqual([
      { kind: "added", text: "b", newLine: 2 },
      { kind: "same", text: "c", oldLine: 2, newLine: 3 },
    ])
  })

  it("reports a whole rewrite when the texts share nothing", () => {
    expect(unified(diffLines("a\nb", "c"))).toEqual(["-a", "-b", "+c"])
  })
})

describe("foldUnchanged", () => {
  const before = Array.from({ length: 20 }, (_, i) => `line ${i}`).join("\n")
  const after = before.replace("line 10", "line ten")

  it("keeps the context either side of a change and folds the rest", () => {
    const segments = foldUnchanged(diffLines(before, after), 2)
    expect(segments.map((s) => [s.kind, s.lines.length])).toEqual([
      ["folded", 8],
      ["lines", 6],
      ["folded", 7],
    ])
  })

  it("folds nothing when nothing changed around it", () => {
    expect(foldUnchanged(diffLines("a", "b"))).toEqual([
      { kind: "lines", lines: diffLines("a", "b") },
    ])
  })
})
