import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/glue/$database/$table")({
  head: ({ params }) => ({
    meta: [{ title: `${params.database}.${params.table} — Glue — Overcast` }],
  }),
  component: function GlueTableRoute() {
    const { database, table } = Route.useParams()
    return (
      <ConsolePendingPage
        service="glue"
        resource={{ name: table, kind: `Table in database ${database}` }}
      />
    )
  },
})
