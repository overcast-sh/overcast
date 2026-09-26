import { useInfiniteQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { File } from "lucide-react"
import { LINK_CLASS } from "@/components/ui/arn-link"
import { Button } from "@/components/ui/button"
import { ResourceTable } from "@/components/ui/resource-table"
import { s3ObjectsQueryOptions } from "@/features/s3/data"
import { formatBytes, formatCount, formatDate } from "@/lib/format"
import { parseS3Uri } from "@/lib/s3-uri"
import type { S3Object } from "@/types"

/**
 * The objects under a table's location, recursively — partitions' files
 * included — each linking to the S3 browser's preview of it.
 */
export function LocationObjects({ location }: { location: string }) {
  const parsed = parseS3Uri(location)
  const bucket = parsed?.bucket ?? ""
  const prefix = parsed?.key ?? ""
  const listing = useInfiniteQuery({
    ...s3ObjectsQueryOptions(bucket, prefix, "recursive"),
    enabled: bucket !== "",
  })
  const objects = listing.data?.pages.flatMap((p) => p.objects)

  return (
    <div className="flex flex-col gap-2">
      <ResourceTable<S3Object>
        query={{ data: objects, isLoading: listing.isLoading, error: listing.error }}
        noun="objects"
        rowKey={(o) => o.key}
        defaultSort={{ id: "key", desc: false }}
        emptyIcon={File}
        emptyTitle="No objects"
        emptyDescription={`Nothing is stored under ${location} yet.`}
        columns={[
          {
            id: "key",
            header: "Object",
            interactive: true,
            sortValue: (o) => o.key,
            cell: (o) => (
              <Link
                to="/s3/$bucket/objects/$"
                params={{ bucket, _splat: o.key }}
                className={LINK_CLASS}
              >
                {o.key.slice(prefix.length)}
              </Link>
            ),
          },
          {
            id: "size",
            header: "Size",
            headerClassName: "text-right",
            cellClassName: "text-right tabular-nums",
            sortValue: (o) => o.size,
            cell: (o) => formatBytes(o.size),
          },
          {
            id: "modified",
            header: "Modified",
            sortValue: (o) => (o.lastModified ? new Date(o.lastModified) : undefined),
            cell: (o) => formatDate(o.lastModified),
          },
        ]}
      />
      {listing.hasNextPage && (
        <Button
          size="sm"
          variant="ghost"
          className="w-fit"
          busy={listing.isFetchingNextPage}
          busyLabel="Loading"
          onClick={() => void listing.fetchNextPage()}
        >
          Showing the first {formatCount(objects?.length ?? 0)} objects · Load more
        </Button>
      )}
    </div>
  )
}
