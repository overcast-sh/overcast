import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import type { QueryExecution } from "@aws-sdk/client-athena"
import {
  athenaKeys,
  executionQueryOptions,
  startQueryMutationOptions,
  stopQueryMutationOptions,
} from "./data"
import { isFinished } from "./execution-state"
import { startQueryInput } from "./query-input"
import type { QueryTab } from "./query-tabs"

export interface QueryRun {
  /** The tab's last execution, polled until it finishes. */
  execution: QueryExecution | undefined
  /** `StartQueryExecution` itself failed: no execution exists for it. */
  startError: Error | null
  /** Starting, queued or running. */
  running: boolean
  stopping: boolean
  /** Runs the tab's query, or `selection`. Ignored while a run is in flight. */
  run: (selection?: string) => void
  stop: () => void
}

/**
 * Running one query tab: start it, follow its execution, stop it. The
 * started execution's id is handed to `onStarted`, which records it on the
 * tab, so a reload finds the tab's last result again.
 */
export function useQueryRun(tab: QueryTab, onStarted: (executionId: string) => void): QueryRun {
  const queryClient = useQueryClient()
  // Not useResourceMutation: a refused start is shown in the editor's error
  // box, where the SQL is, rather than as a toast.
  // eslint-disable-next-line local/prefer-use-resource-mutation
  const start = useMutation({
    ...startQueryMutationOptions(),
    onSuccess: (id) => {
      onStarted(id)
      void queryClient.invalidateQueries({ queryKey: athenaKeys.executionList() })
    },
  })
  const stop = useResourceMutation({
    options: stopQueryMutationOptions(),
    invalidateKeys: [athenaKeys.executions()],
    errorTitle: "Could not stop the query",
  })
  const { data: execution } = useQuery(executionQueryOptions(tab.executionId ?? ""))
  const current = execution?.QueryExecutionId === tab.executionId ? execution : undefined
  const running = start.isPending || (current !== undefined && !isFinished(current.Status?.State))

  return {
    execution: current,
    startError: start.error,
    running,
    stopping: stop.isPending,
    run: (selection) => {
      if (running) return
      start.mutate(startQueryInput(tab, selection))
    },
    stop: () => {
      if (tab.executionId && running) stop.mutate(tab.executionId)
    },
  }
}
