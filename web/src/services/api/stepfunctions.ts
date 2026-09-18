import { awsClients } from "../aws-clients"
import {
  ListStateMachinesCommand,
  CreateStateMachineCommand,
  DeleteStateMachineCommand,
  DescribeStateMachineCommand,
  DescribeStateMachineForExecutionCommand,
  UpdateStateMachineCommand,
  ListExecutionsCommand,
  DescribeExecutionCommand,
  GetExecutionHistoryCommand,
  StartExecutionCommand,
  StopExecutionCommand,
  RedriveExecutionCommand,
  type ExecutionListItem,
  type HistoryEvent,
  type StateMachineType,
} from "@aws-sdk/client-sfn"

export type { StateMachineListItem as StateMachine } from "@aws-sdk/client-sfn"
export type {
  ExecutionListItem,
  DescribeExecutionCommandOutput,
  DescribeStateMachineCommandOutput,
  HistoryEvent,
  StateMachineType,
} from "@aws-sdk/client-sfn"

/**
 * Upper bound on pages fetched for one list. A console page never needs more
 * than a few thousand executions, and a runaway loop against a misbehaving
 * backend must stop somewhere.
 */
const MAX_PAGES = 20

/**
 * An execution's ARN, built from its state machine's ARN and its name — the
 * same derivation AWS uses. It lets a page address an execution directly from
 * the URL instead of finding it in a (paged, possibly stale) list first.
 */
export function executionArnFor(stateMachineArn: string, executionName: string): string {
  return `${stateMachineArn.replace(":stateMachine:", ":execution:")}:${executionName}`
}

/** A state machine's ARN from any of its executions' ARNs. */
export function stateMachineArnFromExecution(executionArn: string): string {
  return executionArn.replace(":execution:", ":stateMachine:").replace(/:[^:]+$/, "")
}

export const stepfunctions = {
  listStateMachines: async () => {
    const res = await awsClients.sfn().send(new ListStateMachinesCommand({}))
    return res.stateMachines ?? []
  },

  describeStateMachine: async (arn: string) =>
    awsClients.sfn().send(new DescribeStateMachineCommand({ stateMachineArn: arn })),

  createStateMachine: async ({
    name,
    definition,
    type,
  }: {
    name: string
    definition: string
    type?: StateMachineType
  }) =>
    awsClients.sfn().send(
      new CreateStateMachineCommand({
        name,
        definition,
        type,
        roleArn: "arn:aws:iam::000000000000:role/StepFunctionsRole",
      }),
    ),

  updateStateMachine: async ({ arn, definition }: { arn: string; definition: string }) =>
    awsClients.sfn().send(new UpdateStateMachineCommand({ stateMachineArn: arn, definition })),

  deleteStateMachine: async (arn: string) => {
    await awsClients.sfn().send(new DeleteStateMachineCommand({ stateMachineArn: arn }))
  },

  /** Executions of a state machine, or — given `{ mapRunArn }` — the child executions of a distributed Map run. */
  listExecutions: async (source: string | { mapRunArn: string }) => {
    const all: ExecutionListItem[] = []
    let nextToken: string | undefined
    const filter = typeof source === "string" ? { stateMachineArn: source } : source
    for (let page = 0; page < MAX_PAGES; page++) {
      const res = await awsClients
        .sfn()
        .send(new ListExecutionsCommand({ ...filter, nextToken, maxResults: 1000 }))
      all.push(...(res.executions ?? []))
      nextToken = res.nextToken
      if (!nextToken) break
    }
    return all
  },

  /** The definition as it was when this execution started — later edits do not rewrite history. */
  describeStateMachineForExecution: async (executionArn: string) =>
    awsClients.sfn().send(new DescribeStateMachineForExecutionCommand({ executionArn })),

  describeExecution: async (executionArn: string) =>
    awsClients.sfn().send(new DescribeExecutionCommand({ executionArn })),

  getExecutionHistory: async (executionArn: string) => {
    const all: HistoryEvent[] = []
    let nextToken: string | undefined
    for (let page = 0; page < MAX_PAGES * 5; page++) {
      const res = await awsClients.sfn().send(
        new GetExecutionHistoryCommand({
          executionArn,
          includeExecutionData: true,
          maxResults: 1000,
          nextToken,
        }),
      )
      all.push(...(res.events ?? []))
      nextToken = res.nextToken
      if (!nextToken) break
    }
    return all
  },

  startExecution: async ({
    stateMachineArn,
    input,
    name,
  }: {
    stateMachineArn: string
    input: string
    name?: string
  }) =>
    awsClients.sfn().send(
      new StartExecutionCommand({
        stateMachineArn,
        input: input || "{}",
        name: name || undefined,
      }),
    ),

  /** Resumes a failed, timed-out or aborted Standard execution from where it stopped. */
  redriveExecution: async (executionArn: string) =>
    awsClients.sfn().send(new RedriveExecutionCommand({ executionArn })),

  stopExecution: async ({
    executionArn,
    error,
    cause,
  }: {
    executionArn: string
    error?: string
    cause?: string
  }) => awsClients.sfn().send(new StopExecutionCommand({ executionArn, error, cause })),
}
