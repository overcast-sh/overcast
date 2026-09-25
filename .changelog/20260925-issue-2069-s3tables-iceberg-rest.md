+ [s3tables] the Iceberg REST catalog at `/iceberg`, so PyIceberg, Spark and Trino can create, commit to and read S3 Tables tables
  namespaces, tables, register, rename, and commits with every format v1/v2 update and requirement, sharing `UpdateTableMetadataLocation`'s compare-and-swap
  staged create, which AWS's catalog lacks, so Trino can write; unsigned clients use `/_overcast/s3tables/iceberg`
