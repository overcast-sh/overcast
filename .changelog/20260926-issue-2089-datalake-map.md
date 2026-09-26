+ [web/athena/glue/s3tables] The system map draws Athena workgroups, Glue databases and S3 Tables buckets, with the queries and tables in them
  Each node has one action: run a query, preview a table's first rows, or see its latest commit. Rows flash on writes and ghost when dropped.
  Edges show where tables' data and query results live, what recent queries read, and s3tablescatalog's federation.
~ [web/s3tables] A table's `--table-s3` warehouse bucket is drawn as part of its table bucket on the map, not as a separate S3 bucket
+ [athena/s3tables] `athena:QueryStateChanged` events name the query's catalog, and `s3tables:TableCommitted` the records the commit added
