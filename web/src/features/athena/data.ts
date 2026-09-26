/**
 * Athena query keys, query options and mutation options.
 *
 * Key factory:
 *   athenaKeys.all()                   -> [...endpoint, "athena"]
 *   athenaKeys.workGroups()            -> [...endpoint, "athena", "workgroups"]
 *   athenaKeys.workGroup(name)         -> [...workGroups(), "detail", name]
 *   athenaKeys.preparedStatements(wg)  -> [...workGroups(), "prepared", wg]
 *   athenaKeys.namedQueries()          -> [...endpoint, "athena", "named-queries"]
 *   athenaKeys.executions()            -> [...endpoint, "athena", "executions"]
 *   athenaKeys.executionList()         -> [...executions(), "list"]
 *   athenaKeys.execution(id)           -> [...executions(), "detail", id]
 *   athenaKeys.result(id)              -> [...endpoint, "athena", "results", id]
 *   athenaKeys.runtimeStatistics(id)   -> [...result(id), "runtime"]
 *   athenaKeys.firstResultPage(id)     -> [...result(id), "first-page"]
 *   athenaKeys.preparedStatement(wg,n) -> [...workGroups(), "prepared", wg, n]
 *   athenaKeys.dataCatalogs()          -> [...endpoint, "athena", "catalogs"]
 *   athenaKeys.metadata()              -> [...endpoint, "athena", "metadata"]
 *   athenaKeys.databases(catalog)      -> [...metadata(), "databases", catalog]
 *   athenaKeys.tables(catalog, db)     -> [...metadata(), "tables", catalog, db]
 *   athenaKeys.engine()                -> [...endpoint, "athena", "engine"]
 *
 * Every query execution query — the history list, one execution — hangs
 * under `executions()`, which is what an `athena:QueryStateChanged` event
 * invalidates. A finished query's result and statistics never change, so
 * they hang elsewhere, under `result(id)`, out of the events' way. The data browser's databases
 * and tables hang under `metadata()`, which Glue's table events invalidate.
 */

import { mutationOptions, queryOptions } from "@tanstack/react-query"
import type {
  CreateDataCatalogInput,
  CreateNamedQueryInput,
  CreateWorkGroupInput,
  StartQueryExecutionInput,
  UpdateNamedQueryInput,
  UpdateWorkGroupInput,
} from "@aws-sdk/client-athena"
import { athena } from "@/services/api"
import { endpointStore } from "@/services/endpoint-store"
import type { AthenaEngineState } from "@/types"
import { isFinished } from "./execution-state"

// ─── Key factory ───────────────────────────────────────────────────────────

export const athenaKeys = {
  all: () => [...endpointStore.getKeys(), "athena"] as const,
  workGroups: () => [...athenaKeys.all(), "workgroups"] as const,
  workGroup: (name: string) => [...athenaKeys.workGroups(), "detail", name] as const,
  preparedStatements: (workGroup: string) =>
    [...athenaKeys.workGroups(), "prepared", workGroup] as const,
  preparedStatement: (workGroup: string, name: string) =>
    [...athenaKeys.preparedStatements(workGroup), name] as const,
  namedQueries: () => [...athenaKeys.all(), "named-queries"] as const,
  executions: () => [...athenaKeys.all(), "executions"] as const,
  executionList: () => [...athenaKeys.executions(), "list"] as const,
  execution: (id: string) => [...athenaKeys.executions(), "detail", id] as const,
  result: (id: string) => [...athenaKeys.all(), "results", id] as const,
  runtimeStatistics: (id: string) => [...athenaKeys.result(id), "runtime"] as const,
  firstResultPage: (id: string) => [...athenaKeys.result(id), "first-page"] as const,
  dataCatalogs: () => [...athenaKeys.all(), "catalogs"] as const,
  metadata: () => [...athenaKeys.all(), "metadata"] as const,
  databases: (catalog: string) => [...athenaKeys.metadata(), "databases", catalog] as const,
  tables: (catalog: string, database: string) =>
    [...athenaKeys.metadata(), "tables", catalog, database] as const,
  engine: () => [...athenaKeys.all(), "engine"] as const,
}

/** How often a running query is polled, on top of its state-change events. */
const RUNNING_POLL_MS = 500
/** How often the engine's state is polled while it is on its way somewhere, and at rest. */
const ENGINE_BUSY_POLL_MS = 1000
const ENGINE_IDLE_POLL_MS = 15_000

/** Engine states that move on by themselves, so the chip wants the next one soon. */
const ENGINE_BUSY = new Set<AthenaEngineState>(["probing", "pulling", "starting"])

// ─── Queries ───────────────────────────────────────────────────────────────

export function workGroupsQueryOptions() {
  return queryOptions({
    queryKey: athenaKeys.workGroups(),
    queryFn: () => athena.listWorkGroups(),
  })
}

export function workGroupQueryOptions(name: string) {
  return queryOptions({
    queryKey: athenaKeys.workGroup(name),
    queryFn: () => athena.getWorkGroup(name),
    enabled: name !== "",
  })
}

