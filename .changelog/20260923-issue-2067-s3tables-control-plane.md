+ [s3tables] Amazon S3 Tables: table buckets, namespaces and Iceberg tables, with all 49 modeled operations answering
  Each table gets a real `--table-s3` warehouse bucket in S3; `CreateTable` with a schema writes the first Iceberg `metadata.json`.
  `UpdateTableMetadataLocation` is a compare-and-swap on `versionToken`; maintenance, expiration and replication are stored, never run.
+ [cloudformation/s3tables] `AWS::S3Tables::TableBucket`, `Namespace`, `Table`, `TableBucketPolicy` and `TablePolicy` provision through S3 Tables
