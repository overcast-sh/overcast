import { useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import type { QueryExecution } from "@aws-sdk/client-athena"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import {
  athenaKeys,
  executionQueryOptions,
  startQueryMutationOptions,
  stopQueryMutationOptions,
} from "./data"
import { isFinished } from "./execution-state"
import { startQueryInput, type QueryRunRequest } from "./query-input"
import type { QueryTab } from "./query-tabs"

export interface QueryRun {
  /** The tab's last execution, polled until it finishes. */
  execution: QueryExecution | undefined
  /**
   * Why there is no execution to show: `StartQueryExecution` was refused, or
   * the tab's last execution cannot be read (gone after a restart).
   */
  error: Error | null
  /** Starting, queued or running. */
  running: boolean
  stopping: boolean
  /** Starts a run. Ignored while one is in flight. */
  run: (request: QueryRunRequest) => void
  stop: () => void
}

/**
 * Running one query tab: start it, follow its execution, stop it. The
 * started execution's id is handed to `onStarted`, which records it on the
 * tab, so a reload finds the tab's last result again.
 */
export function useQueryRun(tab: QueryTab, onStarted: (executionId: string) => void): QueryRun {
  // Set the moment a run is asked for, before the mutation's own pending
  // state reaches a render: a second ⌘⏎ in the same tick must not start twice.
  const starting = useRef(false)
  const start = useResourceMutation({
    options: {
      ...startQueryMutationOptions(),
      onSettled: () => {
        starting.current = false
      },
    },
    invalidateKeys: [athenaKeys.executionList()],
    // A refused start shows in the editor's error box, where the SQL is.
    errorToast: false,
    onSuccess: onStarted,
  })
  const stop = useResourceMutation({
    options: stopQueryMutationOptions(),
    invalidateKeys: [athenaKeys.executions()],
    errorTitle: "Could not stop the query",
  })
  const executionId = tab.executionId ?? ""
  const followed = useQuery(executionQueryOptions(executionId))
  const execution = followed.data?.QueryExecutionId === executionId ? followed.data : undefined
  // From the moment a start returns until its execution first loads, the
  // query is running: Stop, esc and the run guard must not lapse in between.
  const running =
    start.isPending ||
    (executionId !== "" &&
      followed.status !== "error" &&
      (execution === undefined || !isFinished(execution.Status?.State)))

  return {
    execution,
    error: start.error ?? (executionId !== "" ? followed.error : null),
    running,
    stopping: stop.isPending,
    run: (request) => {
      if (running || starting.current) return
      starting.current = true
      start.mutate(startQueryInput(tab, request))
    },
    stop: () => {
      if (executionId && running) stop.mutate(executionId)
    },
  }
}
