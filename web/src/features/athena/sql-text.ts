/**
 * Reading SQL text the way the editor needs to: which `?` are parameters,
 * how to format it, how to name a table in it, and where an error points.
 *
 * Everything here works over `scanSql`'s segments, so a `?` or a keyword
 * inside a string literal, a quoted identifier or a comment is never
 * mistaken for SQL.
 */

export type SqlSegmentKind = "code" | "string" | "identifier" | "comment"

export interface SqlSegment {
  kind: SqlSegmentKind
  text: string
}

/** The end of the literal, quoted identifier or comment that starts at `i`. */
function closeOf(sql: string, i: number): { kind: SqlSegmentKind; end: number } | null {
  const c = sql[i]
  if (c === "'" || c === '"' || c === "`") {
    let j = i + 1
    for (;;) {
      const k = sql.indexOf(c, j)
      if (k < 0) return { kind: c === "'" ? "string" : "identifier", end: sql.length }
      // A doubled quote is the quote itself: 'it''s'.
      if (sql[k + 1] === c) {
        j = k + 2
        continue
      }
      return { kind: c === "'" ? "string" : "identifier", end: k + 1 }
    }
  }
  if (sql.startsWith("--", i)) {
    const k = sql.indexOf("\n", i)
    return { kind: "comment", end: k < 0 ? sql.length : k }
  }
  if (sql.startsWith("/*", i)) {
    const k = sql.indexOf("*/", i + 2)
    return { kind: "comment", end: k < 0 ? sql.length : k + 2 }
  }
  return null
}

/** The SQL split into code and the literals, quoted identifiers and comments between it. */
export function scanSql(sql: string): SqlSegment[] {
  const segments: SqlSegment[] = []
  let code = 0
  let i = 0
  while (i < sql.length) {
    const closed = closeOf(sql, i)
    if (!closed) {
      i++
      continue
    }
    if (i > code) segments.push({ kind: "code", text: sql.slice(code, i) })
    segments.push({ kind: closed.kind, text: sql.slice(i, closed.end) })
    i = code = closed.end
  }
  if (code < sql.length) segments.push({ kind: "code", text: sql.slice(code) })
  return segments
}

/** The statement's code alone, comments dropped: what its keywords are read from. */
function codeText(sql: string): string {
  return scanSql(sql)
    .filter((s) => s.kind !== "comment")
    .map((s) => (s.kind === "code" ? s.text : " x "))
    .join("")
    .trim()
}

/**
 * How many `?` placeholders the SQL has: the values `ExecutionParameters`
 * must supply. A `PREPARE`'s placeholders belong to the statement it
 * prepares, which is given its values when it is executed, so it has none.
 */
export function placeholderCount(sql: string): number {
  if (/^PREPARE\b/i.test(codeText(sql))) return 0
  return scanSql(sql)
    .filter((s) => s.kind === "code")
    .reduce((n, s) => n + (s.text.match(/\?/g)?.length ?? 0), 0)
}

/**
 * The prepared statement an `EXECUTE name` runs, when it takes its values
 * from `ExecutionParameters` rather than a `USING` clause — the form those
 * parameters exist for. Its placeholders are in the prepared statement.
 */
export function executedStatement(sql: string): string | undefined {
  return /^EXECUTE\s+(\w+)\s*;?$/i.exec(codeText(sql))?.[1]
}

// ─── Formatting ────────────────────────────────────────────────────────────

/** Clauses that start a line of their own when formatted, longest alternatives first. */
const CLAUSES = [
  String.raw`(?:LEFT|RIGHT|FULL)(?:\s+OUTER)?\s+JOIN`,
  String.raw`(?:INNER|CROSS)\s+JOIN`,
  "JOIN",
  String.raw`GROUP\s+BY`,
  String.raw`ORDER\s+BY`,
  String.raw`UNION(?:\s+ALL)?`,
  "SELECT",
  "FROM",
  "WHERE",
  "HAVING",
  "LIMIT",
  "OFFSET",
  "VALUES",
]

const CLAUSE_START = new RegExp(String.raw`\s+(${CLAUSES.join("|")})\b`, "gi")

/**
 * The SQL with keywords upper-cased, runs of blanks collapsed and each main
 * clause on a line of its own. Literals, quoted identifiers and comments are
 * left exactly as written.
 */
export function formatSql(sql: string, keywords: ReadonlySet<string>): string {
  const formatted = scanSql(sql)
    .map((segment) => {
      if (segment.kind !== "code") return segment.text
      return segment.text
        .replace(/[ \t]+/g, " ")
        .replace(/\b[A-Za-z_]+\b/g, (word) =>
          keywords.has(word.toUpperCase()) ? word.toUpperCase() : word,
        )
        .replace(CLAUSE_START, (_, clause: string) => `\n${clause.replace(/\s+/g, " ")}`)
    })
    .join("")
  return formatted
    .split("\n")
    .map((line) => line.trimEnd())
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim()
}

// ─── Names ─────────────────────────────────────────────────────────────────

/** A name as SQL needs it: bare when it is a plain lower-case identifier, else double-quoted. */
export function quoteIdentifier(name: string): string {
  return /^[a-z_][a-z0-9_]*$/.test(name) ? name : `"${name.replaceAll('"', '""')}"`
}

/** `database.table` (or `database.table.column`), each part quoted as it needs. */
export function qualifiedName(...parts: string[]): string {
  return parts.map(quoteIdentifier).join(".")
}

// ─── Errors ────────────────────────────────────────────────────────────────

export interface SqlPosition {
  line: number
  column: number
}

/**
 * Where an engine error points: Trino prefixes its message with
 * `line 3:15:`. Null when the message names no position.
 */
export function errorPosition(message: string | undefined): SqlPosition | null {
  const match = message && /\bline (\d+):(\d+)\b/.exec(message)
  return match ? { line: Number(match[1]), column: Number(match[2]) } : null
}
