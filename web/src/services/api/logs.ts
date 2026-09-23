import { awsClients } from "../aws-clients"
import {
  DescribeLogGroupsCommand,
  CreateLogGroupCommand,
  DeleteLogGroupCommand,
  DescribeLogStreamsCommand,
  CreateLogStreamCommand,
  DeleteLogStreamCommand,
  GetLogEventsCommand,
  PutLogEventsCommand,
  FilterLogEventsCommand,
  ListTagsLogGroupCommand,
  PutRetentionPolicyCommand,
} from "@aws-sdk/client-cloudwatch-logs"
import type { LogGroup, LogStream } from "@aws-sdk/client-cloudwatch-logs"

export interface CreateLogGroupInput {
  name: string
  /** Applied by CreateLogGroup itself, atomically with the group. */
  tags?: Record<string, string>
  /** One of the retention values CloudWatch Logs accepts; omit to never expire. */
  retentionInDays?: number
}

/** Pages of 50 to walk before giving up on a nextToken that never clears. */
const maxListPages = 100

export const logs = {
  // DescribeLogGroups and DescribeLogStreams return at most 50 items per call,
  // so both list helpers follow nextToken to the end: the console shows every
  // group and stream, not the first page of them.
  // maxListPages is a stop so a server that kept handing back a token could
  // never spin the browser.
  listGroups: async (prefix?: string) => {
    const groups: LogGroup[] = []
    let nextToken: string | undefined
    for (let page = 0; page < maxListPages; page++) {
      const res = await awsClients
        .logs()
        .send(new DescribeLogGroupsCommand({ logGroupNamePrefix: prefix || undefined, nextToken }))
      groups.push(...(res.logGroups ?? []))
      nextToken = res.nextToken
      if (!nextToken) break
    }
    return groups
  },

  createGroup: async ({ name, tags, retentionInDays }: CreateLogGroupInput) => {
    await awsClients.logs().send(
      new CreateLogGroupCommand({
        logGroupName: name,
        // AWS's Tags shape has a minimum length of 1, so an empty map is not a
        // valid request — send the field only when there is something in it.
        ...(tags && Object.keys(tags).length > 0 ? { tags } : {}),
      }),
    )
    // Retention is a separate operation in the CloudWatch Logs API — there is
    // no create-time equivalent of CloudFormation's RetentionInDays property.
    if (retentionInDays != null) {
      await awsClients
        .logs()
        .send(new PutRetentionPolicyCommand({ logGroupName: name, retentionInDays }))
    }
  },

  listGroupTags: async (name: string) => {
    const res = await awsClients.logs().send(new ListTagsLogGroupCommand({ logGroupName: name }))
    return res.tags ?? {}
  },

  deleteGroup: async (name: string) => {
    await awsClients.logs().send(new DeleteLogGroupCommand({ logGroupName: name }))
  },

  listStreams: async (groupName: string, prefix?: string) => {
    const streams: LogStream[] = []
    let nextToken: string | undefined
    for (let page = 0; page < maxListPages; page++) {
      const res = await awsClients.logs().send(
        new DescribeLogStreamsCommand({
          logGroupName: groupName,
          logStreamNamePrefix: prefix || undefined,
          nextToken,
        }),
      )
      streams.push(...(res.logStreams ?? []))
      nextToken = res.nextToken
      if (!nextToken) break
    }
    return streams
  },

  createStream: async (groupName: string, name: string) => {
    await awsClients
      .logs()
      .send(new CreateLogStreamCommand({ logGroupName: groupName, logStreamName: name }))
  },

  deleteStream: async (groupName: string, streamName: string) => {
    await awsClients
      .logs()
      .send(new DeleteLogStreamCommand({ logGroupName: groupName, logStreamName: streamName }))
  },

  getEvents: async (
    groupName: string,
    streamName: string,
    opts: {
      startTime?: number
      endTime?: number
      nextToken?: string
      limit?: number
      startFromHead?: boolean
    } = {},
  ) => {
    const res = await awsClients.logs().send(
      new GetLogEventsCommand({
        logGroupName: groupName,
        logStreamName: streamName,
        ...(opts.nextToken == null ? { startFromHead: opts.startFromHead ?? false } : {}),
        ...(opts.nextToken != null ? { nextToken: opts.nextToken } : {}),
        ...(opts.startTime != null ? { startTime: opts.startTime } : {}),
        ...(opts.endTime != null ? { endTime: opts.endTime } : {}),
        ...(opts.limit != null ? { limit: opts.limit } : {}),
      }),
    )
    return {
      events: res.events ?? [],
      nextBackwardToken: res.nextBackwardToken,
      nextForwardToken: res.nextForwardToken,
    }
  },

  putEvents: async (
    groupName: string,
    streamName: string,
    events: Array<{ timestamp: number; message: string }>,
  ) => {
    await awsClients.logs().send(
      new PutLogEventsCommand({
        logGroupName: groupName,
        logStreamName: streamName,
        logEvents: events,
      }),
    )
  },

  filterEvents: async (
    groupName: string,
    opts: {
      filterPattern?: string
      startTime?: number
      endTime?: number
      logStreamNames?: string[]
      logStreamNamePrefix?: string
      nextToken?: string
      limit?: number
      /** Read another region's group than the console's own. */
      region?: string
    } = {},
  ) => {
    const res = await awsClients.logs(opts.region).send(
      new FilterLogEventsCommand({
        logGroupName: groupName,
        ...(opts.filterPattern ? { filterPattern: opts.filterPattern } : {}),
        ...(opts.startTime != null ? { startTime: opts.startTime } : {}),
        ...(opts.endTime != null ? { endTime: opts.endTime } : {}),
        ...(opts.logStreamNames?.length ? { logStreamNames: opts.logStreamNames } : {}),
        ...(opts.logStreamNamePrefix ? { logStreamNamePrefix: opts.logStreamNamePrefix } : {}),
        ...(opts.nextToken ? { nextToken: opts.nextToken } : {}),
        ...(opts.limit != null ? { limit: opts.limit } : {}),
      }),
    )
    return {
      events: res.events ?? [],
      searchedLogStreams: res.searchedLogStreams ?? [],
      // Present exactly when more matching events remain — the server caps a
      // page at 10,000 events, and ignoring the token silently truncated
      // anything past it.
      nextToken: res.nextToken,
    }
  },
}
