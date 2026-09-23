import { useQuery } from "@tanstack/react-query"
import { useDebouncedCallback } from "@tanstack/react-pacer"
import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowLeft, Download, Table2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { EmptyState, PageHeader } from "@/components/ui/primitives"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatBytes } from "@/lib/format"
import { s3 } from "@/services/api"
import { s3ObjectMetaQueryOptions } from "../data"
import { dataPreviewKind, isTabularKind } from "../preview-kind"
import { DataFilePreview } from "./data-file-preview"
import { ObjectReadError } from "./data-preview"

/** How long the cursor rests on a row before the URL follows it. */
const ROW_URL_DELAY_MS = 300

/**
 * The full-page data viewer: `/s3/$bucket/view?key=…&row=…`.
 *
 * The same grid as the object inspector, with the whole page to work in — for
 * a file too big to explore in a dialog, and for a link to one row of it:
 * `row` is 1-based, follows the cursor, and opens the grid on that row.
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
  const {
    data: metadata,
    error,
    isLoading,
  } = useQuery(s3ObjectMetaQueryOptions(bucket, objectKey, versionId))
  const kind = metadata ? dataPreviewKind(metadata.contentType, objectKey) : null
  const objectLink = {
    to: "/s3/$bucket/objects/$" as const,
    params: { bucket, _splat: objectKey },
    search: { versionId },
  }

  // The address bar stays a link to the row under the cursor — once the
  // cursor rests, and replacing history, so arrowing down a thousand rows
  // does not leave a thousand Back steps.
  const followCursor = useDebouncedCallback(
    (next: number) =>
      void navigate({
        to: "/s3/$bucket/view",
        params: { bucket },
        search: { key: objectKey, versionId, row: next + 1 },
        replace: true,
      }),
    { wait: ROW_URL_DELAY_MS },
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={objectKey.slice(objectKey.lastIndexOf("/") + 1) || objectKey}
        meta={
          <span className="font-mono text-xs break-all text-fg-muted">
            {bucket}/{objectKey}
            {metadata && ` · ${formatBytes(metadata.contentLength)}`}
          </span>
        }
        actions={
          <>
            <Button asChild variant="ghost" size="sm">
              <Link {...objectLink}>
                <ArrowLeft aria-hidden className="size-3.5" /> Object
              </Link>
            </Button>
            <Button asChild variant="secondary" size="sm">
              <a href={s3.getObjectDownloadUrl(bucket, objectKey, versionId)} download>
                <Download aria-hidden className="size-3.5" /> Download
              </a>
            </Button>
          </>
        }
      />
      {isLoading ? (
        <SkeletonRows rows={8} noun="object" />
      ) : error ? (
        <ObjectReadError error={error} versionId={versionId} />
      ) : metadata && isTabularKind(kind) ? (
        <DataFilePreview
          kind={kind}
          bucket={bucket}
          objectKey={objectKey}
          versionId={versionId}
          size={metadata.contentLength}
          etag={metadata.etag}
          // The rest of the viewport under the page header: the grid
          // scrolls, the page does not.
          gridClassName="h-[calc(100dvh-14rem)] min-h-96"
          initialRow={row === undefined ? undefined : row - 1}
          onCursorChange={followCursor}
        />
      ) : (
        <EmptyState
          icon={<Table2 className="size-6" />}
          title="Not a table"
          description="The data viewer opens CSV, TSV, JSON Lines and Parquet files. This object is something else."
          action={
            <Button asChild variant="secondary" size="sm">
              <Link {...objectLink}>Back to the object</Link>
            </Button>
          }
        />
      )}
    </div>
  )
}
