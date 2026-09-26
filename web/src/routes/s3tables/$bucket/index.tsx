import { createFileRoute } from "@tanstack/react-router"
import { TableBucketDetail } from "@/features/s3tables/components/table-bucket-detail"
import { validateTableBucketSearch } from "@/features/s3tables/search"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"

export const Route = createFileRoute("/s3tables/$bucket/")({
  head: ({ params }) => ({ meta: [{ title: `${params.bucket} — S3 Tables — Overcast` }] }),
  validateSearch: validateTableBucketSearch,
  component: function TableBucketRoute() {
    const { bucket } = Route.useParams()
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    return (
      <TableBucketDetail
        bucketName={bucket}
        tab={search.tab ?? "tables"}
        // Switching tabs clears the filter, which belongs to the tables tab.
        onTabChange={(tab) => void navigate({ search: { tab }, replace: true, resetScroll: false })}
        filter={filter}
        onFilterChange={setFilter}
      />
    )
  },
})
