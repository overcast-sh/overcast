import { awsClients } from "../aws-clients"
import {
  ListStreamsCommand,
  DescribeStreamSummaryCommand,
  CreateStreamCommand,
  DeleteStreamCommand,
  ListShardsCommand,
  ListTagsForStreamCommand,
  AddTagsToStreamCommand,
  RemoveTagsFromStreamCommand,
  IncreaseStreamRetentionPeriodCommand,
  DecreaseStreamRetentionPeriodCommand,
} from "@aws-sdk/client-kinesis"
import type { KinesisStream, KinesisStreamDetail } from "@/types"

export const kinesis = {
  listStreams: async (): Promise<KinesisStream[]> => {
    const client = awsClients.kinesis()
    const res = await client.send(new ListStreamsCommand({ Limit: 100 }))
    const streamNames = res.StreamNames ?? []
    return Promise.all(
      streamNames.map(async (name) => {
        try {
          const detail = await client.send(new DescribeStreamSummaryCommand({ StreamName: name }))
          const s = detail.StreamDescriptionSummary!
          return {
            name: s.StreamName ?? name,
            arn: s.StreamARN ?? "",
            status: s.StreamStatus ?? "UNKNOWN",
            shardCount: s.OpenShardCount ?? 0,
            retentionHours: s.RetentionPeriodHours ?? 24,
            createdAt: s.StreamCreationTimestamp?.getTime(),
          }
        } catch {
          return {
            name,
            arn: "",
            status: "UNKNOWN",
            shardCount: 0,
            retentionHours: 24,
            createdAt: undefined,
          }
        }
      }),
    )
  },

  getStream: async (name: string): Promise<KinesisStreamDetail> => {
    const client = awsClients.kinesis()
    const [detail, shardsRes, tagsRes] = await Promise.all([
      client.send(new DescribeStreamSummaryCommand({ StreamName: name })),
      // AT_LATEST: the open shards only. An unfiltered ListShards also lists
      // the closed parents of every split and merge, as AWS does, and the
      // Shards tab has no column yet that tells a closed shard from an open one.
      client.send(new ListShardsCommand({ StreamName: name, ShardFilter: { Type: "AT_LATEST" } })),
      client.send(new ListTagsForStreamCommand({ StreamName: name })),
    ])
    const s = detail.StreamDescriptionSummary!
    const shards = (shardsRes.Shards ?? []).map((sh) => ({
      shardId: sh.ShardId ?? "",
      parentShardId: sh.ParentShardId,
      adjacentParentShardId: sh.AdjacentParentShardId,
      startingHashKey: sh.HashKeyRange?.StartingHashKey ?? "",
      endingHashKey: sh.HashKeyRange?.EndingHashKey ?? "",
      startingSeqNo: sh.SequenceNumberRange?.StartingSequenceNumber ?? "",
      endingSeqNo: sh.SequenceNumberRange?.EndingSequenceNumber,
    }))
    const tags: Record<string, string> = {}
    for (const tag of tagsRes.Tags ?? []) {
      if (tag.Key) tags[tag.Key] = tag.Value ?? ""
    }
    return {
      name: s.StreamName ?? name,
      arn: s.StreamARN ?? "",
      status: s.StreamStatus ?? "UNKNOWN",
      shardCount: s.OpenShardCount ?? 0,
      retentionHours: s.RetentionPeriodHours ?? 24,
      createdAt: s.StreamCreationTimestamp?.getTime(),
      shards,
      tags,
    }
  },

  createStream: async (name: string, shardCount: number) => {
    await awsClients
      .kinesis()
      .send(new CreateStreamCommand({ StreamName: name, ShardCount: shardCount }))
  },

  deleteStream: async (name: string) => {
    await awsClients.kinesis().send(new DeleteStreamCommand({ StreamName: name }))
  },

  addTags: async (name: string, tags: Record<string, string>) => {
    await awsClients.kinesis().send(new AddTagsToStreamCommand({ StreamName: name, Tags: tags }))
  },

  removeTags: async (name: string, keys: string[]) => {
    await awsClients
      .kinesis()
      .send(new RemoveTagsFromStreamCommand({ StreamName: name, TagKeys: keys }))
  },

  setRetention: async (name: string, hours: number) => {
    const client = awsClients.kinesis()
    const detail = await client.send(new DescribeStreamSummaryCommand({ StreamName: name }))
    const currentHours = detail.StreamDescriptionSummary?.RetentionPeriodHours ?? 24
    if (hours > currentHours) {
      await client.send(
        new IncreaseStreamRetentionPeriodCommand({
          StreamName: name,
          RetentionPeriodHours: hours,
        }),
      )
    } else if (hours < currentHours) {
      await client.send(
        new DecreaseStreamRetentionPeriodCommand({
          StreamName: name,
          RetentionPeriodHours: hours,
        }),
      )
    }
  },
}
