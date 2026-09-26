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
  const delimiter = head.delimiter ?? ","
  const quoted = hasQuotedField(text, delimiter)
  return {
    format,
    delimiter,
    quoted,
    columns: head.columns.map((c, i) => {
      const type = inferTextHiveType(head.first.columns[i] ?? [])
      return column(c.name, quoted ? openCsvType(type) : type)
    }),
  }
}

/** A field opens with a quote: the table needs OpenCSVSerde, which honours them. */
function hasQuotedField(text: string, delimiter: string): boolean {
  const escaped = delimiter.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
  return new RegExp(`(^|[${escaped}\\n])"`).test(text)
}

/**
 * OpenCSVSerde reads a DATE only as days since the epoch and a TIMESTAMP only
 * as epoch millis, so text dates would read as NULL: like a crawler, keep them
 * strings and let the query cast.
 */
function openCsvType(type: string): string {
  return type === "date" || type === "timestamp" ? "string" : type
}

/** The schema of a Parquet object from its footer's fields. */
export function schemaFromParquet(fields: readonly ParquetField[]): InferredSchema {
  return {
    format: "parquet",
    columns: fields.map((f) => column(f.name, parquetHiveType(f.type))),
  }
}
