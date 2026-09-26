import type { Table } from "@aws-sdk/client-glue"

/**
 * What a Glue table holds, read the way Athena and a crawler would: the
 * Iceberg marker first, then the SerDe and input format, then the
 * crawler's `classification`.
 */
export type TableFormat = "ICEBERG" | "PARQUET" | "ORC" | "AVRO" | "JSON" | "CSV" | "VIEW"

/** The parts of a table the format and location are read from. */
type TableShape = Pick<Table, "Parameters" | "StorageDescriptor" | "TableType">

/** Matched against the SerDe, the input format and `classification`, in that order. */
const MARKERS: readonly [TableFormat, RegExp][] = [
  ["PARQUET", /parquet/i],
  // Not a bare /orc/: that would match inside other class names.
  ["ORC", /^orc$|\.orc\.|orcserde|orcinputformat/i],
  ["AVRO", /avro/i],
  ["JSON", /json/i],
  ["CSV", /csv|lazysimpleserde|textinputformat/i],
]

/** A table parameter by name, ignoring case: Athena writes `table_type`, PyIceberg `TABLE_TYPE`. */
export function tableParameter(table: TableShape, name: string): string | undefined {
  const params = table.Parameters ?? {}
  const key = Object.keys(params).find((k) => k.toLowerCase() === name)
  return key === undefined ? undefined : params[key]
}

export function isIcebergTable(table: TableShape): boolean {
  return tableParameter(table, "table_type")?.toUpperCase() === "ICEBERG"
}

/** The table's format, or `undefined` when nothing on it says. */
export function tableFormat(table: TableShape): TableFormat | undefined {
  if (isIcebergTable(table)) return "ICEBERG"
  if (table.TableType === "VIRTUAL_VIEW") return "VIEW"
  const sd = table.StorageDescriptor
  const clues = [
    sd?.SerdeInfo?.SerializationLibrary,
    sd?.InputFormat,
    tableParameter(table, "classification"),
  ]
  for (const clue of clues) {
    const match = clue && MARKERS.find(([, marker]) => marker.test(clue))
    if (match) return match[0]
  }
  return undefined
}

/** Where the table's data lives: the storage descriptor's location. */
export function tableLocation(table: TableShape): string | undefined {
  return table.StorageDescriptor?.Location || undefined
}

/** An Iceberg table's current `metadata.json`. */
export function metadataLocation(table: TableShape): string | undefined {
  return tableParameter(table, "metadata_location")
}
