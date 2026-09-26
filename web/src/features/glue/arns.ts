import type { Database, Table } from "@aws-sdk/client-glue"

/**
 * Glue ARNs, which the API does not return: `arn:aws:glue:<region>:<account>:
 * database/<name>` and `…:table/<database>/<name>`. The account is the
 * resource's `CatalogId`.
 */

export function databaseArn(database: Database, region: string): string {
  return `arn:aws:glue:${region}:${database.CatalogId ?? ""}:database/${database.Name ?? ""}`
}

export function tableArn(table: Table, region: string): string {
  return `arn:aws:glue:${region}:${table.CatalogId ?? ""}:table/${table.DatabaseName ?? ""}/${table.Name ?? ""}`
}
