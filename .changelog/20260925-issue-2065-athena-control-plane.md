+ [athena] named queries, prepared statements, data catalogs, `UpdateWorkGroup`, `BatchGetQueryExecution` and `ListEngineVersions`.
  `GetDatabase`, `ListDatabases`, `GetTableMetadata` and `ListTableMetadata` read the Glue Data Catalog through `AwsDataCatalog`.
  the `primary` workgroup always exists; `QueryExecution` carries its context, statement type, engine version and statistics.
+ [cloudformation/athena] `AWS::Athena::NamedQuery`, `PreparedStatement` and `DataCatalog`; `WorkGroup` now updates in place.
  `WorkGroup` also forwards `State` and `RecursiveDeleteOption`, and exposes `CreationTime` and the effective engine version.
~! [athena] `StartQueryExecution` now requires a result location from the query or its workgroup, as AWS does.
  migration: pass `ResultConfiguration.OutputLocation`, or set one on the workgroup with `UpdateWorkGroup`.
~! [athena] `CreateWorkGroup` now rejects a name that exists; `ListQueryExecutions` lists one workgroup, `primary` by default.
  migration: create each workgroup once, and pass `WorkGroup` to list another workgroup's executions.
