import { createFileRoute } from "@tanstack/react-router"
import { TableDetail } from "@/features/glue/components/table-detail"
import { validateTableSearch } from "@/features/glue/views"

export const Route = createFileRoute("/glue/$database/$table")({
  head: ({ params }) => ({
    meta: [{ title: `${params.database}.${params.table} — Glue — Overcast` }],
  }),
  validateSearch: validateTableSearch,
  component: function GlueTableRoute() {
    const { database, table } = Route.useParams()
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <TableDetail
        database={database}
        name={table}
        search={search}
        onSearchChange={(patch) =>
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
