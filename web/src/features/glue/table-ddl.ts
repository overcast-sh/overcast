import type { Column, Table } from "@aws-sdk/client-glue"
import { hiveIdentifier, hiveString } from "@/lib/sql-quote"
import { isIcebergTable, tableLocation } from "./table-format"

/**
 * The Athena DDL that recreates a Glue table — what `SHOW CREATE TABLE`
 * prints: `CREATE EXTERNAL TABLE` with its SerDe, formats and properties for
 * a Hive table, `CREATE TABLE … TBLPROPERTIES ('table_type'='ICEBERG')` for
 * an Iceberg one. A view has no DDL here (its text is the engine's own
 * encoding), so it gets `undefined`.
 */

/** Table parameters the DDL says another way, or that the service sets itself. */
const IMPLIED_PARAMETERS = new Set([
  "external",
  "table_type",
  "metadata_location",
  "previous_metadata_location",
])

const INDENT = "  "

function columnList(columns: readonly Column[]): string {
  return columns
    .map((c) => {
      const comment = c.Comment ? ` COMMENT ${hiveString(c.Comment)}` : ""
      return `${INDENT}${hiveIdentifier(c.Name ?? "")} ${c.Type ?? "string"}${comment}`
    })
    .join(",\n")
}

function properties(entries: [string, string][]): string {
  return entries.map(([k, v]) => `${INDENT}${hiveString(k)} = ${hiveString(v)}`).join(",\n")
}

function qualifiedName(table: Table): string {
  return `${hiveIdentifier(table.DatabaseName ?? "")}.${hiveIdentifier(table.Name ?? "")}`
}

function hiveDdl(table: Table): string {
  const sd = table.StorageDescriptor ?? {}
  const lines = [
    `CREATE EXTERNAL TABLE ${qualifiedName(table)} (`,
    columnList(sd.Columns ?? []),
    ")",
  ]
  if (table.Description) lines.push(`COMMENT ${hiveString(table.Description)}`)
  if (table.PartitionKeys?.length) {
    lines.push("PARTITIONED BY (", columnList(table.PartitionKeys), ")")
  }
  if (sd.SerdeInfo?.SerializationLibrary) {
    lines.push(`ROW FORMAT SERDE ${hiveString(sd.SerdeInfo.SerializationLibrary)}`)
    const serdeParams = Object.entries(sd.SerdeInfo.Parameters ?? {})
    if (serdeParams.length > 0) lines.push("WITH SERDEPROPERTIES (", properties(serdeParams), ")")
  }
  if (sd.InputFormat || sd.OutputFormat) {
    lines.push(`STORED AS INPUTFORMAT ${hiveString(sd.InputFormat ?? "")}`)
    lines.push(`OUTPUTFORMAT ${hiveString(sd.OutputFormat ?? "")}`)
  }
  return withTail(lines, table)
}

function icebergDdl(table: Table): string {
  const columns = table.StorageDescriptor?.Columns ?? []
  const lines = [`CREATE TABLE ${qualifiedName(table)} (`, columnList(columns), ")"]
  if (table.Description) lines.push(`COMMENT ${hiveString(table.Description)}`)
  lines.push("-- The partition spec is kept in the Iceberg metadata, not in Glue.")
  return withTail(lines, table, [["table_type", "ICEBERG"]])
}

/** LOCATION and TBLPROPERTIES, which both kinds end with. */
function withTail(lines: string[], table: Table, leading: [string, string][] = []): string {
  const location = tableLocation(table)
  if (location) lines.push(`LOCATION ${hiveString(location)}`)
  const params = Object.entries(table.Parameters ?? {}).filter(
    ([k]) => !IMPLIED_PARAMETERS.has(k.toLowerCase()),
  )
  const all = [...leading, ...params]
  if (all.length > 0) lines.push("TBLPROPERTIES (", properties(all), ")")
  return `${lines.join("\n")};\n`
}

export function tableDdl(table: Table): string | undefined {
  if (table.TableType === "VIRTUAL_VIEW") return undefined
  return isIcebergTable(table) ? icebergDdl(table) : hiveDdl(table)
}
