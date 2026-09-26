import { createFileRoute } from "@tanstack/react-router"
import { GluePage } from "@/features/glue/components/glue-page"
import { createTableWizardState } from "@/features/glue/create-table-param"
import { validateListSearch } from "@/features/glue/views"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"

export const Route = createFileRoute("/glue/")({
  head: () => ({ meta: [{ title: "Glue — Overcast" }] }),
  validateSearch: validateListSearch,
  component: function GlueIndexRoute() {
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    const [sort, setSort] = useSortSearchParam(search, navigate)
    return (
      <GluePage
        filter={filter}
        onFilterChange={setFilter}
        sort={sort}
        onSortChange={setSort}
        wizard={createTableWizardState(search, navigate)}
      />
    )
  },
})
