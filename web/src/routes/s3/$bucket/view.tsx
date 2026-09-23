/**
 * The full-page data viewer for one object: `?key=` names it, `?row=` (1-based)
 * opens the grid on a row, and `?versionId=` addresses a stored revision.
 */
import { createFileRoute } from "@tanstack/react-router"
import { DataViewerPage } from "@/features/s3/components/data-viewer-page"

export interface DataViewerSearch {
  key: string
  row?: number
  versionId?: string
}

export const Route = createFileRoute("/s3/$bucket/view")({
  head: ({ params }) => ({ meta: [{ title: `Data viewer — ${params.bucket} — S3 — Overcast` }] }),
  validateSearch: (search: Record<string, unknown>): DataViewerSearch => {
    const row = Number(search.row)
    return {
      key: typeof search.key === "string" ? search.key : "",
      row: Number.isInteger(row) && row > 0 ? row : undefined,
      versionId: typeof search.versionId === "string" ? search.versionId : undefined,
    }
  },
  component: ViewerRoute,
})

function ViewerRoute() {
  const { bucket } = Route.useParams()
  const { key, row, versionId } = Route.useSearch()
  return (
    // Keyed on the object, so a new key opens fresh rather than at the old row.
    <DataViewerPage
      key={`${key}?${versionId ?? ""}`}
      bucket={bucket}
      objectKey={key}
      versionId={versionId}
      row={row}
    />
  )
}
