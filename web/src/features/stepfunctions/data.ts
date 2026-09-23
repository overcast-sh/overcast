import { queryOptions, mutationOptions } from "@tanstack/react-query"
import { stepfunctions } from "@/services/api/stepfunctions"
import { logs } from "@/services/api"
import { logsKeys } from "@/features/cloudwatch/logs/data"
import { endpointStore } from "@/services/endpoint-store"

// ─── Key factory ───────────────────────────────────────────────────────────

export const sfnKeys = {
  all: () => [...endpointStore.getKeys(), "stepfunctions"] as const,
  stateMachines: () => [...sfnKeys.all(), "stateMachines"] as const,
  stateMachine: (arn: string) => [...sfnKeys.stateMachines(), arn] as const,
  executions: (stateMachineArn: string) =>
    [...sfnKeys.all(), "executions", stateMachineArn] as const,
  execution: (executionArn: string) => [...sfnKeys.all(), "execution", executionArn] as const,
  mapRunExecutions: (mapRunArn: string) => [...sfnKeys.all(), "mapRun", mapRunArn] as const,
  executionDefinition: (executionArn: string) =>
    [...sfnKeys.execution(executionArn), "definition"] as const,
  executionHistory: (executionArn: string) =>
    [...sfnKeys.execution(executionArn), "history"] as const,
}

/** How often a page watching a RUNNING execution polls for new history. */
export const LIVE_POLL_MS = 1000

const isRunning = (status: string | undefined) =>
  status === "RUNNING" || status === "PENDING_REDRIVE"

// ─── Query definitions ─────────────────────────────────────────────────────

export function sfnStateMachinesQueryOptions() {
  return queryOptions({
    queryKey: sfnKeys.stateMachines(),
    queryFn: () => stepfunctions.listStateMachines(),
  })
}

export function sfnStateMachineQueryOptions(arn: string) {
  return queryOptions({
    queryKey: sfnKeys.stateMachine(arn),
    queryFn: () => stepfunctions.describeStateMachine(arn),
    enabled: arn !== "",
  })
}

/**
 * Executions of one state machine. While any of them is still running the list
 * refreshes itself, so a status flips from RUNNING to its outcome in place.
 */
export function sfnExecutionsQueryOptions(stateMachineArn: string) {
  return queryOptions({
    queryKey: sfnKeys.executions(stateMachineArn),
    queryFn: () => stepfunctions.listExecutions(stateMachineArn),
    enabled: stateMachineArn !== "",
    refetchInterval: (query) =>
      query.state.data?.some((e) => isRunning(e.status)) ? LIVE_POLL_MS * 2 : false,
  })
}

/** One execution; polls while it is running and stops the moment it finishes. */
export function sfnExecutionQueryOptions(executionArn: string) {
  return queryOptions({
    queryKey: sfnKeys.execution(executionArn),
    queryFn: () => stepfunctions.describeExecution(executionArn),
    enabled: executionArn !== "",
    refetchInterval: (query) => (isRunning(query.state.data?.status) ? LIVE_POLL_MS : false),
  })
}

/** The child executions of a distributed Map run; polls while any is running. */
export function sfnMapRunExecutionsQueryOptions(mapRunArn: string) {
  return queryOptions({
    queryKey: sfnKeys.mapRunExecutions(mapRunArn),
    queryFn: () => stepfunctions.listExecutions({ mapRunArn }),
    enabled: mapRunArn !== "",
    refetchInterval: (query) =>
      query.state.data?.some((e) => isRunning(e.status)) ? LIVE_POLL_MS * 2 : false,
  })
}

export function sfnExecutionDefinitionQueryOptions(executionArn: string) {
  return queryOptions({
    queryKey: sfnKeys.executionDefinition(executionArn),
    queryFn: () => stepfunctions.describeStateMachineForExecution(executionArn),
    enabled: executionArn !== "",
    staleTime: Infinity,
  })
}

/**
 * An execution's full history. `live` is the caller's knowledge that the
 * execution is still running — history carries no status of its own that is
 * cheaper to read than DescribeExecution's.
 */
export function sfnExecutionHistoryQueryOptions(executionArn: string, live = false) {
  return queryOptions({
    queryKey: sfnKeys.executionHistory(executionArn),
    queryFn: () => stepfunctions.getExecutionHistory(executionArn),
    enabled: executionArn !== "",
    refetchInterval: live ? LIVE_POLL_MS : false,
  })
}

/** The most log events read for one Task's attempts — far more than one invocation writes. */
export const TASK_LOG_LIMIT = 5000

/**
 * A function's log events across the window a Task's attempts ran in. A window
 * with no end is one whose attempt is still running: it reads up to now and
 * keeps polling, so the invocation's lines stream in, under a key that does
 * not move with the clock.
 */
export function sfnTaskLogsQueryOptions(
  groupName: string,
  window: { startMs: number; endMs?: number },
  region?: string,
) {
  const live = window.endMs === undefined
  return queryOptions({
    queryKey: [
      ...logsKeys.filter(groupName),
      "sfn-task",
      region ?? "",
      window.startMs,
      window.endMs ?? "live",
    ] as const,
    queryFn: () =>
      logs.filterEvents(groupName, {
        startTime: window.startMs,
        ...(window.endMs !== undefined ? { endTime: window.endMs } : {}),
        limit: TASK_LOG_LIMIT,
        region,
      }),
    enabled: groupName !== "",
    retry: false,
    // A settled window still polls until its end has passed: its slack is
    // there for lines CloudWatch Logs delivers after the attempt settled.
    refetchInterval: () => (live || Date.now() <= (window.endMs ?? 0) ? LIVE_POLL_MS * 2 : false),
  })
}

// ─── Mutation definitions ──────────────────────────────────────────────────

export function createStateMachineMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.stateMachines(), "create"] as const,
    mutationFn: stepfunctions.createStateMachine,
  })
}

export function updateStateMachineMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.stateMachines(), "update"] as const,
    mutationFn: stepfunctions.updateStateMachine,
  })
}

export function deleteStateMachineMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.stateMachines(), "delete"] as const,
    mutationFn: (arn: string) => stepfunctions.deleteStateMachine(arn),
  })
}

export function startExecutionMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.all(), "startExecution"] as const,
    mutationFn: stepfunctions.startExecution,
  })
}

export function redriveExecutionMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.all(), "redriveExecution"] as const,
    mutationFn: (executionArn: string) => stepfunctions.redriveExecution(executionArn),
  })
}

export function stopExecutionMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.all(), "stopExecution"] as const,
    mutationFn: stepfunctions.stopExecution,
  })
}
