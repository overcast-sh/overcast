+ [athena] named queries, prepared statements, data catalogs, `UpdateWorkGroup`, `BatchGetQueryExecution` and `ListEngineVersions`.
  `GetDatabase`, `ListDatabases`, `GetTableMetadata` and `ListTableMetadata` read the Glue Data Catalog through `AwsDataCatalog`.
  the `primary` workgroup always exists; `QueryExecution` carries its context, statement type, engine version and statistics.
+ [cloudformation/athena] `AWS::Athena::NamedQuery`, `PreparedStatement` and `DataCatalog`; `WorkGroup` now updates in place.
  `WorkGroup` also forwards `State` and `RecursiveDeleteOption`, and exposes `CreationTime` and the effective engine version.
~! [athena] `StartQueryExecution` now requires a result location from the query or its workgroup, as AWS does.
  `GetQueryExecution` reports the result object, `<location>/<id>.csv` (`.txt` for DDL), rather than the location itself.
  migration: pass `ResultConfiguration.OutputLocation`, or set one on the workgroup with `UpdateWorkGroup`.
~! [athena] `CreateWorkGroup` now rejects a name that exists, an invalid name and an unknown engine version.
  `Configuration` members are typed as the API types them; PySpark engines answer 501.
  migration: create each workgroup once, with a name matching `[a-zA-Z0-9._-]{1,128}` and engine `AUTO` or version 3.
~! [athena] `ListQueryExecutions` lists one workgroup, `primary` by default; `ListWorkGroups` always includes `primary`.
  migration: pass `WorkGroup` to list another workgroup's executions.
~! [athena] `DeleteWorkGroup` now refuses a workgroup holding named queries; `GetQueryResults` refuses an unfinished query.
  migration: pass `RecursiveDeleteOption`, and poll `GetQueryExecution` until `SUCCEEDED` before reading results.
