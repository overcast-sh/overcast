import type { GridColumn } from "@/components/data-grid/row-source"

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
  format: "csv" | "tsv" | "jsonl" | "parquet"
  columns: readonly GridColumn[]
  delimiter?: string
}): string {
  const folder = objectKey.includes("/") ? objectKey.slice(0, objectKey.lastIndexOf("/") + 1) : ""
  const file = objectKey.slice(objectKey.lastIndexOf("/") + 1)
  const table = identifier(file.replace(/\.[^.]*$/, "") || "data")
  const location = `s3://${bucket}/${folder}`
  const defs = columns
    .map((c) => `  ${quoteIdentifier(c.name)} ${athenaType(c, format)}`)
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
      const separator = (delimiter ?? (format === "tsv" ? "\t" : ",")) === "\t" ? "\\t" : (delimiter ?? ",")
      storage = [
        "ROW FORMAT SERDE 'org.apache.hadoop.hive.serde2.OpenCSVSerde'",
        `WITH SERDEPROPERTIES ('separatorChar' = '${separator}', 'quoteChar' = '"')`,
      ].join("\n")
    }
  }
  const header = format === "csv" || format === "tsv" ? "\nTBLPROPERTIES ('skip.header.line.count' = '1')" : ""
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

function athenaType(column: GridColumn, format: string): string {
  if (format === "csv" || format === "tsv") return "string"
  if (format === "jsonl") return column.numeric ? "double" : "string"
  const t = (column.type ?? "").toUpperCase()
  if (t.startsWith("DECIMAL")) return t.toLowerCase().replace(/\s/g, "")
  if (t.startsWith("TIMESTAMP")) return "timestamp"
  if (t === "DATE") return "date"
  if (t === "INT64" || t === "UINT64") return "bigint"
  if (t === "INT32" || t === "UINT32" || t === "INT16" || t === "INT8") return "int"
  if (t === "DOUBLE") return "double"
  if (t === "FLOAT") return "float"
  if (t === "BOOLEAN") return "boolean"
  if (t.startsWith("LIST<")) return "array<string>"
  if (t.startsWith("STRUCT<")) {
    const fields = (column.type ?? "").slice(7, -1).split(",").map((f) => f.trim())
    return `struct<${fields.map((f) => `${f}:string`).join(",")}>`
  }
  if (t.startsWith("MAP<")) return "map<string,string>"
  return "string"
}

/** A table name Athena accepts unquoted: lowercase letters, digits and underscores. */
function identifier(name: string): string {
  const id = name.toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "")
  return /^[0-9]/.test(id) ? `t_${id}` : id || "data"
}

/** Column names keep their spelling, backquoted as Hive DDL wants. */
function quoteIdentifier(name: string): string {
  return `\`${name.replace(/`/g, "``")}\``
}
