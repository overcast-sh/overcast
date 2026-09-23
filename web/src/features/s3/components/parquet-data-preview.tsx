import { useMemo, useState } from "react"
import { ListTree, Table2 } from "lucide-react"
import { DataGrid } from "@/components/data-grid/data-grid"
import { memorySource } from "@/lib/data-sources/memory-source"
import { openParquetSource, type ParquetRowSource } from "@/lib/data-sources/parquet-source"
import type { DataColumn } from "@/lib/data-sources/row-source"
import { createDataWorker } from "@/lib/data-sources/worker-port"
import { formatQuantity } from "@/lib/format"
import { AthenaQuery } from "./athena-query"
import { useObjectSource, type DataFileProps } from "./data-file-source"
import {
  PreviewPanel,
  PreviewSkeleton,
  UnreadableObject,
  ViewToggle,
  type ToggleOption,
} from "./data-preview"

type ParquetView = "rows" | "schema"

const PARQUET_VIEWS = [
  { value: "rows", label: "Rows", icon: Table2 },
  { value: "schema", label: "Schema", icon: ListTree },
] as const satisfies readonly ToggleOption<ParquetView>[]

const SCHEMA_COLUMNS: DataColumn[] = [
  { name: "column", numeric: false },
  { name: "type", numeric: false },
  { name: "nullable", numeric: false },
]

/**
 * A Parquet object's rows and its schema, in the `DataGrid`. The footer is
 * read once; every scroll reads only the row groups and the columns in view.
 * Rows open first, or the schema when the rows use a codec the console does
 * not decode.
 */
export function ParquetDataPreview(props: DataFileProps) {
  const { objectKey, bucket, size, gridClassName, viewerLink } = props
  const { source, error, url, reload } = useObjectSource<ParquetRowSource>(
    props,
    "parquet",
    (url, signal) => openParquetSource({ url, size, port: createDataWorker(), signal }),
  )
  const [chosen, setChosen] = useState<ParquetView | undefined>(undefined)
  const schema = useMemo(
    () =>
      source &&
      memorySource(
        SCHEMA_COLUMNS,
        source.info.fields.map((f) => [f.name, f.type, f.nullable ? "nullable" : "required"]),
      ),
    [source],
  )

  if (error) {
    return (
      <PreviewPanel format="Parquet" meta="not readable">
        <UnreadableObject
          title="Could not read this file as Parquet"
          description={parquetErrorMessage(error)}
          downloadHref={url}
        />
      </PreviewPanel>
    )
  }
  if (!source || !schema) {
    return (
      <PreviewPanel format="Parquet" meta="reading footer">
        <div className={gridClassName}>
          <PreviewSkeleton noun="Parquet footer" />
        </div>
      </PreviewPanel>
    )
  }

  const { info } = source
  const view = chosen ?? (info.rowsError ? "schema" : "rows")
  const meta = [
    formatQuantity(source.rowCount.value, "row"),
    formatQuantity(source.columns.length, "column"),
    formatQuantity(info.rowGroups, "row group"),
    info.codecs.join(", ").toLowerCase(),
  ]
    .filter(Boolean)
    .join(" · ")

  return (
    <PreviewPanel
      format="Parquet"
      meta={meta}
      control={
        <div className="flex items-center gap-2">
          <ViewToggle
            label="Parquet view"
            value={view}
            options={PARQUET_VIEWS}
            onChange={setChosen}
          />
          {viewerLink}
        </div>
      }
    >
      {view === "schema" ? (
        <DataGrid source={schema} label="Parquet schema" className={gridClassName} />
      ) : info.rowsError ? (
        <div className={gridClassName}>
          <UnreadableObject
            title="Rows not previewed"
            description={`${info.rowsError} Switch to Schema for the columns.`}
            downloadHref={url}
          />
        </div>
      ) : (
        <DataGrid
          source={source}
          label={`Rows of ${objectKey}`}
          className={gridClassName}
          initialRow={props.initialRow}
          onCursorChange={props.onCursorChange}
          emptyMessage="The file has a schema and no rows."
          onReload={reload}
          toolbarEnd={
            <AthenaQuery
              bucket={bucket}
              objectKey={objectKey}
              format="parquet"
              columns={source.columns}
            />
          }
        />
      )}
    </PreviewPanel>
  )
}

/** hyparquet's messages are for its own developers; the one people hit gets a sentence. */
function parquetErrorMessage(error: Error): string {
  if (/PAR1/.test(error.message)) {
    return "It does not end with Parquet's footer marker, so it is not a Parquet file, or it was cut short while being written."
  }
  return error.message
}