export function preparedStatementsQueryOptions(workGroup: string) {
  return queryOptions({
    queryKey: athenaKeys.preparedStatements(workGroup),
    queryFn: () => athena.listPreparedStatements(workGroup),
  })
}

/** A prepared statement, for the `?` count an `EXECUTE` of it needs values for. */
export function preparedStatementQueryOptions(workGroup: string, name: string) {
  return queryOptions({
    queryKey: athenaKeys.preparedStatement(workGroup, name),
    queryFn: () => athena.getPreparedStatement(workGroup, name),
    enabled: name !== "",
  })
}

export function namedQueriesQueryOptions() {
  return queryOptions({
    queryKey: athenaKeys.namedQueries(),
    queryFn: () => athena.listAllNamedQueries(),
  })
}

export function executionsQueryOptions() {
  return queryOptions({
    queryKey: athenaKeys.executionList(),
    queryFn: () => athena.listAllQueryExecutions(),
  })
}

/**
 * One execution, polled until it finishes. Its state-change event already
 * invalidates it; the poll is what keeps a query that started before the
 * page opened moving when events are off. An execution that cannot be read
 * — gone after the emulator restarted — is not polled again.
 */
export function executionQueryOptions(id: string) {
  return queryOptions({
    queryKey: athenaKeys.execution(id),
    queryFn: () => athena.getQueryExecution(id),
    enabled: id !== "",
    retry: false,
    refetchInterval: (query) =>
      query.state.status === "error" || isFinished(query.state.data?.Status?.State)
        ? false
        : RUNNING_POLL_MS,
  })
}

export function runtimeStatisticsQueryOptions(id: string) {
  return queryOptions({
    queryKey: athenaKeys.runtimeStatistics(id),
    queryFn: () => athena.getQueryRuntimeStatistics(id),
    staleTime: Infinity,
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

export function dataCatalogsQueryOptions() {
  return queryOptions({
    queryKey: athenaKeys.dataCatalogs(),
    queryFn: () => athena.listDataCatalogs(),
  })
}

export function databasesQueryOptions(catalog: string) {
  return queryOptions({
    queryKey: athenaKeys.databases(catalog),
    queryFn: () => athena.listDatabases(catalog),
    enabled: catalog !== "",
  })
}

export function tablesQueryOptions(catalog: string, database: string) {
  return queryOptions({
    queryKey: athenaKeys.tables(catalog, database),
    queryFn: () => athena.listTableMetadata(catalog, database),
    enabled: catalog !== "" && database !== "",
  })
}

/**
 * The engine's state, polled slowly at rest and every second while it is on
 * its way somewhere — or while `queryWaiting`: a query that has just been
 * started may be what starts the engine, and the chip must show that start
 * as it happens rather than at the next idle poll.
 */
export function engineStatusQueryOptions(queryWaiting = false) {
  return queryOptions({
    queryKey: athenaKeys.engine(),
    queryFn: () => athena.getEngineStatus(),
    refetchInterval: (query) => {
      const state = query.state.data?.state
      return queryWaiting || !state || ENGINE_BUSY.has(state)
        ? ENGINE_BUSY_POLL_MS
        : ENGINE_IDLE_POLL_MS
    },
  })
}

// ─── Mutations ─────────────────────────────────────────────────────────────

export function startQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.executions(), "start"] as const,
    mutationFn: (input: StartQueryExecutionInput) => athena.startQueryExecution(input),
  })
}

export function stopQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.executions(), "stop"] as const,
    mutationFn: (id: string) => athena.stopQueryExecution(id),
  })
}

export function createNamedQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.namedQueries(), "create"] as const,
    mutationFn: (input: CreateNamedQueryInput) => athena.createNamedQuery(input),
  })
}

export function updateNamedQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.namedQueries(), "update"] as const,
    mutationFn: (input: UpdateNamedQueryInput) => athena.updateNamedQuery(input),
  })
}

export function deleteNamedQueryMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.namedQueries(), "delete"] as const,
    mutationFn: (id: string) => athena.deleteNamedQuery(id),
  })
}

export function createWorkGroupMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.workGroups(), "create"] as const,
    mutationFn: (input: CreateWorkGroupInput) => athena.createWorkGroup(input),
  })
}

export function updateWorkGroupMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.workGroups(), "update"] as const,
    mutationFn: (input: UpdateWorkGroupInput) => athena.updateWorkGroup(input),
  })
}

export function deleteWorkGroupMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.workGroups(), "delete"] as const,
    mutationFn: (name: string) => athena.deleteWorkGroup(name),
  })
}

export function createDataCatalogMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.dataCatalogs(), "create"] as const,
    mutationFn: (input: CreateDataCatalogInput) => athena.createDataCatalog(input),
  })
}

export function deleteDataCatalogMutationOptions() {
  return mutationOptions({
    mutationKey: [...athenaKeys.dataCatalogs(), "delete"] as const,
    mutationFn: (name: string) => athena.deleteDataCatalog(name),
  })
}
