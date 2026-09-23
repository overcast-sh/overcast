import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { FileText, Table2 } from "lucide-react"
import { saveDataOn } from "@/lib/data-sources/device-profile"
import { NotTabularError } from "@/lib/data-sources/row-source"
import { openTextSource, type TextRowSource } from "@/lib/data-sources/text-source"
import { createDataWorker } from "@/lib/data-sources/worker-port"
import type { TextKind } from "@/lib/data-sources/worker-protocol"
import { formatBytes, formatQuantity } from "@/lib/format"
import { OBJECT_PREVIEW_WINDOW } from "@/services/api"
import { s3ObjectPreviewQueryOptions } from "../data"
import { kindLabel } from "../preview-kind"
import { useObjectSource, type DataFileProps } from "./data-file-source"
import {
  PreviewNotice,
  PreviewPanel,
  PreviewSkeleton,
  RawText,
  UnreadableObject,
  ViewToggle,
  type ToggleOption,
} from "./data-preview"
import { ObjectDataGrid } from "./object-data-grid"

type TextView = "table" | "raw"

const TEXT_VIEWS = [
  { value: "table", label: "Table", icon: Table2 },
  { value: "raw", label: "Raw", icon: FileText },
] as const satisfies readonly ToggleOption<TextView>[]

const DELIMITER_NAMES: Record<string, string> = {
  ",": "comma",
  "\t": "tab",
  ";": "semicolon",
  "|": "pipe",
}

/**
 * A CSV, TSV or JSON Lines object in the `DataGrid`, with the raw text one
 * click away. The worker indexes the file as it streams; the first rows show
 * before that finishes.
 *
 * A file that does not read as the table its name promised — an unclosed
 * quote, JSON Lines records of different shapes — opens on the raw text with
 * the reason, rather than as a grid that misreads it.
 */
export function TextDataPreview({ kind, ...props }: DataFileProps & { kind: TextKind }) {
  const { bucket, objectKey, versionId, size, gridClassName, viewerLink } = props
  const { source, error, url, reload } = useObjectSource<TextRowSource>(
    props,
    kind,
    (url, signal) =>
      openTextSource({ url, size, kind, port: createDataWorker(), saveData: saveDataOn(), signal }),
  )
  const [view, setView] = useState<TextView>("table")
  const notTabular = error instanceof NotTabularError
  const showRaw = view === "raw" || notTabular
  const raw = useQuery({
    ...s3ObjectPreviewQueryOptions(bucket, objectKey, versionId),
    enabled: showRaw,
  })
  const label = kindLabel(kind)
  const rawMeta = raw.data?.truncated
    ? `raw · first ${OBJECT_PREVIEW_WINDOW} of ${formatBytes(size)}`
    : "raw"
  const rawBody = raw.data ? (
    <RawText text={raw.data.text} language={null} keepLines />
  ) : (
    <PreviewSkeleton />
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

  // Named only when the text overruled the extension: a `.csv` that is
  // really semicolon-separated reads wrongly unless the reader is told why
  // the columns split where they do.
  const sniffed =
    source?.delimiter && source.delimiter !== (kind === "tsv" ? "\t" : ",")
      ? `${DELIMITER_NAMES[source.delimiter]}-separated`
      : undefined
  const meta = !source
    ? "opening"
    : view === "raw"
      ? rawMeta
      : [formatQuantity(source.columns.length, "column"), formatBytes(size), sniffed]
          .filter(Boolean)
          .join(" · ")

  return (
    <PreviewPanel
      format={label}
      meta={meta}
      control={
        <div className="flex items-center gap-2">
          <ViewToggle
            label={`${label} view`}
            value={view}
            options={TEXT_VIEWS}
            onChange={setView}
          />
          {viewerLink}
        </div>
      }
    >
      {view === "raw" ? (
        rawBody
      ) : source ? (
        <ObjectDataGrid
          source={source}
          format={kind}
          delimiter={source.delimiter}
          emptyMessage="No rows below the header."
          onReload={reload}
          file={props}
        />
      ) : (
        <div className={gridClassName}>
          <PreviewSkeleton noun="first rows" />
        </div>
      )}
    </PreviewPanel>
  )
}
