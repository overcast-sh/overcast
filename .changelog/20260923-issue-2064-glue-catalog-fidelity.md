+ [glue] Glue partitions: create, get, update and delete, singly and in batches, with `GetPartitions` filtering by `Expression` (#2064)
  the expression subset is comparisons, AND/OR/NOT, IN, BETWEEN, LIKE and IS NULL; anything else is InvalidInputException.
+ [glue] `UpdateTable` with `VersionId` concurrency and archived versions, plus `UpdateDatabase` and `BatchDeleteTable` (#2064)
  a stale VersionId is ConcurrentModificationException; GetTableVersion(s), DeleteTableVersion and BatchDeleteTableVersion read and prune versions.
* [glue] `GetTable` returns the whole `TableInput` it was given, and `GetDatabase` the whole `DatabaseInput` (#2064)
  StorageDescriptor, PartitionKeys and Parameters were dropped on write; tables now carry CreateTime, UpdateTime and VersionId.
* [glue] deleting a database deletes its tables and their partitions and versions; deleting a table deletes its partitions (#2064)
*! [glue] creating a database or table that exists is `AlreadyExistsException`; a table's database must exist (#2064)
  names are folded to lowercase as on AWS, and Glue client errors are HTTP 400 where EntityNotFoundException was 404.
  migration: change a definition with UpdateTable or UpdateDatabase instead of re-creating it; re-create mixed-case state.
+ [cloudformation] `AWS::Glue::Partition`, and `AWS::Glue::Table` keeps every `TableInput` property (#2064)
