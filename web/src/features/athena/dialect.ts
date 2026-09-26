import { scanSql } from "./sql-text"

/**
 * Where a failed query ran into one of the dialect differences between
 * Athena and the Trino Overcast runs — the table in
 * `docs/services/athena/limitations.md#dialect`. The error box then says so,
 * so a statement that works on AWS does not read as the developer's mistake.
 */
export interface DialectDifference {
  title: string
  detail: string
}

export const DIALECT_DOCS = "services/athena/limitations.md#dialect"

const DIFFERENCES: [RegExp, DialectDifference][] = [
  [
    /^(?:OPTIMIZE|VACUUM)\b/i,
    {
      title: "OPTIMIZE and VACUUM are Athena's own statements",
      detail:
        "Overcast's engine does not parse them. Use Trino's ALTER TABLE … EXECUTE optimize and expire_snapshots.",
    },
  ],
  [
    /^UNLOAD\b/i,
    {
      title: "UNLOAD is not supported here",
      detail: "Write the result with CREATE TABLE AS SELECT instead.",
    },
  ],
  [
    /\bUSING\s+EXTERNAL\s+FUNCTION\b/i,
    {
      title: "Lambda user-defined functions are not supported here",
      detail: "USING EXTERNAL FUNCTION calls a Lambda on AWS; Overcast's engine cannot.",
    },
  ],
  [
    /^ALTER\s+TABLE\s+\S+\s+(?:ADD\s+COLUMNS|REPLACE\s+COLUMNS|RENAME\s+TO|SET\s+TBLPROPERTIES)\b/i,
    {
      title: "This ALTER TABLE is Hive DDL, which the engine does not take",
      detail:
        "Overcast parses only ADD and DROP PARTITION itself; other ALTER TABLE statements go to Trino, which accepts only its own syntax.",
    },
  ],
  [
    /^DESCRIBE\s+\S+\s+(?:PARTITION\b|\w+\s*$)/i,
    {
      title: "DESCRIBE of one column or partition is not parsed",
      detail: "DESCRIBE the whole table instead.",
    },
  ],
]

/** The statement's code alone: no comments, literals blanked, so they cannot match. */
function codeOf(sql: string): string {
  return scanSql(sql)
    .filter((s) => s.kind !== "comment")
    .map((s) => (s.kind === "string" ? "''" : s.text))
    .join("")
    .trim()
}

export function dialectDifference(sql: string): DialectDifference | null {
  const code = codeOf(sql)
  return DIFFERENCES.find(([pattern]) => pattern.test(code))?.[1] ?? null
}
