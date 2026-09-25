import type { Column } from "@aws-sdk/client-glue"
import type { ParquetField } from "@/lib/data-sources/parquet-reader"
import { delimitedHead, jsonlHead } from "@/lib/data-sources/text-blocks"
import { inferJsonHiveType, inferTextHiveType, parquetHiveType } from "@/lib/hive-types"
import type { TabularKind } from "@/features/s3/preview-kind"

/**
 * A table's columns inferred from one sampled object, the way a Glue crawler
 * would register them: a CSV's header and the types its values agree on,
 * a JSON Lines file's keys, a Parquet file's own schema. The parsing is the
 * S3 preview's — the same functions read the file for the data grid.
 */

export interface InferredSchema {
  format: TabularKind
  columns: Column[]
  /** The delimiter the text chose, for CSV and TSV. */
  delimiter?: string
  /** Values are quoted, so the table needs OpenCSVSerde rather than LazySimpleSerDe. */
  quoted?: boolean
}

/** Rows sampled for types: enough to see past a few empty cells, cheap to parse. */
export const SAMPLE_ROWS = 200

/** Athena stores column names in lower case, and so does a crawler. */
function column(name: string, type: string): Column {
  return { Name: name.toLowerCase(), Type: type }
}

/**
 * The schema of a CSV, TSV or JSON Lines object from its opening bytes.
 * Throws `NotTabularError` when the text is not a table — the message says why.
 */
export function schemaFromText(
  format: Exclude<TabularKind, "parquet">,
  text: string,
  truncated: boolean,
): InferredSchema {
  const options = { rows: SAMPLE_ROWS, truncated }
  if (format === "jsonl") {
    const { head } = jsonlHead(text, options)
    return {
      format,
      columns: head.columns.map((c, i) =>
        column(c.name, inferJsonHiveType(head.first.columns[i] ?? [])),
      ),
    }
  }
  const { head } = delimitedHead(text, format === "tsv" ? "\t" : ",", options)
  return {
    format,
    delimiter: head.delimiter,
    quoted: text.includes('"'),
    columns: head.columns.map((c, i) =>
      column(c.name, inferTextHiveType(head.first.columns[i] ?? [])),
    ),
  }
}

/** The schema of a Parquet object from its footer's fields. */
export function schemaFromParquet(fields: readonly ParquetField[]): InferredSchema {
  return {
    format: "parquet",
    columns: fields.map((f) => column(f.name, parquetHiveType(f.type))),
  }
}
