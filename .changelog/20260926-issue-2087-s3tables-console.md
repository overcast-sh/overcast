+ [web] S3 Tables console: table buckets, namespaces and tables, with create, delete, policy, maintenance, encryption and tags
  A table's page shows its schema and how it evolved, and its snapshots with a diff against the previous one.
  *Query as of this snapshot* opens Athena; the Metadata tab diffs any two `metadata.json` versions and follows commits live.
+ [web] *Connect a client* on every table bucket and table page, with ready-to-paste PyIceberg, Spark, Trino, DuckDB and AWS CLI snippets
  They carry this emulator's endpoint and the bucket's ARN; the Iceberg REST docs show the same snippets from the same source.
+ [web] Create an S3 Tables table from the console: nested structs, required columns and a partition spec, or copy it as CDK
* [web] The console's JSON views keep integers past 2^53 exactly, so an Iceberg snapshot id no longer shows rounded
