import { createFileRoute } from "@tanstack/react-router"
import { TableDetail } from "@/features/s3tables/components/table-detail"
import { validateTableSearch, type TableSearch } from "@/features/s3tables/search"

/**
 * A table is addressed by the id its ARN carries rather than by namespace and
 * name: the id survives RenameTable, and it is all an ARN gives an `ArnLink`.
 */
export const Route = createFileRoute("/s3tables/$bucket/$tableId")({
  head: ({ params }) => ({ meta: [{ title: `${params.tableId} — S3 Tables — Overcast` }] }),
  validateSearch: validateTableSearch,
  component: function TableRoute() {
    const { bucket, tableId } = Route.useParams()
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <TableDetail
        bucketName={bucket}
        tableId={tableId}
        search={search}
        onSearchChange={(patch: Partial<TableSearch>) =>
          void navigate({
            search: (prev) => ({ ...prev, ...patch }),
            replace: true,
            resetScroll: false,
          })
        }
      />
    )
  },
})
