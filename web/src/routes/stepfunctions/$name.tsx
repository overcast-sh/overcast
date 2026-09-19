import { createFileRoute } from "@tanstack/react-router"
import { StateMachineDetail } from "@/features/stepfunctions/components/state-machine-detail"
import { isStateMachineTab, type StateMachineTab } from "@/features/stepfunctions/views"

type StateMachineSearch = {
  /** executions (default), diagram or details — deep-linkable. */
  tab?: StateMachineTab
}

export const Route = createFileRoute("/stepfunctions/$name")({
  head: ({ params }) => ({
    meta: [{ title: `${params.name} — Step Functions — Overcast` }],
  }),
  validateSearch: (search: Record<string, unknown>): StateMachineSearch => ({
    tab: isStateMachineTab(search.tab) ? search.tab : undefined,
  }),
  component: function StateMachineDetailRoute() {
    const { name } = Route.useParams()
    const { tab } = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <StateMachineDetail
        name={name}
        tab={tab}
        onTabChange={(next) =>
          void navigate({
            search: (prev) => ({ ...prev, tab: next }),
            replace: true,
            resetScroll: false,
          })
        }
      />
    )
  },
})
