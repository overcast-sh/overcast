import { createFileRoute } from "@tanstack/react-router"
import { validateAthenaSearch } from "@/features/athena/search"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/athena/")({
  head: () => ({ meta: [{ title: "Athena — Overcast" }] }),
  validateSearch: validateAthenaSearch,
  component: () => <ConsolePendingPage service="athena" />,
})
