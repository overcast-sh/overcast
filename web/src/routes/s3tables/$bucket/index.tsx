import { createFileRoute } from "@tanstack/react-router"
import { ConsolePendingPage } from "@/features/placeholder/console-pending-page"

export const Route = createFileRoute("/s3tables/$bucket/")({
  head: ({ params }) => ({ meta: [{ title: `${params.bucket} — S3 Tables — Overcast` }] }),
  component: function TableBucketRoute() {
    const { bucket } = Route.useParams()
    return (
      <ConsolePendingPage service="s3tables" resource={{ name: bucket, kind: "Table bucket" }} />
    )
  },
})
