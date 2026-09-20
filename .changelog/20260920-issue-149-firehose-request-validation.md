* [firehose] the request validation AWS documents is enforced, where `CreateDeliveryStream`, `PutRecord` and `PutRecordBatch` used to answer `200` (#149).
  a second `CreateDeliveryStream` for a name in use reports `ResourceInUseException` instead of silently overwriting the stream's type, ARN and tags
  `PutRecordBatch` enforces 1-500 records, the 1,000 KiB per-record cap and the documented 4 MiB per call, and no longer acknowledges records it never decoded
  every exception carries HTTP 400, the status the Firehose model gives them, where a missing delivery stream answered 404
* [cloudformation/firehose] `Ref` for a delivery stream is its name, and deleting a stack now deletes the stream (#149).
  the physical ID was the ARN, so teardown asked Firehose to delete a stream named by an ARN, which matched nothing and left the stream behind
