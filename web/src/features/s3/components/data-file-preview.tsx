import { useCallback, useMemo, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useRouter } from "@tanstack/react-router"
import { FileText, ListTree, Maximize2, Table2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DataGrid } from "@/components/data-grid/data-grid"
import type { GridColumn } from "@/components/data-grid/row-source"
import { useRowSource } from "@/components/data-grid/use-row-source"
import {
  memorySource,
  openParquetSource,
  openTextSource,
  type ParquetSource,
  type TextRowSource,
} from "@/lib/data-sources/sources"
import { createDataWorker } from "@/lib/data-sources/worker-port"
import { formatBytes, formatCount } from "@/lib/format"
import { OBJECT_PREVIEW_WINDOW, s3 } from "@/services/api"
import { s3ObjectPreviewQueryOptions } from "../data"
import {
  KIND_LABEL,
  PreviewNotice,
  PreviewPanel,
  PreviewSkeleton,
  RawText,
  UnreadableObject,
  ViewToggle,
  type ToggleOption,
} from "./data-preview"
import { AthenaQuery } from "./athena-query"

/**
 * CSV, TSV, JSON Lines and Parquet in the shared `DataGrid`, for the object
 * inspector and the full-page viewer alike.
 *
 * The grid scrolls the whole file: a text file is indexed in a worker as it
 * streams (the first rows appear before that finishes) and read back a block
 * at a time by Range; a Parquet file reads only the rows and columns in view.
 * See *Large data in the browser* in `docs/plans/data-lake-console.md`.
 */

export type TextDataKind = "csv" | "tsv" | "jsonl"

interface CommonProps {
  bucket: string
  objectKey: string
  versionId?: string
  size: number
  /**
   * The object's ETag when it was opened. Part of the source's identity, so
   * an overwritten object is a new source, never rows stitched from two files.
   */
  etag?: string
  /** The grid's box — a fixed height in the dialog, the rest of the page in the viewer. */
  gridClassName: string
  /** *Open in viewer*, in the dialog. */
  viewerLink?: ReactNode
  initialRow?: number
  onCursorChange?: (row: number) => void
}

/** A counter that, bumped, reopens the source: *File changed — Reload*. */
function useReload(): [number, () => void] {
  const [generation, setGeneration] = useState(0)
  return [generation, () => setGeneration((g) => g + 1)]
}

/** `Save-Data`: the user asked the browser to fetch less, so nothing indexes in the background. */
function saveDataOn(): boolean {
  if (typeof navigator === "undefined") return false
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  return connection?.saveData === true
}

type TextView = "table" | "raw"

const TEXT_VIEWS = [
  { value: "table", label: "Table", icon: Table2 },
  { value: "raw", label: "Raw", icon: FileText },
] as const satisfies readonly ToggleOption<TextView>[]

const DELIMITER_NAME: Record<string, string> = {
  ",": "comma",
  "\t": "tab",
  ";": "semicolon",
  "|": "pipe",
}

/**
 * A CSV, TSV or JSON Lines object as a grid, with the raw text one click
 * away.
 *
 * A file that does not read as the table its name promised — an unclosed
 * quote, JSON Lines records of different shapes — opens on the raw text with
 * the reason, rather than as a grid that misreads it.
 */
export function TextDataPreview({
  kind,
  bucket,
  objectKey,
  versionId,
  size,
  etag,
  gridClassName,
  viewerLink,
  initialRow,
  onCursorChange,
}: CommonProps & { kind: TextDataKind }) {
  const url = s3.getObjectDownloadUrl(bucket, objectKey, versionId)
  const open = useCallback(
    (signal: AbortSignal) =>
      openTextSource({ url, size, kind, port: createDataWorker(), saveData: saveDataOn(), signal }),
    [url, size, kind],
  )
  const [generation, reload] = useReload()
  const { source, error } = useRowSource<TextRowSource>(
    `${kind}:${url}:${size}:${etag ?? ""}:${generation}`,
    open,
  )
  const [view, setView] = useState<TextView>("table")
  const notTabular = !!error && "notTabular" in error
  const showRaw = view === "raw" || notTabular
  const raw = useQuery({
    ...s3ObjectPreviewQueryOptions(bucket, objectKey, versionId),
    enabled: showRaw,
  })
  const label = KIND_LABEL[kind]
  const rawMeta = raw.data?.truncated
    ? `raw · first ${OBJECT_PREVIEW_WINDOW} of ${formatBytes(size)}`
    : "raw"
  const rawBody =
    raw.isLoading || !raw.data ? (
      <PreviewSkeleton />
    ) : (
      <RawText text={raw.data.text} language={null} keepLines />
    )

  if (error && !notTabular) {
    return (
      <PreviewPanel format={label} meta="not readable">
        <UnreadableObject
          title={`Could not read this ${label} file`}
          description={error.message}
          downloadHref={url}
        />
      </PreviewPanel>
    )
  }
  if (notTabular) {
    return (
      <PreviewPanel
        format={label}
        meta={rawMeta}
        notices={<PreviewNotice tone="warning">Shown as text: {error.message}</PreviewNotice>}
      >
        {rawBody}
      </PreviewPanel>
    )
  }

  const delimiter =
    source?.delimiter && source.delimiter !== (kind === "tsv" ? "\t" : ",")
      ? DELIMITER_NAME[source.delimiter]
      : undefined
  const meta = !source
    ? "opening"
    : view === "raw"
      ? rawMeta
      : [
          `${formatCount(source.columns.length)} ${source.columns.length === 1 ? "column" : "columns"}`,
          formatBytes(size),
          // Named only when the text overruled the extension: a `.csv` that
          // is really semicolon-separated reads wrongly unless the user is
          // told why the columns split where they do.
          delimiter && `${delimiter}-separated`,
        ]
          .filter(Boolean)
          .join(" · ")

  return (
    <PreviewPanel
      format={label}
      meta={meta}
      control={
        <div className="flex items-center gap-2">
          <ViewToggle label={`${label} view`} value={view} options={TEXT_VIEWS} onChange={setView} />
          {viewerLink}
        </div>
      }
    >
      {view === "raw" ? (
        rawBody
      ) : !source ? (
        <div className={gridClassName}>
          <PreviewSkeleton noun="first rows" />
        </div>
      ) : (
        <DataGrid
          source={source}
          label={`Rows of ${objectKey}`}
          className={gridClassName}
          initialRow={initialRow}
          onCursorChange={onCursorChange}
          emptyMessage="No rows below the header."
          onReload={reload}
          toolbarEnd={
            <AthenaQuery
              bucket={bucket}
              objectKey={objectKey}
              format={kind}
              columns={source.columns}
              delimiter={source.delimiter}
            />
          }
        />
      )}
    </PreviewPanel>
  )
}

