import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/s3tables/")({
  head: () => ({ meta: [{ title: "S3 Tables — Overcast" }] }),
  component: () => <ConsolePendingPage service="s3tables" />,
})
