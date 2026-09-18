import { queryOptions, mutationOptions } from "@tanstack/react-query"
import { stepfunctions } from "@/services/api/stepfunctions"
import { endpointStore } from "@/services/endpoint-store"

// ─── Key factory ───────────────────────────────────────────────────────────

export const sfnKeys = {
  all: () => [...endpointStore.getKeys(), "stepfunctions"] as const,
  stateMachines: () => [...sfnKeys.all(), "stateMachines"] as const,
  stateMachine: (arn: string) => [...sfnKeys.stateMachines(), arn] as const,
  executions: (stateMachineArn: string) =>
    [...sfnKeys.all(), "executions", stateMachineArn] as const,
  execution: (executionArn: string) => [...sfnKeys.all(), "execution", executionArn] as const,
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

export function stopExecutionMutationOptions() {
  return mutationOptions({
    mutationKey: [...sfnKeys.all(), "stopExecution"] as const,
    mutationFn: stepfunctions.stopExecution,
  })
}
