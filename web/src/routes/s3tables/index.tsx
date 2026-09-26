import { createFileRoute } from "@tanstack/react-router"
import { TableBucketList } from "@/features/s3tables/components/table-bucket-list"
import { validateTableBucketsSearch } from "@/features/s3tables/search"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"

export const Route = createFileRoute("/s3tables/")({
  head: () => ({ meta: [{ title: "S3 Tables — Overcast" }] }),
  validateSearch: validateTableBucketsSearch,
  component: function TableBucketsRoute() {
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    const [sort, setSort] = useSortSearchParam(search, navigate)
    return (
      <TableBucketList
        filter={filter}
        onFilterChange={setFilter}
        sort={sort}
        onSortChange={setSort}
      />
    )
  },
})
