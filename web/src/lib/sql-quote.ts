/**
 * Quoting for the SQL Athena speaks. Trino DML (`SELECT`) quotes names in
 * double quotes.
 */

/** A name as Trino quotes it: `"order date"`. */
export function trinoIdentifier(name: string): string {
  return `"${name.replace(/"/g, '""')}"`
}

/** The first rows of a table, as Athena's own *Preview table* writes it. */
export function previewSql(database: string, table: string, limit = 10): string {
  return `SELECT * FROM ${trinoIdentifier(database)}.${trinoIdentifier(table)} LIMIT ${limit};`
}
