import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/athena/")({
  head: () => ({ meta: [{ title: "Athena — Overcast" }] }),
  component: () => <ConsolePendingPage service="athena" />,
})
