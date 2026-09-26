* [s3] Deleting a bucket named `multipart` no longer discards every in-progress multipart upload's parts (#2234)
  Parts moved to a directory no bucket name can take; parts stored by an earlier release are moved on first use.
* [s3] A multipart object's ETag is now S3's MD5 of its parts' MD5s, so a download checked against its ETag verifies (#2232)
