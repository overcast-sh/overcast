* [kinesis] ListStreams, ListShards, DescribeStream and ListTagsForStream paginate, instead of returning everything in one page
  Each honours its documented cursor, page size and truncation flag: Limit/MaxResults, ExclusiveStart{StreamName,ShardId,TagKey}, NextToken, HasMore{Streams,Shards,Tags}.
  A ListShards NextToken identifies its own stream and expires after 300 seconds with ExpiredNextTokenException, and combining one with StreamName or ExclusiveStartShardId is refused, as on AWS.
  ListStreams also returns the modeled StreamSummaries alongside StreamNames, and ListShards accepts StreamARN in place of StreamName.
* [kinesis] Errors match the AWS model: every exception is HTTP 400, and a missing parameter is InvalidArgumentException
  ResourceNotFoundException answered 404 where all Kinesis exceptions are 400, and a missing required member answered MissingParameter, a code no Kinesis operation models and no SDK error type matches.
