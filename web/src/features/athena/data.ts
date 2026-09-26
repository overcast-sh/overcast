/**
 * Athena query keys, query options and mutation options.
 *
 * Key factory:
 *   athenaKeys.all()                 -> [...endpoint, "athena"]
 *   athenaKeys.workGroups()          -> [...endpoint, "athena", "workgroups"]
 *   athenaKeys.namedQueries()        -> [...endpoint, "athena", "named-queries"]
 *   athenaKeys.executions()          -> [...endpoint, "athena", "executions"]
 *   athenaKeys.execution(id)         -> [...executions(), "detail", id]
 *   athenaKeys.firstResultPage(id)   -> [...executions(), "results", id]
 *
 * Every query execution query — the history list, one execution, its
 * results — hangs under `executions()`, which is what an
 * `athena:QueryStateChanged` event invalidates.
 */

import { mutationOptions, queryOptions } from "@tanstack/react-query"
import type { StartQueryExecutionInput } from "@aws-sdk/client-athena"
import { athena } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import { isFinished } from "./execution-state"

export const athenaKeys = {
  all: () => [...endpointStore.getKeys(), "athena"] as const,
  workGroups: () => [...athenaKeys.all(), "workgroups"] as const,
  namedQueries: () => [...athenaKeys.all(), "named-queries"] as const,
  executions: () => [...athenaKeys.all(), "executions"] as const,
  execution: (id: string) => [...athenaKeys.executions(), "detail", id] as const,
  firstResultPage: (id: string) => [...athenaKeys.executions(), "results", id] as const,
}

/** How often a running query is polled, on top of its state-change events. */
const RUNNING_POLL_MS = 500

/**
 * One execution, polled until it finishes. Its state-change event already
 * invalidates it; the poll is what keeps a query that started before the
 * page opened moving when events are off.
 */
export function executionQueryOptions(id: string) {
  return queryOptions({
    queryKey: athenaKeys.execution(id),
    queryFn: () => athena.getQueryExecution(id),
    enabled: id !== "",
    refetchInterval: (query) =>
      isFinished(query.state.data?.Status?.State) ? false : RUNNING_POLL_MS,
  })
}

/**
 * A finished execution's first `GetQueryResults` page: its columns, its
 * `UpdateCount`, and whether there is more. A result never changes once
 * written, so it is never refetched.
 */
export function firstResultPageQueryOptions(id: string) {
  return queryOptions({
    queryKey: athenaKeys.firstResultPage(id),
    queryFn: ({ signal }) => athena.getQueryResults(id, undefined, signal),
    staleTime: Infinity,
  })
}

export function startQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.executions(), "start"] as const,
    mutationFn: (input: StartQueryExecutionInput) => athena.startQueryExecution(input),
  })
}
