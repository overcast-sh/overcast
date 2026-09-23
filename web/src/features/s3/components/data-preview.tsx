import { useMemo, useState, type ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { AlertTriangle, Download, FileText, Info, ListTree, Table2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { EmptyState } from "@/components/ui/primitives"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatBytes, formatCount } from "@/lib/format"
import { OBJECT_PREVIEW_WINDOW } from "@/services/api"
import { cn } from "@/lib/utils"
import { s3ObjectParquetPreviewQueryOptions } from "../data"
import { delimitedTable, type Delimiter } from "../preview-delimited"
import { jsonlTable } from "../preview-jsonl"
import type { DataPreviewKind } from "../preview-kind"
import type { ParquetPreview } from "../preview-parquet"
import { PREVIEW_ROW_LIMIT, describeRowCount, type PreviewTableModel } from "../preview-table"
import { DataPreviewTable } from "./data-preview-table"

/**
 * The S3 inspector's data-file previews: CSV, TSV and JSON Lines as a table
 * with a raw toggle, Parquet as its first rows and schema, and Avro as an
 * honest "not previewed". Plain text and JSON keep the dialog's own preview,
 * framed by the same `PreviewPanel`.
 */

const KIND_LABEL: Record<DataPreviewKind, string> = {
  csv: "CSV",
  tsv: "TSV",
  jsonl: "JSON Lines",
  parquet: "Parquet",
  avro: "Avro",
  "iceberg-metadata": "Iceberg metadata",
}

// ─── Frame ────────────────────────────────────────────────────────────────

/**
 * One frame for every preview: a header strip naming what is shown and how
 * much of it, an optional control on the right, notes about what was left
 * out, then the body. The body owns its own scrolling.
 */
export function PreviewPanel({
  format,
  meta,
  control,
  notices,
  children,
  className,
}: {
  /** The format, as a badge — "CSV", "Parquet". Omit for plain text. */
  format?: string
  /** What is on screen and how much of the object it is. */
  meta?: ReactNode
  control?: ReactNode
  notices?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    // The card surface, flat: it sits inside a dialog that already floats.
    <Card
      role="region"
      aria-label={format ? `${format} preview` : "Preview"}
      className={cn("flex min-h-0 flex-col overflow-hidden shadow-none", className)}
    >
      <div className="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border bg-bg-muted px-3 py-2">
        {format && <Badge variant="outline">{format}</Badge>}
        <span className="min-w-0 font-mono text-2xs text-fg-muted">{meta}</span>
        {control && <div className="ml-auto">{control}</div>}
      </div>
      {notices}
      <div className="flex min-h-0 flex-1 flex-col">{children}</div>
    </Card>
  )
}

/**
 * A note under the panel's header — what the preview left out and why.
 * `info` for the expected limits (the byte cap), `warning` for a file that
 * did not read the way its name promised.
 */
export function PreviewNotice({
  tone = "info",
  children,
}: {
  tone?: "info" | "warning"
  children: ReactNode
}) {
  const Icon = tone === "warning" ? AlertTriangle : Info
  return (
    <p
      className={cn(
        "flex items-start gap-2 border-b border-border px-3 py-2 text-xs text-fg-muted",
        tone === "warning" ? "bg-warning-muted" : "bg-bg-elevated",
      )}
    >
      <Icon
        aria-hidden
        className={cn(
          "mt-px h-3.5 w-3.5 shrink-0",
          tone === "warning" ? "text-warning" : "text-fg-subtle",
        )}
      />
      <span>{children}</span>
    </p>
  )
}

interface ToggleOption<T extends string> {
  value: T
  label: string
  icon: typeof Table2
}

/**
 * Two or three views of one object, as a segmented control. Each segment is
 * a real button with `aria-pressed`, so Tab reaches it, Space and Enter work,
 * and a screen reader hears which view is on.
 */
export function ViewToggle<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: readonly ToggleOption<T>[]
  onChange: (value: T) => void
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="flex items-center gap-0.5 rounded-control border border-border bg-bg-elevated p-0.5"
    >
      {options.map((option) => {
        const Icon = option.icon
        const pressed = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={pressed}
            onClick={() => onChange(option.value)}
            className={cn(
              "inline-flex h-6 cursor-pointer items-center gap-1.5 rounded-sm px-2 font-mono text-2xs transition-colors",
              "focus-visible:outline-2 focus-visible:outline-accent",
              pressed ? "bg-accent-muted text-accent" : "text-fg-muted hover:text-fg",
            )}
          >
            <Icon aria-hidden className="h-3.5 w-3.5" strokeWidth={1.9} />
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

/** The raw text of an object, as the dialog has always shown it. */
export function RawText({
  text,
  language,
  keepLines = language != null,
}: {
  text: string
  language: string | null
  /**
   * Keep each line on one line and scroll sideways. The default follows the
   * language; a CSV or JSON Lines file has none but is still line-shaped —
   * one record per line — and wrapping it would blur where records end.
   */
  keepLines?: boolean
}) {
  return (
    <HighlightedCode
      text={text}
      language={language}
      className={cn(
        "max-h-[55vh] min-h-0 overflow-auto bg-bg-muted p-3 font-mono text-xs leading-relaxed text-fg",
        // Policy, not accident (kept from the dialog's first commit): a
        // document we chose a language for is code — it keeps its line shape
        // and scrolls horizontally — while arbitrary plain text wraps. The
        // too-large-to-format case (a 1 MiB minified bundle) has no language
        // and so wraps, where it would otherwise be one mile-wide line.
        keepLines ? "whitespace-pre" : "wrap-break-word whitespace-pre-wrap",
      )}
    />
  )
}

/** The preview body while bytes are on their way: static, never a spinner. */
export function PreviewSkeleton({ noun = "preview" }: { noun?: string }) {
  return <SkeletonRows rows={6} noun={noun} />
}

// ─── CSV, TSV, JSON Lines ──────────────────────────────────────────────────

type TextView = "table" | "raw"

type TabularResult =
  | { ok: true; table: PreviewTableModel; clippedFields: number; delimiter?: Delimiter }
  | { ok: false; reason: string }

const TEXT_VIEWS = [
  { value: "table", label: "Table", icon: Table2 },
  { value: "raw", label: "Raw", icon: FileText },
] as const satisfies readonly ToggleOption<TextView>[]

/**
 * A delimited or JSON Lines object as a table of its first rows, with the raw
 * text one click away.
 *
 * A file that does not parse the way its name promised — an unclosed quote,
 * a line that is not JSON, records of different shapes — is not an error: it
 * opens on the raw text, with a note saying why the table is not offered.
 * The parse never throws, so the dialog always has something to show.
 */
export function TabularTextPreview({
  kind,
  text,
  truncated,
  objectBytes,
}: {
  kind: "csv" | "tsv" | "jsonl"
  text: string
  /** `text` is the opening window of a longer object. */
  truncated: boolean
  objectBytes: number
}) {
  const result = useMemo((): TabularResult => {
    if (kind === "jsonl") {
      const jsonl = jsonlTable(text, { truncated, objectBytes })
      return jsonl.ok ? { ...jsonl, clippedFields: 0 } : jsonl
    }
    return delimitedTable(text, { preferred: kind === "tsv" ? "\t" : ",", truncated, objectBytes })
  }, [kind, text, truncated, objectBytes])
  const [view, setView] = useState<TextView>("table")
  const label = KIND_LABEL[kind]
  const readWindow = truncated
    ? `first ${OBJECT_PREVIEW_WINDOW} of ${formatBytes(objectBytes)}`
    : ""

  if (!result.ok) {
    return (
      <PreviewPanel
        format={label}
        meta={truncated ? `raw · ${readWindow}` : "raw"}
        notices={<PreviewNotice tone="warning">Shown as text: {result.reason}</PreviewNotice>}
      >
        <RawText text={text} language={null} keepLines />
      </PreviewPanel>
    )
  }

  const { table, clippedFields: clipped, delimiter } = result
  // Named only when the text overruled the extension: a `.csv` that is
  // really semicolon-separated reads wrongly unless the user is told why the
  // columns split where they do.
  const sniffed =
    delimiter && delimiter !== (kind === "tsv" ? "\t" : ",") ? delimiterName(delimiter) : undefined
  const meta =
    view === "table"
      ? [describeRowCount(table), columnCount(table), sniffed && `${sniffed}-separated`]
          .filter(Boolean)
          .join(" · ")
      : ["raw", readWindow].filter(Boolean).join(" · ")

  return (
    <PreviewPanel
      format={label}
      meta={meta}
      control={
        <ViewToggle label={`${label} view`} value={view} options={TEXT_VIEWS} onChange={setView} />
      }
      notices={
        view === "table" && (
          <>
            {table.truncatedByBytes && (
              <PreviewNotice>
                Read from the first {OBJECT_PREVIEW_WINDOW} of this {formatBytes(objectBytes)}{" "}
                object, so the row total is an estimate. Download the file for all of it.
              </PreviewNotice>
            )}
            {clipped > 0 && (
              <PreviewNotice>
                {clipped === 1 ? "One field is" : `${formatCount(clipped)} fields are`} longer than
                the preview keeps and {clipped === 1 ? "is" : "are"} cut short.
              </PreviewNotice>
            )}
          </>
        )
      }
    >
      {view === "table" ? (
        <DataPreviewTable
          table={table}
          label={`First rows of the ${label} file`}
          emptyMessage="No rows below the header."
        />
      ) : (
        <RawText text={text} language={null} keepLines />
      )}
    </PreviewPanel>
  )
}

function delimiterName(delimiter: string): string {
  return { ",": "comma", "\t": "tab", ";": "semicolon", "|": "pipe" }[delimiter] ?? delimiter
}

function columnCount(table: PreviewTableModel): string {
  const shown = table.columns.length
  const total = shown + table.hiddenColumns
  const noun = total === 1 ? "column" : "columns"
  return table.hiddenColumns > 0
    ? `${formatCount(shown)} of ${formatCount(total)} ${noun}`
    : `${formatCount(total)} ${noun}`
}

// ─── Parquet ───────────────────────────────────────────────────────────────

type ParquetView = "rows" | "schema"

const PARQUET_VIEWS = [
  { value: "rows", label: "Rows", icon: Table2 },
  { value: "schema", label: "Schema", icon: ListTree },
] as const satisfies readonly ToggleOption<ParquetView>[]

/**
 * A Parquet object's first rows and its schema. Read by range — the footer,
 * then the first row group — so the object's size does not matter; see
 * `features/s3/preview-parquet.ts`.
 */
export function ParquetObjectPreview({
  bucket,
  objectKey,
  versionId,
  size,
  downloadHref,
}: {
  bucket: string
  objectKey: string
  versionId?: string
  size: number
  downloadHref: string
}) {
  const { data, error, isLoading } = useQuery(
    s3ObjectParquetPreviewQueryOptions(bucket, objectKey, versionId, size),
  )
  const [chosen, setChosen] = useState<ParquetView | undefined>(undefined)

  if (isLoading) {
    return (
      <PreviewPanel format="Parquet" meta="reading footer">
        <PreviewSkeleton noun="parquet footer" />
      </PreviewPanel>
    )
  }
  if (error || !data) {
    return (
      <PreviewPanel format="Parquet" meta="not readable">
        <UnreadableObject
          title="Could not read this file as Parquet"
          description={
            error instanceof Error
              ? parquetErrorMessage(error)
              : "The preview could not be loaded. The file itself is untouched."
          }
          downloadHref={downloadHref}
        />
      </PreviewPanel>
    )
  }

  // Rows first; the schema is the fallback when there are no rows to show.
  const view = chosen ?? (data.table ? "rows" : "schema")
  const rowsPart =
    view === "rows" && data.table
      ? describeRowCount(data.table)
      : `${formatCount(data.numRows)} ${data.numRows === 1 ? "row" : "rows"}`
  const meta = [
    rowsPart,
    `${formatCount(data.fields.length)} ${data.fields.length === 1 ? "column" : "columns"}`,
    `${formatCount(data.rowGroups)} row ${data.rowGroups === 1 ? "group" : "groups"}`,
    data.codecs.join(", ").toLowerCase(),
  ]
    .filter(Boolean)
    .join(" · ")
  // The table stops short of the row limit because the first row group
  // does, not because the file does — worth one line, since "first 3 rows
  // of 5" otherwise reads like a bug.
  const stoppedAtGroup =
    !!data.table &&
    data.table.rows.length < PREVIEW_ROW_LIMIT &&
    data.numRows > data.table.rows.length

  return (
    <PreviewPanel
      format="Parquet"
      meta={meta}
      control={
        <ViewToggle
          label="Parquet view"
          value={view}
          options={PARQUET_VIEWS}
          onChange={setChosen}
        />
      }
      notices={
        view === "rows" &&
        stoppedAtGroup && (
          <PreviewNotice>
            Rows come from the first row group only; the footer and that group are all that was
            read.
          </PreviewNotice>
        )
      }
    >
      {view === "schema" ? (
        <DataPreviewTable table={schemaTable(data)} label="Parquet schema" />
      ) : data.table ? (
        <DataPreviewTable
          table={data.table}
          label="First rows of the Parquet file"
          emptyMessage="The file has a schema and no rows."
        />
      ) : (
        <UnreadableObject
          title="Rows not previewed"
          description={data.rowsError ?? "The rows could not be read."}
          downloadHref={downloadHref}
          secondary="Switch to Schema for the columns, which come from the footer."
        />
      )}
    </PreviewPanel>
  )
}

/** The schema as a table of its own: one row per column. */
function schemaTable(data: ParquetPreview): PreviewTableModel {
  return {
    columns: [
      { name: "column", numeric: false },
      { name: "type", numeric: false },
      { name: "nullable", numeric: false },
    ],
    rows: data.fields.map((f) => [f.name, f.type, f.nullable ? "nullable" : "required"]),
    totalRows: data.fields.length,
    totalIsEstimate: false,
    truncatedByBytes: false,
    hiddenColumns: 0,
  }
}

/** hyparquet's messages are for its own developers; the one people hit gets a sentence. */
function parquetErrorMessage(error: Error): string {
  if (/PAR1/.test(error.message)) {
    return "It does not end with Parquet's footer marker, so it is not a Parquet file, or it was cut short while being written."
  }
  return error.message
}

// ─── Avro and other unreadable objects ─────────────────────────────────────

/**
 * Avro is binary, and the console does not decode it. Saying so beats the
 * generic "no preview" line: an Iceberg table's manifest list and manifests
 * are Avro, so a developer opening one is mid-investigation and needs to know
 * this is a limit of the console, not a broken file.
 */
export function AvroNotice({ downloadHref }: { downloadHref: string }) {
  return (
    <PreviewPanel format="Avro" meta="binary">
      <UnreadableObject
        title="Avro — not previewed"
        description="The console does not decode Avro. Iceberg manifests and manifest lists are Avro files; download this one to read it with avro-tools."
        downloadHref={downloadHref}
      />
    </PreviewPanel>
  )
}

function UnreadableObject({
  title,
  description,
  secondary,
  downloadHref,
}: {
  title: string
  description: string
  secondary?: string
  downloadHref: string
}) {
  return (
    <EmptyState
      className="px-6 py-10"
      title={title}
      description={secondary ? `${description} ${secondary}` : description}
      action={
        <Button asChild variant="secondary" size="sm">
          <a href={downloadHref} download>
            <Download aria-hidden className="h-3.5 w-3.5" /> Download instead
          </a>
        </Button>
      }
    />
  )
}
