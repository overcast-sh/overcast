import { scanSql } from "./sql-text"

/**
 * What a DDL statement did, in one line — *Created table `sales.orders`* —
 * read from its SQL. Athena reports only `StatementType: DDL`, so the verb
 * and the object come from the statement itself. Null for DDL this does not
 * recognise (`MSCK REPAIR TABLE`, `SHOW …`), which then says nothing more.
 */
export interface DdlSummary {
  verb: "Created" | "Dropped" | "Altered"
  kind: "table" | "view" | "database"
  database: string
  /** Absent for a database. */
  name?: string
}

/** One name part: `"quoted"`, `` `quoted` `` or bare. */
const PART = String.raw`"(?:[^"]|"")+"|\x60[^\x60]+\x60|\w+`
/** A name, optionally qualified by its database. */
const NAME = String.raw`((?:${PART})(?:\.(?:${PART}))?)`

const PATTERNS: [RegExp, DdlSummary["verb"], DdlSummary["kind"]][] = [
  [
    new RegExp(String.raw`^CREATE\s+(?:EXTERNAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?${NAME}`, "i"),
    "Created",
    "table",
  ],
  [new RegExp(String.raw`^CREATE\s+(?:OR\s+REPLACE\s+)?VIEW\s+${NAME}`, "i"), "Created", "view"],
  [
    new RegExp(String.raw`^CREATE\s+(?:DATABASE|SCHEMA)\s+(?:IF\s+NOT\s+EXISTS\s+)?${NAME}`, "i"),
    "Created",
    "database",
  ],
  [new RegExp(String.raw`^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?${NAME}`, "i"), "Dropped", "table"],
  [new RegExp(String.raw`^DROP\s+VIEW\s+(?:IF\s+EXISTS\s+)?${NAME}`, "i"), "Dropped", "view"],
  [
    new RegExp(String.raw`^DROP\s+(?:DATABASE|SCHEMA)\s+(?:IF\s+EXISTS\s+)?${NAME}`, "i"),
    "Dropped",
    "database",
  ],
  [new RegExp(String.raw`^ALTER\s+TABLE\s+${NAME}`, "i"), "Altered", "table"],
]

function unquote(part: string): string {
  if (part.startsWith('"')) return part.slice(1, -1).replaceAll('""', '"')
  if (part.startsWith("`")) return part.slice(1, -1)
  return part.toLowerCase()
}

/** The statement without its comments, so a leading comment does not hide the verb. */
function withoutComments(sql: string): string {
  return scanSql(sql)
    .filter((s) => s.kind !== "comment")
    .map((s) => s.text)
    .join("")
    .trim()
}

export function ddlSummary(sql: string, database: string): DdlSummary | null {
  const statement = withoutComments(sql)
  for (const [pattern, verb, kind] of PATTERNS) {
    const match = pattern.exec(statement)
    if (!match) continue
    const parts = (match[1].match(new RegExp(PART, "g")) ?? []).map(unquote)
    if (kind === "database") return { verb, kind, database: parts[0] }
    const [db, name] = parts.length > 1 ? parts : [database, parts[0]]
    return { verb, kind, database: db, name }
  }
  return null
}
