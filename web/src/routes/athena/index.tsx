import { createFileRoute } from "@tanstack/react-router"
import { AthenaPage } from "@/features/athena/components/athena-page"
import { validateAthenaSearch } from "@/features/athena/search"
import { useFilterSearchParam } from "@/hooks/use-filter-search-param"
import { useSortSearchParam } from "@/hooks/use-sort-search-param"

export const Route = createFileRoute("/athena/")({
  head: () => ({ meta: [{ title: "Athena — Overcast" }] }),
  validateSearch: validateAthenaSearch,
  component: function AthenaRoute() {
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    const [filter, setFilter] = useFilterSearchParam(search, navigate)
    const [sort, setSort] = useSortSearchParam(search, navigate)
    const tab = search.tab ?? "editor"
    const editorLink =
      tab === "editor" && search.sql !== undefined
        ? {
            catalog: search.catalog,
            database: search.database,
            workGroup: search.workgroup,
            sql: search.sql,
            executionId: search.execution,
          }
        : undefined
    return (
      <AthenaPage
        tab={tab}
        onTabChange={(next) =>
          // A tab's filter, sort and selection are its own: switching starts the next one clean.
          void navigate({ search: { tab: next }, replace: true })
        }
        filter={filter}
        onFilterChange={setFilter}
        sort={sort}
        onSortChange={setSort}
        link={editorLink}
        onLinkOpened={() => void navigate({ search: { tab: "editor" }, replace: true })}
        execution={search.execution}
        workGroup={tab === "workgroups" ? search.workgroup : undefined}
        onWorkGroupChange={(workgroup) =>
          void navigate({ search: (prev) => ({ ...prev, workgroup }) })
        }
      />
    )
  },
})
