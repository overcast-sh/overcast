+ [athena/s3tables] Athena queries and writes S3 Tables as `s3tablescatalog/<bucket>`, through each bucket's Iceberg REST catalog.
  `SELECT`, `INSERT`, `MERGE`, `UPDATE`, `DELETE`, Iceberg `CREATE TABLE` and CTAS commit through S3 Tables, moving the table's metadata location.
  `CREATE DATABASE` and `DROP` change the bucket, `SHOW` and `DESCRIBE` read it, and a bucket created while the engine runs is queryable at once.
~ [athena] the engine gateway admits each call only when it is signed for its own service: Glue, S3, or S3 Tables' Iceberg REST catalog.
