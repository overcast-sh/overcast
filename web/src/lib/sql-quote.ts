/**
 * Quoting for the two SQL dialects Athena speaks: Hive DDL (`CREATE EXTERNAL
 * TABLE`, `MSCK REPAIR TABLE`) quotes names in backticks, and Trino DML
 * (`SELECT`) in double quotes.
 */

/** A name as Hive DDL quotes it: `` `order date` ``. */
export function hiveIdentifier(name: string): string {
  return `\`${name.replace(/`/g, "``")}\``
}

/** A name as Trino quotes it: `"order date"`. */
export function trinoIdentifier(name: string): string {
  return `"${name.replace(/"/g, '""')}"`
}

/**
 * A table name Glue and Athena accept unquoted, made from any text (a file
 * name, a prefix): lowercase letters, digits and underscores, not starting
 * with a digit. `fallback` names a text with nothing usable in it.
 */
export function tableIdentifier(text: string, fallback = "data"): string {
  const id = text
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, "_")
    .replace(/^_+|_+$/g, "")
  return /^[0-9]/.test(id) ? `t_${id}` : id || fallback
}

/**
 * A Hive DDL string literal. Hive reads backslash escapes inside quotes, so
 * a tab delimiter is written `'\t'`, the way Athena's own DDL shows it.
 */
export function hiveString(value: string): string {
  const escaped = value
    .replace(/\\/g, "\\\\")
    .replace(/'/g, "\\'")
    .replace(/\t/g, "\\t")
    .replace(/\n/g, "\\n")
  return `'${escaped}'`
}
