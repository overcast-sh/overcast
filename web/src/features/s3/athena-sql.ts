import type { DataColumn } from "@/lib/data-sources/row-source"
import { parquetHiveType } from "@/lib/hive-types"
import { hiveIdentifier, tableIdentifier } from "@/lib/sql-quote"
import type { TabularKind } from "./preview-kind"

/**
 * The SQL *Query with Athena* offers for a data file: a `CREATE EXTERNAL
 * TABLE` over the object's folder, and a `SELECT` to start from.
 *
 * Athena tables point at a prefix, not a file, so the location is the
 * object's folder — every file in it becomes rows of the table, which is what
 * a developer who partitioned their data into files wants anyway.
 *
 * Types:
 * - Parquet columns map from their declared types;
 * - CSV and TSV columns are `string` — OpenCSVSerde reads every column as a
 *   string whatever its DDL says (https://docs.aws.amazon.com/athena/latest/ug/csv-serde.html),
 *   so declaring `bigint` would only mislead; cast in the query instead;
 * - JSON Lines numbers are `double`, everything else `string`.
 */
export function athenaSql({
  bucket,
  objectKey,
  format,
  columns,
  delimiter,
}: {
  bucket: string
  objectKey: string
  format: TabularKind
  columns: readonly DataColumn[]
  delimiter?: string
}): string {
  const folder = objectKey.includes("/") ? objectKey.slice(0, objectKey.lastIndexOf("/") + 1) : ""
  const file = objectKey.slice(objectKey.lastIndexOf("/") + 1)
  const table = tableIdentifier(file.replace(/\.[^.]*$/, "") || "data")
  const location = `s3://${bucket}/${folder}`
  const defs = columns
    .map((c) => `  ${hiveIdentifier(c.name)} ${athenaType(c, format)}`)
    .join(",\n")
  let storage: string
  switch (format) {
    case "parquet":
      storage = "STORED AS PARQUET"
      break
    case "jsonl":
      storage = "ROW FORMAT SERDE 'org.openx.data.jsonserde.JsonSerDe'"
      break
    default: {
      const separator =
        (delimiter ?? (format === "tsv" ? "\t" : ",")) === "\t" ? "\\t" : (delimiter ?? ",")
      storage = [
        "ROW FORMAT SERDE 'org.apache.hadoop.hive.serde2.OpenCSVSerde'",
        `WITH SERDEPROPERTIES ('separatorChar' = '${separator}', 'quoteChar' = '"')`,
      ].join("\n")
    }
  }
  const header =
    format === "csv" || format === "tsv" ? "\nTBLPROPERTIES ('skip.header.line.count' = '1')" : ""
  return `CREATE EXTERNAL TABLE ${table} (
${defs}
)
${storage}
LOCATION '${location}'${header};

SELECT *
FROM ${table}
-- WHERE …
-- ORDER BY …
LIMIT 100;`
}

function athenaType(column: DataColumn, format: string): string {
  if (format === "csv" || format === "tsv") return "string"
  if (format === "jsonl") return column.numeric ? "double" : "string"
  return parquetHiveType(column.type)
}
