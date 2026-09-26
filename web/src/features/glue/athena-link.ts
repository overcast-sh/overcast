import { linkOptions } from "@tanstack/react-router"
import { ATHENA_TAB } from "@/components/ui/arn-routes"
import { trinoIdentifier } from "@/lib/sql-quote"

/** The Athena workgroups tab, filtered to one workgroup: where its result location is set. */
export function athenaWorkGroupLink(name: string) {
  return linkOptions({ to: "/athena", search: { tab: ATHENA_TAB.workgroups, q: name } })
}

/** The first rows of a table, as Athena's own *Preview table* writes it. */
export function previewSql(database: string, table: string, limit = 10): string {
  return `SELECT * FROM ${trinoIdentifier(database)}.${trinoIdentifier(table)} LIMIT ${limit};`
}
