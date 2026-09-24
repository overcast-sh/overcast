import { awsClients } from "../aws-clients"
import { apiFetch } from "./base"
import {
  ListEventBusesCommand,
  CreateEventBusCommand,
  DeleteEventBusCommand,
  ListRulesCommand,
  PutRuleCommand,
  DeleteRuleCommand,
  type EventBus,
} from "@aws-sdk/client-eventbridge"

export type { EventBus, Rule as EventRule } from "@aws-sdk/client-eventbridge"

/** A rule target with the target type the emulator resolved from its ARN. */
export interface EventRuleTarget {
  Id: string
  Arn: string
  /** "Lambda" | "SQS" | "SNS" | "Step Functions" | "Kinesis" | "Firehose" | "ECS" | "Unknown" */
  TargetType: string
}

/** A rule paired with its targets, from the emulator's console endpoint. */
export interface EventRuleTargets {
  Name: string
  State: string
  Targets: EventRuleTarget[]
}

/** The outcome of one target delivery attempt sequence. */
export interface EventDelivery {
  Region: string
  Bus: string
  Rule: string
  TargetId: string
  TargetArn: string
  TargetType: string
  /** "delivered" | "retried" | "dlq" | "dropped" */
  Outcome: string
  Attempts: number
  EventId: string
  Error?: string
  Time: string
}

export const eventbridge = {
  // ListEventBuses pages at 100 buses (#2110), so follow NextToken to the end.
  listBuses: async () => {
    const client = awsClients.eventbridge()
    const all: EventBus[] = []
    let nextToken: string | undefined
    do {
      const res = await client.send(new ListEventBusesCommand({ NextToken: nextToken }))
      all.push(...(res.EventBuses ?? []))
      nextToken = res.NextToken
    } while (nextToken)
    return all
  },

  createBus: async (name: string) => {
    await awsClients.eventbridge().send(new CreateEventBusCommand({ Name: name }))
  },

  deleteBus: async (name: string) => {
    await awsClients.eventbridge().send(new DeleteEventBusCommand({ Name: name }))
  },

  listRules: async (eventBusName?: string) => {
    const res = await awsClients
      .eventbridge()
      .send(new ListRulesCommand({ EventBusName: eventBusName }))
    return res.Rules ?? []
  },

  createRule: async (name: string, eventBusName?: string) => {
    await awsClients.eventbridge().send(
      new PutRuleCommand({
        Name: name,
        EventBusName: eventBusName,
        EventPattern: JSON.stringify({ source: ["overcast.test"] }),
        State: "ENABLED",
      }),
    )
  },

  deleteRule: async (name: string, eventBusName?: string) => {
    await awsClients
      .eventbridge()
      .send(new DeleteRuleCommand({ Name: name, EventBusName: eventBusName }))
  },

  /**
   * Each rule's targets on a bus, with the target type the emulator resolved
   * from each ARN. Served by the emulator's console endpoint rather than the
   * SDK, because ListTargetsByRule is per-rule and carries no resolved type.
   */
  listRuleTargets: async (eventBusName: string) => {
    const res = await apiFetch<{ Rules?: EventRuleTargets[] }>(
      `/eventbridge/rule-targets?bus=${encodeURIComponent(eventBusName)}`,
    )
    return res.Rules ?? []
  },

  /**
   * Recent per-target delivery outcomes on a bus, newest first. The emulator
   * keeps a bounded in-memory ring, so this is empty after a restart.
   */
  listDeliveries: async (eventBusName: string) => {
    const res = await apiFetch<{ Deliveries?: EventDelivery[] }>(
      `/eventbridge/deliveries?bus=${encodeURIComponent(eventBusName)}`,
    )
    return res.Deliveries ?? []
  },
}
