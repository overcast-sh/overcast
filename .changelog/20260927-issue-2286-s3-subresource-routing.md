* [s3] A modeled S3 sub-resource is served as its own operation, returning `501` where unimplemented, never as `CreateBucket`, `PutObject` or `DeleteBucket`.
  `PUT ?metadataJournalTable` no longer creates the bucket, `PUT ?annotation` no longer overwrites the object, and `PUT ?renameObject` no longer writes one.
  Metadata configuration moves from `?metadata` to the modeled `?metadataConfiguration`, and `GET ?analytics&id=` is `GetBucketAnalyticsConfiguration`.
  `?metadata` and `?rename` are now unknown parameters S3 ignores, as AWS does; `WriteGetObjectResponse` is `POST /WriteGetObjectResponse`.
* [s3] A sub-resource sent on a method S3 has no operation for is `405 MethodNotAllowed`, as on AWS.
  `DELETE /bucket?versioning` deleted the bucket, `DELETE /bucket/key?acl` the object, and `PUT /bucket/key?attributes` overwrote it.
