import { createFileRoute } from "@tanstack/react-router"
import { WebFunctionList } from "@/features/lambda-web/components/web-function-list"
import { validateWebFunctionsSearch } from "@/features/lambda-web/search"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"

export const Route = createFileRoute("/lambda-web/")({
  head: () => ({ meta: [{ title: "Lambda Web Functions — Overcast" }] }),
  validateSearch: validateWebFunctionsSearch,
  component: function WebFunctionsRoute() {
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    const [sort, setSort] = useSortSearchParam(search, navigate)
    return (
      <WebFunctionList
        filter={filter}
        onFilterChange={setFilter}
        sort={sort}
        onSortChange={setSort}
      />
    )
  },
})
