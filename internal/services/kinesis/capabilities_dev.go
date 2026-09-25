//go:build dev

package kinesis

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "kinesis", Operation: "AddTagsToStream", Category: "General", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "kinesis", Operation: "CreateStream", Category: "General", Status: capabilities.StatusSupported, Notes: "Stream becomes ACTIVE immediately; inline `Tags` and `StreamModeDetails` applied at creation, defaulting to PROVISIONED"},
		capabilities.Capability{Service: "kinesis", Operation: "DecreaseStreamRetentionPeriod", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores and echoes the new value; does not trim any record now older than the shortened window"},
		capabilities.Capability{Service: "kinesis", Operation: "DeleteStream", Category: "General", Status: capabilities.StatusSupported, Notes: "Also removes all stored records"},
		capabilities.Capability{Service: "kinesis", Operation: "DescribeStream", Category: "General", Status: capabilities.StatusSupported, Notes: "Pages the Shards list on Limit/ExclusiveStartShardId and reports HasMoreShards"},
		capabilities.Capability{Service: "kinesis", Operation: "DescribeStreamSummary", Category: "General", Status: capabilities.StatusSupported, Notes: "Lightweight summary without shard detail"},
		capabilities.Capability{Service: "kinesis", Operation: "GetRecords", Category: "General", Status: capabilities.StatusSupported, Notes: "Returns stored records and a valid NextShardIterator; records are never expired by RetentionPeriodHours, so a shard keeps every record for the life of the stream regardless of the configured retention"},
		capabilities.Capability{Service: "kinesis", Operation: "GetShardIterator", Category: "General", Status: capabilities.StatusSupported, Notes: "Supports TRIM_HORIZON, LATEST, AT/AFTER_SEQUENCE_NUMBER"},
		capabilities.Capability{Service: "kinesis", Operation: "IncreaseStreamRetentionPeriod", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores and echoes the new value from DescribeStream/DescribeStreamSummary; not enforced against stored records (see GetRecords)"},
		capabilities.Capability{Service: "kinesis", Operation: "ListShards", Category: "General", Status: capabilities.StatusSupported, Notes: "Paginates on MaxResults/NextToken/ExclusiveStartShardId, addressable by StreamName or StreamARN; lists closed split/merge parents with their lineage and honours every ShardFilter type. Closed shards never expire, so FROM_TRIM_HORIZON is every shard"},
		capabilities.Capability{Service: "kinesis", Operation: "ListStreams", Category: "General", Status: capabilities.StatusSupported, Notes: "Paginates on Limit/NextToken/ExclusiveStartStreamName and returns StreamSummaries alongside StreamNames"},
		capabilities.Capability{Service: "kinesis", Operation: "ListTagsForResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Stream ARNs; the same tag set `ListTagsForStream` returns"},
		capabilities.Capability{Service: "kinesis", Operation: "ListTagsForStream", Category: "General", Status: capabilities.StatusSupported, Notes: "Pages on Limit/ExclusiveStartTagKey and reports HasMoreTags"},
		capabilities.Capability{Service: "kinesis", Operation: "MergeShards", Category: "General", Status: capabilities.StatusSupported, Notes: "Refuses shards whose hash key ranges are not adjacent; closes both parents and creates a child naming them as ParentShardId/AdjacentParentShardId"},
		capabilities.Capability{Service: "kinesis", Operation: "PutRecord", Category: "General", Status: capabilities.StatusSupported, Notes: "Routes by partition key hash"},
		capabilities.Capability{Service: "kinesis", Operation: "PutRecords", Category: "General", Status: capabilities.StatusSupported, Notes: "Returns FailedRecordCount=0 for all records"},
		capabilities.Capability{Service: "kinesis", Operation: "RemoveTagsFromStream", Category: "General", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "kinesis", Operation: "SplitShard", Category: "General", Status: capabilities.StatusSupported, Notes: "NewStartingHashKey must lie strictly inside the parent's range; closes the parent and creates two children, split at that key, naming it as ParentShardId"},
		capabilities.Capability{Service: "kinesis", Operation: "StartStreamEncryption", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores EncryptionType/KeyId and echoes them from Describe*; records are not actually encrypted at rest"},
		capabilities.Capability{Service: "kinesis", Operation: "StopStreamEncryption", Category: "General", Status: capabilities.StatusSupported, Notes: "Resets EncryptionType to NONE and clears KeyId"},
		capabilities.Capability{Service: "kinesis", Operation: "TagResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Stream ARNs; consumer ARNs are rejected because consumers are not emulated"},
		capabilities.Capability{Service: "kinesis", Operation: "UntagResource", Category: "General", Status: capabilities.StatusSupported, Notes: "Stream ARNs"},
		capabilities.Capability{Service: "kinesis", Operation: "UpdateStreamMode", Category: "General", Status: capabilities.StatusSupported, Notes: "Stores StreamModeDetails and echoes it from Describe*; on-demand capacity is not actually enforced"},
	)
}
