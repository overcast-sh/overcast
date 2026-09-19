import { createFileRoute } from "@tanstack/react-router"
import { ExecutionDetail } from "@/features/stepfunctions/components/execution-detail"
import { isExecutionTab, type ExecutionTab } from "@/features/stepfunctions/views"

type ExecutionSearch = {
  /** Selected state in the diagram — deep-links straight to a state's details. */
  state?: string
  /** Lower panel: timeline (default), events, io or definition. */
  tab?: ExecutionTab
  /** The full execution ARN, for executions whose ARN the names cannot rebuild (distributed Map children). */
  arn?: string
}

export const Route = createFileRoute("/stepfunctions/execution/$name/$execution")({
  head: ({ params }) => ({
    meta: [{ title: `${params.execution} — ${params.name} — Step Functions — Overcast` }],
  }),
  validateSearch: (search: Record<string, unknown>): ExecutionSearch => ({
    state: typeof search.state === "string" && search.state !== "" ? search.state : undefined,
    tab: isExecutionTab(search.tab) ? search.tab : undefined,
    arn: typeof search.arn === "string" && search.arn.startsWith("arn:") ? search.arn : undefined,
  }),
  component: function ExecutionDetailRoute() {
    const { name, execution } = Route.useParams()
    const { state, tab, arn } = Route.useSearch()
    const navigate = Route.useNavigate()
    return (
      <ExecutionDetail
        name={name}
        execution={execution}
        executionArn={arn}
        state={state}
        // Selecting a state is a view change, not a destination: replace the
        // history entry so Back leaves the execution instead of undoing clicks.
        onStateChange={(next) =>
          void navigate({
            search: (prev) => ({ ...prev, state: next }),
            replace: true,
            resetScroll: false,
          })
        }
        tab={tab ?? "timeline"}
        onTabChange={(next) =>
          void navigate({
            search: (prev) => ({ ...prev, tab: next === "timeline" ? undefined : next }),
            replace: true,
            resetScroll: false,
          })
        }
      />
    )
  },
})