type ParquetView = "rows" | "schema"

const PARQUET_VIEWS = [
  { value: "rows", label: "Rows", icon: Table2 },
  { value: "schema", label: "Schema", icon: ListTree },
] as const satisfies readonly ToggleOption<ParquetView>[]

const SCHEMA_COLUMNS: GridColumn[] = [
  { name: "column", numeric: false },
  { name: "type", numeric: false },
  { name: "nullable", numeric: false },
]

/**
 * A Parquet object's rows and its schema. The footer is read once; every
 * scroll reads only the row groups and the columns in view.
 */
export function ParquetDataPreview({
  bucket,
  objectKey,
  versionId,
  size,
  etag,
  gridClassName,
  viewerLink,
  initialRow,
  onCursorChange,
}: CommonProps) {
  const url = s3.getObjectDownloadUrl(bucket, objectKey, versionId)
  const open = useCallback(
    (signal: AbortSignal) => openParquetSource({ url, size, port: createDataWorker(), signal }),
    [url, size],
  )
  const [generation, reload] = useReload()
  const { source, error } = useRowSource<ParquetSource>(
    `parquet:${url}:${size}:${etag ?? ""}:${generation}`,
    open,
  )
  const [chosen, setChosen] = useState<ParquetView | undefined>(undefined)
  const schema = useMemo(
    () =>
      source
        ? memorySource(
            SCHEMA_COLUMNS,
            source.info.fields.map((f) => [f.name, f.type, f.nullable ? "nullable" : "required"]),
          )
        : null,
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
          <PreviewSkeleton noun="parquet footer" />
        </div>
      </PreviewPanel>
    )
  }

  const { info } = source
  // Rows first; the schema when the rows cannot be read.
  const view = chosen ?? (info.rowsError ? "schema" : "rows")
  const meta = [
    `${formatCount(source.rowCount.value)} ${source.rowCount.value === 1 ? "row" : "rows"}`,
    `${formatCount(source.columns.length)} ${source.columns.length === 1 ? "column" : "columns"}`,
    `${formatCount(info.rowGroups)} row ${info.rowGroups === 1 ? "group" : "groups"}`,
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
          initialRow={initialRow}
          onCursorChange={onCursorChange}
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

/** The inspector's entry point: the grid for whichever tabular format the object is. */
export function DataFilePreview({
  kind,
  ...props
}: CommonProps & { kind: TextDataKind | "parquet" }) {
  return kind === "parquet" ? <ParquetDataPreview {...props} /> : <TextDataPreview kind={kind} {...props} />
}

/**
 * Opens the full-page data viewer, for a file too big to work with in a
 * dialog. A router link inside the app; a plain link where there is no router
 * (a component test renders the dialog on its own).
 */
export function OpenInViewer({
  bucket,
  objectKey,
  versionId,
}: {
  bucket: string
  objectKey: string
  versionId?: string
}) {
  const router = useRouter({ warn: false }) as ReturnType<typeof useRouter> | null
  const label = (
    <>
      <Maximize2 aria-hidden className="h-3.5 w-3.5" />
      <span className="sr-only sm:not-sr-only">Open in viewer</span>
    </>
  )
  const search = new URLSearchParams({ key: objectKey })
  if (versionId !== undefined) search.set("versionId", versionId)
  return (
    <Button asChild variant="ghost" size="sm" title="Open in the full-page data viewer">
      {router ? (
        <Link to="/s3/$bucket/view" params={{ bucket }} search={{ key: objectKey, versionId }}>
          {label}
        </Link>
      ) : (
        <a href={`/s3/${encodeURIComponent(bucket)}/view?${search}`}>{label}</a>
      )}
    </Button>
  )
}
