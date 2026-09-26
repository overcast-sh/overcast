+ [web/s3tables] The system map's S3 Tables latest-commit peek offers Query with Athena, in the table bucket's `s3tablescatalog/<bucket>` catalog
~ [athena] Drawing the system map no longer reads every stored query execution: each workgroup keeps a small index of its recent ones
  The index is rebuilt from the executions once per process, and again if an entry cannot be read.
