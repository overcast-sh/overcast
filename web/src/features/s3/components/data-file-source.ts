import { useCallback, useState, type ReactNode } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { useRowSource, type RowSourceState } from "@/components/data-grid/use-row-source"
import type { RowSource } from "@/lib/data-sources/row-source"
import { s3 } from "@/services/api"
import { s3Keys } from "../data"

/** What every data-file preview takes, in the object inspector and the full-page viewer alike. */
export interface DataFileProps {
  bucket: string
  objectKey: string
  versionId?: string
  size: number
  /**
   * The object's ETag when it was opened. Part of the source's identity, so
   * an overwritten object is a new source, never rows stitched from two files.
   */
  etag?: string
  /** The grid's box: a fixed height in the dialog, the rest of the page in the viewer. */
  gridClassName: string
  /** *Open in viewer*, in the dialog. */
  viewerLink?: ReactNode
  initialRow?: number
  onCursorChange?: (row: number) => void
}

export interface ObjectSource<S extends RowSource> extends RowSourceState<S> {
  /** The object's download URL, which every range read goes to. */
  url: string
  /**
   * Opens the source again: *File changed — Reload*. The object's metadata
   * is fetched afresh first, so the new file is read at its new size.
   */
  reload: () => void
}

/**
 * A row source over one S3 object, opened for as long as the preview shows
 * that object at that size and ETag, and opened afresh by `reload`.
 */
export function useObjectSource<S extends RowSource>(
  { bucket, objectKey, versionId, size, etag }: DataFileProps,
  format: string,
  open: (url: string, signal: AbortSignal) => Promise<S>,
): ObjectSource<S> {
  const url = s3.getObjectDownloadUrl(bucket, objectKey, versionId)
  const queryClient = useQueryClient()
  const [generation, setGeneration] = useState(0)
  const reload = useCallback(() => {
    void queryClient
      .refetchQueries({ queryKey: s3Keys.objectMeta(bucket, objectKey, versionId), exact: true })
      .finally(() => setGeneration((g) => g + 1))
  }, [queryClient, bucket, objectKey, versionId])
  const state = useRowSource<S>(`${format}:${url}:${size}:${etag ?? ""}:${generation}`, (signal) =>
    open(url, signal),
  )
  return { ...state, url, reload }
}
