import { inferColumns } from "./column-types"
import {
  columnNames,
  parseDelimited,
  recordFields,
  sniffDelimiter,
  type Delimiter,
} from "./delimited-parse"
import { parseJsonl, recordColumns, uniformColumns } from "./jsonl-parse"
import { NotTabularError } from "./row-source"
import type { ColumnarRows, TextHead } from "./worker-protocol"

/**
 * Text as table rows, for the data worker: the head of a file (its columns
 * and first rows, from its opening bytes) and any later block (the bytes
 * between two index offsets). Pure functions of text, so each format's rules
 * are tested without a worker or a fetch.
 */

/** How the rest of the file is read, as decided by its head. */
export type TextLayout =
  { kind: "delimited"; delimiter: Delimiter; width: number } | { kind: "jsonl"; keys: string[] }

interface HeadOptions {
  /** Rows kept for the first block. */
  rows: number
  /** The bytes are the opening window of a longer object. */
  truncated: boolean
}

/**
 * The header, the columns and the first rows of a CSV or TSV. The delimiter
 * comes from the text itself, `preferred` (the extension's) winning a tie.
 */
export function delimitedHead(
  text: string,
  preferred: Delimiter,
  { rows, truncated }: HeadOptions,
): { head: TextHead; layout: TextLayout } {
  const delimiter = sniffDelimiter(text, preferred)
  const parsed = parseDelimited(text, { delimiter, maxRecords: rows + 1, truncated })
  if (parsed.malformed) throw new NotTabularError(parsed.malformed)
  if (parsed.records.length === 0) {
    throw new NotTabularError(
      truncated
        ? "The header is longer than 64 KB, so the file cannot be read as a table."
        : "The file is empty.",
    )
  }
  const [header, ...body] = parsed.records
  const width = Math.max(header.length, ...body.map((record) => record.length))
  const columns = recordFields(body, width)
  return {
    head: {
      columns: inferColumns(columnNames(header, width), columns),
      delimiter,
      first: { count: body.length, columns },
    },
    layout: { kind: "delimited", delimiter, width },
  }
}

/** The columns and first rows of JSON Lines, when its records are a table. */
export function jsonlHead(
  text: string,
  { rows, truncated }: HeadOptions,
): { head: TextHead; layout: TextLayout } {
  const records = jsonlRecords(text, { maxRecords: rows, truncated })
  const keys = uniformColumns(records)
  if (!keys) {
    throw new NotTabularError(
      records.length === 0
        ? "The file has no complete records in its first 64 KB."
        : "Records do not share the same fields, so they are shown as written.",
    )
  }
  const columns = recordColumns(records, keys)
  return {
    head: {
      columns: inferColumns(keys, columns, { numericText: false }),
      first: { count: records.length, columns },
    },
    layout: { kind: "jsonl", keys },
  }
}

/** One block of rows — the text between two index offsets — laid out as the head decided. */
export function textBlock(text: string, layout: TextLayout, rows: number): ColumnarRows {
  if (layout.kind === "jsonl") {
    const records = jsonlRecords(text, { maxRecords: rows })
    return { count: records.length, columns: recordColumns(records, layout.keys) }
  }
  const { records } = parseDelimited(text, {
    delimiter: layout.delimiter,
    maxRecords: rows,
    truncated: false,
  })
  return { count: records.length, columns: recordFields(records, layout.width) }
}

function jsonlRecords(text: string, options: Parameters<typeof parseJsonl>[1]): unknown[] {
  const parsed = parseJsonl(text, options)
  if (!parsed.ok) throw new NotTabularError(parsed.reason)
  return parsed.records
}
