import { createFileRoute } from "@tanstack/react-router"
import { WebFunctionDetail } from "@/features/lambda-web/components/web-function-detail"
import { validateWebFunctionSearch } from "@/features/lambda-web/search"

export const Route = createFileRoute("/lambda-web/$name")({
  head: ({ params }) => ({ meta: [{ title: `${params.name} — Lambda Web Functions — Overcast` }] }),
  validateSearch: validateWebFunctionSearch,
  component: function WebFunctionRoute() {
    const { name } = Route.useParams()
    const search = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <WebFunctionDetail
        name={name}
        tab={search.tab ?? "overview"}
        onTabChange={(tab) => void navigate({ search: { tab }, replace: true, resetScroll: false })}
      />
    )
  },
})
