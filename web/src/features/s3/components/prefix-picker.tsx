import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { ChevronRight, File, Folder } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Combobox } from "@/components/ui/combobox"
import { Input } from "@/components/ui/input"
import { ResourceName } from "@/components/ui/resource-list-page"
import { ResourceTable } from "@/components/ui/resource-table"
import { formatBytes } from "@/lib/format"
import { parseS3Uri } from "@/lib/s3-uri"
import type { S3Bucket } from "@/types"
import { s3BucketsQueryOptions, s3ObjectsQueryOptions } from "../data"

interface PrefixPickerProps {
  /** The chosen prefix as an `s3://bucket/prefix/` URI; `""` before a bucket is picked. */
  value: string
  onChange: (uri: string) => void
}

interface Entry {
  key: string
  folder: boolean
  size?: number
}

function uriOf(bucket: string, prefix: string): string {
  return `s3://${bucket}/${prefix}`
}

/** `a/b/c/` → the prefixes a breadcrumb steps back to: `""`, `a/`, `a/b/`, `a/b/c/`. */
function ancestors(prefix: string): string[] {
  const parts = prefix.split("/").filter(Boolean)
  return ["", ...parts.map((_, i) => `${parts.slice(0, i + 1).join("/")}/`)]
}

function lastSegment(prefix: string): string {
  return prefix.split("/").filter(Boolean).at(-1) ?? ""
}

/**
 * Picks an S3 prefix: a bucket, then folders, the way the object browser
 * walks them — or an `s3://` URI pasted straight in. Objects are listed
 * beside the folders (muted) so the reader can see there is data where they
 * are standing.
 */
export function PrefixPicker({ value, onChange }: PrefixPickerProps) {
  const location = parseS3Uri(value)
  const bucket = location?.bucket ?? ""
  // A half-typed name lists the folder it is in, as the object browser's search does.
  const prefix = (location?.key ?? "").replace(/[^/]*$/, "")
  const buckets = useQuery(s3BucketsQueryOptions())
  // Nothing is listed until the text names a bucket that exists, so typing
  // `s3://la…` does not list (and fail on) every prefix of a name on the way.
  const known = !!buckets.data?.some((b) => b.name === bucket)
  const listing = useInfiniteQuery({
    ...s3ObjectsQueryOptions(bucket, prefix),
    enabled: known,
  })
  const pages = listing.data?.pages ?? []
  const entries: Entry[] = [
    ...pages.flatMap((p) => p.prefixes.map((f) => ({ key: f.prefix, folder: true }))),
    ...pages.flatMap((p) => p.objects.map((o) => ({ key: o.key, folder: false, size: o.size }))),
  ]

  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-3 @lg:grid-cols-[12rem_1fr]">
        <Combobox<S3Bucket>
          aria-label="Bucket"
          placeholder="Bucket"
          items={buckets.data ?? []}
          isLoading={buckets.isLoading}
          value={bucket}
          onChange={(name) => onChange(uriOf(name, ""))}
          getItemValue={(b) => b.name}
          filterFn={(b, q) => b.name.toLowerCase().includes(q.toLowerCase())}
          renderItem={(b) => <span className="font-mono text-xs">{b.name}</span>}
          emptyMessage="No buckets"
        />
        <Input
          aria-label="S3 location"
          placeholder="s3://bucket/prefix/"
          className="font-mono text-xs"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
      {known && (
        <nav aria-label="Folder" className="flex flex-wrap items-center gap-1 font-mono text-xs">
          {ancestors(prefix).map((p, i) => (
            <span key={p} className="flex items-center gap-1">
              {i > 0 && <ChevronRight aria-hidden className="size-3 text-fg-subtle" />}
              <button
                type="button"
                className="text-accent hover:underline"
                onClick={() => onChange(uriOf(bucket, p))}
              >
                {i === 0 ? bucket : lastSegment(p)}
              </button>
            </span>
          ))}
        </nav>
      )}
      {known && (
        <div className="max-h-72 overflow-y-auto rounded-card border border-border">
          <ResourceTable<Entry>
            variant="embedded"
            query={{ data: entries, isLoading: listing.isLoading, error: listing.error }}
            noun="objects"
            loadingCount={4}
            rowKey={(e) => e.key}
            onRowClick={(e) => e.folder && onChange(uriOf(bucket, e.key))}
            rowClassName={(e) => (e.folder ? undefined : "text-fg-subtle")}
            emptyIcon={Folder}
            emptyTitle="Nothing here"
            emptyDescription="This prefix holds no objects."
            columns={[
              {
                id: "name",
                header: "Name",
                cell: (e) => (
                  <ResourceName icon={e.folder ? Folder : File} name={e.key.slice(prefix.length)} />
                ),
              },
              {
                id: "size",
                header: "Size",
                headerClassName: "text-right",
                cellClassName: "text-right tabular-nums",
                cell: (e) => (e.size === undefined ? "" : formatBytes(e.size)),
              },
            ]}
          />
          {listing.hasNextPage && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="m-1"
              busy={listing.isFetchingNextPage}
              busyLabel="Loading"
              onClick={() => void listing.fetchNextPage()}
            >
              Load more
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
