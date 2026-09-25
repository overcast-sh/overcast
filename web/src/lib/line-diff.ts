/**
 * A line diff of two texts, and the hunks a reader looks at.
 *
 * Myers' O(ND) algorithm ("An O(ND) Difference Algorithm and Its Variations",
 * 1986): the work grows with the size of the change rather than the size of
 * the files, which is what diffing two versions of a table's metadata needs —
 * thousands of lines, a handful of them changed.
 */

export type DiffLine =
  | { kind: "same"; text: string; oldLine: number; newLine: number }
  | { kind: "removed"; text: string; oldLine: number }
  | { kind: "added"; text: string; newLine: number }

/** Edit distance past which the diff gives up and reports a full rewrite. */
const MAX_EDITS = 1000

/** The lines of `before` and `after`, each marked same, removed or added, in reading order. */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = before.split("\n")
  const b = after.split("\n")
  const trace = shortestEditTrace(a, b)
  return trace ? backtrack(a, b, trace) : rewrite(a, b)
}

/**
 * The furthest-reaching D-paths for each D, Myers' `V` arrays, until one
 * reaches the end of both texts. Null past `MAX_EDITS`.
 */
function shortestEditTrace(a: string[], b: string[]): Int32Array[] | null {
  const n = a.length
  const m = b.length
  const max = Math.min(n + m, MAX_EDITS)
  const offset = max + 1
  const v = new Int32Array(2 * max + 3)
  const trace: Int32Array[] = []
  for (let d = 0; d <= max; d++) {
    trace.push(v.slice())
    for (let k = -d; k <= d; k += 2) {
      const down = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1])
      let x = down ? v[offset + k + 1] : v[offset + k - 1] + 1
      let y = x - k
      while (x < n && y < m && a[x] === b[y]) {
        x++
        y++
      }
      v[offset + k] = x
      if (x >= n && y >= m) return trace
    }
  }
  return null
}

function backtrack(a: string[], b: string[], trace: Int32Array[]): DiffLine[] {
  const offset = (trace[0].length - 3) / 2 + 1
  const lines: DiffLine[] = []
  let x = a.length
  let y = b.length
  for (let d = trace.length - 1; d >= 0; d--) {
    const v = trace[d]
    const k = x - y
    const down = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1])
    const prevK = down ? k + 1 : k - 1
    const prevX = v[offset + prevK]
    const prevY = prevX - prevK
    while (x > prevX && y > prevY) {
      x--
      y--
      lines.push({ kind: "same", text: a[x], oldLine: x + 1, newLine: y + 1 })
    }
    if (d === 0) break
    if (down) {
      y--
      lines.push({ kind: "added", text: b[y], newLine: y + 1 })
    } else {
      x--
      lines.push({ kind: "removed", text: a[x], oldLine: x + 1 })
    }
  }
  return lines.reverse()
}

function rewrite(a: string[], b: string[]): DiffLine[] {
  return [
    ...a.map((text, i): DiffLine => ({ kind: "removed", text, oldLine: i + 1 })),
    ...b.map((text, i): DiffLine => ({ kind: "added", text, newLine: i + 1 })),
  ]
}

/** A run of the diff to show, or a run of unchanged lines folded away. */
export type DiffSegment =
  | { kind: "lines"; lines: DiffLine[] }
  | { kind: "folded"; lines: DiffLine[] }

/**
 * The diff as a reader wants it: every change with `context` unchanged lines
 * either side, and each longer unchanged run folded into one segment.
 */
export function foldUnchanged(lines: DiffLine[], context = 3): DiffSegment[] {
  const keep = lines.map((line) => line.kind !== "same")
  lines.forEach((line, i) => {
    if (line.kind === "same") return
    for (let j = Math.max(0, i - context); j <= Math.min(lines.length - 1, i + context); j++) {
      keep[j] = true
    }
  })
  const segments: DiffSegment[] = []
  lines.forEach((line, i) => {
    const kind = keep[i] ? "lines" : "folded"
    const last = segments.at(-1)
    if (last?.kind === kind) last.lines.push(line)
    else segments.push({ kind, lines: [line] })
  })
  return segments
}
