import { createFileRoute } from "@tanstack/react-router"
import { DatabaseDetail } from "@/features/glue/components/database-detail"
import { createTableWizardState } from "@/features/glue/create-table-param"
import { validateListSearch } from "@/features/glue/views"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"

export const Route = createFileRoute("/glue/$database/")({
  head: ({ params }) => ({ meta: [{ title: `${params.database} — Glue — Overcast` }] }),
  validateSearch: validateListSearch,
  component: function GlueDatabaseRoute() {
    const { database } = Route.useParams()
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    const [sort, setSort] = useSortSearchParam(search, navigate)
    return (
      <DatabaseDetail
        name={database}
        filter={filter}
        onFilterChange={setFilter}
        sort={sort}
        onSortChange={setSort}
        wizard={createTableWizardState(search, navigate)}
      />
    )
  },
})
