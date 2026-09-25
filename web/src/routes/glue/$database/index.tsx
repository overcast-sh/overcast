import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/glue/$database/")({
  head: ({ params }) => ({ meta: [{ title: `${params.database} — Glue — Overcast` }] }),
  component: function GlueDatabaseRoute() {
    const { database } = Route.useParams()
    return <ConsolePendingPage service="glue" resource={{ name: database, kind: "Database" }} />
  },
})
