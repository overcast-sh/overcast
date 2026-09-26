import type { TableMetadata } from "@aws-sdk/client-athena"

/**
 * What a table's data is stored as, for the data browser's badge — read off
 * the parameters `GetTableMetadata` carries: `table_type` for Iceberg, then
 * the input format or SerDe Glue records for everything else.
 */
export type TableFormat = "ICEBERG" | "PARQUET" | "ORC" | "JSON" | "AVRO" | "CSV" | "VIEW"

const BY_CLASS: [RegExp, TableFormat][] = [
  [/parquet/i, "PARQUET"],
  [/orc/i, "ORC"],
  [/json/i, "JSON"],
  [/avro/i, "AVRO"],
  [/opencsv|lazysimple|textinputformat|csv/i, "CSV"],
]

export function tableFormat(table: TableMetadata): TableFormat | undefined {
  const params: Partial<Record<string, string>> = table.Parameters ?? {}
  if (params.table_type?.toUpperCase() === "ICEBERG") return "ICEBERG"
  if (table.TableType === "VIRTUAL_VIEW") return "VIEW"
  const hints = [params.classification, params.inputformat, params["serde.serialization.lib"]]
  for (const hint of hints) {
    if (!hint) continue
    const match = BY_CLASS.find(([pattern]) => pattern.test(hint))
    if (match) return match[1]
  }
  return undefined
}
