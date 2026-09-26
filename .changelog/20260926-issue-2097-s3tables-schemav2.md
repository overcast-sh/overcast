+ [s3tables/cloudformation] `CreateTable` and `AWS::S3Tables::Table` accept `schemaV2`, so tables with struct, list and map columns get a first `metadata.json`
  field ids are reassigned depth-first as Iceberg assigns them, and partition, sort and identifier fields follow them to the new ids
~. [s3tables] Iceberg REST commits refuse a partition source in a list or map, and an optional, float, double or list-nested identifier field
  the Iceberg spec forbids both, and the reference implementation refuses them the same way
* [s3tables] a new table's metadata matches Iceberg's: its write order is sort order 1 and the only one, and a format-version 1 file has no `last-sequence-number`
+ [web] the S3 Tables *Create table* dialog creates tables with nested struct columns
