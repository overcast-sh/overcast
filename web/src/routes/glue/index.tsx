import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/glue/")({
  head: () => ({ meta: [{ title: "Glue — Overcast" }] }),
  component: () => <ConsolePendingPage service="glue" />,
})
