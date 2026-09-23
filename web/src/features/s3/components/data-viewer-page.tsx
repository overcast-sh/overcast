import { useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowLeft, Download, Table2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { EmptyState, PageHeader } from "@/components/ui/primitives"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatBytes } from "@/lib/format"
import { s3 } from "@/services/api"
import { s3ObjectMetaQueryOptions } from "../data"
import { describeObjectReadError } from "../object-read-error"
import { dataPreviewKind } from "../preview-kind"
import { DataFilePreview } from "./data-file-preview"

/**
 * The full-page data viewer: `/s3/$bucket/view?key=…&row=…`.
 *
 * The same grid as the object inspector, with the whole page to work in — for
 * a file too big to explore in a dialog, and for a link to one row of it:
 * `row` is 1-based, kept in step with the cursor as it moves, and opens the
 * grid on that row.
 */
export function DataViewerPage({
  bucket,
  objectKey,
  versionId,
  row,
}: {
  bucket: string
  objectKey: string
  versionId?: string
  row?: number
}) {
  const navigate = useNavigate()
  const { data: metadata, error, isLoading } = useQuery(
    s3ObjectMetaQueryOptions(bucket, objectKey, versionId),
  )
  const kind = metadata ? dataPreviewKind(metadata.contentType, objectKey) : null
  const tabular = kind === "csv" || kind === "tsv" || kind === "jsonl" || kind === "parquet"
  const objectHref = {
    to: "/s3/$bucket/objects/$" as const,
    params: { bucket, _splat: objectKey },
    search: { versionId },
  }

  // The cursor's row goes into the URL, so the address bar is always a link
  // to what is on screen — debounced, and replacing history, so arrowing
  // down a thousand rows does not leave a thousand Back steps.
  const pending = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(pending.current), [])
  const onCursorChange = (next: number) => {
    window.clearTimeout(pending.current)
    pending.current = window.setTimeout(() => {
      void navigate({
        to: "/s3/$bucket/view",
        params: { bucket },
        search: { key: objectKey, versionId, row: next + 1 },
        replace: true,
      })
    }, 300)
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <PageHeader
        title={objectKey.slice(objectKey.lastIndexOf("/") + 1) || objectKey}
        meta={
          <span className="font-mono text-xs text-fg-muted" title={objectKey}>
            {bucket}/{objectKey}
            {metadata ? ` · ${formatBytes(metadata.contentLength)}` : ""}
          </span>
        }
        actions={
          <>
            <Button asChild variant="ghost" size="sm">
              <Link {...objectHref}>
                <ArrowLeft aria-hidden className="h-3.5 w-3.5" /> Object
              </Link>
            </Button>
            <Button asChild variant="secondary" size="sm">
              <a href={s3.getObjectDownloadUrl(bucket, objectKey, versionId)} download>
                <Download aria-hidden className="h-3.5 w-3.5" /> Download
              </a>
            </Button>
          </>
        }
      />
      {isLoading ? (
        <SkeletonRows rows={8} noun="object" />
      ) : error ? (
        <div
          role="alert"
          className="rounded-lg border border-border bg-bg-muted px-3 py-2 text-sm text-fg-muted"
        >
          {describeObjectReadError(error, versionId)}
        </div>
      ) : metadata && tabular ? (
        <DataFilePreview
          kind={kind}
          bucket={bucket}
          objectKey={objectKey}
          versionId={versionId}
          size={metadata.contentLength}
          etag={metadata.etag}
          // The page's own height, less the header: the grid scrolls, the page does not.
          gridClassName="h-[calc(100dvh-15rem)] min-h-80"
          initialRow={row !== undefined ? Math.max(row - 1, 0) : undefined}
          onCursorChange={onCursorChange}
        />
      ) : (
        <EmptyState
          icon={<Table2 className="h-6 w-6" />}
          title="Not a table"
          description="The data viewer opens CSV, TSV, JSON Lines and Parquet files. This object is something else."
          action={
            <Button asChild variant="secondary" size="sm">
              <Link {...objectHref}>Back to the object</Link>
            </Button>
          }
        />
      )}
    </div>
  )
}
