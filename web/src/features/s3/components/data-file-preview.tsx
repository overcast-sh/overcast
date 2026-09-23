import { Link } from "@tanstack/react-router"
import { Maximize2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import type { TabularKind } from "../preview-kind"
import type { DataFileProps } from "./data-file-source"
import { ParquetDataPreview } from "./parquet-data-preview"
import { TextDataPreview } from "./text-data-preview"

/**
 * A CSV, TSV, JSON Lines or Parquet object in the shared `DataGrid`, for the
 * object inspector and the full-page viewer alike. The grid scrolls the whole
 * file: text is indexed in a worker as it streams and read back a block at a
 * time by Range; Parquet reads only the rows and columns in view. See *Large
 * data in the browser* in `docs/plans/data-lake-console.md`.
 */
export function DataFilePreview({ kind, ...props }: DataFileProps & { kind: TabularKind }) {
  return kind === "parquet" ? (
    <ParquetDataPreview {...props} />
  ) : (
    <TextDataPreview kind={kind} {...props} />
  )
}

/** Opens the full-page data viewer, for a file too big to work with in a dialog. */
export function OpenInViewer({
  bucket,
  objectKey,
  versionId,
}: {
  bucket: string
  objectKey: string
  versionId?: string
}) {
  return (
    <Button asChild variant="ghost" size="sm" title="Open in the full-page data viewer">
      <Link to="/s3/$bucket/view" params={{ bucket }} search={{ key: objectKey, versionId }}>
        <Maximize2 aria-hidden className="size-3.5" />
        <span className="sr-only sm:not-sr-only">Open in viewer</span>
      </Link>
    </Button>
  )
}
