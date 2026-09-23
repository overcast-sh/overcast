import { DataGrid } from "@/components/data-grid/data-grid"
import type { RowSource } from "@/lib/data-sources/row-source"
import type { TabularKind } from "../preview-kind"
import { AthenaQuery } from "./athena-query"
import type { DataFileProps } from "./data-file-source"

/**
 * An S3 object's rows in the `DataGrid`, as every data-file preview shows
 * them: named for the object, deep-linkable, reloadable when the object
 * changes, and with *Query with Athena* for what the grid will not do.
 */
export function ObjectDataGrid({
  source,
  format,
  delimiter,
  emptyMessage,
  onReload,
  file,
}: {
  source: RowSource
  format: TabularKind
  /** The delimiter the text chose, for CSV and TSV. */
  delimiter?: string
  /** What the file says when it has no rows. */
  emptyMessage: string
  onReload: () => void
  file: DataFileProps
}) {
  return (
    <DataGrid
      source={source}
      label={`Rows of ${file.objectKey}`}
      className={file.gridClassName}
      initialRow={file.initialRow}
      onCursorChange={file.onCursorChange}
      emptyMessage={emptyMessage}
      onReload={onReload}
      toolbarEnd={
        <AthenaQuery
          bucket={file.bucket}
          objectKey={file.objectKey}
          format={format}
          columns={source.columns}
          delimiter={delimiter}
        />
      }
    />
  )
}
