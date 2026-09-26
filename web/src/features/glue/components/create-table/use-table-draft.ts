import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import type { Column } from "@aws-sdk/client-glue"
import { parseS3Uri } from "@/lib/s3-uri"
import { tableIdentifier } from "@/lib/sql-quote"
import {
  glueDatabasesQueryOptions,
  gluePrefixScanQueryOptions,
  glueSampleSchemaQueryOptions,
} from "../../data"
import type { InferredSchema } from "../../infer-schema"
import { buildPartitionInput, buildTableInput, type TableDraft } from "../../table-input"
import { draftColumn, type DraftColumn } from "./draft-column"

/** A location the wizard can make a table over: an `s3://bucket/prefix/` folder. */
export function tableFolder(uri: string): { bucket: string; prefix: string } | null {
  const location = parseS3Uri(uri)
  if (!location?.bucket) return null
  const { bucket, key } = location
  return { bucket, prefix: key === "" || key.endsWith("/") ? key : `${key}/` }
}

function toColumns(drafts: readonly DraftColumn[]): Column[] {
  return drafts.map((c) => ({ Name: c.name.trim(), Type: c.type.trim() }))
}

/** Glue's own name rule, narrowed to what Athena reads unquoted: lower case, digits, `_`. */
const TABLE_NAME = /^[a-z0-9_]{1,255}$/

/** Why the draft cannot be created yet, in a sentence; `undefined` when it can. */
function draftProblem(
  name: string,
  database: string,
  columns: readonly Column[],
): string | undefined {
  if (!TABLE_NAME.test(name))
    return "Name the table with lower-case letters, digits and underscores."
  if (!TABLE_NAME.test(database))
    return "Name the database with lower-case letters, digits and underscores."
  if (columns.length === 0) return "A table needs at least one column."
  if (columns.some((c) => !c.Name || !c.Type)) return "Every column needs a name and a type."
  const names = columns.map((c) => c.Name?.toLowerCase())
  const duplicate = names.find((n, i) => names.indexOf(n) !== i)
  if (duplicate) return `Two columns are called ${duplicate}.`
  return undefined
}

/**
 * Everything the wizard knows about the table it is about to create: the
 * prefix's scan and sampled schema (queries), and the reader's edits to the
 * columns, partition key types, name and database (state), seeded from the
 * inference whenever a new sample arrives.
 */
export function useTableDraft(location: string, initialDatabase: string) {
  const folder = tableFolder(location)
  const bucket = folder?.bucket ?? ""
  const prefix = folder?.prefix ?? ""
  const scan = useQuery(gluePrefixScanQueryOptions(bucket, prefix))
  const sample = useQuery(glueSampleSchemaQueryOptions(bucket, scan.data?.sample))
  const databases = useQuery(glueDatabasesQueryOptions())

  const [seededFrom, setSeededFrom] = useState<InferredSchema>()
  const [columns, setColumns] = useState<DraftColumn[]>([])
  const [partitionKeys, setPartitionKeys] = useState<DraftColumn[]>([])
  const [name, setName] = useState<string>()
  const [database, setDatabase] = useState(initialDatabase)
  const [addPartitions, setAddPartitions] = useState(true)

  // A new sample reseeds the editable schema: adjusting state while
  // rendering, not in an effect, so the stale columns never paint.
  if (sample.data && sample.data !== seededFrom) {
    setSeededFrom(sample.data)
    setColumns(sample.data.columns.map((c) => draftColumn(c.Name ?? "", c.Type ?? "string")))
    setPartitionKeys((scan.data?.layout.keys ?? []).map((k) => draftColumn(k, "string")))
  }

  const tableName = name ?? tableIdentifier(prefix.split("/").filter(Boolean).at(-1) ?? bucket)
  const layout = scan.data?.layout
  const draft: TableDraft | undefined = sample.data && {
    name: tableName,
    location: `s3://${bucket}/${prefix}`,
    format: sample.data.format,
    delimiter: sample.data.delimiter,
    quoted: sample.data.quoted,
    columns: toColumns(columns),
    partitionKeys: toColumns(partitionKeys),
  }
  const tableInput = draft && buildTableInput(draft)
  const partitionInputs =
    tableInput && layout && addPartitions
      ? layout.partitions.map((p) => buildPartitionInput(tableInput, p.values, p.location))
      : []

  return {
    folder,
    scan,
    sample,
    databases,
    columns,
    setColumns,
    partitionKeys,
    setPartitionKeys,
    tableName,
    setName,
    database,
    setDatabase,
    addPartitions,
    setAddPartitions,
    tableInput,
    partitionInputs,
    isNewDatabase: !!databases.data && !databases.data.some((d) => d.Name === database),
    problem: draft && draftProblem(tableName, database, [...draft.columns, ...draft.partitionKeys]),
  }
}

export type TableDraftState = ReturnType<typeof useTableDraft>
