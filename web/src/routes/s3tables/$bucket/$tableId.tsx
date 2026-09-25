import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

/**
 * A table is addressed by the id its ARN carries rather than by namespace and
 * name: the id survives RenameTable, and it is all an ARN gives an `ArnLink`.
 */
export const Route = createFileRoute("/s3tables/$bucket/$tableId")({
  head: ({ params }) => ({ meta: [{ title: `${params.tableId} — S3 Tables — Overcast` }] }),
  component: function TableRoute() {
    const { bucket, tableId } = Route.useParams()
    return (
      <ConsolePendingPage
        service="s3tables"
        resource={{ name: tableId, kind: `Table in table bucket ${bucket}` }}
      />
    )
  },
})
