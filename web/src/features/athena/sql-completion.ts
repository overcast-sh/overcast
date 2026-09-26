import type { Column, TableMetadata } from "@aws-sdk/client-athena"
import { SQL_KEYWORDS } from "./sql-keywords"
import { qualifiedName, quoteIdentifier } from "./sql-text"
import { TRINO_FUNCTIONS } from "./trino-functions"

/**
 * What the editor completes, independent of Monaco: keywords, the Trino
 * functions, and the catalogs, databases, tables and columns the data
 * browser has already loaded — nothing is fetched to complete.
 *
 * After a `name.` only that table's columns (or that database's tables) are
 * offered, which is what a developer typing `orders.` wants.
 */

export type CompletionKind = "keyword" | "function" | "catalog" | "database" | "table" | "column"

export interface CompletionItem {
  label: string
  kind: CompletionKind
  insertText: string
  /** A column's type, a table's database, a function's first signature. */
  detail?: string
  documentation?: string
}

export interface CompletionContext {
  catalogs: readonly string[]
  databases: readonly string[]
  /** The selected database's tables, with their columns. */
  database: string
  tables: readonly TableMetadata[]
}

function columnItems(table: TableMetadata): CompletionItem[] {
  const columns: Column[] = [...(table.Columns ?? []), ...(table.PartitionKeys ?? [])]
  return columns.map((c) => ({
    label: c.Name ?? "",
    kind: "column",
    insertText: quoteIdentifier(c.Name ?? ""),
    detail: `${c.Type ?? ""} · ${table.Name ?? ""}`,
  }))
}

const KEYWORD_ITEMS: CompletionItem[] = [...SQL_KEYWORDS].map((k) => ({
  label: k,
  kind: "keyword",
  insertText: k,
}))

const FUNCTION_ITEMS: CompletionItem[] = TRINO_FUNCTIONS.functions.map((f) => ({
  label: f.name,
  kind: "function",
  insertText: f.name,
  detail: f.signatures[0],
  documentation: [f.description, ...f.signatures].filter(Boolean).join("\n\n"),
}))

/**
 * Completions at a point in the SQL. `qualifier` is the name before a
 * trailing `.` (`orders` in `orders.`), lower-cased, or undefined.
 */
export function completionItems(
  context: CompletionContext,
  qualifier: string | undefined,
): CompletionItem[] {
  if (qualifier !== undefined) {
    const table = context.tables.find((t) => t.Name?.toLowerCase() === qualifier)
    if (table) return columnItems(table)
    if (qualifier === context.database.toLowerCase()) return tableItems(context, false)
    return []
  }
  return [
    ...tableItems(context, true),
    ...context.tables.flatMap(columnItems),
    ...context.databases.map((d): CompletionItem => ({
      label: d,
      kind: "database",
      insertText: quoteIdentifier(d),
    })),
    ...context.catalogs.map((c): CompletionItem => ({
      label: c,
      kind: "catalog",
      insertText: quoteIdentifier(c),
    })),
    ...KEYWORD_ITEMS,
    ...FUNCTION_ITEMS,
  ]
}

function tableItems(context: CompletionContext, qualify: boolean): CompletionItem[] {
  return context.tables.map((t) => ({
    label: t.Name ?? "",
    kind: "table",
    insertText: qualify
      ? qualifiedName(context.database, t.Name ?? "")
      : quoteIdentifier(t.Name ?? ""),
    detail: context.database,
  }))
}

/** The name before a trailing `.` in the text up to the cursor, lower-cased. */
export function qualifierBefore(textBeforeCursor: string): string | undefined {
  const match = /(?:"((?:[^"]|"")+)"|([A-Za-z_][\w]*))\.\w*$/.exec(textBeforeCursor)
  if (!match) return undefined
  const [, quoted, bare]: (string | undefined)[] = match
  return (quoted?.replaceAll('""', '"') ?? bare ?? "").toLowerCase()
}
