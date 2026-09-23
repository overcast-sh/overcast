* [kinesis] `PutRecord`/`PutRecords` route by the MD5 hash-key range AWS uses, honouring `ExplicitHashKey`
  previously routed by a byte-sum modulo shard count, which disagreed with the `HashKeyRange` values `ListShards`/`DescribeStream` report
