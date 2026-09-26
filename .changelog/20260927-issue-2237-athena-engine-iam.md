* [athena/iam] Athena queries that read tables no longer fail when `OVERCAST_ENFORCE_IAM` is on.
  The engine's Glue, S3 and Iceberg REST calls skip enforcement; `StartQueryExecution` is still checked.
